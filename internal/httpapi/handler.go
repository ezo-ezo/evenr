// Package httpapi exposes the planner over HTTP.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"evenr/internal/aggregator"
	"evenr/internal/itinerary"
	"evenr/internal/planner"
	"evenr/internal/providers"
)

const maxBodyBytes = 64 << 10

// Planner is what the handler needs from the planning pipeline.
type Planner interface {
	Plan(ctx context.Context, req itinerary.Request) (planner.Result, error)
}

// New returns the service's HTTP handler.
func New(p Planner, logger *slog.Logger, opts ...Option) http.Handler {
	h := &handler{planner: p, logger: logger}
	for _, opt := range opts {
		opt(h)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("POST /v1/plan", h.plan)
	if h.faults != nil {
		h.routeAdmin(mux)
	}
	return h.logRequests(mux)
}

type handler struct {
	planner Planner
	logger  *slog.Logger
	faults  map[string]*providers.Faults // nil unless admin is enabled
}

type planRequest struct {
	Date            string `json:"date"` // YYYY-MM-DD, in IST
	Area            string `json:"area"`
	PartySize       int    `json:"party_size"`
	BudgetPerPerson int    `json:"budget_per_person"`
}

type planResponse struct {
	Plans      []planJSON    `json:"plans"`
	Degraded   bool          `json:"degraded"`
	Failures   []failureJSON `json:"failures"`
	Considered int           `json:"considered"`
	ElapsedMS  float64       `json:"elapsed_ms"`
}

type planJSON struct {
	Score         float64   `json:"score"`
	CostPerPerson int       `json:"cost_per_person"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	Legs          []legJSON `json:"legs"`
}

type legJSON struct {
	Kind            string    `json:"kind"`
	Venue           string    `json:"venue"`
	Area            string    `json:"area"`
	Title           string    `json:"title"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	TravelMinutes   int       `json:"travel_minutes"`
	TravelEstimated bool      `json:"travel_estimated"`
	CostPerPerson   int       `json:"cost_per_person"`
}

type failureJSON struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

func (h *handler) plan(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRequest(w, r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	res, err := h.planner.Plan(r.Context(), req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toResponse(res))
	case errors.Is(err, planner.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, providers.ErrUnknownArea):
		writeError(w, http.StatusUnprocessableEntity, "unknown area")
	case errors.Is(err, planner.ErrNoData):
		writeError(w, http.StatusServiceUnavailable, "upstream services unavailable")
	case r.Context().Err() != nil:
		// The client went away; there is nobody to answer.
	default:
		h.logger.Error("plan failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func decodeRequest(w http.ResponseWriter, r *http.Request) (itinerary.Request, error) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	var body planRequest
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return itinerary.Request{}, err
		}
		return itinerary.Request{}, errors.New("body must be a JSON object with date, area, party_size and budget_per_person")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return itinerary.Request{}, errors.New("body must contain a single JSON object")
	}

	day, err := time.ParseInLocation(time.DateOnly, body.Date, itinerary.IST)
	if err != nil {
		return itinerary.Request{}, errors.New("date must be in YYYY-MM-DD format")
	}
	return itinerary.Request{
		Day:             day,
		Area:            body.Area,
		PartySize:       body.PartySize,
		BudgetPerPerson: itinerary.Money(body.BudgetPerPerson),
	}, nil
}

func toResponse(res planner.Result) planResponse {
	out := planResponse{
		Plans:      make([]planJSON, len(res.Plans)),
		Degraded:   res.Degraded(),
		Failures:   make([]failureJSON, len(res.Failures)),
		Considered: res.Considered,
		ElapsedMS:  float64(res.Elapsed.Microseconds()) / 1000,
	}
	for i, ranked := range res.Plans {
		p := planJSON{
			Score:         ranked.Score,
			CostPerPerson: int(ranked.Itinerary.CostPerPerson()),
			Start:         ranked.Itinerary.Start(),
			End:           ranked.Itinerary.End(),
			Legs:          make([]legJSON, len(ranked.Itinerary.Legs)),
		}
		for j, l := range ranked.Itinerary.Legs {
			p.Legs[j] = legJSON{
				Kind:            string(l.Kind),
				Venue:           l.Venue.Name,
				Area:            l.Venue.Area,
				Title:           l.Title,
				Start:           l.Start,
				End:             l.End,
				TravelMinutes:   int(l.TravelBefore / time.Minute),
				TravelEstimated: l.TravelEstimated,
				CostPerPerson:   int(l.CostPerPerson),
			}
		}
		out.Plans[i] = p
	}
	for i, f := range res.Failures {
		out.Failures[i] = failureJSON{Source: f.Source, Target: f.Target, Reason: failureReason(f)}
	}
	return out
}

// failureReason gives clients a stable code without leaking internal errors.
func failureReason(f aggregator.Failure) string {
	switch {
	case errors.Is(f.Err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(f.Err, context.Canceled):
		return "canceled"
	case errors.Is(f.Err, providers.ErrUnavailable):
		return "unavailable"
	case errors.Is(f.Err, providers.ErrUnknownArea):
		return "unknown_area"
	default:
		return "error"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
