package itinerary_test

import (
	"context"
	"testing"
	"time"

	"evenr/internal/itinerary"
	"evenr/internal/providers"
)

// Runs the solver over the full mock catalog and checks that every plan it
// returns really satisfies the constraints, whatever the party or budget.
func TestSolveOutputAlwaysSatisfiesConstraints(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 9, 26, 0, 0, 0, 0, itinerary.IST)
	areas := []string{"indiranagar", "koramangala", "mg-road"}

	shows := &providers.MockShowtimes{}
	rests := &providers.MockRestaurants{}
	trav := &providers.MockTravel{}

	var allShows []itinerary.Showtime
	for _, a := range areas {
		s, err := shows.Showtimes(ctx, a, day)
		if err != nil {
			t.Fatal(err)
		}
		allShows = append(allShows, s...)
	}

	matrix := map[[2]string]time.Duration{}
	for _, a := range areas {
		for _, b := range areas {
			d, err := trav.TravelTime(ctx, a, b)
			if err != nil {
				t.Fatal(err)
			}
			matrix[[2]string{a, b}] = d
		}
	}
	travel := func(from, to string) (time.Duration, bool) {
		d, ok := matrix[[2]string{from, to}]
		return d, ok
	}

	opts := itinerary.DefaultSolveOptions()
	latest := day.Add(opts.LatestEnd)
	total := 0

	for _, party := range []int{1, 2, 4, 6, 8} {
		var allTables []itinerary.TableSlot
		for _, a := range areas {
			tb, err := rests.Tables(ctx, a, day, party)
			if err != nil {
				t.Fatal(err)
			}
			allTables = append(allTables, tb...)
		}

		for _, budget := range []itinerary.Money{600, 900, 1200, 1500, 2500} {
			req := itinerary.Request{Day: day, Area: "indiranagar", PartySize: party, BudgetPerPerson: budget}
			plans, err := itinerary.Solve(req, allShows, allTables, travel, opts)
			if err != nil {
				t.Fatalf("Solve() = %v", err)
			}
			total += len(plans)

			for _, p := range plans {
				if len(p.Legs) != 2 || p.Legs[0].Kind == p.Legs[1].Kind {
					t.Fatalf("party %d budget %d: want one dinner and one film, got %+v", party, budget, p.Legs)
				}
				if !p.Feasible() {
					t.Fatalf("party %d budget %d: infeasible plan %+v", party, budget, p.Legs)
				}
				if p.CostPerPerson() > budget {
					t.Fatalf("party %d budget %d: plan costs %d", party, budget, p.CostPerPerson())
				}
				if p.End().After(latest) {
					t.Fatalf("party %d budget %d: plan ends %v, after %v", party, budget, p.End(), latest)
				}
				for _, l := range p.Legs {
					if l.Kind == itinerary.KindRestaurant && l.End.Sub(l.Start) != 90*time.Minute {
						t.Fatalf("unexpected dinner window %+v", l)
					}
				}
			}
		}
	}

	if total == 0 {
		t.Fatal("the mock catalog produced no plans at all; the constraints are probably too tight")
	}
	t.Logf("checked %d plans", total)
}
