package itinerary

import (
	"testing"
	"time"
)

func at(h, m int) time.Time {
	return time.Date(2026, 9, 26, h, m, 0, 0, IST)
}

func TestRequestValidate(t *testing.T) {
	valid := Request{Day: at(0, 0), Area: "indiranagar", PartySize: 4, BudgetPerPerson: 1500}

	cases := []struct {
		name    string
		mutate  func(*Request)
		wantErr bool
	}{
		{"valid", func(*Request) {}, false},
		{"missing day", func(r *Request) { r.Day = time.Time{} }, true},
		{"missing area", func(r *Request) { r.Area = "" }, true},
		{"empty party", func(r *Request) { r.PartySize = 0 }, true},
		{"zero budget", func(r *Request) { r.BudgetPerPerson = 0 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.mutate(&r)
			if err := r.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestItineraryFeasible(t *testing.T) {
	dinner := Leg{Kind: KindRestaurant, Start: at(18, 30), End: at(20, 0), CostPerPerson: 700}

	cases := []struct {
		name string
		show Leg
		want bool
	}{
		{
			name: "exactly enough travel time",
			show: Leg{Kind: KindCinema, Start: at(20, 30), End: at(23, 0), TravelBefore: 30 * time.Minute, CostPerPerson: 350},
			want: true,
		},
		{
			name: "one minute short",
			show: Leg{Kind: KindCinema, Start: at(20, 29), End: at(23, 0), TravelBefore: 30 * time.Minute, CostPerPerson: 350},
			want: false,
		},
		{
			name: "overlaps dinner",
			show: Leg{Kind: KindCinema, Start: at(19, 30), End: at(22, 0), TravelBefore: 10 * time.Minute, CostPerPerson: 350},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := Itinerary{Legs: []Leg{dinner, tc.show}}
			if got := it.Feasible(); got != tc.want {
				t.Fatalf("Feasible() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestItineraryTotals(t *testing.T) {
	it := Itinerary{Legs: []Leg{
		{Start: at(18, 30), End: at(20, 0), CostPerPerson: 700},
		{Start: at(20, 30), End: at(23, 0), TravelBefore: 20 * time.Minute, CostPerPerson: 350},
	}}
	if got := it.CostPerPerson(); got != 1050 {
		t.Errorf("CostPerPerson() = %d, want 1050", got)
	}
	if !it.Start().Equal(at(18, 30)) || !it.End().Equal(at(23, 0)) {
		t.Errorf("Start/End = %v / %v", it.Start(), it.End())
	}

	var empty Itinerary
	if !empty.Start().IsZero() || !empty.End().IsZero() || empty.CostPerPerson() != 0 || !empty.Feasible() {
		t.Error("empty itinerary should be zero-valued and trivially feasible")
	}
}
