package itinerary

import (
	"testing"
	"time"
)

// plan builds a dinner-then-film itinerary, failing the test if it is not
// feasible.
func plan(t *testing.T, tb TableSlot, sh Showtime, travel TravelFunc) Itinerary {
	t.Helper()
	opts := DefaultSolveOptions()
	latest := at(0, 0).Add(opts.LatestEnd)
	it, ok := chain(dinnerLeg(tb), filmLeg(sh), travel, opts, latest)
	if !ok {
		t.Fatalf("plan %s + %s is not feasible", tb.Venue.ID, sh.ID)
	}
	return it
}

func score1(p Itinerary) float64 {
	return score(req, p, func(string) float64 { return neutralProximity })
}

func TestRankPrefersLessIdleTime(t *testing.T) {
	// Dinner ends 19:30. Same travel and price, only the film start differs.
	tb := table("d", "a", 18, 0, 700, 4)
	tight := plan(t, tb, show("tight", "a", 20, 0, 2*time.Hour, 300), fixedTravel(10*time.Minute))
	loose := plan(t, tb, show("loose", "a", 22, 30, 90*time.Minute, 300), fixedTravel(10*time.Minute))

	got := Rank(req, []Itinerary{loose, tight}, unknownTravel, RankOptions{})
	if got[0].Itinerary.Legs[1].Venue.ID != "tight" {
		t.Errorf("best = %s, want tight (scores %.3f vs %.3f)",
			got[0].Itinerary.Legs[1].Venue.ID, got[0].Score, got[1].Score)
	}
}

func TestRankPrefersShorterTravel(t *testing.T) {
	// Both films start with 20 minutes of idle time after travel, so only
	// the travel time differs.
	tb := table("d", "a", 18, 0, 700, 4)
	near := plan(t, tb, show("near", "a", 20, 0, 2*time.Hour, 300), fixedTravel(10*time.Minute))
	far := plan(t, tb, show("far", "b", 20, 40, 2*time.Hour, 300), fixedTravel(50*time.Minute))

	if score1(near) <= score1(far) {
		t.Errorf("near %.3f should beat far %.3f", score1(near), score1(far))
	}
}

func TestRankDiscountsEstimatedTravel(t *testing.T) {
	tb := table("d", "a", 18, 0, 700, 4)
	sh := show("s", "b", 20, 35, 2*time.Hour, 300)

	known := plan(t, tb, sh, fixedTravel(45*time.Minute))
	guessed := plan(t, tb, sh, unknownTravel)
	if !guessed.Legs[1].TravelEstimated || known.Legs[1].TravelEstimated {
		t.Fatal("fixture should differ only in whether travel was estimated")
	}

	if score1(known) <= score1(guessed) {
		t.Errorf("known travel %.3f should beat estimated %.3f", score1(known), score1(guessed))
	}
	if got, want := score1(guessed), score1(known)*estimatedTravelDiscount; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("estimated score = %.4f, want %.4f", got, want)
	}
}

func TestRankPrefersDinnerNearPreferredTime(t *testing.T) {
	sh := show("s", "a", 22, 30, 90*time.Minute, 300)
	good := plan(t, table("good", "a", 19, 30, 700, 4), sh, fixedTravel(10*time.Minute))
	early := plan(t, table("early", "a", 18, 0, 700, 4), sh, fixedTravel(10*time.Minute))

	if good.Legs[0].Start.Equal(early.Legs[0].Start) {
		t.Fatal("fixture error: dinners should start at different times")
	}
	if got := Rank(req, []Itinerary{early, good}, unknownTravel, RankOptions{}); got[0].Itinerary.Legs[0].Venue.ID != "good" {
		t.Errorf("best = %s, want good", got[0].Itinerary.Legs[0].Venue.ID)
	}
}

