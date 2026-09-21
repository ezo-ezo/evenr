package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubTravel counts calls and lets a test control latency and failures.
type stubTravel struct {
	calls   atomic.Int64
	delay   time.Duration
	release chan struct{} // if set, calls block until it is closed
	failFor atomic.Int64  // fail this many calls before succeeding
}

func (s *stubTravel) TravelTime(ctx context.Context, _, _ string) (time.Duration, error) {
	s.calls.Add(1)
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if s.failFor.Add(-1) >= 0 {
		return 0, ErrUnavailable
	}
	return 25 * time.Minute, nil
}

func TestCacheServesRepeatsFromMemory(t *testing.T) {
	stub := &stubTravel{}
	c := NewCachedTravel(stub, time.Minute)

	for range 5 {
		d, err := c.TravelTime(context.Background(), "a", "b")
		if err != nil || d != 25*time.Minute {
			t.Fatalf("TravelTime() = %v, %v", d, err)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1", got)
	}
	if s := c.Stats(); s.Hits != 4 || s.Misses != 1 {
		t.Errorf("stats = %+v, want 4 hits and 1 miss", s)
	}
}

func TestCacheTreatsPairsAsSymmetric(t *testing.T) {
	stub := &stubTravel{}
	c := NewCachedTravel(stub, time.Minute)

	_, _ = c.TravelTime(context.Background(), "a", "b")
	_, _ = c.TravelTime(context.Background(), "b", "a")
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times for (a,b) then (b,a), want 1", got)
	}
}

func TestCacheExpires(t *testing.T) {
	stub := &stubTravel{}
	c := NewCachedTravel(stub, time.Minute)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	_, _ = c.TravelTime(context.Background(), "a", "b")
	now = now.Add(59 * time.Second)
	_, _ = c.TravelTime(context.Background(), "a", "b")
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("entry should still be fresh at 59s, upstream calls = %d", got)
	}

	now = now.Add(2 * time.Second)
	_, _ = c.TravelTime(context.Background(), "a", "b")
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("entry should have expired at 61s, upstream calls = %d, want 2", got)
	}
}

func TestCacheDoesNotStoreErrors(t *testing.T) {
	stub := &stubTravel{}
	stub.failFor.Store(1)
	c := NewCachedTravel(stub, time.Minute)

	if _, err := c.TravelTime(context.Background(), "a", "b"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first call = %v, want ErrUnavailable", err)
	}
	if d, err := c.TravelTime(context.Background(), "a", "b"); err != nil || d != 25*time.Minute {
		t.Fatalf("second call = %v, %v; the failure must not have been cached", d, err)
	}
}

func TestCacheCoalescesConcurrentMisses(t *testing.T) {
	stub := &stubTravel{release: make(chan struct{})}
	c := NewCachedTravel(stub, time.Minute)

	const callers = 50
	var wg sync.WaitGroup
	results := make(chan time.Duration, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := c.TravelTime(context.Background(), "a", "b")
			if err != nil {
				t.Errorf("TravelTime() = %v", err)
			}
			results <- d
		}()
	}

	// Wait until everyone has joined the one in-flight fetch, then let it finish.
	deadline := time.Now().Add(2 * time.Second)
	for c.Stats().Misses+c.Stats().Coalesced < callers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(stub.release)
	wg.Wait()
	close(results)

	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times for %d concurrent callers, want 1", got, callers)
	}
	for d := range results {
		if d != 25*time.Minute {
			t.Errorf("caller got %v", d)
		}
	}
}

func TestCacheFetchSurvivesCallerDeadline(t *testing.T) {
	stub := &stubTravel{delay: 100 * time.Millisecond}
	c := NewCachedTravel(stub, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.TravelTime(ctx, "a", "b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first call = %v, want DeadlineExceeded", err)
	}

	// The abandoned fetch should finish on its own and warm the cache.
	time.Sleep(250 * time.Millisecond)
	d, err := c.TravelTime(context.Background(), "a", "b")
	if err != nil || d != 25*time.Minute {
		t.Fatalf("second call = %v, %v", d, err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (the second call should be a hit)", got)
	}
	if s := c.Stats(); s.Hits != 1 {
		t.Errorf("stats = %+v, want the second call to be a hit", s)
	}
}

func TestCacheFetchTimeoutBoundsAbandonedFetch(t *testing.T) {
	stub := &stubTravel{release: make(chan struct{})} // never released
	c := NewCachedTravel(stub, time.Minute)
	c.FetchTimeout = 30 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _ = c.TravelTime(ctx, "a", "b")

	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	pending := len(c.inflight)
	c.mu.Unlock()
	if pending != 0 {
		t.Errorf("%d fetches still in flight after FetchTimeout", pending)
	}
}

func TestCacheClear(t *testing.T) {
	stub := &stubTravel{}
	c := NewCachedTravel(stub, time.Minute)

	_, _ = c.TravelTime(context.Background(), "a", "b")
	c.Clear()
	_, _ = c.TravelTime(context.Background(), "a", "b")
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("upstream called %d times around a Clear, want 2", got)
	}
}
