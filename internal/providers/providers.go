// Package providers defines the upstream data sources the planner depends on
// and ships mock implementations with injectable latency and failure.
package providers

import (
	"context"
	"errors"
	"time"

	"evenr/internal/itinerary"
)

var (
	// ErrUnavailable is returned when an upstream is down or fails a call.
	ErrUnavailable = errors.New("providers: upstream unavailable")
	// ErrUnknownArea is returned for an area the provider has no data for.
	ErrUnknownArea = errors.New("providers: unknown area")
)

// ShowtimeProvider returns screenings in an area on a given day.
type ShowtimeProvider interface {
	Showtimes(ctx context.Context, area string, day time.Time) ([]itinerary.Showtime, error)
}

// RestaurantProvider returns bookable table windows in an area on a given
// day for a party of the given size.
type RestaurantProvider interface {
	Tables(ctx context.Context, area string, day time.Time, partySize int) ([]itinerary.TableSlot, error)
}

// TravelProvider estimates door-to-door travel time between two areas.
type TravelProvider interface {
	TravelTime(ctx context.Context, from, to string) (time.Duration, error)
}
