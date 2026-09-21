# Evenr design notes

Evenr answers "we're N people, X budget, near Y, want dinner and a film" with a short
list of plans that actually work: the table is free, the film starts after dinner plus the
trip, and the total fits the budget. It has to answer inside a fixed time even when the
services it depends on are slow, flaky or down.

## Request flow

```
POST /v1/plan
     |
     v
  httpapi        validate, map errors to status codes, log + metrics
     |
     v
  planner        one deadline (PLAN_BUDGET) for the whole request
     |
     v
  aggregator     fan out in parallel:  showtimes x areas
     |                                  tables    x areas
     |                                  travel    x area pairs
     |            wait for all of them or the deadline, whichever is first
     v
  solver         every feasible dinner + film pair, in either order
     |
     v
  ranker         score, diversify, keep the best few
```

Each upstream is wrapped as **cache -> hedge -> upstream** (see below).

## Decisions and trade-offs

### One deadline, partial results beat errors
The aggregator gives every upstream call the same context deadline and returns what
arrived, plus a list of what did not. A missing travel time degrades to a pessimistic
estimate (flagged `travel_estimated`); a missing showtimes list means no plans, but that
is still a valid, fast `200`, not a timeout.

The aggregator does **not** wait for calls still running at the deadline. A client that
ignores its context cannot hold a request past the budget; the abandoned goroutine
finishes into a buffered channel and exits. (Tested with a provider that sleeps for 2s
while ignoring its context.)

Only "nothing at all could be fetched" is an error (`503`), because there is nothing to
plan from.

### Fallback travel is pessimistic and visible
When travel time is unknown the solver assumes 45 minutes. That may hide a plan that would
have worked, but it never proposes one that cannot. Plans built on a guess are flagged in
the response and discounted 10% in ranking, so real data wins ties.

### Cache: coalescing, and a fetch that outlives the request
Travel times between two areas change slowly, so they are cached (TTL, symmetric keys).
Two details matter more than the map itself:

- **Coalescing.** Concurrent misses for the same pair share one upstream call, so a cold
  cache under load does not stampede the upstream.
- **The fetch is detached from the request that started it.** If that request's deadline
  passes first, the fetch carries on (bounded by its own timeout) and fills the cache. The
  next request is a hit instead of timing out the same way. Errors are never cached.

### Hedging, with a budget
For the slow tail, a second identical attempt is sent if the first has not answered after
`HEDGE_DELAY`; the first success wins and the other is cancelled. A fast failure
(`ErrUnavailable`) triggers the second attempt immediately; errors that repeating cannot
fix (unknown area) do not.

The risk with hedging is that when an upstream is slow for *everyone*, every request
hedges and the load doubles just when the upstream is struggling. So hedges are budgeted:
each request earns `HEDGE_RATIO` tokens and each hedge spends one. At 0.1, at most about
one call in ten is hedged no matter how bad things get. The token bucket is capped so a
quiet period cannot bank a burst.

The cache sits *outside* the hedge. If it were inside, the hedge's second attempt would
just join the first attempt's in-flight fetch and hedging would do nothing.

### Solver: exhaustive, deterministic, and cheap enough
A plan is one dinner and one film in either order. With tens of tables and screenings the
search is a few thousand pairs, so it is exhaustive rather than clever. Output order does
not depend on input order or goroutine timing, which makes the tests exact instead of
statistical. A property test runs it across five party sizes and five budgets and checks
every returned plan against the constraints independently.

### Ranking is a judgment call, and says so
Plans are scored 0-1 on idle time between stops, travel time, proximity to the requested
area, budget fit (prefers around 80% of budget, not the cheapest) and dinner start near
19:30. The weights are my judgment, not fitted to data. With real usage the right move is
to log which plan users pick and tune the weights against that. Results are also capped at
two per venue so the user sees variety, but the cap is soft so a thin catalogue still
returns enough plans.

Proximity was added after running the service by hand: a request for Indiranagar returned
four plans in MG Road, because nothing in the score rewarded staying near the requested
area. It is measured as travel time from the requested area, and is a constant when that
data is unavailable so a failed travel lookup cannot scramble the order.

### Load testing measures from the scheduled time
The load generator is open-loop: it sends at a fixed rate and measures latency from when
each request was *due*. A closed-loop tester slows down when the server does, which hides
the slow requests it never sent (coordinated omission).

## Failure modes

| Failure | What happens |
|---------|--------------|
| One upstream slow | Cut off at the budget; response says which and why; other data still used |
| Travel upstream slow or down | Fallback estimate, flagged; cache masks it if warm |
| Showtimes or tables down | Fast `200` with no plans and `degraded: true` |
| All upstreams down | `503` |
| Upstream ignores its context | Request still returns at the budget |
| Client disconnects | Context cancelled, in-flight work stops, nothing written |
| Unknown area | `422` |
| Malformed or oversized body | `400` / `413` |

## What is mocked, and what that means

The three upstreams are deterministic mocks with injectable latency, jitter, tail latency
and errors. The benchmarks therefore measure the budget, cache and hedging behaviour, not a
real network or real data volumes. `/admin/faults` exists to change the mocks at runtime
and is off unless `ENABLE_ADMIN` is set.

## Not done, and what I would do next

- **Real upstreams and a real catalogue.** Swap the mocks for HTTP clients behind the same
  three interfaces; nothing else changes.
- **Per-source deadlines.** The budget is a ceiling that is paid in full whenever any
  dependency is slow (see `docs/benchmarks.md`, scenario 2). Travel has a safe fallback, so
  it could get a much shorter deadline than showtimes and tables, which have none.
- **Adaptive hedge delay** from a rolling p95 per upstream, instead of a fixed value.
- **Circuit breaker** per upstream, so a dead dependency is skipped immediately instead of
  being called and timed out on every request.
- **Shared cache (Redis)** so instances share travel times and a restart is not cold.
- **Distributed tracing** across the fan-out.
- **Race detector in CI.** The tests are written to be race-free (shared state is behind
  mutexes and atomics) but `go test -race` needs cgo, which was not available where this
  was developed.
