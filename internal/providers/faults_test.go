package providers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFaultsZeroConfigIsInstant(t *testing.T) {
	f := NewFaults(1, FaultConfig{})
	start := time.Now()
	if err := f.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("zero config took %v", elapsed)
	}
}

func TestFaultsNilInjectsNothing(t *testing.T) {
	var f *Faults
	if err := f.Apply(context.Background()); err != nil {
		t.Fatalf("nil Faults Apply() = %v, want nil", err)
	}
}

func TestFaultsDownFailsImmediately(t *testing.T) {
	f := NewFaults(1, FaultConfig{Down: true, Latency: time.Hour})
	start := time.Now()
	err := f.Apply(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Apply() = %v, want ErrUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Down should not wait out the latency, took %v", elapsed)
	}
}

func TestFaultsErrorRate(t *testing.T) {
	always := NewFaults(1, FaultConfig{ErrorRate: 1})
	for range 20 {
		if err := always.Apply(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("ErrorRate 1: Apply() = %v, want ErrUnavailable", err)
		}
	}

	never := NewFaults(1, FaultConfig{ErrorRate: 0})
	for range 20 {
		if err := never.Apply(context.Background()); err != nil {
			t.Fatalf("ErrorRate 0: Apply() = %v, want nil", err)
		}
	}
}

func TestFaultsRespectsContextDeadline(t *testing.T) {
	f := NewFaults(1, FaultConfig{Latency: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := f.Apply(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Apply() = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Apply did not return at the deadline, took %v", elapsed)
	}
}

func TestFaultsAppliesLatencyAndJitter(t *testing.T) {
	f := NewFaults(7, FaultConfig{Latency: 20 * time.Millisecond, Jitter: 10 * time.Millisecond})
	start := time.Now()
	if err := f.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("returned after %v, want at least the base latency", elapsed)
	}
}

func TestFaultsSetTakesEffect(t *testing.T) {
	f := NewFaults(1, FaultConfig{})
	if err := f.Apply(context.Background()); err != nil {
		t.Fatalf("before Set: %v", err)
	}
	f.Set(FaultConfig{Down: true})
	if err := f.Apply(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("after Set: Apply() = %v, want ErrUnavailable", err)
	}
}

func TestFaultsConcurrentUse(t *testing.T) {
	f := NewFaults(1, FaultConfig{Jitter: time.Millisecond, ErrorRate: 0.5})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%10 == 0 {
				f.Set(FaultConfig{Jitter: time.Millisecond, ErrorRate: 0.5})
			}
			_ = f.Apply(context.Background())
		}()
	}
	wg.Wait()
}
