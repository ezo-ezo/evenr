package aggregator

import (
	"context"
	"errors"
	"testing"
	"time"

	"evenr/internal/itinerary"
	"evenr/internal/providers"
)

var (
	saturday = time.Date(2026, 9, 26, 0, 0, 0, 0, itinerary.IST)
	areas    = []string{"koramangala", "mg-road"}
	request  = itinerary.Request{Day: saturday, Area: "indiranagar", PartySize: 2, BudgetPerPerson: 1500}
)

// build wires an Aggregator to mock providers with the given fault injectors.
func build(showtimes, tables, travel *providers.Faults) *Aggregator {
	return &Aggregator{
		Showtimes:   &providers.MockShowtimes{Faults: showtimes},
		Restaurants: &providers.MockRestaurants{Faults: tables},
		Travel:      &providers.MockTravel{Faults: travel},
	}
}

func TestFetchHealthy(t *testing.T) {
	data, err := build(nil, nil, nil).Fetch(context.Background(), request, areas, time.Second)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if data.Degraded() {
		t.Fatalf("unexpected failures: %+v", data.Failures)
	}

	// 3 areas: indiranagar plus the two extras, 8 screenings per area.
	if got, want := len(data.Showtimes), 3*8; got != want {
		t.Errorf("got %d showtimes, want %d", got, want)
	}
	if len(data.Tables) == 0 {
		t.Error("no tables returned")
	}
	// 3 self pairs + 3 cross pairs.
	if got, want := len(data.Travel), 6; got != want {
		t.Errorf("got %d travel pairs, want %d", got, want)
	}
	if d, ok := data.Travel.Between("mg-road", "indiranagar"); !ok || d <= 0 {
		t.Errorf("Between(mg-road, indiranagar) = %v, %v", d, ok)
	}
}

func TestFetchIsDeterministic(t *testing.T) {
	agg := build(nil, nil, nil)
	first, _ := agg.Fetch(context.Background(), request, areas, time.Second)
	for range 10 {
		next, _ := agg.Fetch(context.Background(), request, areas, time.Second)
		if len(next.Showtimes) != len(first.Showtimes) {
			t.Fatal("result size changed between runs")
		}
		for i := range first.Showtimes {
			if first.Showtimes[i].ID != next.Showtimes[i].ID {
				t.Fatalf("showtime order changed at %d: %s vs %s", i, first.Showtimes[i].ID, next.Showtimes[i].ID)
			}
		}
	}
}

func TestFetchOneProviderDownStillReturnsTheRest(t *testing.T) {
	agg := build(nil, nil, providers.NewFaults(1, providers.FaultConfig{Down: true}))
	data, err := agg.Fetch(context.Background(), request, areas, time.Second)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if len(data.Showtimes) == 0 || len(data.Tables) == 0 {
		t.Errorf("healthy sources were dropped: %d showtimes, %d tables", len(data.Showtimes), len(data.Tables))
	}
	if len(data.Travel) != 0 {
		t.Errorf("got %d travel pairs from a down provider", len(data.Travel))
	}
	if len(data.Failures) != 6 {
		t.Fatalf("got %d failures, want 6 (one per travel pair): %+v", len(data.Failures), data.Failures)
	}
	for _, f := range data.Failures {
		if f.Source != SourceTravel || !errors.Is(f.Err, providers.ErrUnavailable) {
			t.Errorf("unexpected failure %+v", f)
		}
	}
}

func TestFetchSlowProviderIsCutOffAtBudget(t *testing.T) {
	slow := providers.NewFaults(1, providers.FaultConfig{Latency: 5 * time.Second})
	agg := build(slow, nil, nil)

	start := time.Now()
	data, err := agg.Fetch(context.Background(), request, areas, 150*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if elapsed > 600*time.Millisecond {
		t.Errorf("took %v with a 150ms budget", elapsed)
	}
	if len(data.Showtimes) != 0 {
		t.Errorf("got %d showtimes from a provider that timed out", len(data.Showtimes))
	}
	if len(data.Tables) == 0 || len(data.Travel) == 0 {
		t.Errorf("fast sources were dropped: %d tables, %d travel", len(data.Tables), len(data.Travel))
	}
	if len(data.Failures) != 3 {
		t.Fatalf("got %d failures, want 3 (one per area): %+v", len(data.Failures), data.Failures)
	}
	for _, f := range data.Failures {
		if f.Source != SourceShowtimes || !errors.Is(f.Err, context.DeadlineExceeded) {
			t.Errorf("unexpected failure %+v", f)
		}
	}
}

// stuckShowtimes ignores its context entirely, like a badly written client.
type stuckShowtimes struct{}

func (stuckShowtimes) Showtimes(context.Context, string, time.Time) ([]itinerary.Showtime, error) {
	time.Sleep(2 * time.Second)
	return nil, nil
}

func TestFetchDoesNotWaitForProviderThatIgnoresContext(t *testing.T) {
	agg := build(nil, nil, nil)
	agg.Showtimes = stuckShowtimes{}

	start := time.Now()
	data, err := agg.Fetch(context.Background(), request, areas, 100*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("Fetch waited %v on a stuck provider, budget was 100ms", elapsed)
	}
	if len(data.Tables) == 0 {
		t.Error("tables missing")
	}
	if !data.Degraded() {
		t.Error("expected the stuck provider to be reported as a failure")
	}
}

func TestFetchRejectsBadInput(t *testing.T) {
	agg := build(nil, nil, nil)
	if _, err := agg.Fetch(context.Background(), itinerary.Request{}, areas, time.Second); err == nil {
		t.Error("invalid request should be rejected")
	}
	if _, err := agg.Fetch(context.Background(), request, areas, 0); err == nil {
		t.Error("zero budget should be rejected")
	}
}

func TestFetchParentCancellation(t *testing.T) {
	slow := providers.NewFaults(1, providers.FaultConfig{Latency: 5 * time.Second})
	agg := build(slow, slow, slow)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	data, err := agg.Fetch(ctx, request, areas, 10*time.Second)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("did not stop on cancellation, took %v", elapsed)
	}
	for _, f := range data.Failures {
		if !errors.Is(f.Err, context.Canceled) {
			t.Errorf("failure %+v should be Canceled", f)
		}
	}
}
