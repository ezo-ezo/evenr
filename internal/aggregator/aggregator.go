// Package aggregator gathers everything the solver needs from the upstream
// providers in parallel, under one shared deadline, and returns whatever
// arrived in time.
package aggregator

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"evenr/internal/itinerary"
	"evenr/internal/providers"
)

// Sources named in Failure.Source.
const (
	SourceShowtimes = "showtimes"
	SourceTables    = "tables"
	SourceTravel    = "travel"
)

// TravelMatrix holds travel times between areas. Travel is symmetric, so each
// pair is stored once.
type TravelMatrix map[[2]string]time.Duration

func pairKey(a, b string) [2]string {
	if b < a {
		a, b = b, a
	}
	return [2]string{a, b}
}

// Between returns the travel time between two areas, if it was fetched.
func (m TravelMatrix) Between(a, b string) (time.Duration, bool) {
	d, ok := m[pairKey(a, b)]
	return d, ok
}

// Failure records one upstream call that did not produce data.
type Failure struct {
	Source string // one of the Source constants
	Target string // an area, or "from|to" for travel
	Err    error
}

// Data is what could be gathered inside the budget. It is usable even when
// Failures is non-empty: the caller decides how much of it is enough.
type Data struct {
	Showtimes []itinerary.Showtime
	Tables    []itinerary.TableSlot
	Travel    TravelMatrix
	Failures  []Failure
}

// Degraded reports whether any upstream call failed or timed out.
func (d Data) Degraded() bool { return len(d.Failures) > 0 }

// Aggregator fans requests out to the three upstream providers.
type Aggregator struct {
	Showtimes   providers.ShowtimeProvider
	Restaurants providers.RestaurantProvider
	Travel      providers.TravelProvider
}

type result struct {
	idx       int
	showtimes []itinerary.Showtime
	tables    []itinerary.TableSlot
	travel    time.Duration
	err       error
}

type call struct {
	source, target string
	run            func(ctx context.Context, r *result)
}

// Fetch queries every area in areas (plus req.Area) for showtimes and tables,
// and every pair of those areas for travel time, all concurrently. It returns
// when every call has finished or when budget has elapsed, whichever is first.
//
// It does not wait for calls still in flight at the deadline, so a provider
// that ignores its context cannot hold the request past the budget.
func (a *Aggregator) Fetch(ctx context.Context, req itinerary.Request, areas []string, budget time.Duration) (Data, error) {
	if err := req.Validate(); err != nil {
		return Data{}, err
	}
	if budget <= 0 {
		return Data{}, errors.New("aggregator: budget must be positive")
	}

	areas = uniqueAreas(req.Area, areas)
	calls := a.plan(req, areas)

	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// Buffered so late goroutines can finish and exit without a reader.
	results := make(chan result, len(calls))
	for i, c := range calls {
		go func() {
			r := result{idx: i}
			c.run(ctx, &r)
			results <- r
		}()
	}

	data := Data{Travel: TravelMatrix{}}
	done := make([]bool, len(calls))
	record := func(r result) {
		done[r.idx] = true
		c := calls[r.idx]
		if r.err != nil {
			data.Failures = append(data.Failures, Failure{Source: c.source, Target: c.target, Err: r.err})
			return
		}
		switch c.source {
		case SourceShowtimes:
			data.Showtimes = append(data.Showtimes, r.showtimes...)
		case SourceTables:
			data.Tables = append(data.Tables, r.tables...)
		case SourceTravel:
			data.Travel[pairKey(splitTarget(c.target))] = r.travel
		}
	}

collect:
	for range calls {
		select {
		case r := <-results:
			record(r)
		case <-ctx.Done():
			break collect
		}
	}
	// Take anything that landed at the same instant as the deadline.
drain:
	for {
		select {
		case r := <-results:
			record(r)
		default:
			break drain
		}
	}

	for i, c := range calls {
		if !done[i] {
			data.Failures = append(data.Failures, Failure{Source: c.source, Target: c.target, Err: ctx.Err()})
		}
	}

	sortData(&data)
	return data, nil
}

func (a *Aggregator) plan(req itinerary.Request, areas []string) []call {
	var calls []call
	for _, area := range areas {
		calls = append(calls,
			call{SourceShowtimes, area, func(ctx context.Context, r *result) {
				r.showtimes, r.err = a.Showtimes.Showtimes(ctx, area, req.Day)
			}},
			call{SourceTables, area, func(ctx context.Context, r *result) {
				r.tables, r.err = a.Restaurants.Tables(ctx, area, req.Day, req.PartySize)
			}},
		)
	}
	for i, from := range areas {
		for _, to := range areas[i:] {
			calls = append(calls, call{SourceTravel, joinTarget(from, to), func(ctx context.Context, r *result) {
				r.travel, r.err = a.Travel.TravelTime(ctx, from, to)
			}})
		}
	}
	return calls
}

func uniqueAreas(primary string, extra []string) []string {
	out := []string{primary}
	for _, a := range extra {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

func joinTarget(from, to string) string { return from + "|" + to }

func splitTarget(t string) (string, string) {
	for i := range len(t) {
		if t[i] == '|' {
			return t[:i], t[i+1:]
		}
	}
	panic(fmt.Sprintf("aggregator: malformed travel target %q", t))
}

// sortData makes the output independent of goroutine completion order.
func sortData(d *Data) {
	slices.SortFunc(d.Showtimes, func(a, b itinerary.Showtime) int {
		return cmp.Or(a.Start.Compare(b.Start), cmp.Compare(a.ID, b.ID))
	})
	slices.SortFunc(d.Tables, func(a, b itinerary.TableSlot) int {
		return cmp.Or(a.Start.Compare(b.Start), cmp.Compare(a.Venue.ID, b.Venue.ID))
	})
	slices.SortFunc(d.Failures, func(a, b Failure) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Target, b.Target))
	})
}
