package server

import (
	"container/list"
	"sync"
	"time"
)

// resultCache remembers responses by request id so retries are idempotent.
// It is bounded (LRU by insertion) and time-limited; error results expire
// quickly so a transient runner fault does not pin a bad answer for an hour.
type resultCache struct {
	mu       sync.Mutex
	max      int
	ttl      time.Duration
	errTTL   time.Duration
	now      func() time.Time
	order    *list.List
	entries  map[string]*list.Element
	lastSwep time.Time
}

type cacheEntry struct {
	id      string
	resp    *Response
	expires time.Time
}

func newResultCache(max int, ttl, errTTL time.Duration) *resultCache {
	return &resultCache{max: max, ttl: ttl, errTTL: errTTL, now: time.Now, order: list.New(), entries: map[string]*list.Element{}}
}

func (c *resultCache) get(id string) (*Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[id]
	if !ok {
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if !c.now().Before(e.expires) {
		c.remove(el)
		return nil, false
	}
	return e.resp, true
}

func (c *resultCache) put(id string, resp *Response) {
	ttl := c.ttl
	if resp.Status == StatusError {
		ttl = c.errTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[id]; ok {
		c.remove(el)
	}
	c.entries[id] = c.order.PushBack(&cacheEntry{id: id, resp: resp, expires: c.now().Add(ttl)})
	for c.order.Len() > c.max {
		c.remove(c.order.Front())
	}
	if now := c.now(); now.Sub(c.lastSwep) > time.Minute {
		c.lastSwep = now
		c.sweepLocked()
	}
}

// sweep drops every expired entry; put also sweeps at most once a minute.
func (c *resultCache) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked()
}

func (c *resultCache) sweepLocked() {
	now := c.now()
	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if !now.Before(el.Value.(*cacheEntry).expires) {
			c.remove(el)
		}
		el = next
	}
}

func (c *resultCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// caller holds mu
func (c *resultCache) remove(el *list.Element) {
	delete(c.entries, el.Value.(*cacheEntry).id)
	c.order.Remove(el)
}
