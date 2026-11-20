// Package cache provides a small generic in-process TTL + LRU cache.
package cache

import (
	"container/list"
	"sync"
	"time"
)

// Clock returns the current time. Injectable for tests.
type Clock func() time.Time

type entry[K comparable, V any] struct {
	key     K
	value   V
	expires time.Time
}

// TTL is a concurrency-safe cache whose entries expire after a TTL and which
// evicts the least recently used entry once maxEntries is exceeded.
type TTL[K comparable, V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	now   Clock
	items map[K]*list.Element
	order *list.List // front = most recently used
}

// New creates a cache. ttl <= 0 means entries never expire; maxEntries <= 0
// means unbounded.
func New[K comparable, V any](ttl time.Duration, maxEntries int) *TTL[K, V] {
	return NewWithClock[K, V](ttl, maxEntries, time.Now)
}

// NewWithClock is New with an injectable clock.
func NewWithClock[K comparable, V any](ttl time.Duration, maxEntries int, now Clock) *TTL[K, V] {
	if now == nil {
		now = time.Now
	}
	return &TTL[K, V]{
		ttl:   ttl,
		max:   maxEntries,
		now:   now,
		items: make(map[K]*list.Element),
		order: list.New(),
	}
}

// Get returns the value for key if present and not expired.
func (c *TTL[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	if c.expired(e) {
		c.removeElement(el)
		var zero V
		return zero, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

// Set stores a value using the cache's default TTL.
func (c *TTL[K, V]) Set(key K, value V) { c.SetWithTTL(key, value, c.ttl) }

// SetWithTTL stores a value with a specific TTL (<= 0 = never expires).
func (c *TTL[K, V]) SetWithTTL(key K, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var exp time.Time
	if ttl > 0 {
		exp = c.now().Add(ttl)
	}
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry[K, V])
		e.value, e.expires = value, exp
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&entry[K, V]{key: key, value: value, expires: exp})
	if c.max > 0 {
		for c.order.Len() > c.max {
			c.evictOldest()
		}
	}
}

// Delete removes a key.
func (c *TTL[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
}

// Len returns the number of stored entries (may include expired, not yet reaped ones).
func (c *TTL[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Purge removes all entries.
func (c *TTL[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[K]*list.Element)
	c.order.Init()
}

func (c *TTL[K, V]) expired(e *entry[K, V]) bool {
	return !e.expires.IsZero() && !c.now().Before(e.expires)
}

func (c *TTL[K, V]) evictOldest() {
	// Prefer dropping an expired entry from the cold end; otherwise the LRU one.
	if el := c.order.Back(); el != nil {
		c.removeElement(el)
	}
}

func (c *TTL[K, V]) removeElement(el *list.Element) {
	e := el.Value.(*entry[K, V])
	delete(c.items, e.key)
	c.order.Remove(el)
}
