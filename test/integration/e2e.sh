#!/usr/bin/env bash
# End-to-end check: Temporal dev server + worker + API, driven by rolloutctl.
#   make e2e                       # needs `temporal` on PATH (brew install temporal)
#   TEMPORAL=/path/to/temporal make e2e
set -euo pipefail
cd "$(dirname "$0")/../.."

TEMPORAL="${TEMPORAL:-temporal}"
TPORT="${E2E_TEMPORAL_PORT:-7244}"
APORT="${E2E_API_PORT:-18080}"
if ! command -v "$TEMPORAL" >/dev/null 2>&1; then
  echo "temporal CLI not found: brew install temporal (or set TEMPORAL=/path/to/temporal)" >&2
  exit 1
fi

LOGS="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT
fail() { echo "E2E FAILED: $*" >&2; echo "logs: $LOGS" >&2; exit 1; }

wait_port() {
  for _ in $(seq 1 120); do nc -z 127.0.0.1 "$1" 2>/dev/null && return 0; sleep 0.5; done
  fail "port $1 never opened"
}

go build -o bin/ ./cmd/...
"$TEMPORAL" server start-dev --headless --port "$TPORT" --log-level error >"$LOGS/temporal.log" 2>&1 &
PIDS+=($!)
wait_port "$TPORT"
bin/worker --temporal "127.0.0.1:$TPORT" --registry plugins.yaml >"$LOGS/worker.log" 2>&1 &
PIDS+=($!)
bin/server --listen "127.0.0.1:$APORT" --temporal "127.0.0.1:$TPORT" --registry plugins.yaml >"$LOGS/server.log" 2>&1 &
PIDS+=($!)
wait_port "$APORT"

export ROLLOUT_SERVER="http://127.0.0.1:$APORT"
ctl=bin/rolloutctl

# targets <id>: prints "<rollout-state>" then "<target-id> <state>" lines.
targets() {
  local enc
  enc="$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1],safe=""))' "$1")"
  curl -fsS "$ROLLOUT_SERVER/v1/rollouts/$enc" | python3 -c '
import json, sys
s = json.load(sys.stdin)
print(s["state"])
for tid in s["order"]:
    print(tid, s["targets"][tid]["state"])'
}

# drive <id> <expected-final> [retry]: approves awaiting targets (and
# retries paused ones when asked) until the rollout finishes.
drive() {
  local id=$1 want=$2 retry=${3:-} saw_pause=0
  for _ in $(seq 1 600); do
    local out state
    out="$(targets "$id")"
    state="$(head -1 <<<"$out")"
    while read -r tid tstate; do
      case "$tstate" in
        awaiting_approval) $ctl approve "$id" --target "$tid" >/dev/null 2>&1 || true ;;
        paused)
          saw_pause=1
          [ -n "$retry" ] && { $ctl retry "$id" --target "$tid" >/dev/null 2>&1 || true; } ;;
      esac
    done < <(tail -n +2 <<<"$out")
    case "$state" in
      SUCCEEDED|HELD|ABORTED)
        [ "$state" = "$want" ] || fail "$id finished $state, want $want"
        [ -z "$retry" ] || [ "$saw_pause" = 1 ] || fail "$id never paused"
        return 0 ;;
    esac
    sleep 0.5
  done
  fail "$id did not finish"
}

echo "== plan"
$ctl plan -f examples/policy.yaml --version v1 >/dev/null || fail "plan"

echo "== happy path"
id="$($ctl start -f examples/policy.yaml --version v1)"
[ "$id" = "ro/support-agent" ] || fail "unexpected id $id"
drive "$id" SUCCEEDED
$ctl status "$id"

echo "== failing policy: pause, retry, recover"
id="$($ctl start -f examples/policy-failing.yaml --version v2)"
drive "$id" SUCCEEDED retry
$ctl status "$id"

echo "E2E OK"
