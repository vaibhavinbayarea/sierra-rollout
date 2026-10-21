#!/usr/bin/env bash
# Agent rollout service demo.
#
#   demo/demo.sh            run every act, pausing between steps
#   demo/demo.sh 1 3        run selected acts
#   DEMO_AUTO=1 demo/demo.sh   no pauses (CI / dry run)
#
# Acts
#   1  one team: staging → pre-prod → prod ramp, manual sign-off on the canary
#   2  two teams (sdk-core, agent-foobar) rolling out at the same time
#   3  three teams; agent-foobar-2 fails validation, 1 and 3 are unaffected
#   4  guardrails: compile-time checks, one rollout per agent, signal checks
#
# (An SDK validation-pack act is planned once the pack feature exists.)
#
# Requires: go, temporal CLI (brew install temporal, or TEMPORAL=/path/to/temporal).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEMO="$ROOT/demo"
LOGS="$DEMO/.logs"
R="$ROOT/bin/rolloutctl"
export ROLLOUT_SERVER="${ROLLOUT_SERVER:-http://127.0.0.1:8080}"
TEMPORAL="${TEMPORAL:-temporal}"
PIDS=()

# ---------- presentation ----------
bold=$'\033[1m'; dim=$'\033[2m'; cyan=$'\033[1;36m'; green=$'\033[32m'; red=$'\033[31m'; reset=$'\033[0m'

act()  { printf '\n%s━━ %s ━━%s\n' "$cyan" "$*" "$reset"; }
say()  { printf '%s» %s%s\n' "$bold" "$*" "$reset"; }
pause() { [[ "${DEMO_AUTO:-}" == 1 ]] || read -rp "${dim}(enter to continue)${reset} " _; }
run()  { printf '%s$ %s%s\n' "$dim" "$*" "$reset"; "$@"; }

# expect_exit CODE cmd... runs a command that must fail with CODE.
expect_exit() {
  local want=$1; shift
  printf '%s$ %s%s\n' "$dim" "$*" "$reset"
  set +e; "$@"; local got=$?; set -e
  if [[ $got == "$want" ]]; then
    printf '%s✔ rejected (exit %s), as expected%s\n' "$green" "$got" "$reset"
  else
    printf '%s✘ expected exit %s, got %s%s\n' "$red" "$want" "$got" "$reset"; exit 1
  fi
}

# ---------- rollout helpers ----------
# approve ID TARGET [VALIDATION]: approves a waiting target. If it was
# already approved elsewhere (e.g. the dashboard's Approve button), the API
# refuses with "target is <state>"; that counts as done.
approve() {
  local id=$1 tid=$2 v=${3:-} out code
  local args=(approve "$id" --target "$tid")
  [[ -n "$v" ]] && args+=(--validation "$v")
  printf '%s$ %s %s%s\n' "$dim" "$R" "${args[*]}" "$reset"
  set +e; out=$("$R" "${args[@]}" 2>&1); code=$?; set -e
  [[ -n "$out" ]] && echo "$out"
  if [[ $code == 3 && $out == *"target is "* && $out != *"target is awaiting_approval"* ]]; then
    say "already approved (e.g. from the dashboard), continuing"
    return 0
  fi
  return $code
}

# rollout_state ID: prints the rollout's state, or nothing if it doesn't exist.
rollout_state() { { "$R" status "$1" 2>/dev/null || true; } | head -1 | awk '{print $3}'; }

# wait_until ID REGEX [TIMEOUT_S] — waits until `status ID` output matches.
wait_until() {
  local id=$1 re=$2 timeout=${3:-180} start=$SECONDS
  printf '%s… waiting for %s to match /%s/%s\n' "$dim" "$id" "$re" "$reset"
  until "$R" status "$id" 2>/dev/null | grep -qE "$re"; do
    (( SECONDS - start > timeout )) && { echo "${red}timed out${reset}"; "$R" status "$id"; exit 1; }
    sleep 1
  done
}

