package itinerary

import (
	"cmp"
	"slices"
	"time"
)

// TravelFunc returns the travel time between two areas. The bool is false
// when the value is unknown, for example because the travel upstream timed out.
type TravelFunc func(from, to string) (time.Duration, bool)

// SolveOptions tunes the constraints the solver enforces.
type SolveOptions struct {
	// ArrivalBuffer is the time needed to get seated before a film starts. It
	// applies only when the film is the second stop.
	ArrivalBuffer time.Duration
	// FallbackTravel is assumed between two areas when TravelFunc has no
	// value. It is deliberately pessimistic, and legs that use it are flagged.
	FallbackTravel time.Duration
	// LatestEnd is the latest an itinerary may finish, as an offset from
	// midnight at the start of the requested day.
	LatestEnd time.Duration
}

func DefaultSolveOptions() SolveOptions {
	return SolveOptions{
		ArrivalBuffer:  15 * time.Minute,
		FallbackTravel: 45 * time.Minute,
		LatestEnd:      24*time.Hour + 30*time.Minute,
	}
}

// Solve returns every feasible two-stop plan for the request: one dinner and
// one film, in either order. A plan is feasible when the party fits the table,
// the combined cost per person is within budget, the second stop starts after
// the first ends plus travel (plus ArrivalBuffer before a film), and the whole
// plan ends by LatestEnd.
//
// The result is in a fixed order, so equal inputs give equal output. It is not
// ranked.
func Solve(req Request, showtimes []Showtime, tables []TableSlot, travel TravelFunc, opts SolveOptions) ([]Itinerary, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	y, m, d := req.Day.In(IST).Date()
	latest := time.Date(y, m, d, 0, 0, 0, 0, IST).Add(opts.LatestEnd)

	var out []Itinerary
	for _, tb := range tables {
		if tb.SeatsAvailable < req.PartySize {
			continue
		}
		dinner := dinnerLeg(tb)

		for _, sh := range showtimes {
			if tb.AvgSpendPerPerson+sh.TicketPrice > req.BudgetPerPerson {
				continue
			}
			film := filmLeg(sh)

			if it, ok := chain(dinner, film, travel, opts, latest); ok {
				out = append(out, it)
			}
			if it, ok := chain(film, dinner, travel, opts, latest); ok {
				out = append(out, it)
			}
		}
	}

	slices.SortFunc(out, compareItineraries)
	return out, nil
}

func dinnerLeg(t TableSlot) Leg {
	return Leg{
		Kind:          KindRestaurant,
		Venue:         t.Venue,
		Title:         t.Venue.Name,
		Start:         t.Start,
		End:           t.End(),
		CostPerPerson: t.AvgSpendPerPerson,
	}
}

func filmLeg(s Showtime) Leg {
	return Leg{
		Kind:          KindCinema,
		Venue:         s.Venue,
		Title:         s.Movie,
		Start:         s.Start,
		End:           s.End(),
		CostPerPerson: s.TicketPrice,
	}
}

// chain places second after first, returning false if it cannot be reached in
// time or the plan runs past latest.
func chain(first, second Leg, travel TravelFunc, opts SolveOptions, latest time.Time) (Itinerary, bool) {
	tt, known := travel(first.Venue.Area, second.Venue.Area)
	if !known {
		tt = opts.FallbackTravel
	}

	need := tt
	if second.Kind == KindCinema {
		need += opts.ArrivalBuffer
	}
	if second.Start.Before(first.End.Add(need)) || second.End.After(latest) {
		return Itinerary{}, false
	}

	second.TravelBefore = tt
	second.TravelEstimated = !known
	return Itinerary{Legs: []Leg{first, second}}, true
}

// compareItineraries orders by start time, then end time, then venue IDs, so
// the order never depends on input order.
func compareItineraries(a, b Itinerary) int {
	return cmp.Or(
		a.Start().Compare(b.Start()),
		a.End().Compare(b.End()),
		cmp.Compare(a.Legs[0].Venue.ID, b.Legs[0].Venue.ID),
		cmp.Compare(a.Legs[1].Venue.ID, b.Legs[1].Venue.ID),
		a.Legs[0].Start.Compare(b.Legs[0].Start),
		a.Legs[1].Start.Compare(b.Legs[1].Start),
	)
}
