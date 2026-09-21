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
internal/httpapi/    HTTP handlers
docs/                design notes, benchmark results
```

## Run

```bash
go run ./cmd/server
curl localhost:8080/healthz
```

## Roadmap

- [x] Project skeleton, health endpoint, graceful shutdown
- [x] Domain model: `Venue`, `Showtime`, `TableSlot`, `Itinerary`
- [x] Mock providers with configurable latency and failure injection
- [ ] Concurrent fan-out under a shared deadline, with partial results
- [ ] Solver: chain movie + dinner subject to time, travel and budget
- [ ] Ranking
- [ ] Travel-time cache
- [ ] Hedged requests
- [ ] Load test (k6) and `docs/benchmarks.md`
- [ ] Metrics and tracing

## How AI was used

This section is filled in as the project goes: what was scaffolded or generated with AI assistance, and where the output had to be corrected.
