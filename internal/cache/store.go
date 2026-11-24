package cache

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// entryOverhead is what an entry costs besides its body and headers (the
// structs, map slots and list element). It is a round estimate; the point is
// that a cache of many tiny responses can't use far more than its limit.
const entryOverhead = 512

// maxVariants is the most stored variants of one URL (different Vary values).
const maxVariants = 8

// Key says which response a request is about. Two requests with the same Key
// (and the same values in the headers the response varies on) get the same
// stored response.
type Key struct {
	Host   string // the site's host name, lower case
	Path   string // the path as the client sent it (percent-encoded)
	Query  string // the raw query string
	Scheme string // "http" or "https": a backend may answer them differently
}

func (k Key) pathKey() string { return k.Host + "\x00" + k.Path }
func (k Key) variant() string { return k.Scheme + "\x00" + k.Query }

// State is what a lookup found.
type State int

const (
	Miss  State = iota // nothing usable
	Fresh              // within its lifetime
	Stale              // past its lifetime but still inside the stale window
)

// Entry is a stored response. Everything in it is read-only once it has been
// added to a Store, so requests can share it without copying.
type Entry struct {
	status  int
	header  http.Header
	body    []byte
	stored  time.Time // the moment the response was made (now minus its Age)
	fresh   time.Time // end of its lifetime
	until   time.Time // end of the stale window
	lastMod time.Time
	noStale bool
	vary    []string
	varyVal string
	host    string
	path    string
	variant string
	size    int64
	elem    *list.Element
	pk      string
}

// Stats is the state and the counters of a Store.
type Stats struct {
	Entries  int
	Bytes    int64
	Max      int64
	MaxEntry int64
	Hits     uint64
	Misses   uint64
	Stale    uint64 // requests answered with a stale entry because the backend failed
	Bypass   uint64
	Stored   uint64
	Evicted  uint64
	Purged   uint64
}

// Store holds the responses of one cache zone.
type Store struct {
	mu    sync.Mutex
	max   int64
	used  int64
	n     int
	paths map[string]map[string][]*Entry // by Key.pathKey, then Key.variant
	lru   *list.List                     // of *Entry, most recently used first

	maxEntry  atomic.Int64
	maxBytes  atomic.Int64 // the same as max, readable without the lock
	capturing atomic.Int64 // bytes held by responses that are being stored
	clock     atomic.Pointer[func() time.Time]

	flMu    sync.Mutex
	flights map[string]*flight

	hit, miss, stale, bypass, stored, evicted, purged atomic.Uint64
}

// New returns a Store that keeps at most max bytes, and no entry larger than
// maxEntry bytes.
func New(max, maxEntry int64) *Store {
	s := &Store{
		max:     max,
		paths:   map[string]map[string][]*Entry{},
		lru:     list.New(),
		flights: map[string]*flight{},
	}
	s.maxEntry.Store(maxEntry)
	s.maxBytes.Store(max)
	return s
}

// Configure changes the limits of a running Store (a reload). A smaller limit
// evicts at once.
func (s *Store) Configure(max, maxEntry int64) {
	s.maxEntry.Store(maxEntry)
	s.maxBytes.Store(max)
	s.mu.Lock()
	s.max = max
	s.evictLocked()
	s.mu.Unlock()
}

// SetClock replaces the time source. Tests use it.
func (s *Store) SetClock(f func() time.Time) { s.clock.Store(&f) }

func (s *Store) now() time.Time {
	if f := s.clock.Load(); f != nil {
		return (*f)()
	}
	return time.Now()
}

func (s *Store) limit() int64 { return s.maxBytes.Load() }

// Stats returns the current state.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	st := Stats{Entries: s.n, Bytes: s.used, Max: s.max}
	s.mu.Unlock()
	st.MaxEntry = s.maxEntry.Load()
	st.Hits, st.Misses, st.Stale, st.Bypass = s.hit.Load(), s.miss.Load(), s.stale.Load(), s.bypass.Load()
	st.Stored, st.Evicted, st.Purged = s.stored.Load(), s.evicted.Load(), s.purged.Load()
	return st
}

// lookup finds the stored response for a request. An entry that is past its
// stale window is removed on the way.
func (s *Store) lookup(k Key, req http.Header, now time.Time) (*Entry, State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.paths[k.pathKey()][k.variant()] {
		if e.varyVal != varyValue(e.vary, req) {
			continue
		}
		switch {
		case now.Before(e.fresh):
			s.lru.MoveToFront(e.elem)
			return e, Fresh
		case !e.noStale && now.Before(e.until):
			s.lru.MoveToFront(e.elem)
			return e, Stale
		}
		s.removeLocked(e)
		return nil, Miss
	}
	return nil, Miss
}