func TestRankScoresStayInRange(t *testing.T) {
	tb := table("d", "a", 18, 0, 700, 4)
	plans := []Itinerary{
		plan(t, tb, show("a", "a", 20, 0, 2*time.Hour, 300), fixedTravel(10*time.Minute)),
		plan(t, tb, show("b", "a", 23, 0, 90*time.Minute, 300), fixedTravel(55*time.Minute)),
		plan(t, tb, show("c", "a", 21, 0, 2*time.Hour, 800), fixedTravel(5*time.Minute)),
	}
	for _, r := range Rank(req, plans, unknownTravel, RankOptions{}) {
		if r.Score < 0 || r.Score > 1 {
			t.Errorf("score %.3f outside [0, 1]", r.Score)
		}
	}
}

func TestRankSortsAndLimits(t *testing.T) {
	tb := table("d", "a", 18, 0, 700, 4)
	var plans []Itinerary
	for i, start := range []int{20, 21, 22, 23} {
		plans = append(plans, plan(t, tb, show(string(rune('a'+i)), "a", start, 0, 60*time.Minute, 300), fixedTravel(10*time.Minute)))
	}

	all := Rank(req, plans, unknownTravel, RankOptions{})
	if len(all) != 4 {
		t.Fatalf("got %d results, want 4", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Score > all[i-1].Score {
			t.Errorf("results not sorted: %.3f after %.3f", all[i].Score, all[i-1].Score)
		}
	}

	top2 := Rank(req, plans, unknownTravel, RankOptions{Limit: 2})
	if len(top2) != 2 || top2[0].Score != all[0].Score || top2[1].Score != all[1].Score {
		t.Errorf("Limit 2 did not return the top two")
	}
}

func TestRankDiversifiesVenues(t *testing.T) {
	// Three restaurants and three cinemas with identical scores per pairing.
	// Ties break by venue ID, so without a cap the top four would all be d1
	// or repeat s1.
	var plans []Itinerary
	for _, d := range []string{"d1", "d2", "d3"} {
		tb := table(d, "a", 18, 0, 700, 4)
		for _, s := range []string{"s1", "s2", "s3"} {
			plans = append(plans, plan(t, tb, show(s, "a", 21, 0, 2*time.Hour, 300), fixedTravel(10*time.Minute)))
		}
	}

	got := Rank(req, plans, unknownTravel, RankOptions{Limit: 4, MaxPerVenue: 2})
	if len(got) != 4 {
		t.Fatalf("got %d results, want 4", len(got))
	}
	counts := map[string]int{}
	for _, r := range got {
		for _, l := range r.Itinerary.Legs {
			counts[l.Venue.ID]++
		}
	}
	for venue, n := range counts {
		if n > 2 {
			t.Errorf("venue %s appears %d times, want at most 2", venue, n)
		}
	}
}

func TestRankCapIsSoft(t *testing.T) {
	// One restaurant and six films: the cap would allow two results, but the
	// caller asked for five and only has one restaurant to offer.
	tb := table("only", "a", 18, 0, 700, 4)
	var plans []Itinerary
	for i := range 6 {
		plans = append(plans, plan(t, tb, show(string(rune('a'+i)), "a", 21, i*5, 2*time.Hour, 300), fixedTravel(10*time.Minute)))
	}
	if got := Rank(req, plans, unknownTravel, RankOptions{Limit: 5, MaxPerVenue: 2}); len(got) != 5 {
		t.Errorf("got %d results, want 5 despite the cap", len(got))
	}
}

func TestRankIsDeterministic(t *testing.T) {
	tb := table("d", "a", 18, 0, 700, 4)
	var plans []Itinerary
	for _, s := range []string{"x", "y", "z"} {
		plans = append(plans, plan(t, tb, show(s, "a", 21, 0, 2*time.Hour, 300), fixedTravel(10*time.Minute)))
	}
	want := Rank(req, plans, unknownTravel, DefaultRankOptions())

	slicesReverse(plans)
	got := Rank(req, plans, unknownTravel, DefaultRankOptions())
	for i := range want {
		if got[i].Itinerary.Legs[1].Venue.ID != want[i].Itinerary.Legs[1].Venue.ID {
			t.Fatalf("order differs at %d after reversing the input", i)
		}
	}
}

func TestRankEmpty(t *testing.T) {
	if got := Rank(req, nil, unknownTravel, DefaultRankOptions()); len(got) != 0 {
		t.Errorf("got %d results from no plans", len(got))
	}
}

// distances answers proximity lookups from the requested area "a": itself in 5
// minutes, "b" in 25, "c" in 60.
func distances(from, to string) (time.Duration, bool) {
	if from != "a" {
		return 0, false
	}
	switch to {
	case "a":
		return 5 * time.Minute, true
	case "b":
		return 25 * time.Minute, true
	case "c":
		return 60 * time.Minute, true
	}
	return 0, false
}

func TestRankPrefersRequestedArea(t *testing.T) {
	// Identical plans in every respect except where the venues are. The
	// request is for area "a", so the plan in "a" must win, and the one in
	// "b" must beat the one in "c".
	inArea := func(dinner, film, area string) Itinerary {
		return plan(t, table(dinner, area, 18, 0, 700, 4), show(film, area, 20, 0, 2*time.Hour, 300), fixedTravel(10*time.Minute))
	}
	here := inArea("d-a", "s-a", "a")
	near := inArea("d-b", "s-b", "b")
	far := inArea("d-c", "s-c", "c")

	got := Rank(req, []Itinerary{far, near, here}, distances, RankOptions{})
	order := []string{
		got[0].Itinerary.Legs[0].Venue.Area,
		got[1].Itinerary.Legs[0].Venue.Area,
		got[2].Itinerary.Legs[0].Venue.Area,
	}
	if order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("order by area = %v, want [a b c]", order)
	}
	if got[0].Score <= got[1].Score || got[1].Score <= got[2].Score {
		t.Errorf("scores should fall with distance: %.3f %.3f %.3f", got[0].Score, got[1].Score, got[2].Score)
	}
}

