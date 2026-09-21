package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"evenr/internal/aggregator"
	"evenr/internal/itinerary"
	"evenr/internal/planner"
	"evenr/internal/providers"
)

const validBody = `{"date":"2026-09-26","area":"indiranagar","party_size":2,"budget_per_person":1500}`

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func realPlanner(showtimes, tables, travel *providers.Faults) *planner.Planner {
	return planner.New(&aggregator.Aggregator{
		Showtimes:   &providers.MockShowtimes{Faults: showtimes},
		Restaurants: &providers.MockRestaurants{Faults: tables},
		Travel:      &providers.MockTravel{Faults: travel},
	})
}

func down() *providers.Faults { return providers.NewFaults(1, providers.FaultConfig{Down: true}) }

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/plan", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) planResponse {
	t.Helper()
	var out planResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, rec.Body)
	}
	return out
}

func TestPlanHappyPath(t *testing.T) {
	rec := post(t, New(realPlanner(nil, nil, nil), quiet), validBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"failures":[]`) {
		t.Errorf("failures should be an empty array, not null: %s", rec.Body)
	}

	res := decode(t, rec)
	if res.Degraded || len(res.Plans) != 5 {
		t.Fatalf("degraded = %v, plans = %d", res.Degraded, len(res.Plans))
	}
	for _, p := range res.Plans {
		if len(p.Legs) != 2 || p.CostPerPerson > 1500 || p.Score < 0 || p.Score > 1 {
			t.Errorf("implausible plan %+v", p)
		}
		if !p.End.After(p.Start) {
			t.Errorf("plan ends before it starts: %+v", p)
		}
	}
}

func TestPlanDegradedResponseSaysWhy(t *testing.T) {
	rec := post(t, New(realPlanner(nil, nil, down()), quiet), validBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	res := decode(t, rec)
	if !res.Degraded || len(res.Failures) == 0 {
		t.Fatalf("expected a degraded response: %+v", res)
	}
	for _, f := range res.Failures {
		if f.Source != aggregator.SourceTravel || f.Reason != "unavailable" {
			t.Errorf("unexpected failure %+v", f)
		}
	}
	for _, p := range res.Plans {
		if !p.Legs[1].TravelEstimated {
			t.Errorf("travel should be flagged as estimated: %+v", p.Legs[1])
		}
	}
}

func TestPlanRejectsBadInput(t *testing.T) {
	h := New(realPlanner(nil, nil, nil), quiet)
	cases := map[string]string{
		"not json":         `hello`,
		"empty body":       ``,
		"unknown field":    `{"date":"2026-09-26","area":"indiranagar","party_size":2,"budget_per_person":1500,"vibes":"good"}`,
		"trailing content": validBody + `{}`,
		"bad date":         `{"date":"26/09/2026","area":"indiranagar","party_size":2,"budget_per_person":1500}`,
		"missing date":     `{"area":"indiranagar","party_size":2,"budget_per_person":1500}`,
		"zero party":       `{"date":"2026-09-26","area":"indiranagar","party_size":0,"budget_per_person":1500}`,
		"zero budget":      `{"date":"2026-09-26","area":"indiranagar","party_size":2,"budget_per_person":0}`,
		"missing area":     `{"date":"2026-09-26","party_size":2,"budget_per_person":1500}`,
		"wrong type":       `{"date":"2026-09-26","area":"indiranagar","party_size":"two","budget_per_person":1500}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body)
			}
			var e map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e["error"] == "" {
				t.Errorf("want a JSON error body, got %s", rec.Body)
			}
		})
	}
}

func TestPlanRejectsOversizedBody(t *testing.T) {
	big := `{"date":"2026-09-26","area":"` + strings.Repeat("x", maxBodyBytes) + `","party_size":2,"budget_per_person":1500}`
	rec := post(t, New(realPlanner(nil, nil, nil), quiet), big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestPlanUnknownArea(t *testing.T) {
	body := `{"date":"2026-09-26","area":"atlantis","party_size":2,"budget_per_person":1500}`
	rec := post(t, New(realPlanner(nil, nil, nil), quiet), body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body)
	}
}

func TestPlanAllUpstreamsDown(t *testing.T) {
	rec := post(t, New(realPlanner(down(), down(), down()), quiet), validBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rec.Code, rec.Body)
	}
}

type failingPlanner struct{ err error }

func (f failingPlanner) Plan(context.Context, itinerary.Request) (planner.Result, error) {
	return planner.Result{}, f.err
}

func TestPlanInternalErrorDoesNotLeakDetails(t *testing.T) {
	rec := post(t, New(failingPlanner{errors.New("db password is hunter2")}, quiet), validBody)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("internal error leaked to the client: %s", rec.Body)
	}
}

func TestMethodAndRoutes(t *testing.T) {
	h := New(realPlanner(nil, nil, nil), quiet)

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/plan", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/plan = %d, want 405", get.Code)
	}

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || health.Body.String() != "ok" {
		t.Errorf("GET /healthz = %d %q", health.Code, health.Body)
	}

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if missing.Code != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", missing.Code)
	}
}