// add stores an entry, replacing the variant it supersedes and evicting the
// least recently used entries to stay within the limit.
func (s *Store) add(e *Entry) bool {
	e.size = int64(len(e.body)) + int64(len(e.pk)+len(e.variant)+entryOverhead)
	for k, v := range e.header {
		e.size += int64(len(k))
		for _, x := range v {
			e.size += int64(len(x))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.size > s.max {
		return false
	}
	// The new entry replaces the stored variant that has the same Vary values.
	for _, old := range s.paths[e.pk][e.variant] {
		if old.varyVal == e.varyVal && strings.Join(old.vary, ",") == strings.Join(e.vary, ",") {
			s.removeLocked(old)
			break
		}
	}
	if vs := s.paths[e.pk][e.variant]; len(vs) >= maxVariants {
		oldest := vs[0]
		for _, v := range vs {
			if v.stored.Before(oldest.stored) {
				oldest = v
			}
		}
		s.removeLocked(oldest)
	}
	byVariant := s.paths[e.pk]
	if byVariant == nil {
		byVariant = map[string][]*Entry{}
		s.paths[e.pk] = byVariant
	}
	byVariant[e.variant] = append(byVariant[e.variant], e)
	e.elem = s.lru.PushFront(e)
	s.used += e.size
	s.n++
	s.stored.Add(1)
	s.evictLocked()
	return true
}

func (s *Store) evictLocked() {
	for s.used > s.max {
		back := s.lru.Back()
		if back == nil {
			return
		}
		s.removeLocked(back.Value.(*Entry))
		s.evicted.Add(1)
	}
}

func (s *Store) removeLocked(e *Entry) {
	byVariant := s.paths[e.pk]
	vs := byVariant[e.variant]
	for i, v := range vs {
		if v == e {
			vs = append(vs[:i], vs[i+1:]...)
			break
		}
	}
	if len(vs) == 0 {
		delete(byVariant, e.variant)
		if len(byVariant) == 0 {
			delete(s.paths, e.pk)
		}
	} else {
		byVariant[e.variant] = vs
	}
	s.lru.Remove(e.elem)
	s.used -= e.size
	s.n--
}

// Invalidate removes everything stored for a path (every query string, scheme
// and variant). A write to a URL calls it, since the stored reads are stale then.
func (s *Store) Invalidate(k Key) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var doomed []*Entry
	for _, vs := range s.paths[k.pathKey()] {
		doomed = append(doomed, vs...)
	}
	for _, e := range doomed {
		s.removeLocked(e)
	}
	s.purged.Add(uint64(len(doomed)))
	return len(doomed)
}

// Match selects entries to purge. All removes everything. Otherwise Path (one
// path) or Prefix (every path that starts with it) must be set, and Host, when
// set, limits the purge to one site.
type Match struct {
	All    bool
	Host   string
	Path   string
	Prefix string
}

// Purge removes the matching entries and returns how many.
func (s *Store) Purge(m Match) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var doomed []*Entry
	for el := s.lru.Front(); el != nil; el = el.Next() {
		e := el.Value.(*Entry)
		switch {
		case m.All:
		case m.Host != "" && !strings.EqualFold(m.Host, e.host):
			continue
		case m.Path != "" && e.path == m.Path:
		case m.Prefix != "" && strings.HasPrefix(e.path, m.Prefix):
		default:
			continue
		}
		doomed = append(doomed, e)
	}
	for _, e := range doomed {
		s.removeLocked(e)
	}
	s.purged.Add(uint64(len(doomed)))
	return len(doomed)
}

// flight is a miss that one request is fetching while others wait for it.
type flight struct {
	s      *Store
	key    string
	done   chan struct{}
	once   sync.Once
	failed atomic.Bool // the backend failed and a stale entry was used
}

func (s *Store) joinFlight(k Key) (*flight, bool) {
	key := k.pathKey() + "\x00" + k.variant()
	s.flMu.Lock()
	defer s.flMu.Unlock()
	if f, ok := s.flights[key]; ok {
		return f, false
	}
	f := &flight{s: s, key: key, done: make(chan struct{})}
	s.flights[key] = f
	return f, true
}

// finish releases the requests waiting for the flight. It is safe to call twice.
func (f *flight) finish() {
	f.once.Do(func() {
		f.s.flMu.Lock()
		if f.s.flights[f.key] == f {
			delete(f.s.flights, f.key)
		}
		f.s.flMu.Unlock()
		close(f.done)
	})
}