func TestRankProximityBeatsSmallTimingAdvantage(t *testing.T) {
	// The regression that motivated proximity: a plan in another area with
	// marginally nicer timing used to outrank a plan in the requested area.
	home := plan(t, table("d-a", "a", 18, 30, 700, 4), show("s-a", "a", 20, 40, 2*time.Hour, 300), fixedTravel(10*time.Minute))
	away := plan(t, table("d-b", "b", 19, 30, 700, 4), show("s-b", "b", 21, 40, 2*time.Hour, 300), fixedTravel(10*time.Minute))

	got := Rank(req, []Itinerary{away, home}, distances, RankOptions{})
	if got[0].Itinerary.Legs[0].Venue.Area != "a" {
		t.Errorf("a plan 20 minutes away outranked one in the requested area: %s first (%.3f vs %.3f)",
			got[0].Itinerary.Legs[0].Venue.Area, got[0].Score, got[1].Score)
	}
}

func TestRankUnknownProximityDoesNotReorder(t *testing.T) {
	// If travel from the requested area is unavailable, proximity must be a
	// constant so the other factors still decide.
	tb := table("d", "a", 18, 0, 700, 4)
	tight := plan(t, tb, show("tight", "b", 20, 0, 2*time.Hour, 300), fixedTravel(10*time.Minute))
	loose := plan(t, tb, show("loose", "c", 22, 30, 90*time.Minute, 300), fixedTravel(10*time.Minute))

	for name, travel := range map[string]TravelFunc{"nil": nil, "unknown": unknownTravel} {
		got := Rank(req, []Itinerary{loose, tight}, travel, RankOptions{})
		if got[0].Itinerary.Legs[1].Venue.ID != "tight" {
			t.Errorf("%s travel: best = %s, want tight", name, got[0].Itinerary.Legs[1].Venue.ID)
		}
	}
}
