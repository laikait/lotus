package proxy

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// pick chooses a backend that is up, not at its limit, and not already tried
// for this request.
func (u *Upstream) pick(req *http.Request, tried map[*backend]bool) (*backend, error) {
	now := time.Now()
	cands := make([]*backend, 0, len(u.backends))
	anyUp := false
	for _, b := range u.backends {
		if tried[b] || !b.usable(now) {
			continue
		}
		anyUp = true
		if b.full() {
			continue
		}
		cands = append(cands, b)
	}
	if len(cands) == 0 {
		if anyUp {
			return nil, ErrBusy
		}
		return nil, ErrNoBackend
	}
	if len(cands) == 1 {
		return cands[0], nil
	}
	switch u.cfg.Balance {
	case LeastConn:
		return u.leastConn(cands), nil
	case Random:
		return randomTwo(cands), nil
	case IPHash:
		return rendezvous(cands, clientKey(req)), nil
	case URIHash:
		return rendezvous(cands, req.URL.Path), nil
	}
	return u.roundRobin(cands), nil
}

// roundRobin is nginx's smooth weighted round robin: each round every
// candidate gains its weight, the richest one is chosen and pays the total.
// Weights 5:1:1 give a, a, b, a, c, a, a rather than five a in a row.
func (u *Upstream) roundRobin(cands []*backend) *backend {
	u.mu.Lock()
	defer u.mu.Unlock()
	total := 0
	var best *backend
	for _, b := range cands {
		b.current += b.spec.Weight
		total += b.spec.Weight
		if best == nil || b.current > best.current {
			best = b
		}
	}
	best.current -= total
	return best
}

// leastConn picks the backend with the fewest active requests per unit of
// weight; ties go round robin.
func (u *Upstream) leastConn(cands []*backend) *backend {
	u.mu.Lock()
	u.counter++
	start := int(u.counter % uint64(len(cands)))
	u.mu.Unlock()
	var best *backend
	var bestLoad float64
	for i := range cands {
		b := cands[(start+i)%len(cands)]
		load := float64(b.state.Load()) / float64(b.spec.Weight)
		if best == nil || load < bestLoad {
			best, bestLoad = b, load
		}
	}
	return best
}

// randomTwo is "power of two choices": two random candidates, the less loaded one.
func randomTwo(cands []*backend) *backend {
	a := cands[rand.IntN(len(cands))]
	b := cands[rand.IntN(len(cands))]
	if float64(b.state.Load())/float64(b.spec.Weight) < float64(a.state.Load())/float64(a.spec.Weight) {
		return b
	}
	return a
}

// clientKey is what ip_hash hashes: the client address LOTUS resolved, which
// the proxy puts in X-Forwarded-For before the request gets here.
func clientKey(req *http.Request) string {
	xff := req.Header.Get("X-Forwarded-For")
	if i := strings.IndexByte(xff, ','); i >= 0 {
		xff = xff[:i]
	}
	if xff != "" {
		return strings.TrimSpace(xff)
	}
	return req.RemoteAddr
}

// rendezvous is weighted rendezvous hashing: for a key every backend gets a
// score, the highest wins. When a backend goes away only its own keys move.
func rendezvous(cands []*backend, key string) *backend {
	var best *backend
	bestScore := math.Inf(-1)
	for _, b := range cands {
		h := fnv.New64a()
		h.Write([]byte(key))
		h.Write([]byte{0})
		h.Write([]byte(b.spec.Address))
		// uniform in (0,1]; score = -weight / ln(u) grows with weight
		u := (float64(h.Sum64()>>11) + 1) / (1 << 53)
		score := -float64(b.spec.Weight) / math.Log(u)
		if score > bestScore {
			best, bestScore = b, score
		}
	}
	return best
}
