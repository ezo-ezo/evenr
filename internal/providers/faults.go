package providers

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// FaultConfig describes how a mock upstream misbehaves.
type FaultConfig struct {
	// Latency is the base delay added to every call.
	Latency time.Duration
	// Jitter adds a uniformly random extra delay in [0, Jitter).
	Jitter time.Duration
	// ErrorRate is the probability in [0, 1] that a call fails after its delay.
	ErrorRate float64
	// Down makes every call fail immediately with ErrUnavailable.
	Down bool
}

// Faults injects latency and failures into a mock provider. The
// configuration can be changed while requests are in flight, which lets a
// benchmark slow one dependency down mid-run. It is safe for concurrent use.
type Faults struct {
	mu  sync.Mutex
	cfg FaultConfig
	rng *rand.Rand
}

// NewFaults returns a Faults seeded for reproducible jitter and error draws.
func NewFaults(seed uint64, cfg FaultConfig) *Faults {
	return &Faults{cfg: cfg, rng: rand.New(rand.NewPCG(seed, seed))}
}

// Set replaces the current configuration.
func (f *Faults) Set(cfg FaultConfig) {
	f.mu.Lock()
	f.cfg = cfg
	f.mu.Unlock()
}

// Apply simulates the cost of one upstream call. It blocks for the configured
// delay, returning ctx.Err() early if the context ends first, and then either
// returns nil or ErrUnavailable. A nil *Faults injects nothing.
func (f *Faults) Apply(ctx context.Context) error {
	if f == nil {
		return ctx.Err()
	}

	f.mu.Lock()
	cfg := f.cfg
	delay := cfg.Latency
	if cfg.Jitter > 0 {
		delay += time.Duration(f.rng.Int64N(int64(cfg.Jitter)))
	}
	fail := cfg.ErrorRate > 0 && f.rng.Float64() < cfg.ErrorRate
	f.mu.Unlock()

	if cfg.Down {
		return ErrUnavailable
	}

	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if err := ctx.Err(); err != nil {
		return err
	}

	if fail {
		return ErrUnavailable
	}
	return nil
}
