#!/usr/bin/env bash
# Runs each benchmark scenario against a fresh server and prints one JSON
# summary per scenario.
#
#   scripts/bench.sh [rate] [duration]        run everything
#   ONLY="7 8" scripts/bench.sh               run just those scenarios (appends to the results file)
#
# Each scenario starts its own server so cache and hedge state never carry
# over. Upstreams are mocks with injected latency, so these numbers measure the
# resilience machinery (budget, cache, hedging), not a real network.
set -euo pipefail

RATE="${1:-250}"
DURATION="${2:-20s}"
PORT="${PORT:-18090}"
BASE="http://localhost:$PORT"
OUT="${OUT:-bench-results}"
ONLY="${ONLY:-}"

mkdir -p "$OUT" bin
go build -o bin/evenr-server ./cmd/server
go build -o bin/loadgen ./cmd/loadgen

SERVER_PID=""
stop_server() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
  fi
}
trap stop_server EXIT

start_server() { # env assignments as arguments
  stop_server
  env ADDR=":$PORT" ENABLE_ADMIN=1 "$@" ./bin/evenr-server >"$OUT/server.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 50); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return
    sleep 0.1
  done
  echo "server did not start" >&2
  exit 1
}

fault() { # provider json
  curl -sf -X PUT "$BASE/admin/faults/$1" -d "$2" >/dev/null
}

request='{"date":"2026-09-26","area":"indiranagar","party_size":2,"budget_per_person":1500}'
warm() { for _ in 1 2 3; do curl -sf -X POST "$BASE/v1/plan" -d "$request" >/dev/null; done; }

run() { # label
  ./bin/loadgen -url "$BASE" -rate "$RATE" -duration "$DURATION" -label "$1" -json | tee -a "$OUT/results.jsonl"
}

want() { [[ -z "$ONLY" || " $ONLY " == *" $1 "* ]]; }

# Every upstream is a little slow and jittery, like a real service.
BASELINE='{"latency_ms":15,"jitter_ms":10}'
tail_fault() { echo "{\"latency_ms\":15,\"jitter_ms\":10,\"tail_rate\":$1,\"tail_latency_ms\":1000}"; }
set_all() { for p in showtimes tables travel; do fault "$p" "$1"; done; }

scenario_1() {
  echo "### 1. healthy upstreams" >&2
  start_server PLAN_BUDGET=300ms
  set_all "$BASELINE"; warm
  run "1 healthy"
}

scenario_2() {
  echo "### 2. travel upstream 2s slow, no cache" >&2
  start_server PLAN_BUDGET=300ms CACHE_TTL=0
  set_all "$BASELINE"; fault travel '{"latency_ms":2000}'
  run "2 travel slow, no cache"
}

scenario_3() {
  echo "### 3. travel upstream 2s slow, cache warmed first" >&2
  start_server PLAN_BUDGET=300ms
  set_all "$BASELINE"; warm
  fault travel '{"latency_ms":2000}'
  run "3 travel slow, warm cache"
}

scenario_4() {
  echo "### 4. travel upstream down" >&2
  start_server PLAN_BUDGET=300ms CACHE_TTL=0
  set_all "$BASELINE"; fault travel '{"down":true}'
  run "4 travel down, no cache"
}

tail_no_hedge() { # scenario-number tail-rate
  echo "### $1. $(awk "BEGIN{print $2*100}")% of upstream calls take 1s, no hedging" >&2
  start_server PLAN_BUDGET=300ms
  set_all "$(tail_fault "$2")"
  run "$1 tail $(awk "BEGIN{print $2*100}")%, no hedging"
}

tail_hedged() { # scenario-number tail-rate
  echo "### $1. same $(awk "BEGIN{print $2*100}")% tail, hedging on" >&2
  start_server PLAN_BUDGET=300ms HEDGE_DELAY=50ms HEDGE_RATIO=0.1
  set_all "$(tail_fault "$2")"
  run "$1 tail $(awk "BEGIN{print $2*100}")%, hedging on"
  curl -s "$BASE/metrics" | grep -E '^evenr_hedge_(requests|attempts|suppressed)_total' >"$OUT/hedge-metrics-$1.txt" || true
}

scenario_5() { tail_no_hedge 5 0.05; }
scenario_6() { tail_hedged 6 0.05; }
scenario_7() { tail_no_hedge 7 0.02; }
scenario_8() { tail_hedged 8 0.02; }

[[ -z "$ONLY" ]] && : >"$OUT/results.jsonl"
for n in 1 2 3 4 5 6 7 8; do
  if want "$n"; then "scenario_$n"; fi
done

echo "wrote $OUT/results.jsonl" >&2
