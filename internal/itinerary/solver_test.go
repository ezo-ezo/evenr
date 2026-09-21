package itinerary

import (
	"testing"
	"time"
)

func testVenue(id, area string, kind VenueKind) Venue {
	return Venue{ID: id, Name: id, Kind: kind, Area: area}
}

func table(id, area string, h, m int, spend Money, seats int) TableSlot {
	return TableSlot{
		Venue:             testVenue(id, area, KindRestaurant),
		Start:             at(h, m),
		Duration:          90 * time.Minute,
		SeatsAvailable:    seats,
		AvgSpendPerPerson: spend,
	}
}

func show(id, area string, h, m int, runtime time.Duration, price Money) Showtime {
	return Showtime{
		ID:          id,
		Venue:       testVenue(id, area, KindCinema),
		Movie:       "Movie " + id,
		Start:       at(h, m),
		Runtime:     runtime,
		TicketPrice: price,
	}
}

func fixedTravel(d time.Duration) TravelFunc {
	return func(_, _ string) (time.Duration, bool) { return d, true }
}

func unknownTravel(_, _ string) (time.Duration, bool) { return 0, false }

var req = Request{Day: at(0, 0), Area: "a", PartySize: 4, BudgetPerPerson: 1500}

func solve(t *testing.T, r Request, shows []Showtime, tables []TableSlot, travel TravelFunc) []Itinerary {
	t.Helper()
	out, err := Solve(r, shows, tables, travel, DefaultSolveOptions())
	if err != nil {
		t.Fatalf("Solve() = %v", err)
	}
	return out
}

func TestSolveDinnerThenFilmTimingBoundary(t *testing.T) {
	// Dinner 18:00-19:30, 30 minutes of travel and a 15 minute arrival buffer
	// means the earliest film start is 20:15.
	dinner := []TableSlot{table("d", "a", 18, 0, 700, 4)}
	travel := fixedTravel(30 * time.Minute)

	if got := solve(t, req, []Showtime{show("s", "b", 20, 15, 2*time.Hour, 300)}, dinner, travel); len(got) != 1 {
		t.Fatalf("film at 20:15: got %d itineraries, want 1", len(got))
	}
	if got := solve(t, req, []Showtime{show("s", "b", 20, 14, 2*time.Hour, 300)}, dinner, travel); len(got) != 0 {
		t.Fatalf("film at 20:14: got %d itineraries, want 0", len(got))
	}
}

func TestSolveFilmThenDinner(t *testing.T) {
	// The only film is before dinner, so the plan must run film first.
	// The arrival buffer does not apply before a dinner.
	shows := []Showtime{show("s", "a", 15, 20, 2*time.Hour, 300)} // ends 17:20
	tables := []TableSlot{table("d", "a", 18, 0, 700, 4)}

	got := solve(t, req, shows, tables, fixedTravel(40*time.Minute))
	if len(got) != 1 {
		t.Fatalf("got %d itineraries, want 1", len(got))
	}
	it := got[0]
	if it.Legs[0].Kind != KindCinema || it.Legs[1].Kind != KindRestaurant {
		t.Errorf("legs = %s then %s, want cinema then restaurant", it.Legs[0].Kind, it.Legs[1].Kind)
	}
	if it.Legs[1].TravelBefore != 40*time.Minute {
		t.Errorf("TravelBefore = %v, want 40m", it.Legs[1].TravelBefore)
	}
	if !it.Feasible() {
		t.Error("returned itinerary is not feasible")
	}

	late := []Showtime{show("s", "a", 15, 21, 2*time.Hour, 300)} // ends 17:21, one minute too late
	if got := solve(t, req, late, tables, fixedTravel(40*time.Minute)); len(got) != 0 {
		t.Errorf("film ending 17:21: got %d itineraries, want 0", len(got))
	}
}

func TestSolveBudget(t *testing.T) {
	shows := []Showtime{show("s", "a", 21, 0, 2*time.Hour, 500)}
	tables := []TableSlot{table("d", "a", 18, 0, 1000, 4)}

	if got := solve(t, req, shows, tables, fixedTravel(5*time.Minute)); len(got) != 1 {
		t.Fatalf("cost 1500 with budget 1500: got %d itineraries, want 1", len(got))
	}
	tight := req
	tight.BudgetPerPerson = 1499
	if got := solve(t, tight, shows, tables, fixedTravel(5*time.Minute)); len(got) != 0 {
		t.Fatalf("cost 1500 with budget 1499: got %d itineraries, want 0", len(got))
	}
}

