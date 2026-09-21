package main

import (
	"math"
	"slices"
	"time"
)

// sample is the outcome of one request.
type sample struct {
	latency  time.Duration // measured from when the request was scheduled
	status   int           // 0 means the request failed before a response
	degraded bool
}

// percentile returns the p-th percentile (0-100) of an ascending slice using
// the nearest-rank method. It returns 0 for an empty slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

// summary is what a run reports.
type summary struct {
	Label       string             `json:"label"`
	TargetRate  float64            `json:"target_rate"`
	AchievedRPS float64            `json:"achieved_rps"`
	Sent        int                `json:"sent"`
	Dropped     int                `json:"dropped"`
	OK          int                `json:"ok"`
	Degraded    int                `json:"degraded"`
	DegradedPct float64            `json:"degraded_pct"`
	Statuses    map[int]int        `json:"statuses"`
	LatencyMS   map[string]float64 `json:"latency_ms"`
}

func summarise(label string, targetRate float64, elapsed time.Duration, dropped int, samples []sample) summary {
	s := summary{
		Label:      label,
		TargetRate: targetRate,
		Sent:       len(samples),
		Dropped:    dropped,
		Statuses:   map[int]int{},
		LatencyMS:  map[string]float64{},
	}

	latencies := make([]time.Duration, 0, len(samples))
	for _, x := range samples {
		s.Statuses[x.status]++
		if x.status == 200 {
			s.OK++
			if x.degraded {
				s.Degraded++
			}
		}
		latencies = append(latencies, x.latency)
	}
	slices.Sort(latencies)

	if s.OK > 0 {
		s.DegradedPct = 100 * float64(s.Degraded) / float64(s.OK)
	}
	if elapsed > 0 {
		s.AchievedRPS = float64(len(samples)) / elapsed.Seconds()
	}

	ms := func(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*100) / 100 }
	s.LatencyMS["p50"] = ms(percentile(latencies, 50))
	s.LatencyMS["p90"] = ms(percentile(latencies, 90))
	s.LatencyMS["p99"] = ms(percentile(latencies, 99))
	s.LatencyMS["p99.9"] = ms(percentile(latencies, 99.9))
	if n := len(latencies); n > 0 {
		s.LatencyMS["max"] = ms(latencies[n-1])
	}
	return s
}
