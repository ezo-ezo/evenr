package httpapi

import (
	"net/http"
	"time"
)

// statusRecorder captures the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// logRequests writes one structured line per request with its status and
// latency, and records the same in metrics, which is what you need to reason
// about p99.
func (h *handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)

		h.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", float64(elapsed.Microseconds())/1000,
		)
		if h.metrics != nil {
			// The mux fills in the matched pattern, which is a small fixed set;
			// raw paths would make label cardinality unbounded.
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			h.metrics.ObserveRequest(route, rec.status, elapsed)
		}
	})
}
