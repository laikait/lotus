package proxy

import (
	"fmt"
	"testing"
)

// The share of keys each backend wins must follow its weight, whatever the keys
// look like (client addresses that differ in their last digits, paths).
func TestRendezvousFollowsTheWeights(t *testing.T) {
	mk := func(addr string, w int) *backend {
		b := &backend{}
		b.spec.Address, b.spec.Weight = addr, w
		return b
	}
	cands := []*backend{mk("127.0.0.1:41001", 2), mk("127.0.0.1:41002", 1), mk("127.0.0.1:41003", 1)}
	for name, key := range map[string]func(int) string{
		"addresses": func(i int) string { return fmt.Sprintf("203.0.113.%d", i%256) + fmt.Sprintf(":%d", 1024+i/256) },
		"paths":     func(i int) string { return fmt.Sprintf("/item/%d", i) },
		"numbers":   func(i int) string { return fmt.Sprint(i) },
	} {
		wins := map[string]int{}
		const n = 20000
		for i := 0; i < n; i++ {
			wins[rendezvous(cands, key(i)).spec.Address]++
		}
		for _, c := range cands {
			want := float64(n) * float64(c.spec.Weight) / 4
			got := float64(wins[c.spec.Address])
			if got < want*0.9 || got > want*1.1 {
				t.Errorf("%s: %s won %.0f keys, want about %.0f (%v)", name, c.spec.Address, got, want, wins)
			}
		}
	}
}