# watch_all ID... — prints one line per poll with each rollout's state until all finish.
watch_all() {
  local t0=$SECONDS
  while :; do
    local done=1 line="  [+$(( SECONDS - t0 ))s]"
    for id in "$@"; do
      local st; st=$(rollout_state "$id")
      line+="  ${id#ro/}: ${st:-?}"
      [[ $st =~ ^(SUCCEEDED|HELD|ABORTED)$ ]] || done=0
    done
    echo "$line"
    (( done )) && break
    sleep 3
  done
}

ramp_log() { grep "mock ramp" "$LOGS/worker.log" | grep "Agent:$1 " | sed -E 's/.*Region:([^ }]+).*version=([^ ]+) percent=([0-9]+).*/    \1  \2  \3%/' | tail -n "${2:-20}"; }

# ---------- setup / teardown ----------
cleanup() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
}
trap cleanup EXIT

port_open() { nc -z 127.0.0.1 "$1" >/dev/null 2>&1; }

# cleanup_previous aborts demo rollouts a previous, interrupted demo run left
# running on the dev server; otherwise Act 1's start would be refused.
cleanup_previous() {
  local id st
  for a in agent-foobar sdk-core agent-foobar-1 agent-foobar-2 agent-foobar-3; do
    id="ro/$a"
    st=$(rollout_state "$id")
    [[ -z "$st" || $st =~ ^(SUCCEEDED|HELD|ABORTED)$ ]] && continue
    say "aborting $id left running by an earlier demo run"
    "$R" abort "$id" >/dev/null 2>&1 || true
    wait_until "$id" '(SUCCEEDED|HELD|ABORTED)' 60
  done
}

setup() {
  command -v "$TEMPORAL" >/dev/null || { echo "temporal CLI not found: brew install temporal (or set TEMPORAL=/path)"; exit 1; }
  mkdir -p "$LOGS" "$ROOT/bin"
  say "building binaries"
  (cd "$ROOT" && go build -o bin/ ./cmd/rolloutctl ./cmd/server ./cmd/worker)

  if port_open 7233; then
    say "using Temporal already running on :7233"
  else
    say "starting Temporal dev server (UI: http://localhost:8233)"
    "$TEMPORAL" server start-dev --log-level error >"$LOGS/temporal.log" 2>&1 & PIDS+=($!)
    until port_open 7233; do sleep 0.5; done
  fi
  "$ROOT/bin/worker" --registry "$DEMO/plugins.yaml" >"$LOGS/worker.log" 2>&1 & PIDS+=($!)
  "$ROOT/bin/server" --registry "$DEMO/plugins.yaml" >"$LOGS/server.log" 2>&1 & PIDS+=($!)
  until port_open 8080; do sleep 0.5; done
  cleanup_previous
  say "worker + API up (logs: demo/.logs/)."
  say "Live dashboard: $ROLLOUT_SERVER/ui   ·   Temporal UI: http://localhost:8233"
  [[ "${DEMO_AUTO:-}" == 1 || "${DEMO_OPEN:-1}" == 0 ]] || open "$ROLLOUT_SERVER/ui" 2>/dev/null || true
}

# ---------- acts ----------
act1() {
  act "Act 1 — one team, full ramp with a manual sign-off"
  say "The owner's policy: 3 stages, % steps, per-stage validations, customer waves in prod."
  run sed -n '1,40p' "$DEMO/policies/agent-foobar.yaml"
  pause
  say "rolloutctl plan compiles it into a deterministic, fully resolved plan (nothing runs yet)."
  run "$R" plan -f "$DEMO/policies/agent-foobar.yaml" --version v1.0.0
  pause
  run "$R" start -f "$DEMO/policies/agent-foobar.yaml" --version v1.0.0
  say "Each target ramps 10% → 50% → 100%, baking and validating after every step."
  say "Before any customer sees prod, prod-dark mirrors real traffic to v1 for every prod target (responses discarded), validates it, then tears mirroring down."
  wait_until ro/agent-foobar 'awaiting_approval|SUCCEEDED' 180   # SUCCEEDED: already approved on the dashboard
  run "$R" status ro/agent-foobar
  say "The prod canary waits for a human. Approve the sign-off here (enter), or click Approve on the dashboard:"
  pause
  approve ro/agent-foobar prod/canary/acme/us-east-1 signoff
  wait_until ro/agent-foobar 'SUCCEEDED' 180
  run "$R" status ro/agent-foobar
  say "Every traffic change the ramper made, in order:"
  ramp_log agent-foobar 30
  pause
}

