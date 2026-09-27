package server

import (
	"container/list"
	"sync"
	"time"
)

// resultCache remembers responses by request id so retries are idempotent.
// It is bounded by entry count and by bytes (oldest insertion evicted first)
// and time-limited; error results expire quickly so a transient runner fault
// does not pin a bad answer for an hour.
type resultCache struct {
	mu       sync.Mutex
	max      int
	maxBytes int64
	ttl      time.Duration
	errTTL   time.Duration
	now      func() time.Time
	order    *list.List
	entries  map[string]*list.Element
	size     int64
	lastSwep time.Time
}

type cacheEntry struct {
	id      string
	resp    *Response
	size    int64
	expires time.Time
}

// newResultCache bounds the cache to max entries and maxBytes of accounted
// response size; either at or below zero means unbounded on that axis.
func newResultCache(max int, maxBytes int64, ttl, errTTL time.Duration) *resultCache {
	return &resultCache{max: max, maxBytes: maxBytes, ttl: ttl, errTTL: errTTL, now: time.Now, order: list.New(), entries: map[string]*list.Element{}}
}

// responseSize is the memory a cached response is charged for: every string
// it holds plus a fixed share for the structs and the cache's own bookkeeping.
// It is a cheap sum rather than the JSON length, which would cost an
// encoding per put; the two agree to within the escaping.
func responseSize(resp *Response) int64 {
	const entryOverhead, resultOverhead = 256, 96
	n := int64(entryOverhead + len(resp.ID) + len(resp.Status) + len(resp.CompileOutput))
	for i := range resp.Results {
		r := &resp.Results[i]
		n += int64(resultOverhead + len(r.TestID) + len(r.Status) + len(r.StdoutHash) + len(r.StdoutTail) + len(r.StderrTail))
	}
	return n
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
	e := &cacheEntry{id: id, resp: resp, size: responseSize(resp), expires: c.now().Add(ttl)}
	c.entries[id] = c.order.PushBack(e)
	c.size += e.size
	// Oldest first, but never the entry just stored: a response larger than
	// the whole budget is still the answer a retry must find.
	for c.order.Len() > 1 && (c.max > 0 && c.order.Len() > c.max || c.maxBytes > 0 && c.size > c.maxBytes) {
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
	// An idle runner should hold nothing: a Go map never shrinks, so once
	// the last entry is gone the map itself is replaced and its buckets
	// released along with the responses they pointed to.
	if c.order.Len() == 0 {
		c.entries = map[string]*list.Element{}
		c.order.Init()
		c.size = 0
	}
}

func (c *resultCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// bytes is the accounted size of everything cached.
func (c *resultCache) bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}

// remove drops el and lets go of its response: the element is cleared so a
// stale reference to it cannot keep the response alive. Caller holds mu.
func (c *resultCache) remove(el *list.Element) {
	e := el.Value.(*cacheEntry)
	delete(c.entries, e.id)
	c.order.Remove(el)
	c.size -= e.size
	e.resp = nil
	el.Value = nil
}
