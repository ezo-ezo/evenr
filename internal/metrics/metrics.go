// Package metrics defines the service's Prometheus metrics.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"evenr/internal/planner"
	"evenr/internal/providers"
)

// latencyBuckets span 1 ms to 2.5 s, which brackets the 300 ms plan budget
// with enough resolution to read p99 off a dashboard.
var latencyBuckets = []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}

// Metrics holds every collector the service updates.
type Metrics struct {
	Registry *prometheus.Registry

	requests     *prometheus.HistogramVec
	planDuration prometheus.Histogram
	degraded     prometheus.Counter
	upstreamFail *prometheus.CounterVec
}

// New creates a Metrics with its own registry, so nothing leaks into global
// state and tests can build as many as they like.
func New() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		requests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "evenr_http_request_duration_seconds",
			Help:    "HTTP request latency by route and status.",
			Buckets: latencyBuckets,
		}, []string{"route", "status"}),
		planDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "evenr_plan_duration_seconds",
			Help:    "Time spent producing a plan: fetch, solve and rank.",
			Buckets: latencyBuckets,
		}),
		degraded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "evenr_plans_degraded_total",
			Help: "Plans answered with partial data because an upstream call failed or timed out.",
		}),
		upstreamFail: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "evenr_upstream_failures_total",
			Help: "Failed upstream calls by source and reason.",
		}, []string{"source", "reason"}),
	}
	m.Registry.MustRegister(
		m.requests, m.planDuration, m.degraded, m.upstreamFail,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler serves the metrics in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// ObserveRequest records one finished HTTP request. route must be a bounded
// set (a route pattern, never a raw URL) to keep label cardinality fixed.
func (m *Metrics) ObserveRequest(route string, status int, d time.Duration) {
	m.requests.WithLabelValues(route, strconv.Itoa(status)).Observe(d.Seconds())
}

// ObservePlan records the outcome of one planned request. failureReason maps
// an upstream failure to a stable label value.
func (m *Metrics) ObservePlan(res planner.Result, failureReason func(source string, err error) string) {
	m.planDuration.Observe(res.Elapsed.Seconds())
	if res.Degraded() {
		m.degraded.Inc()
	}
	for _, f := range res.Failures {
		m.upstreamFail.WithLabelValues(f.Source, failureReason(f.Source, f.Err)).Inc()
	}
}

// RegisterCache exposes a travel cache's counters.
func (m *Metrics) RegisterCache(c *providers.CachedTravel) {
	counter := func(name, help string, value func(providers.CacheStats) int64) {
		m.Registry.MustRegister(prometheus.NewCounterFunc(
			prometheus.CounterOpts{Name: name, Help: help},
			func() float64 { return float64(value(c.Stats())) },
		))
	}
	counter("evenr_travel_cache_hits_total", "Travel lookups answered from cache.",
		func(s providers.CacheStats) int64 { return s.Hits })
	counter("evenr_travel_cache_misses_total", "Travel lookups that started an upstream fetch.",
		func(s providers.CacheStats) int64 { return s.Misses })
	counter("evenr_travel_cache_coalesced_total", "Travel lookups that joined an in-flight fetch.",
		func(s providers.CacheStats) int64 { return s.Coalesced })
}

// RegisterHedgers exposes hedging counters for each named upstream.
func (m *Metrics) RegisterHedgers(hedgers map[string]*providers.Hedger) {
	for name, h := range hedgers {
		labels := prometheus.Labels{"upstream": name}
		counter := func(metric, help string, value func(providers.HedgeStats) int64) {
			m.Registry.MustRegister(prometheus.NewCounterFunc(
				prometheus.CounterOpts{Name: metric, Help: help, ConstLabels: labels},
				func() float64 { return float64(value(h.Stats())) },
			))
		}
		counter("evenr_hedge_requests_total", "Upstream calls made through the hedger.",
			func(s providers.HedgeStats) int64 { return s.Requests })
		counter("evenr_hedge_attempts_total", "Extra hedged attempts launched.",
			func(s providers.HedgeStats) int64 { return s.Hedges })
		counter("evenr_hedge_suppressed_total", "Hedges skipped because the budget was empty.",
			func(s providers.HedgeStats) int64 { return s.Suppressed })
	}
}
