package itinerary

import (
	"cmp"
	"slices"
	"time"
)

// Scoring parameters. Every component is scaled to [0, 1] and combined with
// fixed weights that sum to 1, so a plan's score is also in [0, 1].
const (
	weightGap    = 0.35
	weightTravel = 0.25
	weightBudget = 0.15
	weightTiming = 0.25

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
func Rank(req Request, plans []Itinerary, opts RankOptions) []Ranked {
	ranked := make([]Ranked, len(plans))
	for i, p := range plans {
		ranked[i] = Ranked{Itinerary: p, Score: score(req, p)}
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

func score(req Request, it Itinerary) float64 {
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

	s := weightGap*gap + weightTravel*trav + weightBudget*budget + weightTiming*timing
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
