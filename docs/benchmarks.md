# Benchmarks

What these numbers are: how the service behaves when its dependencies misbehave, measured
with the built-in mock upstreams. What they are not: a claim about real-world throughput or
a real network. Read the [caveats](#caveats) before quoting anything.

## Setup

- **Machine:** Intel Core i5-13420H (8 cores / 12 threads), 16 GB RAM, Windows 11. The load
  generator and the server ran on the same machine.
- **Load:** 250 requests/s for 20 s per scenario (5,000 measured requests) after a 2 s
  warm-up that is not counted. Request mix varies area (5), party size (1-6) and budget.
- **Load generator:** `cmd/loadgen`, open-loop. It sends on a fixed schedule and measures
  latency from when each request was *due*, so a stalled server cannot hide its slow
  requests by making the tester wait (coordinated omission).
- **Service config:** `PLAN_BUDGET=300ms`. Every upstream call has 15 ms base latency plus
  0-10 ms jitter, injected through `/admin/faults`. Each scenario starts a fresh server.
- **Reproduce:** `scripts/bench.sh` runs all of it (`ONLY="7 8" scripts/bench.sh` for a
  subset). Results are written to `bench-results/`.

"Degraded" means the response was `200` but built from partial data because an upstream call
timed out or failed. Every request in every scenario returned `200`; none were dropped.

## Results

### A slow or failed dependency

| # | Scenario | p50 | p90 | p99 | p99.9 | Degraded |
|---|----------|----:|----:|----:|------:|---------:|
| 1 | Healthy upstreams | 25.5 | 27.0 | 28.3 | 29.2 | 0% |
| 2 | Travel upstream 2 s slow, no cache | 301.4 | 302.4 | 303.6 | 309.9 | 100% |
| 3 | Travel upstream 2 s slow, cache warmed first | 25.5 | 26.9 | 28.1 | 28.9 | 0% |
| 4 | Travel upstream down, no cache | 25.4 | 26.8 | 27.8 | 28.5 | 100% |

Latencies in milliseconds.

- **#2:** a dependency taking 2 s does not make requests take 2 s. They are cut off at the
  300 ms budget and answered with estimated travel times, flagged in the response.
- **#3:** with the cache warm, the same slow upstream has no visible effect.
- **#4:** a *down* dependency is cheaper than a slow one: it fails immediately, so requests
  answer at normal speed with estimated travel.

### Tail latency, with and without hedging

A fraction of upstream calls take 1 s instead of ~25 ms. Hedging: send a second attempt
after 50 ms, hedge budget 10% of requests.

| # | Scenario | p50 | p90 | p99 | p99.9 | Degraded |
|---|----------|----:|----:|----:|------:|---------:|
| 5 | 5% slow calls, no hedging | 26.0 | 301.5 | 302.7 | 303.6 | 25.8% |
| 6 | 5% slow calls, hedging | 26.0 | 74.1 | 300.9 | 302.6 | 1.34% |
| 7 | 2% slow calls, no hedging | 25.6 | 300.7 | 302.4 | 303.4 | 11.74% |
| 8 | 2% slow calls, hedging | 25.7 | 68.8 | 77.0 | 301.0 | 0.14% |

Extra upstream load from hedging: 4.9% more calls in #6, 2.1% in #8 (from the hedge
counters), roughly the fraction of calls that were slow, and well inside the 10% budget.

**Reading these honestly:**

- Hedging cut degraded responses from 25.8% to 1.3% at a 5% tail and from 11.7% to 0.14% at
  a 2% tail.
- At a 5% tail **p99 did not improve** (302.7 -> 300.9 ms). A hedge only fails when both
  attempts are slow: 0.05 x 0.05 = 0.25% per call, across about six calls per request, is
  roughly 1.5% of requests, just over the 1% that p99 looks at. The measured 1.34% agrees.
- At a 2% tail the same arithmetic gives about 0.24% (six calls x 0.02 x 0.02); measured
  0.14%, well under 1%, so p99 falls from 302 ms to 77 ms.
- So hedging helps a lot, but how much p99 improves depends on how bad the tail is. A
  third attempt, or a lower hedge delay, would help the 5% case, at the cost of more load.

## Things the results show that I would fix

- **The budget is a ceiling paid in full.** In #2 the p50 is 301 ms even though five of the
  six data sources answered in about 25 ms; the request waits for the slow one until the
  deadline. Travel time has a safe fallback, so a shorter per-source deadline for travel
  would let those requests answer in ~50 ms while still waiting longer for showtimes and
  tables, which have no fallback.
- **The p99.9 column is noise.** With 5,000 requests, p99.9 is set by about five samples.
  It is included for completeness, not as evidence.

## Caveats

- **Mocks, not a network.** The upstreams are in-process with injected delays. There is no
  network latency, connection reuse, TLS or real payload size on the upstream side.
- **One run per scenario.** No repeats and no confidence intervals. The p50/p90/p99 values
  were very consistent across scenarios that should be identical (#1 and #3), which suggests
  low run-to-run noise, but that is not a substitute for repeating runs.
- **Load generator and server share a machine and its CPU.** At 250 requests/s neither was
  CPU-bound (the healthy p99 is 28 ms, almost all of it the injected latency; without
  injected latency a smoke run at 200 requests/s measured p99 of about 3 ms), but they do
  compete.
- **Windows timers.** Timer granularity affects the small hedge and latency values.
- **The tail model is synthetic.** Real tails are correlated (a slow replica stays slow for
  a while), which makes independent hedges less effective than this model suggests.
