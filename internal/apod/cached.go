package apod

import (
	"context"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/cache"
)

// Fetcher is anything that can fetch APOD entries. Both HTTPClient and
// CachedClient satisfy it, so they can be stacked (decorator pattern).
type Fetcher interface {
	Fetch(ctx context.Context, q Query) ([]Item, error)
}

// DefaultFetchTimeout bounds a single upstream call made on behalf of
// (possibly many) waiting requests.
const DefaultFetchTimeout = 10 * time.Second

// CachedClient wraps another Fetcher with a TTL cache and request
// coalescing.
//
// # The problem: cache stampede
//
// When a popular key is missing from the cache (first request, or right
// after it expires), every request that arrives before the first upstream
// call returns also sees a miss, and each one calls NASA. 100 concurrent
// requests mean 100 identical upstream calls: wasted latency, and on
// DEMO_KEY a quick way to hit the rate limit.
//
// # The fix: singleflight
//
// singleflight.Group runs at most one function per key at a time. The first
// caller for a key starts the fetch; callers arriving while it is in flight
// wait for that same result instead of starting their own. So 100
// concurrent misses produce exactly one upstream call.
type CachedClient struct {
	next  Fetcher
	cache *cache.Cache[string, []Item]
	group singleflight.Group

	// FetchTimeout bounds each shared upstream call. Defaults to
	// DefaultFetchTimeout.
	FetchTimeout time.Duration
}

// NewCachedClient returns a Fetcher that caches and coalesces calls to next.
func NewCachedClient(next Fetcher, c *cache.Cache[string, []Item]) *CachedClient {
	return &CachedClient{next: next, cache: c, FetchTimeout: DefaultFetchTimeout}
}

// Fetch returns cached entries when possible and otherwise performs (or
// joins) a single upstream fetch for the query.
//
// The returned slice may be shared with other callers and the cache, so
// callers must treat it as read-only.
func (c *CachedClient) Fetch(ctx context.Context, q Query) ([]Item, error) {
	// Random queries (?count=N) must never be cached or shared: each call
	// is supposed to return a different random set. Caching them made
	// infinite scroll show the same images over and over.
	if q.Count > 0 {
		return c.next.Fetch(ctx, q)
	}

	key := q.Key()
	if items, ok := c.cache.Get(key); ok {
		return items, nil // fast path: no locking beyond the cache's RLock
	}

	// DoChan (rather than Do) returns a channel, so each waiter can still
	// give up on its own context while the shared fetch carries on.
	ch := c.group.DoChan(key, func() (any, error) {
		// Re-check the cache inside the flight. Without this, a caller that
		// missed the cache just before a previous flight finished (and was
		// removed from the group) would start a second, redundant fetch.
		if items, ok := c.cache.Get(key); ok {
			return items, nil
		}

		// The fetch is shared by every waiter, so it must not die just
		// because the *first* caller's request was cancelled (e.g. that
		// user closed the tab). context.WithoutCancel keeps the request's
		// values but drops its cancellation; the timeout bounds the call.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.FetchTimeout)
		defer cancel()

		items, err := c.next.Fetch(fetchCtx, q)
		if err != nil {
			return nil, err // errors are not cached: the next request retries
		}
		c.cache.Set(key, items)
		return items, nil
	})

	select {
	case <-ctx.Done():
		// This caller gave up; the shared fetch keeps running for others.
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.([]Item), nil
	}
}
