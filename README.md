# Evenr

A backend that turns *"Saturday, 4 friends, near Indiranagar, ₹1500 each, want a movie and dinner"* into ranked, **feasible** itineraries, for example dinner at 6:30, then the 9:15 show, with travel time accounted for.

Built in Go. The interesting part is not the CRUD, it is two problems:

1. **Constraint solving.** Showtimes, table availability, travel time and budget all interact. A plan is only valid if every leg fits.
2. **A hard latency budget.** The answer needs several upstream sources (showtimes, restaurants, travel-time estimates). The service has to return a good answer within a fixed time even when one of them is slow or down.

## Goals

| Goal | Target |
|------|--------|
| p99 latency, all dependencies healthy | measured, see `docs/benchmarks.md` |
| p99 latency, one dependency slowed | measured, must still return results |
| Behaviour when a dependency is down | degrade to partial results, never a 5xx |

Numbers get filled in only once they have been measured.

## Design intent

- **Latency budget propagated with `context.Context`.** The request has one deadline. Each upstream call gets a slice of it.
- **Fan-out with partial results.** Upstream calls run concurrently. Whatever has returned by the deadline is used, and the response says what was missing.
- **Hedged requests** for slow-tail upstreams.
- **Cached travel-time matrix** so repeated area pairs don't hit the upstream.
- **Deterministic solver.** Same inputs give the same ranked output, which makes it testable.

## Layout

```
cmd/server/          entrypoint
internal/itinerary/  domain model, constraint solver, ranking
internal/providers/  upstream clients (mock providers with injectable latency and failure)
internal/aggregator/ parallel fan-out to providers under one deadline
internal/planner/    runs fetch, solve and rank for one request
internal/httpapi/    HTTP handlers, fault-injection admin endpoints
internal/app/        assembles the service from config
docs/                design notes, benchmark results
```

## Run

```bash
go run ./cmd/server                 # listens on :8080; ADDR and PLAN_BUDGET (e.g. 300ms) are configurable
curl localhost:8080/healthz
```

### Configuration

| Variable | Default | Meaning |
|----------|---------|---------|
| `ADDR` | `:8080` | Listen address |
| `PLAN_BUDGET` | `300ms` | Total time allowed for a request's upstream calls |
| `CACHE_TTL` | `10m` | Travel-time cache lifetime; `0` turns the cache off |
| `HEDGE_DELAY` | `0` (off) | Wait this long before sending a hedged second attempt to an upstream |
| `HEDGE_RATIO` | `0.1` | Hedge budget: hedges allowed per request |
| `ENABLE_ADMIN` | off | Exposes `/admin/faults` to inject latency/errors into the mock upstreams (benchmarking only) |

## API

`POST /v1/plan`

```bash
curl -X POST localhost:8080/v1/plan -d '{
  "date": "2026-09-26",
  "area": "indiranagar",
  "party_size": 4,
  "budget_per_person": 1500
}'
```

Returns the best few dinner-and-film plans, best first. Each leg says how long the
trip to it takes and whether that travel time was `travel_estimated`. If an upstream
timed out or failed, the response still succeeds with what was available and sets
`"degraded": true` with a `failures` list (`timeout`, `unavailable`, ...).

| Status | Meaning |
|--------|---------|
| 200 | Plans returned (possibly degraded, possibly empty) |
| 400 | Malformed body, bad date, or invalid party size / budget |
| 413 | Body over 64 KB |
| 422 | Area not recognised |
| 503 | Every upstream failed, nothing to plan from |
## Roadmap

- [x] Project skeleton, health endpoint, graceful shutdown
- [x] Domain model: `Venue`, `Showtime`, `TableSlot`, `Itinerary`
- [x] Mock providers with configurable latency and failure injection
- [x] Concurrent fan-out under a shared deadline, with partial results
- [x] Solver: chain movie + dinner subject to time, travel and budget
- [x] Ranking (idle time, travel, budget fit, dinner timing, venue diversity)
- [x] HTTP endpoint: `POST /v1/plan`
- [x] Travel-time cache (TTL, symmetric keys, request coalescing, fetch survives caller deadline)
- [x] Hedged requests (with a retry budget so hedging cannot amplify an outage)
- [ ] Load test (k6) and `docs/benchmarks.md`
- [ ] Metrics and tracing

## How AI was used

This section is filled in as the project goes: what was scaffolded or generated with AI assistance, and where the output had to be corrected.
