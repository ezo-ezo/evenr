package planner

import (
	"context"
	"errors"
	"testing"
	"time"

	"evenr/internal/aggregator"
	"evenr/internal/itinerary"
	"evenr/internal/providers"
)

var request = itinerary.Request{
	Day:             time.Date(2026, 9, 26, 0, 0, 0, 0, itinerary.IST),
	Area:            "indiranagar",
	PartySize:       2,
	BudgetPerPerson: 1500,
}

func newPlanner(showtimes, tables, travel *providers.Faults) *Planner {
	p := New(&aggregator.Aggregator{
		Showtimes:   &providers.MockShowtimes{Faults: showtimes},
		Restaurants: &providers.MockRestaurants{Faults: tables},
		Travel:      &providers.MockTravel{Faults: travel},
	})
	p.Budget = 500 * time.Millisecond
	return p
}

func down() *providers.Faults { return providers.NewFaults(1, providers.FaultConfig{Down: true}) }

func TestPlanHealthy(t *testing.T) {
	res, err := newPlanner(nil, nil, nil).Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if res.Degraded() {
		t.Fatalf("unexpected failures: %+v", res.Failures)
	}
	if len(res.Plans) != 5 {
		t.Fatalf("got %d plans, want 5", len(res.Plans))
	}
	if res.Considered <= len(res.Plans) {
		t.Errorf("considered %d, expected far more than the %d returned", res.Considered, len(res.Plans))
	}
	for i, r := range res.Plans {
		if !r.Itinerary.Feasible() || r.Itinerary.CostPerPerson() > request.BudgetPerPerson {
			t.Errorf("plan %d violates constraints: %+v", i, r.Itinerary)
		}
		if i > 0 && r.Score > res.Plans[i-1].Score {
			t.Errorf("plans not sorted at %d", i)
		}
	}
}

func TestPlanTravelDownFallsBackToEstimates(t *testing.T) {
	res, err := newPlanner(nil, nil, down()).Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if !res.Degraded() {
		t.Fatal("expected the failed travel calls to be reported")
	}
	if len(res.Plans) == 0 {
		t.Fatal("no plans despite showtimes and tables being available")
	}
	for _, r := range res.Plans {
		if !r.Itinerary.Legs[1].TravelEstimated {
			t.Errorf("travel should be flagged as estimated: %+v", r.Itinerary.Legs[1])
		}
	}
}

func TestPlanSlowUpstreamReturnsWithinBudget(t *testing.T) {
	slow := providers.NewFaults(1, providers.FaultConfig{Latency: 5 * time.Second})
	p := newPlanner(slow, nil, nil)
	p.Budget = 100 * time.Millisecond

	start := time.Now()
	res, err := p.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v with a 100ms budget", elapsed)
	}
	if !res.Degraded() {
		t.Error("timed-out showtimes should be reported")
	}
	if len(res.Plans) != 0 {
		t.Errorf("got %d plans without any showtimes", len(res.Plans))
	}
}

func TestPlanEverythingDown(t *testing.T) {
	_, err := newPlanner(down(), down(), down()).Plan(context.Background(), request)
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("Plan() = %v, want ErrNoData", err)
	}
}

func TestPlanUnknownArea(t *testing.T) {
	req := request
	req.Area = "atlantis"
	_, err := newPlanner(nil, nil, nil).Plan(context.Background(), req)
	if !errors.Is(err, providers.ErrUnknownArea) {
		t.Fatalf("Plan() = %v, want ErrUnknownArea", err)
	}
}

func TestPlanInvalidRequest(t *testing.T) {
	req := request
	req.PartySize = 0
	_, err := newPlanner(nil, nil, nil).Plan(context.Background(), req)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Plan() = %v, want ErrInvalidRequest", err)
	}
}

func TestPlanSearchesNearbyAreas(t *testing.T) {
	p := newPlanner(nil, nil, nil)
	p.Rank = itinerary.RankOptions{} // return everything

	res, err := p.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	areas := map[string]bool{}
	for _, r := range res.Plans {
		for _, l := range r.Itinerary.Legs {
			areas[l.Venue.Area] = true
		}
	}
	for _, want := range []string{"indiranagar", "mg-road", "koramangala"} {
		if !areas[want] {
			t.Errorf("no plan uses %s; areas seen: %v", want, areas)
		}
	}
	if areas["whitefield"] {
		t.Error("whitefield is beyond the search radius")
	}
}
