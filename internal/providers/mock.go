package providers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"evenr/internal/itinerary"
)

var (
	_ ShowtimeProvider   = (*MockShowtimes)(nil)
	_ RestaurantProvider = (*MockRestaurants)(nil)
	_ TravelProvider     = (*MockTravel)(nil)
)

// MockShowtimes serves a deterministic catalog of screenings. Faults may be
// nil for an upstream that never misbehaves.
type MockShowtimes struct {
	Faults *Faults
}

func (m *MockShowtimes) Showtimes(ctx context.Context, areaKey string, day time.Time) ([]itinerary.Showtime, error) {
	if err := m.Faults.Apply(ctx); err != nil {
		return nil, err
	}
	a, ok := findArea(areaKey)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownArea, areaKey)
	}

	base := midnight(day)
	var out []itinerary.Showtime
	for _, cinema := range cinemasIn(a) {
		// Stagger cinemas so their screenings don't all line up.
		stagger := time.Duration(hash(cinema.ID)%3) * 10 * time.Minute
		for i, offset := range screeningStarts {
			mv := movies[hash(cinema.ID, fmt.Sprint(i))%uint32(len(movies))]
			start := base.Add(offset + stagger)
			out = append(out, itinerary.Showtime{
				ID:          fmt.Sprintf("%s-%s-%d", cinema.ID, base.Format("20060102"), i),
				Venue:       cinema,
				Movie:       mv.title,
				Start:       start,
				Runtime:     mv.runtime,
				TicketPrice: itinerary.Money(200 + hash(cinema.ID, mv.title)%251),
			})
		}
	}
	return out, nil
}

// MockRestaurants serves deterministic table availability.
type MockRestaurants struct {
	Faults *Faults
}

func (m *MockRestaurants) Tables(ctx context.Context, areaKey string, day time.Time, partySize int) ([]itinerary.TableSlot, error) {
	if err := m.Faults.Apply(ctx); err != nil {
		return nil, err
	}
	if partySize < 1 {
		return nil, errors.New("providers: party size must be at least 1")
	}
	a, ok := findArea(areaKey)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownArea, areaKey)
	}

	base := midnight(day)
	var out []itinerary.TableSlot
	for _, r := range restaurantsIn(a) {
		spend := itinerary.Money(400 + hash(r.ID)%801)
		for i := range tableSlotsPerDay {
			start := base.Add(firstTableStart + time.Duration(i)*tableInterval)
			seats := int(hash(r.ID, start.Format("1504"))%10) + 1
			if seats < partySize {
				continue
			}
			out = append(out, itinerary.TableSlot{
				Venue:             r,
				Start:             start,
				Duration:          tableDuration,
				SeatsAvailable:    seats,
				AvgSpendPerPerson: spend,
			})
		}
	}
	return out, nil
}

// MockTravel estimates travel time from the distance between area centres.
type MockTravel struct {
	Faults *Faults
}

const (
	travelBase       = 5 * time.Minute
	travelPerKm      = 3 * time.Minute
	roadDistanceBias = 1.4 // roads are longer than the straight line
)

func (m *MockTravel) TravelTime(ctx context.Context, from, to string) (time.Duration, error) {
	if err := m.Faults.Apply(ctx); err != nil {
		return 0, err
	}
	a, ok := findArea(from)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownArea, from)
	}
	b, ok := findArea(to)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownArea, to)
	}
	km := math.Hypot(a.x-b.x, a.y-b.y) * roadDistanceBias
	return (travelBase + time.Duration(km*float64(travelPerKm))).Round(time.Minute), nil
}
