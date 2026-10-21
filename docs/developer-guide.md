# Developer guide

## Setup

- **Go 1.24.** The module is pinned to `go 1.24`; check this before
  bumping dependencies. Temporal SDK releases newer than v1.40 require a
  newer Go.
- **Temporal CLI** (`brew install temporal`). Needed for `make dev-temporal`,
  `make e2e` and the demo. The integration tests download their own copy.
- **No other global installs.** The protobuf generators run as `go tool`
  (see the `tool` block in `go.mod`).

## Everyday commands

| Command | What it does |
|---|---|
| `make test` | Unit and workflow tests (`go test ./...`), including the replay test |
| `make lint` | `go vet ./...` |
| `make build` | Builds `bin/server`, `bin/worker` and `bin/rolloutctl` |
| `make proto` | Regenerates `proto/plugin/v1/*.pb.go` (`go tool buf generate`) |
| `make integration` | Integration tests against a real Temporal dev server (about 70s) |
| `make e2e` | Runs the binaries and drives the examples with `rolloutctl` |
| `make dev-temporal` | Starts a local Temporal dev server |

`make e2e` and `demo/demo.sh` accept `TEMPORAL=/path/to/temporal` if the
CLI isn't on your `PATH`. The e2e script uses ports 7244 and 18080, so it
won't clash with a dev server you already have running.

## Running locally

```sh
make dev-temporal
bin/worker --temporal 127.0.0.1:7233 --registry plugins.yaml
bin/server --listen 127.0.0.1:8080 --temporal 127.0.0.1:7233 --registry plugins.yaml
```

The server and the worker must load the same registry file. Restart both
after changing it.

## Code layout

```
cmd/{server,worker,rolloutctl}   binaries (flag parsing and wiring only)
internal/spec                    policy types and parsing
internal/registry                plugins.yaml loader; built-in param schemas in schemas/
internal/compiler                checks.go (diagnostics) and compile.go (resolution)
internal/plan                    plan types and ID helpers
internal/decision                verdicts → action
internal/plugin{,/builtin,/remote}
internal/activities
internal/workflows               target.go, stage.go, global.go, status.go, signals.go
internal/api, internal/cli
pkg/plugincontract               exported test kit for plugin teams
proto/plugin/v1                  gRPC contract (and generated code)
examples/, demo/                 sample policies and the demo script
test/integration                 integration tests and e2e.sh
```

See [architecture.md](architecture.md) for how these fit together.

## Testing

Write the failing test first. Each layer has its own style:

- **Parser and compiler:** table tests. Every diagnostic asserts the path
  and the source line. `internal/compiler/testdata/*.plan.json` are golden
  plans; after an intended change, regenerate them with
  `go test ./internal/compiler -run TestCompileGolden -update` and review
  the diff. `TestExamplesCompile` keeps `examples/` valid against the root
  `plugins.yaml`.
- **Decision engine:** a pure table test (`decision_test.go`).
- **Workflows:** use Temporal's test environment, which skips time, with
  mocked activities. `target_test.go` defines a `harness` that records
  ramps, validations, notifications and progress signals. Use
  `newHarness` for a single target, where progress signals are mocked, and
  `newEnv` for stage and global tests, where they are delivered for real.
  Send operator signals at fixed times with `h.signalAt(d, name, arg)`.
- **API:** handler tests against `go.temporal.io/sdk/mocks.Client`.
- **Integration:** `test/integration` (build tag `integration`) runs a real
  dev server, worker and API.
- **Replay:** `TestReplay` replays each file in
  `internal/workflows/testdata/histories/` 20 times.

## Changing workflows

Running rollouts replay their history against the code currently
deployed. A change that issues different Temporal commands for the same
history breaks those rollouts. Rules:

1. Read only the plan, signals and activity results. Use `workflow.Now`,
   `workflow.Sleep`, `workflow.Go`, `workflow.NewSelector`; never
   `time.Now`, goroutines, or randomness.
2. Never iterate over a map while issuing commands (starting activities or
   children, sending signals, starting timers). Sort the keys first.
3. To change the commands an existing code path issues, gate the change
   with `workflow.GetVersion`.
4. Run `make test` (it includes `TestReplay`). For a significant change,
   record fresh histories and commit them:
   ```sh
   go test -tags integration ./test/integration -run 'TestE2EHappyPath|TestHeldStopsSiblings' -args -record
   ```

## Policy reference

```yaml
apiVersion: rollout/v1                 # required, exactly this value
agent: <name>                          # required
ramper: { plugin: <ramper>, params: {...} }   # default ramper
concurrency: { pipelining: false }     # true: one active rollout per stage instead of per agent

defaults:                              # any field below can also be set on a stage or a wave
  ramper: {...}
  steps: [10, 25, 50, 100]             # strictly increasing, 1..100, must end at 100
  bake: 5m                             # Go duration
  regions: [us-east-1]                 # rolled out in order
  validate: [health, ...]              # names from `validations`
  decision:
    critical: all_pass
    warning: notify | ignore | fail
    inconclusive: { retry: 3, backoff: 1m, then: fail | pass }
  on_failure:                          # run in order
    - { notify: <notifier> }
    - { retry: { max: 2, backoff: 1m } }
    - { pause: { timeout: 24h, on_timeout: hold | abort } }
    - hold                             # must be last

validations:
  <name>: { plugin: <validator>, params: {...}, severity: critical | warning, when: every_step | final_step }

stages:                                # run in order
  - name: <name>
    <overrides>
    waves:                             # optional; run in order
      - name: <name>
        customers: [a, b]              # in parallel, up to max_parallel
        max_parallel: 1
        <overrides>
```

- **Precedence:** wave, then stage, then `defaults`, then the built-in
  defaults. For `decision`, each field resolves separately.
- **Built-in defaults:**
  - `steps: [100]`
  - `bake: 0s`
  - `decision: {critical: all_pass, warning: notify, inconclusive: {retry: 3, backoff: 1m, then: fail}}`
  - `on_failure: [{pause: {timeout: 24h, on_timeout: hold}}]`
  - `max_parallel: 1`
  - `severity: critical`
  - `when: every_step`
- **Names:**
  - Agent, stage, wave, customer and region names must match
    `^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`, because they become part of
    workflow IDs.
  - `--version` must match `^[A-Za-z0-9][A-Za-z0-9._+-]*$`.

## HTTP API

All bodies are JSON. Rollout and target IDs contain `/`, so send each one
URL-escaped as a single path segment (`ro%2Fsupport-agent`).

| Method and path | Body | Responses |
|---|---|---|
| `POST /v1/plans` | `{policy, version}` | 200 plan, 400, 422 `{errors: [{path, line, message}]}` |
| `POST /v1/rollouts` | `{policy, version}` | 201 `{id}`, 409 if already running, 422 |
| `GET /v1/rollouts/{id}` | — | 200 status tree, 404 |
| `POST /v1/rollouts/{id}/targets/{tid}/signal` | `{signal, validation?}` | 200, 400 unknown signal, 404, 409 if not valid in the target's state |
| `POST /v1/rollouts/{id}/abort` | — | 200, 404 |

Which signal is valid in which target state:

| Signal | Target must be |
|---|---|
| `approve` / `reject` | `awaiting_approval` (and, with `validation`, that validation must be awaiting) |
| `resume` / `retry` / `abort` | `paused` |

Target states: `pending`, `ramping`, `baking`, `validating`,
`awaiting_approval`, `retrying`, `paused`, `succeeded`, `held`, `aborted`,
`skipped`. Rollout states: `running`, `waiting_for_stage`, `SUCCEEDED`,
`HELD`, `ABORTED`.
