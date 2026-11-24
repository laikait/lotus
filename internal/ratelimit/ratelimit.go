// Package ratelimit holds the in-memory limiters of LOTUS: a token bucket per
// key, and a lock-out for repeated failures.
//
// Both are bounded. Keys come from clients, so a table that grows with them
// would be a way to exhaust memory: each limiter keeps at most a fixed number
// of keys, forgets idle ones, and when it is still full makes room by dropping
// the key that was used least recently.
package ratelimit

import (
	"hash/fnv"
	"math"
	"net/netip"
	"sync"
	"time"
)

const (
	shards      = 64
	maxKeys     = 1 << 16 // per limiter; 1024 per shard
	sweepEvery  = 256     // operations between sweeps of one shard
	perShardMax = maxKeys / shards
)

// Key turns a client address into a limiter key. IPv6 clients usually get a
// whole /64 (or more), so one address per request would give an attacker an
// endless supply of fresh keys; they are grouped by /64.
func Key(a netip.Addr) string {
	a = a.Unmap()
	if !a.IsValid() {
		return ""
	}
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

func shardOf(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() % shards)
}

// ---- token bucket ----

// Limiter allows Rate events per Per, with bursts of up to Burst.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64
	idle  time.Duration // a bucket this long untouched is full again, so forgettable
	sh    [shards]bucketShard
}

