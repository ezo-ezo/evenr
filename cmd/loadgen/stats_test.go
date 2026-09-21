package main

import (
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestPercentileNearestRank(t *testing.T) {
	// 1ms..100ms, one of each.
	var xs []time.Duration
	for i := 1; i <= 100; i++ {
		xs = append(xs, ms(i))
	}

	cases := []struct {
		p    float64
		want time.Duration
	}{
		{50, ms(50)},
		{90, ms(90)},
		{99, ms(99)},
		{99.9, ms(100)},
		{100, ms(100)},
		{0, ms(1)}, // clamps to the smallest sample
	}
	for _, tc := range cases {
		if got := percentile(xs, tc.p); got != tc.want {
			t.Errorf("percentile(%v) = %v, want %v", tc.p, got, tc.want)
		}
	}

	if got := percentile(nil, 99); got != 0 {
		t.Errorf("percentile of nothing = %v, want 0", got)
	}
	if got := percentile([]time.Duration{ms(7)}, 99); got != ms(7) {
		t.Errorf("percentile of one sample = %v, want 7ms", got)
	}
}

func TestSummarise(t *testing.T) {
	samples := []sample{
		{latency: ms(10), status: 200},
		{latency: ms(20), status: 200, degraded: true},
		{latency: ms(30), status: 200},
		{latency: ms(400), status: 503},
		{latency: ms(5000), status: 0}, // transport failure
	}
	s := summarise("test", 100, 2*time.Second, 3, samples)

	if s.Sent != 5 || s.OK != 3 || s.Degraded != 1 || s.Dropped != 3 {
		t.Errorf("counts = %+v", s)
	}
	if s.Statuses[200] != 3 || s.Statuses[503] != 1 || s.Statuses[0] != 1 {
		t.Errorf("statuses = %v", s.Statuses)
	}
	// Degraded share is of successful responses only.
	if s.DegradedPct < 33.3 || s.DegradedPct > 33.4 {
		t.Errorf("DegradedPct = %v, want ~33.3", s.DegradedPct)
	}
	if s.AchievedRPS != 2.5 {
		t.Errorf("AchievedRPS = %v, want 2.5", s.AchievedRPS)
	}
	if s.LatencyMS["p50"] != 30 || s.LatencyMS["max"] != 5000 {
		t.Errorf("latency = %v", s.LatencyMS)
	}
}

func TestSummariseEmpty(t *testing.T) {
	s := summarise("empty", 100, time.Second, 0, nil)
	if s.Sent != 0 || s.LatencyMS["p99"] != 0 || s.DegradedPct != 0 {
		t.Errorf("empty summary = %+v", s)
	}
}
