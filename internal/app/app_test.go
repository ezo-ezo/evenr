package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const planBody = `{"date":"2026-09-26","area":"indiranagar","party_size":2,"budget_per_person":1500}`

type planResult struct {
	Plans    []json.RawMessage `json:"plans"`
	Degraded bool              `json:"degraded"`
	Failures []struct {
		Source string `json:"source"`
		Reason string `json:"reason"`
	} `json:"failures"`
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, planResult) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var res planResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

func TestConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	cfg, err := ConfigFromEnv(env(nil))
	if err != nil || cfg != DefaultConfig() {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}

	cfg, err = ConfigFromEnv(env(map[string]string{
		"PLAN_BUDGET": "150ms", "CACHE_TTL": "0", "HEDGE_DELAY": "40ms", "HEDGE_RATIO": "0.25", "ENABLE_ADMIN": "true",
	}))
	if err != nil {
		t.Fatalf("ConfigFromEnv() = %v", err)
	}
	if cfg.PlanBudget != 150*time.Millisecond || cfg.CacheTTL != 0 || cfg.HedgeDelay != 40*time.Millisecond ||
		cfg.HedgeRatio != 0.25 || !cfg.EnableAdmin {
		t.Errorf("parsed config = %+v", cfg)
	}

	for name, m := range map[string]map[string]string{
		"bad budget":     {"PLAN_BUDGET": "fast"},
		"zero budget":    {"PLAN_BUDGET": "0s"},
		"negative ttl":   {"CACHE_TTL": "-1s"},
		"bad ratio":      {"HEDGE_RATIO": "2"},
		"bad admin flag": {"ENABLE_ADMIN": "maybe"},
	} {
		if _, err := ConfigFromEnv(env(m)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestServesPlans(t *testing.T) {
	a := New(DefaultConfig(), quiet)
	rec, res := do(t, a.Handler, http.MethodPost, "/v1/plan", planBody)
	if rec.Code != http.StatusOK || len(res.Plans) != 5 || res.Degraded {
		t.Fatalf("status %d, %d plans, degraded %v: %s", rec.Code, len(res.Plans), res.Degraded, rec.Body)
	}
}

func TestAdminIsOffUnlessEnabled(t *testing.T) {
	off := New(DefaultConfig(), quiet)
	if rec, _ := do(t, off.Handler, http.MethodGet, "/admin/faults", ""); rec.Code != http.StatusNotFound {
		t.Errorf("admin without ENABLE_ADMIN: status %d, want 404", rec.Code)
	}

	cfg := DefaultConfig()
	cfg.EnableAdmin = true
	on := New(cfg, quiet)
	if rec, _ := do(t, on.Handler, http.MethodGet, "/admin/faults", ""); rec.Code != http.StatusOK {
		t.Errorf("admin with ENABLE_ADMIN: status %d, want 200", rec.Code)
	}
}

func TestAdminSetsFaults(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableAdmin = true
	a := New(cfg, quiet)

	rec, _ := do(t, a.Handler, http.MethodPut, "/admin/faults/travel", `{"latency_ms":40,"tail_rate":0.1,"tail_latency_ms":500}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body)
	}
	got := a.Faults["travel"].Config()
	if got.Latency != 40*time.Millisecond || got.TailRate != 0.1 || got.TailLatency != 500*time.Millisecond {
		t.Errorf("config = %+v", got)
	}

	for name, tc := range map[string]struct{ path, body string }{
		"unknown provider": {"/admin/faults/nope", `{}`},
		"negative latency": {"/admin/faults/travel", `{"latency_ms":-1}`},
		"rate above one":   {"/admin/faults/travel", `{"error_rate":1.5}`},
		"huge latency":     {"/admin/faults/travel", `{"latency_ms":9999999}`},
		"unknown field":    {"/admin/faults/travel", `{"chaos":true}`},
		"not json":         {"/admin/faults/travel", `slow please`},
	} {
		rec, _ := do(t, a.Handler, http.MethodPut, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 400 or 404", name, rec.Code)
		}
	}
}

// The cache should hide a travel upstream that later becomes slow.
func TestCacheMasksSlowTravelUpstream(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableAdmin = true
	cfg.PlanBudget = 200 * time.Millisecond
	a := New(cfg, quiet)

	if _, res := do(t, a.Handler, http.MethodPost, "/v1/plan", planBody); res.Degraded {
		t.Fatal("warm-up request should not be degraded")
	}

	do(t, a.Handler, http.MethodPut, "/admin/faults/travel", `{"latency_ms":2000}`)

	start := time.Now()
	_, res := do(t, a.Handler, http.MethodPost, "/v1/plan", planBody)
	if res.Degraded || len(res.Plans) == 0 {
		t.Errorf("cached travel times should have covered the slow upstream: degraded=%v plans=%d", res.Degraded, len(res.Plans))
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("took %v; nothing should have waited on the slow upstream", elapsed)
	}
	if s := a.Cache.Stats(); s.Hits == 0 {
		t.Errorf("cache stats = %+v, expected hits", s)
	}
}

// Without a cache, the same slowdown degrades the response but still answers
// within the budget.
func TestSlowTravelDegradesButAnswersWithinBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableAdmin = true
	cfg.CacheTTL = 0
	cfg.PlanBudget = 150 * time.Millisecond
	a := New(cfg, quiet)

	do(t, a.Handler, http.MethodPut, "/admin/faults/travel", `{"latency_ms":2000}`)

	start := time.Now()
	rec, res := do(t, a.Handler, http.MethodPost, "/v1/plan", planBody)
	if rec.Code != http.StatusOK || !res.Degraded || len(res.Plans) == 0 {
		t.Fatalf("status %d degraded=%v plans=%d", rec.Code, res.Degraded, len(res.Plans))
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %v with a 150ms budget", elapsed)
	}
	for _, f := range res.Failures {
		if f.Source != "travel" || f.Reason != "timeout" {
			t.Errorf("unexpected failure %+v", f)
		}
	}
}

func TestFeatureTogglesBuildTheRightStack(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CacheTTL = 0
	if a := New(cfg, quiet); a.Cache != nil || len(a.Hedgers) != 0 {
		t.Error("cache and hedging should be off")
	}

	cfg.CacheTTL = time.Minute
	cfg.HedgeDelay = 20 * time.Millisecond
	a := New(cfg, quiet)
	if a.Cache == nil || len(a.Hedgers) != 3 {
		t.Errorf("cache=%v hedgers=%d, want cache on and 3 hedgers", a.Cache != nil, len(a.Hedgers))
	}
	if rec, res := do(t, a.Handler, http.MethodPost, "/v1/plan", planBody); rec.Code != http.StatusOK || len(res.Plans) == 0 {
		t.Errorf("full stack request failed: %d %s", rec.Code, rec.Body)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableAdmin = true
	cfg.CacheTTL = time.Minute
	cfg.HedgeDelay = 20 * time.Millisecond
	cfg.PlanBudget = 100 * time.Millisecond
	a := New(cfg, quiet)

	do(t, a.Handler, http.MethodPost, "/v1/plan", planBody)
	do(t, a.Handler, http.MethodPost, "/v1/plan", planBody) // second one hits the cache
	do(t, a.Handler, http.MethodPost, "/v1/plan", `not json`)
	do(t, a.Handler, http.MethodGet, "/some/random/path/123", "")

	// Make showtimes time out so the degraded and failure counters move.
	do(t, a.Handler, http.MethodPut, "/admin/faults/showtimes", `{"latency_ms":5000}`)
	do(t, a.Handler, http.MethodPost, "/v1/plan", planBody)

	rec, _ := do(t, a.Handler, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`evenr_http_request_duration_seconds_count{route="POST /v1/plan",status="200"} 3`,
		`evenr_http_request_duration_seconds_count{route="POST /v1/plan",status="400"} 1`,
		`route="unmatched",status="404"`,
		`evenr_plans_degraded_total 1`,
		`evenr_upstream_failures_total{reason="timeout",source="showtimes"}`,
		`evenr_plan_duration_seconds_count 3`,
		`evenr_travel_cache_hits_total`,
		`evenr_hedge_requests_total{upstream="travel"}`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
	if strings.Contains(body, "/some/random/path") {
		t.Error("raw request paths must not appear as label values")
	}
}
