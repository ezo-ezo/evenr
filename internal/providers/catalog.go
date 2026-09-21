package providers

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"time"

	"evenr/internal/itinerary"
)

// area is a neighbourhood with rough map coordinates in kilometres. Travel
// time between areas is derived from the distance between these points.
type area struct {
	key  string
	name string
	x, y float64
}

var areas = []area{
	{"indiranagar", "Indiranagar", 0, 0},
	{"koramangala", "Koramangala", 2, -5},
	{"mg-road", "MG Road", -3, -1},
	{"whitefield", "Whitefield", 14, 2},
	{"hsr-layout", "HSR Layout", 4, -9},
	{"jayanagar", "Jayanagar", -1, -10},
}

func findArea(key string) (area, bool) {
	for _, a := range areas {
		if a.key == key {
			return a, true
		}
	}
	return area{}, false
}

type movie struct {
	title   string
	runtime time.Duration
}

var movies = []movie{
	{"The Last Monsoon", 165 * time.Minute},
	{"Midnight Local", 128 * time.Minute},
	{"Paper Kites", 112 * time.Minute},
	{"Two Stops Ahead", 141 * time.Minute},
	{"Salt & Saffron", 119 * time.Minute},
}

// Daily screening start times, as offsets from midnight.
var screeningStarts = []time.Duration{
	12 * time.Hour,
	15*time.Hour + 15*time.Minute,
	18*time.Hour + 30*time.Minute,
	21*time.Hour + 45*time.Minute,
}

var cinemaSuffixes = []string{"Cineplex", "Multiplex"}

var restaurantStyles = []string{"Trattoria", "Tandoor House", "Noodle Bar"}

const (
	firstTableStart  = 18 * time.Hour
	tableInterval    = 30 * time.Minute
	tableSlotsPerDay = 8
	tableDuration    = 90 * time.Minute
)

func cinemasIn(a area) []itinerary.Venue {
	venues := make([]itinerary.Venue, len(cinemaSuffixes))
	for i, suffix := range cinemaSuffixes {
		venues[i] = itinerary.Venue{
			ID:   fmt.Sprintf("cin-%s-%d", a.key, i+1),
			Name: a.name + " " + suffix,
			Kind: itinerary.KindCinema,
			Area: a.key,
		}
	}
	return venues
}

func restaurantsIn(a area) []itinerary.Venue {
	venues := make([]itinerary.Venue, len(restaurantStyles))
	for i, style := range restaurantStyles {
		venues[i] = itinerary.Venue{
			ID:   fmt.Sprintf("rest-%s-%d", a.key, i+1),
			Name: a.name + " " + style,
			Kind: itinerary.KindRestaurant,
			Area: a.key,
		}
	}
	return venues
}

// hash gives a stable pseudo-random number for a set of strings, so the mock
// catalog is identical on every run without storing it.
func hash(parts ...string) uint32 {
	h := fnv.New32a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum32()
}

// midnight returns 00:00 IST on the calendar day of t.
func midnight(t time.Time) time.Time {
	y, m, d := t.In(itinerary.IST).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, itinerary.IST)
}

// NearbyAreas returns the areas within radiusKm (straight-line) of the given
// area, nearest first, excluding the area itself. It returns nil for an
// unknown area.
func NearbyAreas(key string, radiusKm float64) []string {
	origin, ok := findArea(key)
	if !ok {
		return nil
	}

	type candidate struct {
		key string
		km  float64
	}
	var found []candidate
	for _, a := range areas {
		if a.key == key {
			continue
		}
		if km := math.Hypot(a.x-origin.x, a.y-origin.y); km <= radiusKm {
			found = append(found, candidate{a.key, km})
		}
	}
	slices.SortFunc(found, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.km, b.km), cmp.Compare(a.key, b.key))
	})

	out := make([]string, len(found))
	for i, c := range found {
		out[i] = c.key
	}
	return out
}
