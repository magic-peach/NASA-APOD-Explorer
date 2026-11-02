package cache

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock, so expiry tests are instant and
// deterministic instead of relying on time.Sleep.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// newTestCache returns a cache driven by a fake clock.
func newTestCache(ttl time.Duration) (*Cache[string, int], *fakeClock) {
	clock := &fakeClock{t: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := New[string, int](ttl)
	c.now = clock.Now
	return c, clock
}

func TestCacheGetSetExpiry(t *testing.T) {
	type step struct {
		advance time.Duration // move the clock forward first
		set     bool          // then optionally Set key "k" ...
		ttl     time.Duration // ... with this TTL (0 = default)
		value   int
		wantOK  bool // what Get("k") should report afterwards
		wantVal int
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name:  "missing key is a miss",
			steps: []step{{wantOK: false}},
		},
		{
			name:  "fresh value is a hit",
			steps: []step{{set: true, value: 1, wantOK: true, wantVal: 1}},
		},
		{
			name: "value is still valid just before expiry",
			steps: []step{
				{set: true, value: 1, wantOK: true, wantVal: 1},
				{advance: time.Minute - time.Nanosecond, wantOK: true, wantVal: 1},
			},
		},
		{
			name: "value expires exactly at its TTL",
			steps: []step{
				{set: true, value: 1, wantOK: true, wantVal: 1},
				{advance: time.Minute, wantOK: false},
			},
		},
		{
			name: "per-entry TTL overrides the default",
			steps: []step{
				{set: true, value: 1, ttl: 5 * time.Second, wantOK: true, wantVal: 1},
				{advance: 5 * time.Second, wantOK: false},
			},
		},
		{
			name: "overwriting refreshes value and expiry",
			steps: []step{
				{set: true, value: 1, wantOK: true, wantVal: 1},
				{advance: 50 * time.Second, set: true, value: 2, wantOK: true, wantVal: 2},
				{advance: 50 * time.Second, wantOK: true, wantVal: 2}, // 100s after first Set, 50s after second
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, clock := newTestCache(time.Minute)
			for i, s := range tt.steps {
				clock.Advance(s.advance)
				if s.set {
					if s.ttl > 0 {
						c.SetWithTTL("k", s.value, s.ttl)
					} else {
						c.Set("k", s.value)
					}
				}
				got, ok := c.Get("k")
				if ok != s.wantOK || got != s.wantVal {
					t.Fatalf("step %d: Get = (%d, %v), want (%d, %v)", i, got, ok, s.wantVal, s.wantOK)
				}
			}
		})
	}
}

func TestDeleteExpiredEvictsOnlyExpiredEntries(t *testing.T) {
	c, clock := newTestCache(time.Minute)
	c.SetWithTTL("short", 1, 10*time.Second)
	c.SetWithTTL("long", 2, time.Hour)

	clock.Advance(30 * time.Second)

	if removed := c.DeleteExpired(); removed != 1 {
		t.Fatalf("DeleteExpired removed %d entries, want 1", removed)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
	if _, ok := c.Get("long"); !ok {
		t.Error("unexpired entry was evicted")
	}
}

func TestExpiredEntryStaysUntilEvicted(t *testing.T) {
	c, clock := newTestCache(time.Minute)
	c.Set("k", 1)
	clock.Advance(2 * time.Minute)

	if _, ok := c.Get("k"); ok {
		t.Fatal("expired entry returned by Get")
	}
	// Get never deletes (it only holds the read lock), so memory is held
	// until the janitor runs. This documents that design choice.
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1 before eviction", c.Len())
	}
}

func TestJanitorEvictsAndStopsOnCancel(t *testing.T) {
	c, clock := newTestCache(time.Minute)
	c.Set("k", 1)
	clock.Advance(2 * time.Minute) // entry is now expired

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.RunJanitor(ctx, time.Millisecond)
		close(done) // signals that RunJanitor returned
	}()

	// Wait (bounded) for the janitor to evict the entry.
	deadline := time.Now().Add(2 * time.Second)
	for c.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("janitor did not evict the expired entry")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor goroutine did not stop after context cancel (goroutine leak)")
	}
}

// TestConcurrentAccess hammers the cache from many goroutines. It asserts
// little by itself; its job is to give the race detector (-race) something
// to catch if locking is ever broken.
func TestConcurrentAccess(t *testing.T) {
	c := New[string, int](time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.RunJanitor(ctx, time.Millisecond)

	var wg sync.WaitGroup
	for g := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				key := strconv.Itoa(i % 10)
				c.Set(key, g)
				c.Get(key)
				if i%50 == 0 {
					c.Delete(key)
				}
			}
		}()
	}
	wg.Wait()
}

// BenchmarkCacheGet measures the hot path: a cache hit.
func BenchmarkCacheGet(b *testing.B) {
	c := New[string, int](time.Hour)
	c.Set("k", 42)

	for b.Loop() { // b.Loop (Go 1.24) excludes the setup above from timing
		c.Get("k")
	}
}

// BenchmarkCacheGetParallel measures hits from many goroutines at once.
// Readers never block each other, but RLock still performs an atomic
// update on the mutex's shared reader count, so under heavy parallel reads
// that cache line bounces between CPU cores and ns/op rises compared with
// the single-goroutine benchmark. If this ever became a bottleneck, the
// usual fixes are sharding the map across several mutexes or sync.Map; for
// this app (a handful of keys, network-bound requests) it is irrelevant.
func BenchmarkCacheGetParallel(b *testing.B) {
	c := New[string, int](time.Hour)
	c.Set("k", 42)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Get("k")
		}
	})
}
