package providers

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"evenr/internal/itinerary"
)

var saturday = time.Date(2026, 9, 26, 15, 0, 0, 0, itinerary.IST)

func TestMockShowtimes(t *testing.T) {
	m := &MockShowtimes{}
	got, err := m.Showtimes(context.Background(), "indiranagar", saturday)
	if err != nil {
		t.Fatalf("Showtimes() = %v", err)
	}
	if want := len(cinemaSuffixes) * len(screeningStarts); len(got) != want {
		t.Fatalf("got %d showtimes, want %d", len(got), want)
	}

	seen := map[string]bool{}
	for _, s := range got {
		if seen[s.ID] {
			t.Errorf("duplicate showtime ID %s", s.ID)
		}
		seen[s.ID] = true
		if s.Venue.Area != "indiranagar" || s.Venue.Kind != itinerary.KindCinema {
			t.Errorf("unexpected venue %+v", s.Venue)
		}
		if y, mo, d := s.Start.In(itinerary.IST).Date(); y != 2026 || mo != time.September || d != 26 {
			t.Errorf("showtime %s starts on %v, want 2026-09-26", s.ID, s.Start)
		}
		if !s.End().After(s.Start) || s.TicketPrice < 200 || s.TicketPrice > 450 {
			t.Errorf("implausible showtime %+v", s)
		}
	}
}

func TestMockShowtimesDeterministic(t *testing.T) {
	m := &MockShowtimes{}
	a, _ := m.Showtimes(context.Background(), "koramangala", saturday)
	b, _ := m.Showtimes(context.Background(), "koramangala", saturday)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two calls returned different catalogs")
	}
}

func TestMockRestaurantsFilterByPartySize(t *testing.T) {
	m := &MockRestaurants{}
	all, err := m.Tables(context.Background(), "indiranagar", saturday, 1)
	if err != nil {
		t.Fatalf("Tables() = %v", err)
	}
	if want := len(restaurantStyles) * tableSlotsPerDay; len(all) != want {
		t.Fatalf("party of 1: got %d slots, want %d", len(all), want)
	}

	big, err := m.Tables(context.Background(), "indiranagar", saturday, 8)
	if err != nil {
		t.Fatalf("Tables() = %v", err)
	}
	if len(big) >= len(all) {
		t.Errorf("party of 8 should have fewer slots than party of 1 (%d vs %d)", len(big), len(all))
	}
	for _, s := range big {
		if s.SeatsAvailable < 8 {
			t.Errorf("slot with %d seats returned for a party of 8", s.SeatsAvailable)
		}
		if s.Duration != tableDuration {
			t.Errorf("unexpected duration %v", s.Duration)
		}
	}

	if _, err := m.Tables(context.Background(), "indiranagar", saturday, 0); err == nil {
		t.Error("party of 0 should be rejected")
	}
}

func TestMockTravel(t *testing.T) {
	m := &MockTravel{}
	ctx := context.Background()

	same, err := m.TravelTime(ctx, "indiranagar", "indiranagar")
	if err != nil || same != 5*time.Minute {
		t.Fatalf("same area = %v, %v; want 5m", same, err)
	}

	ab, _ := m.TravelTime(ctx, "indiranagar", "koramangala")
	ba, _ := m.TravelTime(ctx, "koramangala", "indiranagar")
	if ab != ba {
		t.Errorf("travel not symmetric: %v vs %v", ab, ba)
	}

	near, _ := m.TravelTime(ctx, "indiranagar", "koramangala")
	far, _ := m.TravelTime(ctx, "indiranagar", "whitefield")
	if far <= near {
		t.Errorf("whitefield (%v) should be farther than koramangala (%v)", far, near)
	}
}

func TestMocksRejectUnknownArea(t *testing.T) {
	ctx := context.Background()
	if _, err := (&MockShowtimes{}).Showtimes(ctx, "atlantis", saturday); !errors.Is(err, ErrUnknownArea) {
		t.Errorf("Showtimes: %v, want ErrUnknownArea", err)
	}
	if _, err := (&MockRestaurants{}).Tables(ctx, "atlantis", saturday, 2); !errors.Is(err, ErrUnknownArea) {
		t.Errorf("Tables: %v, want ErrUnknownArea", err)
	}
	if _, err := (&MockTravel{}).TravelTime(ctx, "indiranagar", "atlantis"); !errors.Is(err, ErrUnknownArea) {
		t.Errorf("TravelTime: %v, want ErrUnknownArea", err)
	}
}

func TestMocksHonourFaults(t *testing.T) {
	down := NewFaults(1, FaultConfig{Down: true})
	if _, err := (&MockShowtimes{Faults: down}).Showtimes(context.Background(), "indiranagar", saturday); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Showtimes with Down: %v, want ErrUnavailable", err)
	}

	slow := NewFaults(1, FaultConfig{Latency: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := (&MockRestaurants{Faults: slow}).Tables(ctx, "indiranagar", saturday, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Tables with slow upstream: %v, want DeadlineExceeded", err)
	}
}

func TestNearbyAreas(t *testing.T) {
	got := NearbyAreas("indiranagar", 6)
	want := []string{"mg-road", "koramangala"} // nearest first; whitefield is too far
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NearbyAreas(indiranagar, 6) = %v, want %v", got, want)
	}
	if got := NearbyAreas("indiranagar", 0); len(got) != 0 {
		t.Errorf("radius 0 = %v, want none", got)
	}
	if got := NearbyAreas("atlantis", 100); got != nil {
		t.Errorf("unknown area = %v, want nil", got)
	}
}
