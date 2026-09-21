package providers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// slowFirst is slow on its first call and instant afterwards, like a request
// that landed on a bad replica. It records whether the slow call was cancelled.
type slowFirst struct {
	calls     atomic.Int64
	slow      time.Duration
	cancelled atomic.Bool
}

func (s *slowFirst) TravelTime(ctx context.Context, _, _ string) (time.Duration, error) {
	if s.calls.Add(1) == 1 {
		select {
		case <-time.After(s.slow):
		case <-ctx.Done():
			s.cancelled.Store(true)
			return 0, ctx.Err()
		}
	}
	return 25 * time.Minute, nil
}

// waitFor polls until cond holds or a second passes.
func waitFor(cond func() bool) bool {
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

func TestHedgeFastPrimaryNeverHedges(t *testing.T) {
	stub := &slowFirst{slow: 0}
	stub.calls.Store(1) // make every call instant
	p := HedgedTravel{Next: stub, Hedger: NewHedger(50*time.Millisecond, 1)}

	if _, err := p.TravelTime(context.Background(), "a", "b"); err != nil {
		t.Fatalf("TravelTime() = %v", err)
	}
	if got := stub.calls.Load(); got != 2 { // 1 preset + this call
		t.Errorf("upstream calls = %d, want exactly one real call", got)
	}
	if s := p.Hedger.Stats(); s.Hedges != 0 || s.Requests != 1 {
		t.Errorf("stats = %+v", s)
	}
}

func TestHedgeRescuesSlowPrimary(t *testing.T) {
	stub := &slowFirst{slow: 2 * time.Second}
	p := HedgedTravel{Next: stub, Hedger: NewHedger(20*time.Millisecond, 1)}

	start := time.Now()
	d, err := p.TravelTime(context.Background(), "a", "b")
	elapsed := time.Since(start)

	if err != nil || d != 25*time.Minute {
		t.Fatalf("TravelTime() = %v, %v", d, err)
	}
	if elapsed > time.Second {
		t.Errorf("took %v; the hedge should have answered long before the slow primary", elapsed)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
	if !waitFor(stub.cancelled.Load) {
		t.Error("the slow primary should have been cancelled once the hedge won")
	}
	if s := p.Hedger.Stats(); s.Hedges != 1 {
		t.Errorf("stats = %+v, want 1 hedge", s)
	}
}

func TestHedgeBudgetLimitsExtraLoad(t *testing.T) {
	// Ratio 0.1 means a hedge needs ten requests' worth of tokens.
	h := NewHedger(5*time.Millisecond, 0.1)

	stub := &slowFirst{slow: 60 * time.Millisecond}
	start := time.Now()
	if _, err := (HedgedTravel{Next: stub, Hedger: h}).TravelTime(context.Background(), "a", "b"); err != nil {
		t.Fatalf("TravelTime() = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("returned after %v; with an empty budget the slow primary should have been waited for", elapsed)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (hedge suppressed)", got)
	}
	if s := h.Stats(); s.Hedges != 0 || s.Suppressed != 1 {
		t.Errorf("stats = %+v, want 0 hedges and 1 suppressed", s)
	}

	// Nine more requests earn the tenth token; now a slow call may be hedged.
	fast := &slowFirst{}
	fast.calls.Store(1)
	for range 9 {
		_, _ = (HedgedTravel{Next: fast, Hedger: h}).TravelTime(context.Background(), "a", "b")
	}
	stub2 := &slowFirst{slow: 2 * time.Second}
	if _, err := (HedgedTravel{Next: stub2, Hedger: h}).TravelTime(context.Background(), "a", "b"); err != nil {
		t.Fatalf("TravelTime() = %v", err)
	}
	if s := h.Stats(); s.Hedges != 1 {
		t.Errorf("stats = %+v, want the budget to have allowed 1 hedge", s)
	}
}

// failing returns a fixed error a set number of times, then succeeds.
type failing struct {
	calls atomic.Int64
	err   error
	times int64
}

func (f *failing) TravelTime(context.Context, string, string) (time.Duration, error) {
	if f.calls.Add(1) <= f.times {
		return 0, f.err
	}
	return 25 * time.Minute, nil
}

func TestHedgeRetriesImmediatelyOnUnavailable(t *testing.T) {
	stub := &failing{err: ErrUnavailable, times: 1}
	p := HedgedTravel{Next: stub, Hedger: NewHedger(time.Hour, 1)}

	start := time.Now()
	d, err := p.TravelTime(context.Background(), "a", "b")
	if err != nil || d != 25*time.Minute {
		t.Fatalf("TravelTime() = %v, %v", d, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v; a failed primary should be retried without waiting for the delay", elapsed)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
}

func TestHedgeReturnsFirstErrorWhenBothFail(t *testing.T) {
	stub := &failing{err: ErrUnavailable, times: 10}
	p := HedgedTravel{Next: stub, Hedger: NewHedger(time.Hour, 1)}

	if _, err := p.TravelTime(context.Background(), "a", "b"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("TravelTime() = %v, want ErrUnavailable", err)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (one retry, no more)", got)
	}
}

func TestHedgeDoesNotRetryDeterministicErrors(t *testing.T) {
	stub := &failing{err: ErrUnknownArea, times: 10}
	p := HedgedTravel{Next: stub, Hedger: NewHedger(time.Hour, 1)}

	if _, err := p.TravelTime(context.Background(), "a", "b"); !errors.Is(err, ErrUnknownArea) {
		t.Fatalf("TravelTime() = %v, want ErrUnknownArea", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1; retrying an unknown area cannot help", got)
	}
}

func TestHedgeHonoursCallerDeadline(t *testing.T) {
	stub := &slowFirst{slow: 5 * time.Second}
	stub.calls.Store(0)
	// Ratio 0 so no hedge is ever allowed and the only way out is the deadline.
	p := HedgedTravel{Next: stub, Hedger: NewHedger(time.Millisecond, 0)}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := p.TravelTime(ctx, "a", "b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("TravelTime() = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v to honour a 30ms deadline", elapsed)
	}
}

func TestNilHedgerPassesThrough(t *testing.T) {
	stub := &failing{}
	p := HedgedTravel{Next: stub}
	if d, err := p.TravelTime(context.Background(), "a", "b"); err != nil || d != 25*time.Minute {
		t.Fatalf("TravelTime() = %v, %v", d, err)
	}
}
