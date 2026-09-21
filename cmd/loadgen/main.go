// Command loadgen drives the planner at a fixed request rate and reports
// latency percentiles.
//
// It is open-loop: requests are sent on a schedule whatever the server is
// doing, and latency is measured from the moment each request was due, not
// from when it was actually sent. A closed-loop tester waits for each response
// before sending the next, so a stalled server makes it slow down and the slow
// requests it never sent never show up in the numbers ("coordinated
// omission"). Measuring from the scheduled time avoids that.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func main() {
	var (
		url         = flag.String("url", "http://localhost:8080", "base URL of the service")
		rate        = flag.Float64("rate", 200, "requests per second")
		duration    = flag.Duration("duration", 15*time.Second, "how long to measure")
		warmup      = flag.Duration("warmup", 2*time.Second, "extra time to run first without recording")
		maxInflight = flag.Int("max-inflight", 5000, "requests allowed in flight before new ones are dropped")
		seed        = flag.Uint64("seed", 1, "seed for the request mix")
		label       = flag.String("label", "", "name for this run in the output")
		asJSON      = flag.Bool("json", false, "print the summary as JSON")
	)
	flag.Parse()
	if *rate <= 0 || *duration <= 0 || *warmup < 0 || *maxInflight < 1 {
		fmt.Fprintln(os.Stderr, "rate, duration and max-inflight must be positive, warmup non-negative")
		os.Exit(2)
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *maxInflight,
			MaxIdleConnsPerHost: *maxInflight,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	warmupN := int(*rate * warmup.Seconds())
	total := warmupN + int(*rate*duration.Seconds())
	interval := time.Duration(float64(time.Second) / *rate)

	var (
		mu      sync.Mutex
		samples = make([]sample, 0, total)
		dropped int
		wg      sync.WaitGroup
		sem     = make(chan struct{}, *maxInflight)
		rng     = rand.New(rand.NewPCG(*seed, *seed))
	)

	start := time.Now()
	var measureStart time.Time
	for i := 0; i < total; i++ {
		scheduled := start.Add(time.Duration(i) * interval)
		if i == warmupN {
			measureStart = scheduled
		}
		if d := time.Until(scheduled); d > 0 {
			time.Sleep(d)
		}

		body := nextBody(rng)
		recorded := i >= warmupN

		select {
		case sem <- struct{}{}:
		default:
			if recorded {
				dropped++
			}
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			s := send(client, *url+"/v1/plan", body)
			s.latency = time.Since(scheduled)
			if recorded {
				mu.Lock()
				samples = append(samples, s)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	sum := summarise(*label, *rate, time.Since(measureStart), dropped, samples)
	if *asJSON {
		out, _ := json.Marshal(sum)
		fmt.Println(string(out))
		return
	}
	printSummary(sum)
}

// send makes one request. Failures before a response are reported as status 0.
func send(client *http.Client, url string, body []byte) sample {
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return sample{}
	}
	defer resp.Body.Close()

	s := sample{status: resp.StatusCode}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.status = 0
		return s
	}
	if resp.StatusCode == http.StatusOK {
		var r struct {
			Degraded bool `json:"degraded"`
		}
		if json.Unmarshal(raw, &r) == nil {
			s.degraded = r.Degraded
		}
	}
	return s
}

var (
	areas   = []string{"indiranagar", "koramangala", "mg-road", "hsr-layout", "jayanagar"}
	parties = []int{1, 2, 2, 3, 4, 4, 5, 6}
	budgets = []int{900, 1200, 1500, 1800, 2500}
)

// nextBody builds a varied request so the run is not one request repeated.
func nextBody(rng *rand.Rand) []byte {
	b, _ := json.Marshal(map[string]any{
		"date":              "2026-09-26",
		"area":              areas[rng.IntN(len(areas))],
		"party_size":        parties[rng.IntN(len(parties))],
		"budget_per_person": budgets[rng.IntN(len(budgets))],
	})
	return b
}

func printSummary(s summary) {
	if s.Label != "" {
		fmt.Printf("== %s ==\n", s.Label)
	}
	fmt.Printf("target %.0f req/s, achieved %.1f req/s\n", s.TargetRate, s.AchievedRPS)
	fmt.Printf("sent %d, ok %d, dropped %d, degraded %d (%.1f%% of ok)\n", s.Sent, s.OK, s.Dropped, s.Degraded, s.DegradedPct)

	codes := make([]int, 0, len(s.Statuses))
	for c := range s.Statuses {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	fmt.Print("statuses:")
	for _, c := range codes {
		fmt.Printf(" %d=%d", c, s.Statuses[c])
	}
	fmt.Println()

	fmt.Printf("latency ms: p50 %.1f  p90 %.1f  p99 %.1f  p99.9 %.1f  max %.1f\n",
		s.LatencyMS["p50"], s.LatencyMS["p90"], s.LatencyMS["p99"], s.LatencyMS["p99.9"], s.LatencyMS["max"])
}
