package itinerary

import (
	"cmp"
	"slices"
	"time"
)

// Scoring parameters. Every component is scaled to [0, 1] and combined with
// fixed weights that sum to 1, so a plan's score is also in [0, 1].
const (
	weightGap       = 0.30
	weightTravel    = 0.15
	weightBudget    = 0.10
	weightTiming    = 0.20
	weightProximity = 0.25

	// A venue this much further from the requested area than the area is from
	// itself scores zero on proximity. "Near Indiranagar" should not quietly
	// mean 30 minutes away.
	proximityWorst = 30 * time.Minute
	// Used when the travel time from the requested area is unknown. It is the
	// same for every plan, so it cannot reorder them.
	neutralProximity = 0.5

	// A short wait between stops is welcome; only waiting beyond this counts
	// against a plan.
	idleGrace = 30 * time.Minute
	// A plan wasting this much extra time scores zero on the gap component.
	idleWorst = 3 * time.Hour
	// Travel of this length or more scores zero on the travel component.
	travelWorst = 60 * time.Minute
	// Plans that spend about this share of the budget score best: cheap
	// enough to be comfortable, without defaulting to the cheapest option.
	targetBudgetShare = 0.8
	// Dinner at this time of day scores best; being 3 hours away scores zero.
	preferredDinnerStart = 19*time.Hour + 30*time.Minute
	dinnerTimeWorst      = 3 * time.Hour
	// Plans built on estimated travel are less trustworthy, so they are
	// discounted rather than excluded.
	estimatedTravelDiscount = 0.9
)

// Ranked is a plan with its score. Higher is better.
type Ranked struct {
	Itinerary Itinerary
	Score     float64
}

// RankOptions controls how many plans are returned and how varied they are.
type RankOptions struct {
	// Limit is the maximum number of plans returned. Zero or less means all.
	Limit int
	// MaxPerVenue caps how often one venue may appear in the results so the
	// user is not shown five variations of the same dinner. The cap is soft:
	// if it would leave fewer than Limit results, the best remaining plans are
	// added anyway. Zero or less disables it.
	MaxPerVenue int
}

func DefaultRankOptions() RankOptions {
	return RankOptions{Limit: 5, MaxPerVenue: 2}
}

// Rank scores plans against the request and returns the best ones, best
// first. Ties break on the same fixed order Solve uses, so equal inputs give
// equal output.
//
// travel is used to measure how far each venue is from req.Area; it may be
// nil or return unknown values, in which case proximity does not affect the
// order.
func Rank(req Request, plans []Itinerary, travel TravelFunc, opts RankOptions) []Ranked {
	proximity := proximityScorer(req, travel)

	ranked := make([]Ranked, len(plans))
	for i, p := range plans {
		ranked[i] = Ranked{Itinerary: p, Score: score(req, p, proximity)}
	}
	slices.SortFunc(ranked, compareRanked)

	if opts.MaxPerVenue > 0 {
		ranked = diversify(ranked, opts)
	} else if opts.Limit > 0 && len(ranked) > opts.Limit {
		ranked = ranked[:opts.Limit]
	}
	return ranked
}

// diversify picks the best plans subject to the per-venue cap, then tops up
// from the ones it skipped if the cap left the list short.
func diversify(sorted []Ranked, opts RankOptions) []Ranked {
	limit := opts.Limit
	if limit <= 0 {
		limit = len(sorted)
	}

	used := map[string]int{}
	var picked, skipped []Ranked
	for _, r := range sorted {
		if len(picked) == limit {
			break
		}
		if overCap(used, r.Itinerary, opts.MaxPerVenue) {
			skipped = append(skipped, r)
			continue
		}
		for _, l := range r.Itinerary.Legs {
			used[l.Venue.ID]++
		}
		picked = append(picked, r)
	}
	for _, r := range skipped {
		if len(picked) == limit {
			break
		}
		picked = append(picked, r)
	}

	slices.SortFunc(picked, compareRanked)
	return picked
}

func overCap(used map[string]int, it Itinerary, max int) bool {
	for _, l := range it.Legs {
		if used[l.Venue.ID] >= max {
			return true
		}
	}
	return false
}

func compareRanked(a, b Ranked) int {
	return cmp.Or(cmp.Compare(b.Score, a.Score), compareItineraries(a.Itinerary, b.Itinerary))
}

// proximityScorer returns a function scoring how close an area is to the
// requested one, from 1 (as close as it gets) to 0 (proximityWorst or more
// further). Results are cached because a plan set only touches a few areas.
func proximityScorer(req Request, travel TravelFunc) func(area string) float64 {
	var self time.Duration
	if travel != nil {
		self, _ = travel(req.Area, req.Area)
	}

	cache := map[string]float64{}
	return func(area string) float64 {
		if s, ok := cache[area]; ok {
			return s
		}
		s := neutralProximity
		if travel != nil {
			if d, ok := travel(req.Area, area); ok {
				s = 1 - clamp01(float64(max(d-self, 0))/float64(proximityWorst))
			}
		}
		cache[area] = s
		return s
	}
}

func score(req Request, it Itinerary, proximity func(area string) float64) float64 {
	var (
		idle, travel time.Duration
		estimated    bool
		dinnerStart  time.Duration
	)
	for n, l := range it.Legs {
		if l.Kind == KindRestaurant {
			dinnerStart = timeOfDay(l.Start)
		}
		if n == 0 {
			continue
		}
		travel += l.TravelBefore
		idle += l.Start.Sub(it.Legs[n-1].End) - l.TravelBefore
		estimated = estimated || l.TravelEstimated
	}

	waste := max(idle-idleGrace, 0)
	gap := 1 - clamp01(float64(waste)/float64(idleWorst))
	trav := 1 - clamp01(float64(travel)/float64(travelWorst))

	share := float64(it.CostPerPerson()) / float64(req.BudgetPerPerson)
	budget := 1 - clamp01(abs(share-targetBudgetShare)/targetBudgetShare)

	timing := 1 - clamp01(float64(abs(dinnerStart-preferredDinnerStart))/float64(dinnerTimeWorst))

	var near float64
	for _, l := range it.Legs {
		near += proximity(l.Venue.Area)
	}
	near /= float64(max(len(it.Legs), 1))

	s := weightGap*gap + weightTravel*trav + weightBudget*budget + weightTiming*timing + weightProximity*near
	if estimated {
		s *= estimatedTravelDiscount
	}
	return s
}

func timeOfDay(t time.Time) time.Duration {
	t = t.In(IST)
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

func clamp01(x float64) float64 { return min(max(x, 0), 1) }

func abs[T ~int64 | ~float64](x T) T {
	if x < 0 {
		return -x
	}
	return x
}
