package apod

import (
	"context"

	"github.com/magic-peach/NASA-APOD-Explorer/internal/cache"
)

// Fetcher is anything that can fetch APOD entries. Both HTTPClient and
// CachedClient satisfy it, so they can be stacked (decorator pattern).
type Fetcher interface {
	Fetch(ctx context.Context, q Query) ([]Item, error)
}

// CachedClient wraps another Fetcher and caches successful responses by
// query key, so repeated identical requests skip the upstream call.
type CachedClient struct {
	next  Fetcher
	cache *cache.Cache[string, []Item]
}

// NewCachedClient returns a Fetcher that serves repeated queries from c.
func NewCachedClient(next Fetcher, c *cache.Cache[string, []Item]) *CachedClient {
	return &CachedClient{next: next, cache: c}
}

// Fetch returns a cached result when available, otherwise calls the wrapped
// Fetcher and caches what it returns. Errors are never cached.
func (c *CachedClient) Fetch(ctx context.Context, q Query) ([]Item, error) {
	key := q.Key()
	if items, ok := c.cache.Get(key); ok {
		return items, nil
	}
	items, err := c.next.Fetch(ctx, q)
	if err != nil {
		return nil, err
	}
	c.cache.Set(key, items)
	return items, nil
}
