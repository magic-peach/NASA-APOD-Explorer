// Package cache provides a small in-memory key/value cache with per-entry
// expiry, safe for concurrent use.
//
// Expired entries are invisible to Get immediately, but their memory is only
// reclaimed when DeleteExpired runs. RunJanitor calls it on a timer, so a
// long-running server doesn't slowly fill up with dead entries.
package cache

import (
	"context"
	"sync"
	"time"
)

type entry[V any] struct {
	value   V
	expires time.Time
}

// Cache maps keys to values, each with its own expiry time.
//
// A sync.RWMutex guards the map: any number of readers can hold the read
// lock at once (cache hits don't block each other), while writers take the
// exclusive lock. Go maps are not safe for concurrent writes, so without the
// mutex concurrent requests would crash the program with "concurrent map
// writes".
type Cache[K comparable, V any] struct {
	mu         sync.RWMutex
	items      map[K]entry[V]
	defaultTTL time.Duration

	// now returns the current time. It is a field rather than a direct
	// time.Now call so tests can control the clock and check expiry
	// deterministically, without sleeping.
	now func() time.Time
}

// New returns an empty cache whose entries live for defaultTTL unless Set
// is given a different TTL.
func New[K comparable, V any](defaultTTL time.Duration) *Cache[K, V] {
	return &Cache[K, V]{
		items:      make(map[K]entry[V]),
		defaultTTL: defaultTTL,
		now:        time.Now,
	}
}

// Get returns the value for key if it is present and not expired.
//
// It only takes the read lock, so it never deletes an expired entry itself;
// that would need the write lock and make every read contend with writers.
// Cleanup is left to DeleteExpired / RunJanitor.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()

	if !ok || !c.now().Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores value under key using the default TTL.
func (c *Cache[K, V]) Set(key K, value V) {
	c.SetWithTTL(key, value, c.defaultTTL)
}

// SetWithTTL stores value under key, expiring after ttl.
func (c *Cache[K, V]) SetWithTTL(key K, value V, ttl time.Duration) {
	expires := c.now().Add(ttl)
	c.mu.Lock()
	c.items[key] = entry[V]{value: value, expires: expires}
	c.mu.Unlock()
}

// Delete removes key, if present.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

// Len returns the number of stored entries, including expired ones that
// have not been evicted yet.
func (c *Cache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// DeleteExpired evicts every expired entry and reports how many it removed.
// (Deleting from a map while ranging over it is explicitly allowed in Go.)
func (c *Cache[K, V]) DeleteExpired() int {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	for k, e := range c.items {
		if !now.Before(e.expires) {
			delete(c.items, k)
			removed++
		}
	}
	return removed
}

// RunJanitor evicts expired entries every interval until ctx is cancelled.
// It blocks, so start it with `go c.RunJanitor(ctx, time.Minute)`; leaving
// the goroutine to the caller makes its lifetime explicit and lets the
// caller wait for it during shutdown.
func (c *Cache[K, V]) RunJanitor(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop() // release the ticker's resources when we return

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.DeleteExpired()
		}
	}
}
