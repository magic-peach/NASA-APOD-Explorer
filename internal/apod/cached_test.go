package apod

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/cache"
)

// slowUpstream is a fake Fetcher that counts calls and takes `delay` to
// answer. The delay keeps the first call "in flight" long enough for every
// other goroutine to arrive while it is still running, which is exactly
// the window in which a cache stampede happens.
type slowUpstream struct {
	calls atomic.Int64 // atomic: incremented from many goroutines at once
	delay time.Duration
	err   error
}

func (u *slowUpstream) Fetch(ctx context.Context, q Query) ([]Item, error) {
	u.calls.Add(1)
	select {
	case <-time.After(u.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if u.err != nil {
		return nil, u.err
	}
	return []Item{{Date: q.Date, Title: "Pillars of Creation"}}, nil
}

// naiveCachedClient is the "before" picture: a cache with no request
// coalescing (check the cache, on a miss call upstream, then store). It
// exists only in tests, as a baseline to show the stampede singleflight
// prevents.
type naiveCachedClient struct {
	next  Fetcher
	cache *cache.Cache[string, []Item]
}

func (c *naiveCachedClient) Fetch(ctx context.Context, q Query) ([]Item, error) {
	if items, ok := c.cache.Get(q.Key()); ok {
		return items, nil
	}
	items, err := c.next.Fetch(ctx, q)
	if err != nil {
		return nil, err
	}
	c.cache.Set(q.Key(), items)
	return items, nil
}

// fireConcurrently starts n goroutines that call f.Fetch for the same query
// at the same instant, waits for all of them, and returns each result.
func fireConcurrently(t testing.TB, f Fetcher, n int) [][]Item {
	t.Helper()
	q := Query{Date: "2024-01-01"}
	results := make([][]Item, n)
	errs := make([]error, n)

	start := make(chan struct{}) // closed to release every goroutine together
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Each goroutine writes only its own index, so no lock is needed.
			results[i], errs[i] = f.Fetch(context.Background(), q)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	return results
}

// TestSingleflightPreventsStampede is the key concurrency test: 100
// simultaneous requests for an uncached key must produce exactly ONE
// upstream call, and every caller must receive that same result.
func TestSingleflightPreventsStampede(t *testing.T) {
	const n = 100
	upstream := &slowUpstream{delay: 50 * time.Millisecond}
	client := NewCachedClient(upstream, cache.New[string, []Item](time.Minute))

	results := fireConcurrently(t, client, n)

	if got := upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream called %d times for %d concurrent requests, want exactly 1", got, n)
	}
	for i, items := range results {
		if len(items) != 1 || items[0].Title != "Pillars of Creation" {
			t.Fatalf("goroutine %d got %+v", i, items)
		}
		// Same backing array means it is literally the one shared result.
		if &items[0] != &results[0][0] {
			t.Fatalf("goroutine %d got a different result instance", i)
		}
	}

	// A follow-up request is a plain cache hit.
	fireConcurrently(t, client, 1)
	if got := upstream.calls.Load(); got != 1 {
		t.Errorf("cache hit still called upstream: %d calls", got)
	}
}

// TestNaiveCacheStampedes proves the problem is real: under the same load,
// a cache without singleflight calls upstream many times.
func TestNaiveCacheStampedes(t *testing.T) {
	const n = 100
	upstream := &slowUpstream{delay: 50 * time.Millisecond}
	client := &naiveCachedClient{next: upstream, cache: cache.New[string, []Item](time.Minute)}

	fireConcurrently(t, client, n)

	got := upstream.calls.Load()
	if got <= 1 {
		t.Fatalf("expected a stampede (many upstream calls), got %d", got)
	}
	t.Logf("without singleflight: %d upstream calls for %d concurrent requests", got, n)
}

func TestCallerCancellationDoesNotCancelSharedFetch(t *testing.T) {
	upstream := &slowUpstream{delay: 50 * time.Millisecond}
	client := NewCachedClient(upstream, cache.New[string, []Item](time.Minute))
	q := Query{Date: "2024-01-01"}

	// The first caller starts the flight and then gives up almost at once.
	impatient, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	firstErr := make(chan error, 1)
	go func() {
		_, err := client.Fetch(impatient, q)
		firstErr <- err
	}()
	time.Sleep(time.Millisecond) // let the first caller start the flight

	// A patient caller joins the same flight and must still succeed.
	items, err := client.Fetch(context.Background(), q)
	if err != nil {
		t.Fatalf("patient caller failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("patient caller got %+v", items)
	}
	if err := <-firstErr; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("impatient caller err = %v, want deadline exceeded", err)
	}
	if got := upstream.calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	upstream := &slowUpstream{err: errors.New("nasa is down")}
	client := NewCachedClient(upstream, cache.New[string, []Item](time.Minute))
	q := Query{Date: "2024-01-01"}

	for range 2 {
		if _, err := client.Fetch(context.Background(), q); err == nil {
			t.Fatal("expected an error")
		}
	}
	if got := upstream.calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (a failure must not be cached)", got)
	}
}

func TestRandomCountQueriesBypassCache(t *testing.T) {
	upstream := &slowUpstream{}
	client := NewCachedClient(upstream, cache.New[string, []Item](time.Minute))

	for range 3 {
		if _, err := client.Fetch(context.Background(), Query{Count: 6}); err != nil {
			t.Fatal(err)
		}
	}
	if got := upstream.calls.Load(); got != 3 {
		t.Errorf("upstream calls = %d, want 3 (each ?count= must be fresh)", got)
	}
}

// BenchmarkStampede compares a burst of 100 concurrent requests for one
// uncached key with and without singleflight. The interesting number is
// the custom "upstream-calls/op" metric, not ns/op.
func BenchmarkStampede(b *testing.B) {
	const n = 100
	run := func(b *testing.B, newClient func(Fetcher) Fetcher) {
		var total int64
		for b.Loop() {
			upstream := &slowUpstream{delay: time.Millisecond}
			fireConcurrently(b, newClient(upstream), n)
			total += upstream.calls.Load()
		}
		b.ReportMetric(float64(total)/float64(b.N), "upstream-calls/op")
	}

	b.Run("with_singleflight", func(b *testing.B) {
		run(b, func(up Fetcher) Fetcher {
			return NewCachedClient(up, cache.New[string, []Item](time.Minute))
		})
	})
	b.Run("cache_only", func(b *testing.B) {
		run(b, func(up Fetcher) Fetcher {
			return &naiveCachedClient{next: up, cache: cache.New[string, []Item](time.Minute)}
		})
	})
}
