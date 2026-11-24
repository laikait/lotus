package ratelimit

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func TestBucketBurstThenRate(t *testing.T) {
	l := NewLimiter(10, time.Second, 5) // 10 per second, bursts of 5
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("a", t0); !ok {
			t.Fatalf("request %d of the burst was refused", i)
		}
	}
	ok, wait := l.Allow("a", t0)
	if ok {
		t.Fatal("the 6th request at the same instant was allowed")
	}
	if wait < 90*time.Millisecond || wait > 110*time.Millisecond {
		t.Errorf("retry after %v, want about 100ms", wait)
	}
	if ok, _ := l.Allow("a", t0.Add(99*time.Millisecond)); ok {
		t.Error("allowed before a token was back")
	}
	if ok, _ := l.Allow("a", t0.Add(101*time.Millisecond)); !ok {
		t.Error("refused after a token was back")
	}
	if ok, _ := l.Allow("b", t0); !ok {
		t.Error("another key shares the first key's bucket")
	}
	// A long pause refills to the burst, not beyond.
	later := t0.Add(time.Hour)
	n := 0
	for ; n < 20; n++ {
		if ok, _ := l.Allow("a", later); !ok {
			break
		}
	}
	if n != 5 {
		t.Errorf("after a long pause %d requests were allowed, want the burst of 5", n)
	}
}

func TestBucketSteadyRate(t *testing.T) {
	l := NewLimiter(60, time.Minute, 1) // one per second
	allowed := 0
	for i := 0; i < 600; i++ { // 60 seconds at 10 attempts per second
		if ok, _ := l.Allow("k", t0.Add(time.Duration(i)*100*time.Millisecond)); ok {
			allowed++
		}
	}
	if allowed < 59 || allowed > 61 {
		t.Errorf("%d allowed in 60 seconds at 1 per second", allowed)
	}
}

func TestBucketMemoryIsBounded(t *testing.T) {
	l := NewLimiter(1, time.Hour, 1)
	for i := 0; i < 5*maxKeys; i++ {
		l.Allow(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255), t0.Add(time.Duration(i)*time.Microsecond))
	}
	if n := l.Len(); n > maxKeys {
		t.Errorf("%d keys held, limit %d", n, maxKeys)
	}
}

func TestIdleBucketsAreForgotten(t *testing.T) {
	l := NewLimiter(10, time.Second, 10)
	for i := 0; i < 100; i++ {
		l.Allow(fmt.Sprintf("k%d", i), t0)
	}
	// Later activity sweeps what has been idle long enough to be full again.
	later := t0.Add(time.Minute)
	for i := 0; i < sweepEvery*shards*4; i++ { // enough to reach every shard's sweep
		l.Allow(fmt.Sprintf("busy%d", i%2000), later)
	}
	for i := 0; i < 100; i++ {
		l.sh[shardOf(fmt.Sprintf("k%d", i))].mu.Lock()
		_, kept := l.sh[shardOf(fmt.Sprintf("k%d", i))].m[fmt.Sprintf("k%d", i)]
		l.sh[shardOf(fmt.Sprintf("k%d", i))].mu.Unlock()
		if kept {
			t.Errorf("idle key k%d was kept", i)
		}
	}
}

func TestIPv6IsGroupedBy64(t *testing.T) {
	a := Key(netip.MustParseAddr("2001:db8:1:2::1"))
	b := Key(netip.MustParseAddr("2001:db8:1:2:ffff:ffff:ffff:ffff"))
	c := Key(netip.MustParseAddr("2001:db8:1:3::1"))
	if a != b || a == c {
		t.Errorf("%s %s %s", a, b, c)
	}
	if Key(netip.MustParseAddr("::ffff:192.0.2.1")) != "192.0.2.1" {
		t.Error("a mapped IPv4 address must key as IPv4")
	}
	if Key(netip.Addr{}) != "" {
		t.Error("an invalid address has no key")
	}
}

func TestBucketIsSafeUnderConcurrency(t *testing.T) {
	l := NewLimiter(1000, time.Second, 1000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := 0
			for i := 0; i < 500; i++ {
				if ok, _ := l.Allow("shared", t0); ok {
					n++
				}
			}
			mu.Lock()
			allowed += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	if allowed != 1000 {
		t.Errorf("%d allowed at one instant with a burst of 1000", allowed)
	}
}

func TestLockout(t *testing.T) {
	l := NewLockout(3, time.Minute, 10*time.Minute)
	if locked, _ := l.Locked("a", t0); locked {
		t.Fatal("locked with no failures")
	}
	l.Fail("a", t0)
	l.Fail("a", t0.Add(time.Second))
	if locked, _ := l.Locked("a", t0.Add(2*time.Second)); locked {
		t.Fatal("locked after 2 of 3 failures")
	}
	l.Fail("a", t0.Add(2*time.Second))
	locked, left := l.Locked("a", t0.Add(3*time.Second))
	if !locked || left < 9*time.Minute {
		t.Fatalf("not locked after 3 failures: %v %v", locked, left)
	}
	if locked, _ := l.Locked("b", t0.Add(3*time.Second)); locked {
		t.Error("another key is locked")
	}
	if locked, _ := l.Locked("a", t0.Add(11*time.Minute)); locked {
		t.Error("still locked after the lock-out")
	}
	// Failures outside the window don't add up.
	for i := 0; i < 10; i++ {
		l.Fail("slow", t0.Add(time.Duration(i)*2*time.Minute))
	}
	if locked, _ := l.Locked("slow", t0.Add(20*time.Minute)); locked {
		t.Error("slow failures locked a key out")
	}
	// A success forgets earlier failures.
	l.Fail("c", t0)
	l.Fail("c", t0)
	l.Reset("c")
	l.Fail("c", t0)
	if locked, _ := l.Locked("c", t0); locked {
		t.Error("failures before a success counted")
	}
}

func TestNilLimitersNeverRefuse(t *testing.T) {
	var l *Lockout
	l.Fail("x", t0)
	l.Reset("x")
	if locked, _ := l.Locked("x", t0); locked || NewLockout(0, time.Second, time.Second) != nil {
		t.Error("a nil lock-out locks")
	}
	var p *PerIP
	if rel, ok := p.Acquire("x"); !ok {
		t.Error("a nil PerIP refuses")
	} else {
		rel()
	}
	if NewPerIP(0) != nil {
		t.Error("max 0 must mean unlimited")
	}
}

func TestLockoutMemoryIsBounded(t *testing.T) {
	l := NewLockout(5, time.Hour, time.Hour)
	for i := 0; i < 3*maxKeys; i++ {
		l.Fail(fmt.Sprintf("k%d", i), t0.Add(time.Duration(i)*time.Microsecond))
	}
	if n := l.Len(); n > maxKeys {
		t.Errorf("%d keys held", n)
	}
}

func TestPerIP(t *testing.T) {
	p := NewPerIP(2)
	r1, ok1 := p.Acquire("a")
	r2, ok2 := p.Acquire("a")
	_, ok3 := p.Acquire("a")
	if !ok1 || !ok2 || ok3 {
		t.Fatalf("%v %v %v", ok1, ok2, ok3)
	}
	if _, ok := p.Acquire("b"); !ok {
		t.Error("another address is limited")
	}
	r1()
	r1() // a second release must not free a second slot
	if _, ok := p.Acquire("a"); !ok {
		t.Error("a freed slot was not reusable")
	}
	if _, ok := p.Acquire("a"); ok {
		t.Error("a double release freed two slots")
	}
	r2()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.m) > 2 {
		t.Errorf("table: %v", p.m)
	}
}
