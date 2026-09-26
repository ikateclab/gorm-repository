// Package memory implements an in-process LRU cache that satisfies the
// plugin.Cache interface. It is intended for tests and single-node
// deployments; production setups should use a shared backend such as
// Redis.
package memory

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// Cache is a bounded, goroutine-safe in-memory cache with tag-based
// invalidation.
type Cache struct {
	mu    sync.Mutex
	max   int
	items map[string]*list.Element        // key -> element holding *entry
	order *list.List                      // LRU order; front = most recently used
	tags  map[string]map[string]struct{}  // tag -> set of keys
	now   func() time.Time
}

type entry struct {
	key     string
	value   []byte
	tags    []string
	expires time.Time // zero means no expiry
}

// Option tunes cache behavior.
type Option func(*Cache)

// WithMaxEntries sets the maximum number of entries before LRU eviction
// kicks in. A non-positive value disables eviction (unbounded — caller
// beware).
func WithMaxEntries(n int) Option { return func(c *Cache) { c.max = n } }

// WithClock injects a clock for deterministic tests.
func WithClock(fn func() time.Time) Option { return func(c *Cache) { c.now = fn } }

// New constructs a new Cache. The default capacity is 1000 entries.
func New(opts ...Option) *Cache {
	c := &Cache{
		max:   1000,
		items: make(map[string]*list.Element),
		order: list.New(),
		tags:  make(map[string]map[string]struct{}),
		now:   time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Get returns the cached bytes for key if present and unexpired. The
// returned slice is a copy and is safe to mutate.
func (c *Cache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*entry)
	if !e.expires.IsZero() && c.now().After(e.expires) {
		c.removeLocked(el)
		return nil, false, nil
	}
	c.order.MoveToFront(el)
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true, nil
}

// Set stores value under key, links it to the given tags, and applies
// an optional TTL (zero means no expiry).
func (c *Cache) Set(_ context.Context, key string, value []byte, tags []string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
	buf := make([]byte, len(value))
	copy(buf, value)
	e := &entry{
		key:   key,
		value: buf,
		tags:  append([]string(nil), tags...),
	}
	if ttl > 0 {
		e.expires = c.now().Add(ttl)
	}
	el := c.order.PushFront(e)
	c.items[key] = el
	for _, t := range tags {
		set, ok := c.tags[t]
		if !ok {
			set = make(map[string]struct{})
			c.tags[t] = set
		}
		set[key] = struct{}{}
	}
	c.evictIfNeededLocked()
	return nil
}

// Invalidate removes every entry tagged with any of the given tags.
func (c *Cache) Invalidate(_ context.Context, tags []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range tags {
		keys := c.tags[t]
		for k := range keys {
			if el, ok := c.items[k]; ok {
				c.removeLocked(el)
			}
		}
		// removeLocked already cleans up the tag set when emptied, but
		// be explicit in case the tag was orphaned (no entries).
		delete(c.tags, t)
	}
	return nil
}

// Len returns the current entry count. Primarily for tests.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Tags returns the number of distinct tags currently tracked. Primarily
// for tests.
func (c *Cache) Tags() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tags)
}

func (c *Cache) removeLocked(el *list.Element) {
	e := el.Value.(*entry)
	c.order.Remove(el)
	delete(c.items, e.key)
	for _, t := range e.tags {
		if set, ok := c.tags[t]; ok {
			delete(set, e.key)
			if len(set) == 0 {
				delete(c.tags, t)
			}
		}
	}
}

func (c *Cache) evictIfNeededLocked() {
	if c.max <= 0 {
		return
	}
	for c.order.Len() > c.max {
		el := c.order.Back()
		if el == nil {
			return
		}
		c.removeLocked(el)
	}
}
