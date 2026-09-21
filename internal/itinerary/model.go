// Package itinerary holds the domain model and, later, the solver that chains
// a movie and a dinner into feasible plans.
package itinerary

import (
	"errors"
	"time"
)

// IST is the timezone every timestamp in the system is expressed in. A fixed
// zone avoids depending on the tzdata database being present on the host.
var IST = time.FixedZone("IST", 5*60*60+30*60)

// Money is an amount in whole rupees. Paise never matter for planning.
type Money int

type VenueKind string

const (
	KindCinema     VenueKind = "cinema"
	KindRestaurant VenueKind = "restaurant"
)

// Venue is a place a party can go to. Area is a neighbourhood key such as
// "indiranagar"; travel time is estimated between areas, not exact addresses.
type Venue struct {
	ID   string
	Name string
	Kind VenueKind
	Area string
}

// Showtime is one screening of a movie.
type Showtime struct {
	ID          string
	Venue       Venue
	Movie       string
	Start       time.Time
	Runtime     time.Duration
	TicketPrice Money
}

func (s Showtime) End() time.Time { return s.Start.Add(s.Runtime) }

// TableSlot is a bookable window at a restaurant.
type TableSlot struct {
	Venue             Venue
	Start             time.Time
	Duration          time.Duration
	SeatsAvailable    int
	AvgSpendPerPerson Money
}

func (t TableSlot) End() time.Time { return t.Start.Add(t.Duration) }

// Request is what a user asks for: a movie and dinner near Area on Day for
// PartySize people, spending at most BudgetPerPerson in total.
type Request struct {
	Day             time.Time
	Area            string
	PartySize       int
	BudgetPerPerson Money
}

func (r Request) Validate() error {
	switch {
	case r.Day.IsZero():
		return errors.New("itinerary: day is required")
	case r.Area == "":
		return errors.New("itinerary: area is required")
	case r.PartySize < 1:
		return errors.New("itinerary: party size must be at least 1")
	case r.BudgetPerPerson <= 0:
		return errors.New("itinerary: budget must be positive")
	}
	return nil
}

// Leg is one stop in an itinerary. TravelBefore is the time spent getting
// here from the previous stop (zero for the first leg).
type Leg struct {
	Kind          VenueKind
	Venue         Venue
	Title         string
	Start         time.Time
	End           time.Time
	TravelBefore  time.Duration
	CostPerPerson Money
}

// Itinerary is an ordered plan. Legs do not overlap once travel is included.
type Itinerary struct {
	Legs []Leg
}

func (i Itinerary) Start() time.Time {
	if len(i.Legs) == 0 {
		return time.Time{}
	}
	return i.Legs[0].Start
}

func (i Itinerary) End() time.Time {
	if len(i.Legs) == 0 {
		return time.Time{}
	}
	return i.Legs[len(i.Legs)-1].End
}

func (i Itinerary) CostPerPerson() Money {
	var total Money
	for _, l := range i.Legs {
		total += l.CostPerPerson
	}
	return total
}

// Feasible reports whether every leg starts after the previous one ends plus
// the travel needed to reach it.
func (i Itinerary) Feasible() bool {
	for n := 1; n < len(i.Legs); n++ {
		prev, cur := i.Legs[n-1], i.Legs[n]
		if cur.Start.Before(prev.End.Add(cur.TravelBefore)) {
			return false
		}
	}
	return true
}
