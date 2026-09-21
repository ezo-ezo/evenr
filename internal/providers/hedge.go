package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"evenr/internal/itinerary"
)

// maxHedgeTokens caps how many hedges can be banked for a burst.
const maxHedgeTokens = 10

// HedgeStats counts hedging activity.
type HedgeStats struct {
	Requests   int64 // calls made through the hedger
	Hedges     int64 // extra attempts actually launched
	Suppressed int64 // hedges skipped because the budget was empty
}

// Hedger cuts tail latency by sending a second attempt when the first is slow.
//
// If an attempt has not finished after Delay, a second identical attempt is
// launched and whichever succeeds first wins; the loser is cancelled. If the
// first attempt fails with ErrUnavailable, the second is launched at once.
// Other errors (an unknown area, a cancelled context) are returned as they
// are, since repeating them would not help.
//
// Hedging adds load, and if the upstream is slow for everyone it would double
// that load right when it can least take it. So hedges are budgeted: every
// request earns Ratio tokens and each hedge spends one. With Ratio 0.1, at
// most about one request in ten is hedged, however slow the upstream gets.
//
// Delay should sit near the upstream's p95 latency, so only the slow tail is
// hedged. It is a fixed value here; an adaptive one is future work.
type Hedger struct {
	Delay time.Duration
	Ratio float64

	mu     sync.Mutex
	tokens float64

	requests, hedges, suppressed atomic.Int64
}

func NewHedger(delay time.Duration, ratio float64) *Hedger {
	return &Hedger{Delay: delay, Ratio: ratio}
}

func (h *Hedger) Stats() HedgeStats {
	return HedgeStats{Requests: h.requests.Load(), Hedges: h.hedges.Load(), Suppressed: h.suppressed.Load()}
}

func (h *Hedger) earn() {
	h.mu.Lock()
	h.tokens = min(h.tokens+h.Ratio, maxHedgeTokens)
	h.mu.Unlock()
}

func (h *Hedger) spend() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tokens < 1 {
		return false
	}
	h.tokens--
	return true
}

type outcome[T any] struct {
	value T
	err   error
}

// hedged runs fn under h. A nil Hedger just calls fn.
func hedged[T any](ctx context.Context, h *Hedger, fn func(context.Context) (T, error)) (T, error) {
	if h == nil {
		return fn(ctx)
	}
	h.requests.Add(1)
	h.earn()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // cancels whichever attempt is still running

	results := make(chan outcome[T], 2)
	launch := func() {
		go func() {
			v, err := fn(ctx)
			results <- outcome[T]{v, err}
		}()
	}
	launch()
	launched, failed := 1, 0

	timer := time.NewTimer(h.Delay)
	defer timer.Stop()

	// tryHedge launches the second attempt if the budget allows it.
	tryHedge := func() {
		if launched > 1 {
			return
		}
		if !h.spend() {
			h.suppressed.Add(1)
			return
		}
		h.hedges.Add(1)
		launched++
		launch()
	}

	var firstErr error
	for {
		select {
		case <-timer.C:
			tryHedge()

		case r := <-results:
			if r.err == nil {
				return r.value, nil
			}
			failed++
			if firstErr == nil {
				firstErr = r.err
			}
			if failed == launched {
				if launched == 1 && errors.Is(r.err, ErrUnavailable) {
					// The first attempt failed outright: try once more right now.
					tryHedge()
					if launched > 1 {
						continue
					}
				}
				var zero T
				return zero, firstErr
			}

		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}

// HedgedShowtimes hedges calls to a ShowtimeProvider.
type HedgedShowtimes struct {
	Next   ShowtimeProvider
	Hedger *Hedger
}

func (p HedgedShowtimes) Showtimes(ctx context.Context, area string, day time.Time) ([]itinerary.Showtime, error) {
	return hedged(ctx, p.Hedger, func(ctx context.Context) ([]itinerary.Showtime, error) {
		return p.Next.Showtimes(ctx, area, day)
	})
}

// HedgedRestaurants hedges calls to a RestaurantProvider.
type HedgedRestaurants struct {
	Next   RestaurantProvider
	Hedger *Hedger
}

func (p HedgedRestaurants) Tables(ctx context.Context, area string, day time.Time, partySize int) ([]itinerary.TableSlot, error) {
	return hedged(ctx, p.Hedger, func(ctx context.Context) ([]itinerary.TableSlot, error) {
		return p.Next.Tables(ctx, area, day, partySize)
	})
}

// HedgedTravel hedges calls to a TravelProvider.
type HedgedTravel struct {
	Next   TravelProvider
	Hedger *Hedger
}

func (p HedgedTravel) TravelTime(ctx context.Context, from, to string) (time.Duration, error) {
	return hedged(ctx, p.Hedger, func(ctx context.Context) (time.Duration, error) {
		return p.Next.TravelTime(ctx, from, to)
	})
}

var (
	_ ShowtimeProvider   = HedgedShowtimes{}
	_ RestaurantProvider = HedgedRestaurants{}
	_ TravelProvider     = HedgedTravel{}
)
