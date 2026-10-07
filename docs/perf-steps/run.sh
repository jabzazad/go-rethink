#!/usr/bin/env bash
# Benchmarks every optimisation stage of internal/decision/rethink.go.
#   bash docs/perf-steps/run.sh [count]      (run from the repo root)
# Each stage file is copied over rethink.go in turn, benchmarked, and the original is restored.
set -euo pipefail
COUNT="${1:-5}"
DIR=internal/decision
# These tests use internals that only exist in later stages, so set them aside.
ASIDE=(rethink_core_bench_test.go rethink_reason_test.go)
TMP=$(mktemp -d)
cp "$DIR/rethink.go" "$TMP/rethink.go"
for t in "${ASIDE[@]}"; do mv "$DIR/$t" "$TMP/$t"; done
trap 'cp "$TMP/rethink.go" "$DIR/rethink.go"; for t in "${ASIDE[@]}"; do mv "$TMP/$t" "$DIR/$t"; done' EXIT
for f in docs/perf-steps/stage*.go.txt; do
  cp "$f" "$DIR/rethink.go"
  echo "== $(basename "$f" .go.txt)"
  go test ./$DIR -run '^$' -bench RethinkDecide -benchmem -count "$COUNT" | grep Benchmark
done
