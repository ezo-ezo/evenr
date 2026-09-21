package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"evenr/internal/metrics"
	"evenr/internal/providers"
)

// Option customises the handler built by New.
type Option func(*handler)

// WithFaultAdmin exposes endpoints to view and change the fault injection on
// the mock upstreams while the service runs, so a load test can slow a
// dependency down mid-run. It is for benchmarking and must not be enabled in
// a real deployment.
func WithFaultAdmin(faults map[string]*providers.Faults) Option {
	return func(h *handler) { h.faults = faults }
}

const maxFaultDuration = time.Minute

type faultJSON struct {
	LatencyMS     float64 `json:"latency_ms"`
	JitterMS      float64 `json:"jitter_ms"`
	TailRate      float64 `json:"tail_rate"`
	TailLatencyMS float64 `json:"tail_latency_ms"`
	ErrorRate     float64 `json:"error_rate"`
	Down          bool    `json:"down"`
}

func toFaultJSON(c providers.FaultConfig) faultJSON {
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	return faultJSON{
		LatencyMS:     ms(c.Latency),
		JitterMS:      ms(c.Jitter),
		TailRate:      c.TailRate,
		TailLatencyMS: ms(c.TailLatency),
		ErrorRate:     c.ErrorRate,
		Down:          c.Down,
	}
}

func (f faultJSON) config() (providers.FaultConfig, error) {
	dur := func(ms float64) (time.Duration, bool) {
		d := time.Duration(ms * float64(time.Millisecond))
		return d, ms >= 0 && d <= maxFaultDuration // false for NaN too
	}
	rate := func(r float64) bool { return r >= 0 && r <= 1 }

	latency, ok1 := dur(f.LatencyMS)
	jitter, ok2 := dur(f.JitterMS)
	tail, ok3 := dur(f.TailLatencyMS)
	if !ok1 || !ok2 || !ok3 {
		return providers.FaultConfig{}, errors.New("latencies must be between 0 and 60000 ms")
	}
	if !rate(f.TailRate) || !rate(f.ErrorRate) {
		return providers.FaultConfig{}, errors.New("tail_rate and error_rate must be between 0 and 1")
	}
	return providers.FaultConfig{
		Latency:     latency,
		Jitter:      jitter,
		TailRate:    f.TailRate,
		TailLatency: tail,
		ErrorRate:   f.ErrorRate,
		Down:        f.Down,
	}, nil
}

func (h *handler) routeAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/faults", func(w http.ResponseWriter, _ *http.Request) {
		out := make(map[string]faultJSON, len(h.faults))
		for name, f := range h.faults {
			out[name] = toFaultJSON(f.Config())
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("PUT /admin/faults/{provider}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("provider")
		f, ok := h.faults[name]
		if !ok {
			names := make([]string, 0, len(h.faults))
			for n := range h.faults {
				names = append(names, n)
			}
			sort.Strings(names)
			writeError(w, http.StatusNotFound, "unknown provider; expected one of "+join(names))
			return
		}

		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		dec.DisallowUnknownFields()
		var body faultJSON
		if err := dec.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "body must be a JSON object of fault settings")
			return
		}
		cfg, err := body.config()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		f.Set(cfg)
		writeJSON(w, http.StatusOK, toFaultJSON(cfg))
	})
}

func join(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// WithMetrics records request and plan metrics and serves them at /metrics.
func WithMetrics(m *metrics.Metrics) Option {
	return func(h *handler) { h.metrics = m }
}