func TestSolvePartySize(t *testing.T) {
	shows := []Showtime{show("s", "a", 21, 0, 2*time.Hour, 300)}
	tables := []TableSlot{table("small", "a", 18, 0, 700, 3), table("big", "a", 18, 30, 700, 4)}

	got := solve(t, req, shows, tables, fixedTravel(5*time.Minute))
	if len(got) != 1 || got[0].Legs[0].Venue.ID != "big" {
		t.Fatalf("got %+v, want only the table that seats 4", got)
	}
}

func TestSolveLatestEnd(t *testing.T) {
	// Default cut-off is 00:30 the next day: a film ending exactly then is fine.
	tables := []TableSlot{table("d", "a", 18, 0, 700, 4)}
	onTime := []Showtime{show("s", "a", 22, 0, 150*time.Minute, 300)} // ends 00:30
	tooLate := []Showtime{show("s", "a", 22, 1, 150*time.Minute, 300)}

	if got := solve(t, req, onTime, tables, fixedTravel(5*time.Minute)); len(got) != 1 {
		t.Errorf("ending 00:30: got %d itineraries, want 1", len(got))
	}
	if got := solve(t, req, tooLate, tables, fixedTravel(5*time.Minute)); len(got) != 0 {
		t.Errorf("ending 00:31: got %d itineraries, want 0", len(got))
	}
}

func TestSolveUnknownTravelUsesFlaggedFallback(t *testing.T) {
	// Fallback is 45m: 19:30 + 45m + 15m buffer = 20:30.
	tables := []TableSlot{table("d", "a", 18, 0, 700, 4)}

	got := solve(t, req, []Showtime{show("s", "b", 20, 30, 2*time.Hour, 300)}, tables, unknownTravel)
	if len(got) != 1 {
		t.Fatalf("got %d itineraries, want 1", len(got))
	}
	leg := got[0].Legs[1]
	if !leg.TravelEstimated || leg.TravelBefore != 45*time.Minute {
		t.Errorf("leg = %+v, want estimated 45m travel", leg)
	}
	if got := solve(t, req, []Showtime{show("s", "b", 20, 29, 2*time.Hour, 300)}, tables, unknownTravel); len(got) != 0 {
		t.Errorf("film at 20:29 with fallback travel: got %d itineraries, want 0", len(got))
	}

	known := solve(t, req, []Showtime{show("s", "b", 20, 30, 2*time.Hour, 300)}, tables, fixedTravel(45*time.Minute))
	if known[0].Legs[1].TravelEstimated {
		t.Error("real travel time should not be flagged as estimated")
	}
}

func TestSolveIsIndependentOfInputOrder(t *testing.T) {
	shows := []Showtime{
		show("s1", "a", 20, 30, 2*time.Hour, 300),
		show("s2", "a", 21, 0, 2*time.Hour, 320),
		show("s3", "a", 21, 30, 2*time.Hour, 280),
	}
	tables := []TableSlot{
		table("d1", "a", 18, 0, 700, 4),
		table("d2", "a", 18, 30, 650, 6),
	}
	want := solve(t, req, shows, tables, fixedTravel(10*time.Minute))
	if len(want) == 0 {
		t.Fatal("expected some itineraries")
	}

	slicesReverse(shows)
	slicesReverse(tables)
	got := solve(t, req, shows, tables, fixedTravel(10*time.Minute))

	if len(got) != len(want) {
		t.Fatalf("got %d itineraries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Legs[0].Venue.ID != want[i].Legs[0].Venue.ID || got[i].Legs[1].Venue.ID != want[i].Legs[1].Venue.ID {
			t.Fatalf("order differs at %d", i)
		}
	}
}

func slicesReverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func TestSolveRejectsInvalidRequest(t *testing.T) {
	if _, err := Solve(Request{}, nil, nil, unknownTravel, DefaultSolveOptions()); err == nil {
		t.Error("invalid request should be rejected")
	}
}
