package server

import (
	"testing"
	"time"

	"github.com/laikait/lotus/internal/config/model"
)

func TestTrafficCountsWhatWasServed(t *testing.T) {
	root := docroot(t, "hello traffic")
	cfg := baseConfig(siteConf("a.test", root, "http"), siteConf("b.test", root, "http"))
	cfg.Listeners = append(cfg.Listeners, model.Listener{Name: "empty", Address: "127.0.0.1:0"})
	h := start(t, cfg)
	base := "http://" + h.addr("http")

	before := h.srv.Traffic()
	for i := 0; i < 3; i++ {
		get200(t, base+"/", "a.test")
	}
	get200(t, base+"/nope", "a.test")                         // 404
	get200(t, "http://"+h.addr("empty")+"/", "anything.test") // no site answers
	after := h.srv.Traffic()

	if len(after.Sites) != 2 || after.Sites[0].Name != "a.test" || after.Sites[1].Name != "b.test" {
		t.Fatalf("sites: %+v", after.Sites)
	}
	a, b := after.Sites[0], after.Sites[1]
	if a.Requests != 4 || a.Class[1] != 3 || a.Class[3] != 1 {
		t.Errorf("a.test: %+v (want 4 requests: three 2xx, one 4xx)", a)
	}
	if a.Bytes < 3*uint64(len("hello traffic")) {
		t.Errorf("a.test bytes = %d", a.Bytes)
	}
	// The latency sum is not checked: on Windows the clock is coarse enough
	// that a fast request can be measured as 0 ns.
	if a.LatencySum < 0 {
		t.Errorf("a.test latency sum = %v", a.LatencySum)
	}
	if b.Requests != 0 {
		t.Errorf("b.test served nothing, got %+v", b)
	}
	if after.Unrouted.Requests != 1 || after.Unrouted.Name != "" {
		t.Errorf("unrouted: %+v", after.Unrouted)
	}
	if after.ConnectionLimit != model.DefaultLimits().MaxConnections {
		t.Errorf("connection limit = %d", after.ConnectionLimit)
	}
	if !after.At.After(before.At) && !after.At.Equal(before.At) {
		t.Error("the snapshot has no time")
	}
	if after.Uptime < 0 || after.Uptime > time.Minute {
		t.Errorf("uptime = %v", after.Uptime)
	}
	// Totals only grow, so a rate is the difference of two snapshots.
	if before.Sites[0].Requests > after.Sites[0].Requests {
		t.Error("a total went down")
	}
	if got := ClassNames(); len(got) != 6 || got[1] != "2xx" {
		t.Errorf("class names: %v", got)
	}
}