act2() {
  act "Act 2 — two teams at the same time, fully independent"
  say "sdk-core and agent-foobar start together. Each rollout has its own workflow and its own stage locks."
  run "$R" start -f "$DEMO/policies/sdk-core.yaml" --version v2.0.0
  run "$R" start -f "$DEMO/policies/agent-foobar.yaml" --version v1.1.0
  say "agent-foobar still needs its canary sign-off; approving it as soon as it asks."
  wait_until ro/agent-foobar 'awaiting_approval|SUCCEEDED' 240
  approve ro/agent-foobar prod/canary/acme/us-east-1
  watch_all ro/sdk-core ro/agent-foobar
  say "Interleaved ramps from the worker log (both teams progressing in parallel):"
  grep "mock ramp" "$LOGS/worker.log" | grep -E "Agent:(sdk-core|agent-foobar) " | tail -n 12 |
    sed -E 's/.*Agent:([^ ]+) Stage:([^ ]+) .*Region:([^ }]+).*version=([^ ]+) percent=([0-9]+).*/    \1  \2\/\3  \4  \5%/'
  pause
}

act3() {
  act "Act 3 — three teams; one fails, the others keep shipping"
  say "agent-foobar-2's error-rate check is scripted to FAIL. 1 and 3 are healthy."
  for i in 1 2 3; do run "$R" start -f "$DEMO/policies/agent-foobar-$i.yaml" --version v3.0.0; done

  say "Guardrail: one active rollout per agent — a second start for agent-foobar-1 is refused."
  expect_exit 3 "$R" start -f "$DEMO/policies/agent-foobar-1.yaml" --version v3.0.1

  wait_until ro/agent-foobar-2 'paused' 120
  say "agent-foobar-2 failed in pre-prod: its failure policy notified and paused. Traffic stays at 10% — nothing else moves."
  run "$R" status ro/agent-foobar-2
  say "Notifier output for the failure:"
  grep "rollout event" "$LOGS/worker.log" | grep "agent-foobar-2" | tail -n 2 | sed 's/^/    /'
  pause

  say "Guardrail: signals are checked against target state — approving a paused target is refused."
  expect_exit 3 "$R" approve ro/agent-foobar-2 --target pre-prod/default/_/us-east-1

  say "Operator retries validation; it fails again, so the target re-pauses."
  run "$R" retry ro/agent-foobar-2 --target pre-prod/default/_/us-east-1
  sleep 5
  run "$R" status ro/agent-foobar-2
  pause

  say "Meanwhile agent-foobar-1 and -3 finish on their own:"
  watch_all ro/agent-foobar-1 ro/agent-foobar-3
  for i in 1 2 3; do "$R" status "ro/agent-foobar-$i" | head -1 | sed 's/^/  /'; done

  say "The owner of agent-foobar-2 aborts. Its later regions and all of prod were never touched."
  run "$R" abort ro/agent-foobar-2
  wait_until ro/agent-foobar-2 ABORTED 60
  run "$R" status ro/agent-foobar-2
  pause
}

act4() {
  act "Act 4 — guardrails before anything runs"
  say "A policy with mistakes is rejected at compile time, with file:line diagnostics:"
  run cat "$DEMO/policies/broken.yaml"
  expect_exit 2 "$R" plan -f "$DEMO/policies/broken.yaml" --version v1
  say "Unknown rollouts and targets are reported, not guessed at:"
  expect_exit 1 "$R" status ro/no-such-agent
  pause
}

main() {
  local acts=("$@"); [[ ${#acts[@]} -eq 0 ]] && acts=(1 2 3 4)
  setup
  for a in "${acts[@]}"; do "act$a"; done
  act "Done"
  say "Dashboard: $ROLLOUT_SERVER/ui · Temporal UI: http://localhost:8233 (GlobalRollout → StageRollout → TargetRollout)."
}

main "$@"
