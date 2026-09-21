// Package planner runs the whole pipeline for one request: fetch upstream data
// under a latency budget, solve for feasible plans, and rank them.
package planner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"evenr/internal/aggregator"
	"evenr/internal/itinerary"
	"evenr/internal/providers"
)

var (
	// ErrInvalidRequest wraps a request that failed validation.
	ErrInvalidRequest = errors.New("planner: invalid request")
	// ErrNoData means every upstream call failed, so there was nothing to plan
	// from. Callers should treat it as a service outage, not a bad request.
	ErrNoData = errors.New("planner: no upstream data available")
)

// Defaults for New.
const (
	DefaultBudget       = 300 * time.Millisecond
	DefaultNearbyRadius = 6.0 // km
)

// Planner turns a Request into ranked itineraries.
type Planner struct {
	Aggregator *aggregator.Aggregator
	// Nearby returns extra areas to search besides the requested one.
	Nearby func(area string) []string
	// Budget is the total time allowed for all upstream calls.
	Budget time.Duration
	Solve  itinerary.SolveOptions
	Rank   itinerary.RankOptions
}

// New returns a Planner with default budget and options.
func New(agg *aggregator.Aggregator) *Planner {
	return &Planner{
		Aggregator: agg,
		Nearby:     func(area string) []string { return providers.NearbyAreas(area, DefaultNearbyRadius) },
		Budget:     DefaultBudget,
		Solve:      itinerary.DefaultSolveOptions(),
		Rank:       itinerary.DefaultRankOptions(),
	}
}

// Result is the answer to one request.
type Result struct {
	// Plans are the best itineraries, best first. Empty is a valid answer.
	Plans []itinerary.Ranked
	// Failures lists upstream calls that failed or timed out. When non-empty
	// the plans were built from partial data.
	Failures []aggregator.Failure
	// Considered is how many feasible plans existed before ranking cut them
	// down.
	Considered int
	Elapsed    time.Duration
}

func (r Result) Degraded() bool { return len(r.Failures) > 0 }

// Plan answers a request. It returns ErrInvalidRequest for a bad request,
// providers.ErrUnknownArea if the area has no data, and ErrNoData if nothing
// could be fetched at all. Any other shortfall from upstream failures still
// yields a Result, flagged by Failures.
func (p *Planner) Plan(ctx context.Context, req itinerary.Request) (Result, error) {
	start := time.Now()
	if err := req.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	var extra []string
	if p.Nearby != nil {
		extra = p.Nearby(req.Area)
	}
	data, err := p.Aggregator.Fetch(ctx, req, extra, p.Budget)
	if err != nil {
		return Result{}, err
	}

	for _, f := range data.Failures {
		if f.Source != aggregator.SourceTravel && f.Target == req.Area && errors.Is(f.Err, providers.ErrUnknownArea) {
			return Result{}, fmt.Errorf("%w: %q", providers.ErrUnknownArea, req.Area)
		}
	}
	if len(data.Showtimes) == 0 && len(data.Tables) == 0 && data.Degraded() {
		return Result{}, ErrNoData
	}

	solved, err := itinerary.Solve(req, data.Showtimes, data.Tables, data.Travel.Between, p.Solve)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Plans:      itinerary.Rank(req, solved, data.Travel.Between, p.Rank),
		Failures:   data.Failures,
		Considered: len(solved),
		Elapsed:    time.Since(start),
	}, nil
}
