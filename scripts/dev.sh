#!/usr/bin/env bash
# Starts a local stack: Temporal dev server (unless one is already on :7233),
# the rollout worker and the API. Ctrl-C stops everything it started.
#   make dev                         # needs `temporal` (brew install temporal)
#   TEMPORAL=/path/to/temporal make dev
#   REGISTRY=demo/plugins.yaml make dev
set -euo pipefail
cd "$(dirname "$0")/.."

TEMPORAL="${TEMPORAL:-temporal}"
REGISTRY="${REGISTRY:-plugins.yaml}"
LOGS=".dev-logs"
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT INT TERM
port_open() { nc -z 127.0.0.1 "$1" >/dev/null 2>&1; }

if port_open 8080; then
  echo "something is already listening on :8080 (another server or demo?); stop it first" >&2
  exit 1
fi
mkdir -p "$LOGS"
go build -o bin/ ./cmd/...

if port_open 7233; then
  echo "using Temporal already running on :7233"
else
  command -v "$TEMPORAL" >/dev/null || { echo "temporal CLI not found: brew install temporal (or set TEMPORAL=/path)" >&2; exit 1; }
  "$TEMPORAL" server start-dev --log-level error >"$LOGS/temporal.log" 2>&1 & PIDS+=($!)
  until port_open 7233; do sleep 0.5; done
fi
bin/worker --registry "$REGISTRY" >"$LOGS/worker.log" 2>&1 & PIDS+=($!)
bin/server --registry "$REGISTRY" >"$LOGS/server.log" 2>&1 & PIDS+=($!)
until port_open 8080; do sleep 0.5; done

cat <<MSG

  Rollout stack is up (logs in $LOGS/):
    API          http://127.0.0.1:8080
    Dashboard    http://127.0.0.1:8080/ui
    Temporal UI  http://localhost:8233

  In another terminal:  bin/rolloutctl plan -f examples/quickstart.yaml --version v1
  Ctrl-C to stop.
MSG
wait