type bucketShard struct {
	mu  sync.Mutex
	m   map[string]*bucket
	ops int
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter makes a limiter: requests per period, burst (0 means requests).
func NewLimiter(requests int, per time.Duration, burst int) *Limiter {
	if burst <= 0 {
		burst = requests
	}
	if per <= 0 {
		per = time.Second
	}
	l := &Limiter{rate: float64(requests) / per.Seconds(), burst: float64(burst)}
	l.idle = time.Duration(l.burst/l.rate*float64(time.Second)) + time.Second
	for i := range l.sh {
		l.sh[i].m = make(map[string]*bucket)
	}
	return l
}

// Allow takes one token for key. When there is none it says how long until one
// is available.
func (l *Limiter) Allow(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	s := &l.sh[shardOf(key)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ops++; s.ops%sweepEvery == 0 {
		s.sweep(now, l.idle)
	}
	b := s.m[key]
	if b == nil {
		if len(s.m) >= perShardMax {
			s.dropOldest()
		}
		b = &bucket{tokens: l.burst, last: now}
		s.m[key] = b
	} else if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = math.Min(l.burst, b.tokens+el*l.rate)
		b.last = now
	}
	if b.tokens >= 1-1e-9 { // not 1: refills add up in floating point
		b.tokens = math.Max(0, b.tokens-1)
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

func (s *bucketShard) sweep(now time.Time, idle time.Duration) {
	for k, b := range s.m {
		if now.Sub(b.last) > idle {
			delete(s.m, k)
		}
	}
}

// evictSample is how many entries are looked at to find one to drop. Looking
// at all of them on every insert would let a flood of new keys cost a full
// scan each; a small random sample (map iteration order is random) finds an old
// entry well enough and costs the same however many keys there are.
const evictSample = 5

func (s *bucketShard) dropOldest() {
	var oldest string
	var at time.Time
	n := 0
	for k, b := range s.m {
		if oldest == "" || b.last.Before(at) {
			oldest, at = k, b.last
		}
		if n++; n >= evictSample {
			break
		}
	}
	delete(s.m, oldest)
}

// Len is the number of keys held (for tests and metrics).
func (l *Limiter) Len() int {
	n := 0
	for i := range l.sh {
		l.sh[i].mu.Lock()
		n += len(l.sh[i].m)
		l.sh[i].mu.Unlock()
	}
	return n
}

// ---- lock-out ----

// Lockout locks a key out for Lock after Max failures within Window. It is how
// repeated wrong passwords are slowed down.
type Lockout struct {
	max    int
	window time.Duration
	lock   time.Duration
	sh     [shards]lockShard
}

type lockShard struct {
	mu  sync.Mutex
	m   map[string]*failures
	ops int
}

type failures struct {
	count  int
	first  time.Time
	locked time.Time // locked until
}

// NewLockout makes a lock-out. max <= 0 returns nil, which never locks.
func NewLockout(max int, window, lock time.Duration) *Lockout {
	if max <= 0 {
		return nil
	}
	l := &Lockout{max: max, window: window, lock: lock}
	for i := range l.sh {
		l.sh[i].m = make(map[string]*failures)
	}
	return l
}

// Locked says whether key is locked out, and for how much longer. It is safe to
// call on a nil Lockout.
func (l *Lockout) Locked(key string, now time.Time) (bool, time.Duration) {
	if l == nil {
		return false, 0
	}
	s := &l.sh[shardOf(key)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.m[key]; f != nil && now.Before(f.locked) {
		return true, f.locked.Sub(now)
	}
	return false, 0
}

// Fail records a failure of key.
func (l *Lockout) Fail(key string, now time.Time) {
	if l == nil {
		return
	}
	s := &l.sh[shardOf(key)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ops++; s.ops%sweepEvery == 0 {
		s.sweep(now, l)
	}
	f := s.m[key]
	if f == nil {
		if len(s.m) >= perShardMax {
			s.dropOldest(now)
		}
		f = &failures{first: now}
		s.m[key] = f
	}
	if now.Sub(f.first) > l.window {
		f.count, f.first = 0, now
	}
	if f.count++; f.count >= l.max {
		f.locked = now.Add(l.lock)
		f.count, f.first = 0, now
	}
}

// Reset forgets key's failures: its next attempt was right.
func (l *Lockout) Reset(key string) {
	if l == nil {
		return
	}
	s := &l.sh[shardOf(key)]
	s.mu.Lock()
	if f := s.m[key]; f != nil && f.locked.IsZero() {
		delete(s.m, key)
	} else if f != nil {
		f.count = 0
	}
	s.mu.Unlock()
}

func (s *lockShard) sweep(now time.Time, l *Lockout) {
	for k, f := range s.m {
		if now.After(f.locked) && now.Sub(f.first) > l.window {
			delete(s.m, k)
		}
	}
}

// dropOldest makes room: among a small sample it drops a key that is not
// locked out if there is one, the one seen first the longest ago.
func (s *lockShard) dropOldest(now time.Time) {
	var victim string
	var at time.Time
	n := 0
	for k, f := range s.m {
		if !now.Before(f.locked) && (victim == "" || f.first.Before(at)) {
			victim, at = k, f.first
		}
		if n++; n >= evictSample {
			break
		}
	}
	if victim == "" { // the sample is all locked out: keep those locks, drop any other key
		for k, f := range s.m {
			if !now.Before(f.locked) {
				victim = k
				break
			}
		}
	}
	if victim != "" {
		delete(s.m, victim)
	}
}

// Len is the number of keys held.
func (l *Lockout) Len() int {
	if l == nil {
		return 0
	}
	n := 0
	for i := range l.sh {
		l.sh[i].mu.Lock()
		n += len(l.sh[i].m)
		l.sh[i].mu.Unlock()
	}
	return n
}

// ---- connection counts per address ----

// PerIP counts open connections per client address and refuses more than Max.
type PerIP struct {
	max int
	mu  sync.Mutex
	m   map[string]int
}

// NewPerIP makes a counter; max <= 0 returns nil, which never refuses.
func NewPerIP(max int) *PerIP {
	if max <= 0 {
		return nil
	}
	return &PerIP{max: max, m: make(map[string]int)}
}

// Acquire reserves a connection for key. The returned function gives it back.
// It is safe on a nil PerIP.
func (p *PerIP) Acquire(key string) (release func(), ok bool) {
	if p == nil {
		return func() {}, true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m[key] >= p.max {
		return nil, false
	}
	p.m[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.m[key]--; p.m[key] <= 0 {
				delete(p.m, key)
			}
			p.mu.Unlock()
		})
	}, true
}
