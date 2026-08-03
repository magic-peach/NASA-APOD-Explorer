// Package cache provides a small in-memory key/value cache with expiry.
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value   V
	expires time.Time
}

// Cache maps keys to values that expire after a fixed TTL.
// It is safe for concurrent use.
type Cache[K comparable, V any] struct {
	mu    sync.RWMutex
	ttl   time.Duration
	items map[K]entry[V]
}

// New returns an empty cache whose entries live for ttl.
func New[K comparable, V any](ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{ttl: ttl, items: make(map[K]entry[V])}
}

// Get returns the value for key if it is present and not expired.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores value under key, replacing any previous value.
func (c *Cache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	c.items[key] = entry[V]{value: value, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}
