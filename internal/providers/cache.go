package providers

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultFetchTimeout bounds an upstream fetch started by CachedTravel.
const DefaultFetchTimeout = 2 * time.Second

// CacheStats counts how requests were served.
type CacheStats struct {
	Hits      int64 // answered from cache
	Misses    int64 // started an upstream fetch
	Coalesced int64 // joined a fetch another request had already started
}

// CachedTravel wraps a TravelProvider with an in-memory cache.
//
//   - Travel is symmetric, so (a, b) and (b, a) share one entry.
//   - Concurrent requests for the same missing pair share one upstream call.
//   - The upstream fetch is not tied to the request that triggered it. If that
//     request's deadline passes first, the fetch carries on (up to
//     FetchTimeout) and fills the cache, so the next request is a hit instead
//     of timing out again.
//   - Errors are never cached.
//
// Entries are keyed by area pair, so the cache is bounded by the number of
// areas squared and needs no eviction beyond expiry.
type CachedTravel struct {
	// FetchTimeout bounds each upstream fetch. Zero means DefaultFetchTimeout.
	FetchTimeout time.Duration

	next TravelProvider
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	entries  map[[2]string]cacheEntry
	inflight map[[2]string]*flight

	hits, misses, coalesced atomic.Int64
}

type cacheEntry struct {
	value   time.Duration
	expires time.Time
}

type flight struct {
	done  chan struct{}
	value time.Duration
	err   error
}

var _ TravelProvider = (*CachedTravel)(nil)

// NewCachedTravel caches successful lookups from next for ttl.
func NewCachedTravel(next TravelProvider, ttl time.Duration) *CachedTravel {
	return &CachedTravel{
		next:     next,
		ttl:      ttl,
		now:      time.Now,
		entries:  map[[2]string]cacheEntry{},
		inflight: map[[2]string]*flight{},
	}
}

// Stats returns a snapshot of the cache counters.
func (c *CachedTravel) Stats() CacheStats {
	return CacheStats{Hits: c.hits.Load(), Misses: c.misses.Load(), Coalesced: c.coalesced.Load()}
}

func (c *CachedTravel) TravelTime(ctx context.Context, from, to string) (time.Duration, error) {
	key := [2]string{from, to}
	if to < from {
		key = [2]string{to, from}
	}

	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		if c.now().Before(e.expires) {
			c.mu.Unlock()
			c.hits.Add(1)
			return e.value, nil
		}
		delete(c.entries, key)
	}

	f, joining := c.inflight[key]
	if !joining {
		f = &flight{done: make(chan struct{})}
		c.inflight[key] = f
	}
	c.mu.Unlock()

	if joining {
		c.coalesced.Add(1)
	} else {
		c.misses.Add(1)
		go c.fetch(ctx, key, from, to, f)
	}

	select {
	case <-f.done:
		return f.value, f.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (c *CachedTravel) fetch(ctx context.Context, key [2]string, from, to string, f *flight) {
	timeout := c.FetchTimeout
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	// Detach from the caller's cancellation but keep its values.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	value, err := c.next.TravelTime(fctx, from, to)

	c.mu.Lock()
	if err == nil {
		c.entries[key] = cacheEntry{value: value, expires: c.now().Add(c.ttl)}
	}
	delete(c.inflight, key)
	c.mu.Unlock()

	f.value, f.err = value, err
	close(f.done)
}
