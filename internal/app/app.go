// Package app assembles the service: mock upstreams wrapped in hedging and
// caching, the planner, and the HTTP handler.
package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"evenr/internal/aggregator"
	"evenr/internal/httpapi"
	"evenr/internal/planner"
	"evenr/internal/providers"
)

// Config selects which resilience features are on.
type Config struct {
	// PlanBudget is the total time allowed for the upstream calls of one request.
	PlanBudget time.Duration
	// CacheTTL is how long travel times are cached. Zero disables the cache.
	CacheTTL time.Duration
	// HedgeDelay is how long to wait before sending a hedged second attempt
	// to an upstream. Zero disables hedging.
	HedgeDelay time.Duration
	// HedgeRatio is the hedge budget: hedges allowed per request.
	HedgeRatio float64
	// EnableAdmin exposes /admin/faults for changing upstream faults at
	// runtime. For benchmarking only.
	EnableAdmin bool
}

func DefaultConfig() Config {
	return Config{
		PlanBudget: planner.DefaultBudget,
		CacheTTL:   10 * time.Minute,
		HedgeRatio: 0.1,
	}
}

// ConfigFromEnv reads PLAN_BUDGET, CACHE_TTL, HEDGE_DELAY (durations such as
// 300ms), HEDGE_RATIO (a number) and ENABLE_ADMIN (1 or true) over the defaults.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := DefaultConfig()

	duration := func(key string, dst *time.Duration, allowZero bool) error {
		v := getenv(key)
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 || (d == 0 && !allowZero) {
			return fmt.Errorf("%s must be a valid duration such as 300ms, got %q", key, v)
		}
		*dst = d
		return nil
	}
	if err := duration("PLAN_BUDGET", &cfg.PlanBudget, false); err != nil {
		return Config{}, err
	}
	if err := duration("CACHE_TTL", &cfg.CacheTTL, true); err != nil {
		return Config{}, err
	}
	if err := duration("HEDGE_DELAY", &cfg.HedgeDelay, true); err != nil {
		return Config{}, err
	}

	if v := getenv("HEDGE_RATIO"); v != "" {
		r, err := strconv.ParseFloat(v, 64)
		if err != nil || r < 0 || r > 1 {
			return Config{}, fmt.Errorf("HEDGE_RATIO must be a number between 0 and 1, got %q", v)
		}
		cfg.HedgeRatio = r
	}
	if v := getenv("ENABLE_ADMIN"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("ENABLE_ADMIN must be true or false, got %q", v)
		}
		cfg.EnableAdmin = b
	}
	return cfg, nil
}

// App is the assembled service. The exported fields let tests and metrics
// reach the pieces they need.
type App struct {
	Handler http.Handler
	Planner *planner.Planner
	// Faults controls the mock upstreams, keyed "showtimes", "tables", "travel".
	Faults map[string]*providers.Faults
	// Cache is nil when caching is disabled.
	Cache *providers.CachedTravel
	// Hedgers is empty when hedging is disabled.
	Hedgers map[string]*providers.Hedger
}

// New builds the service. Each upstream is wired as
//
//	cache -> hedge -> mock upstream
//
// so a hedge protects the one fetch the cache makes instead of racing it.
func New(cfg Config, logger *slog.Logger) *App {
	a := &App{
		Faults: map[string]*providers.Faults{
			"showtimes": providers.NewFaults(1, providers.FaultConfig{}),
			"tables":    providers.NewFaults(2, providers.FaultConfig{}),
			"travel":    providers.NewFaults(3, providers.FaultConfig{}),
		},
		Hedgers: map[string]*providers.Hedger{},
	}

	var (
		showtimes providers.ShowtimeProvider   = &providers.MockShowtimes{Faults: a.Faults["showtimes"]}
		tables    providers.RestaurantProvider = &providers.MockRestaurants{Faults: a.Faults["tables"]}
		travel    providers.TravelProvider     = &providers.MockTravel{Faults: a.Faults["travel"]}
	)

	if cfg.HedgeDelay > 0 {
		hedger := func(name string) *providers.Hedger {
			h := providers.NewHedger(cfg.HedgeDelay, cfg.HedgeRatio)
			a.Hedgers[name] = h
			return h
		}
		showtimes = providers.HedgedShowtimes{Next: showtimes, Hedger: hedger("showtimes")}
		tables = providers.HedgedRestaurants{Next: tables, Hedger: hedger("tables")}
		travel = providers.HedgedTravel{Next: travel, Hedger: hedger("travel")}
	}
	if cfg.CacheTTL > 0 {
		a.Cache = providers.NewCachedTravel(travel, cfg.CacheTTL)
		travel = a.Cache
	}

	a.Planner = planner.New(&aggregator.Aggregator{
		Showtimes:   showtimes,
		Restaurants: tables,
		Travel:      travel,
	})
	a.Planner.Budget = cfg.PlanBudget

	var opts []httpapi.Option
	if cfg.EnableAdmin {
		opts = append(opts, httpapi.WithFaultAdmin(a.Faults))
	}
	a.Handler = httpapi.New(a.Planner, logger, opts...)
	return a
}
