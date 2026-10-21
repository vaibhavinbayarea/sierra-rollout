# Agent Rollout Service — Design

- **Date:** 2026-10-21
- **Status:** Draft, pending review
- **Scope:** v1

## 1. Intent

An internal platform service that deploys agents/services according to a rollout policy written by the agent owner in YAML. The service compiles the policy into a deterministic, fully resolved plan and executes it as Temporal workflows.

### Goals (v1)

- Owners describe rollout intent declaratively: stages (staging → pre-prod → prod), regions, ramp steps, validations, decision rules, failure policy.
- Prod is split into customer **waves**; one policy can cover one or many customers.
- The smallest unit of execution is a **target rollout**: ramp → bake → validate → decide, repeated per ramp step.
- Validation, ramping and notification are **pluggable**. Teams publish plugins via a remote gRPC contract; a small set of built-ins ships with the service (stubbed in v1).
- Deterministic execution: a running rollout is unaffected by later edits to the policy, registry, or defaults.
- Fully demonstrable end-to-end locally (Temporal dev server + CLI + API + worker).

### Success criteria

1. `rolloutctl plan` prints the fully resolved step sequence for a policy, or line-referenced compile errors.
2. `rolloutctl start` runs the example policy end-to-end against the local Temporal dev server using built-in stubs.
3. Failure paths (fail → notify → pause → resume/retry/abort; inconclusive → retry → fail; manual approval) are exercised by workflow tests and demoable via stub params.
4. A third-party validator implemented against the gRPC contract can be registered in `plugins.yaml` and used from a policy without changes to service code.

### Non-goals (v1)

- Automatic rollback (any form). Failure policy is notify / pause / retry / hold only.
- Real ramper integrations (Istio, Argo Rollouts, feature flags). v1 ships `mock-ramper` only.
- Per-customer stage pipelines and customer groups (layout "C"; see §11).
- Per-customer failure isolation within a wave (see §11).
- A database, dynamic plugin registration, API authentication, plugin mTLS.
- GitOps triggering.

## 2. Glossary

| Term | Meaning |
|---|---|
| Policy | The owner's YAML file (`apiVersion: rollout/v1`). |
| Plan | Immutable, fully resolved compile output of a policy + registry + defaults + version. Workflow input. |
| Stage | `staging`, `pre-prod`, `prod` (names are owner-defined, order is the list order). |
| Wave | Ordered group of customers within a stage. Stages without `waves` get one implicit wave with no customer. |
| Target | Leaf unit: `(stage, wave, customer \| _, region)`. Has its own resolved steps, validations, decision rule and failure policy. |
| Step | One ramp percentage within a target. |
| Plugin | A `ramper`, `validator` or `notifier`, either built-in or remote. |

## 3. Architecture

```
 CLI (rolloutctl) ──HTTP──▶  API server ──────────▶ Temporal ◀── Rollout worker
                              │  ├ parse+validate YAML          │  ├ GlobalRollout wf
                              │  ├ compiler → Plan              │  ├ StageRollout wf
                              │  ├ lock (workflow-ID based)     │  ├ TargetRollout wf
                              │  └ start/signal/query wf        │  └ plugin activities ──▶ plugin client
                              └─ plugin registry (plugins.yaml) │                           ├ built-ins (in-process)
                                                                │                           └ remote (gRPC)
```

Language: Go, Temporal Go SDK. Go module path: `sierra-rollout`. No database: Temporal is the only state store; the registry is a config file.

### Packages

| Package | Responsibility | Depends on |
|---|---|---|
| `internal/spec` | Policy types, YAML parsing (with line info), structural validation | — |
| `internal/plan` | Plan IR types (tree; targets are leaves), JSON-serializable | — |
| `internal/compiler` | Pure function `Compile(spec, registry, version) → (Plan, []Diagnostic)` | spec, plan, registry |
| `internal/registry` | Load `plugins.yaml`; plugin metadata and param JSON schemas | — |
| `internal/plugin` | `Ramper`/`Validator`/`Notifier` interfaces; gRPC remote adapter; built-in stubs | registry |
| `internal/decision` | Pure decision engine: `(verdicts, rule, attempt) → Action` | plan |
| `internal/workflows` | `GlobalRollout`, `StageRollout`, `TargetRollout`; status tree | plan, decision |
| `internal/activities` | `Ramp`, `Validate`, `Notify` — resolve plugin, call it, map errors | plugin |
| `internal/api` | HTTP handlers | compiler, Temporal client |
| `proto/plugin/v1` | gRPC contract for remote plugins | — |
| `cmd/server`, `cmd/worker`, `cmd/rolloutctl` | Binaries | — |

## 4. Policy schema (v1)

```yaml
apiVersion: rollout/v1
agent: support-agent
ramper: { plugin: mock-ramper, params: { service: support-agent } }   # default; overridable per stage

concurrency: { pipelining: false }          # true → one active rollout per stage (default false)

defaults:
  steps: [10, 25, 50, 100]
  bake: 5m
  decision:
    critical: all_pass
    warning: notify
    inconclusive: { retry: 3, backoff: 1m, then: fail }
  on_failure:
    - { notify: log-notifier }
    - { pause: { timeout: 24h, on_timeout: hold } }

validations:
  error-rate: { plugin: prometheus, severity: critical,
                params: { query: 'rate(errors[5m])', max: 0.01 } }
  health:     { plugin: http-health, severity: critical, params: { path: /healthz } }
  csat-drift: { plugin: eval-team/csat, severity: warning }
  signoff:    { plugin: manual-approval, when: final_step }

stages:
  - name: staging
    regions: [us-east-1]
    steps: [100]
    validate: [health]
  - name: pre-prod
    regions: [us-east-1, eu-west-1]
    validate: [health, error-rate]
  - name: prod
    validate: [health, error-rate, csat-drift, signoff]
    waves:
      - name: canary
        customers: [acme]
        regions: [us-east-1]
      - name: broad
        customers: [globex, initech]
        max_parallel: 2
        regions: [us-east-1, eu-west-1]
        steps: [25, 100]
```

### Field semantics

- **Override precedence:** wave > stage > `defaults` > built-in defaults. Overridable fields: `ramper`, `steps`, `bake`, `decision`, `on_failure`, `regions`, `validate`. The compiler writes resolved values into every target.
- **Built-in defaults:** `steps: [100]`, `bake: 0s`, `decision` as in the example above, `on_failure: [{ pause: { timeout: 24h, on_timeout: hold } }]`, `max_parallel: 1`, `when: every_step`, `severity: critical`.
- **Ordering:** stages run sequentially in list order. Waves run sequentially. Customers within a wave run in parallel up to `max_parallel`. Regions run sequentially per customer, in list order.
- **`validate`:** a list of names from `validations`. `when: every_step` (default) runs after every step; `when: final_step` runs only after the last step of the target. All validations applicable to a step run in parallel.
- **`severity`:** `critical` or `warning`.
- **`decision`:**
  - `critical: all_pass`: any critical `FAIL` → step fails.
  - `warning: notify | ignore | fail`: what a warning `FAIL` does (`notify` sends via the first `notify` action in `on_failure`, or `log-notifier` if none, and continues).
  - `inconclusive: { retry: N, backoff: D, then: fail | pass }`: re-run the inconclusive validations up to N times with backoff D, then treat them as `then`.
- **`on_failure`:** an ordered list; each action runs in order until one terminates the target or resumes it.
  - `notify: <notifier-plugin>`
  - `retry: { max: N, backoff: D }`: re-run all step validations (not the ramp); if one passes, continue.
  - `pause: { timeout: D, on_timeout: hold | abort }`: wait for an operator signal.
  - `hold`: end the target `HELD`; traffic stays at the current percentage.
- **Stages without `waves`** compile to a single implicit wave with `customer = _` (shared environment).

### Compile-time checks (→ diagnostics with YAML path and line)

- Unknown `apiVersion`, unknown fields, missing required fields (`agent`, `stages`, a ramper at some level).
- Plugin not in registry, or wrong kind (e.g. a notifier used as a validator).
- `params` not valid against the plugin's `params_schema`.
- `steps` not strictly increasing, outside 1–100, or not ending at 100.
- Duplicate stage, wave, region (within a list) or customer (within a stage) names.
- `validate` references to undefined validations; `on_failure` with actions after a terminal `hold`.
- `max_parallel < 1`; negative durations.

## 5. Plan IR

```go
type Plan struct {
    ID, Agent, Version string
    PolicyHash         string            // sha256 of the policy bytes
    Pipelining         bool
    Plugins            map[string]PluginRef // frozen registry entries actually used
    Stages             []StagePlan
}
type StagePlan  struct { Name string; Waves []WavePlan }
type WavePlan   struct { Name string; MaxParallel int; Customers []CustomerPlan }
type CustomerPlan struct { Customer string; Targets []TargetPlan } // targets in region order
type TargetPlan struct {
    ID          string // <stage>/<wave>/<customer|_>/<region>
    Stage, Wave, Customer, Region string
    Ramper      PluginCall
    Steps       []int
    Bake        time.Duration
    Validations []ValidationCall // name, plugin, params, severity, when
    Decision    DecisionRule
    OnFailure   []FailureAction
}
```

The plan is JSON-serializable and is the only workflow input. Compiling the same policy, registry and version always produces a byte-identical plan (map iteration is sorted).

## 6. Workflows

### Hierarchy and IDs

| Workflow | ID | Notes |
|---|---|---|
| `GlobalRollout` | `ro/<agent>` (pipelining off) / `ro/<agent>/<version>` (pipelining on) | Started with `WorkflowIDConflictPolicy=Fail` + reuse-allowed-after-close; a running duplicate → API 409. |
| `StageRollout` | `ro/<agent>/stage/<stage>` (pipelining on) / `<global-id>/stage/<stage>` (off) | With pipelining on, this ID is the stage lock. |
| `TargetRollout` | `<global-id>/<target.ID>` | One per target. |

- `GlobalRollout` runs stages sequentially. With pipelining on, if starting a stage child fails because the ID is already running, it records `waiting_for_stage` in status, sleeps on a workflow timer (30s), and retries. Versions therefore queue for a busy stage.
- `StageRollout` runs waves sequentially; within a wave it runs customers concurrently (bounded by `MaxParallel`, using `workflow.Go` + a semaphore channel), and each customer's targets sequentially.
- If any target ends `HELD` or `ABORTED`, the stage starts no further targets. Running siblings finish their current step and then end `HELD`. The stage and the global rollout end with the first terminal non-success status (`ABORTED` > `HELD`).

### TargetRollout state machine

```
for i, pct in Steps:
    Ramp(pct)                      # activity; idempotent absolute set
    sleep(Bake)
  validate:
    verdicts = parallel Validate(v) for v in applicable(Validations, i)
    for v with AWAIT_SIGNAL: wait for approve|reject signal for v (optional timeout → INCONCLUSIVE)
    action = decision.Decide(verdicts, Decision, inconclusiveAttempt)
    RETRY_VALIDATION → sleep(backoff); re-run inconclusive validations; goto decide
    CONTINUE         → next step
    FAIL             → runFailurePolicy(OnFailure)
                         notify → Notify activity, next action
                         retry  → re-run validate block (bounded by max); pass → CONTINUE
                         pause  → wait signal: resume → CONTINUE | retry → goto validate | abort → ABORTED
                                  timeout → on_timeout (hold | abort)
                         hold   → end HELD
                       list exhausted without resolution → end HELD
end SUCCEEDED
```

Terminal statuses: `SUCCEEDED`, `HELD`, `ABORTED`.

### Status

Each `TargetRollout` signals `GlobalRollout` (`SignalExternalWorkflow`, signal `target-progress`) on every state transition: `{targetID, state, step, percent, verdicts, message, at}`. `GlobalRollout` maintains the full status tree and serves it via the `status` query. Target states: `pending, ramping, baking, validating, awaiting_approval, retrying, paused, succeeded, held, aborted`.

### Signals

| Signal | Sent to | Effect |
|---|---|---|
| `approve` / `reject {validation}` | TargetRollout | Resolve an `AWAIT_SIGNAL` verdict to PASS/FAIL. |
| `resume` / `retry` / `abort` | TargetRollout | Only valid while `paused`. |
| `abort` | GlobalRollout | Cancel all children; targets catch cancellation, run their `notify` actions, end `ABORTED`. No traffic change. |

Signals that don't apply to the current state are ignored by the workflow (and rejected earlier by the API after a status query).

### Determinism

- Workflows read only the plan, signals, and activity results; time is `workflow.Now`; concurrency uses `workflow.Go`/`workflow.Selector`.
- Engine changes after release are gated with `workflow.GetVersion`.
- One child workflow per target keeps histories small; no continue-as-new in v1.

### Activity options

| Activity | Timeout | Retry | On exhaustion |
|---|---|---|---|
| `Ramp` | plugin `timeout` (default 5m) | 5 attempts, exp. backoff from 5s | target goes to `runFailurePolicy` |
| `Validate` | plugin `timeout` (default 2m) | 3 attempts, exp. backoff from 5s | verdict = `INCONCLUSIVE` |
| `Notify` | 30s | 3 attempts | logged, ignored |

## 7. Plugin contract

### Go interfaces

```go
type Target struct{ Agent, Stage, Wave, Customer, Region string }

type Ramper interface {
    SetPercent(ctx context.Context, r RampRequest) (RampResult, error) // RampRequest{Target, Version, Percent, Params}; idempotent, absolute
    Current(ctx context.Context, t Target) (percent int, version string, err error)
}
type Validator interface {
    Validate(ctx context.Context, r ValidateRequest) (Verdict, error) // {Target, Version, Step, Percent, Params, Attempt}
}
type Notifier interface {
    Notify(ctx context.Context, e Event) error // {RolloutID, Target, Kind, Message, Details}
}
type Verdict struct {
    Outcome  Outcome // PASS | FAIL | INCONCLUSIVE | AWAIT_SIGNAL
    Summary  string
    Evidence map[string]any
}
```

Plugins are stateless; anything long-running (e.g. human approval) is expressed as `AWAIT_SIGNAL` and handled by the workflow.

### Remote (gRPC)

`proto/plugin/v1` defines `RamperService`, `ValidatorService`, `NotifierService` mirroring the interfaces, plus `Describe() → {name, kind, version, params_json_schema}` on each. `params` travels as `google.protobuf.Struct`. The remote adapter implements the Go interfaces, applies the per-plugin timeout, and maps gRPC status codes to errors (all retried by activity policy). v1 uses plaintext gRPC; the client is constructed via a `DialOptions` hook so mTLS/token auth can be added without interface changes.

### Built-ins (v1, all stubs)

| Name | Kind | Behavior |
|---|---|---|
| `mock-ramper` | ramper | Records `(target → version, percent)` in worker memory; logs each change. |
| `prometheus` | validator | Returns the outcome given in `params.stub` (`{outcome, after_attempt}`); default PASS. Real `query`/`max` params are schema-validated but not executed. |
| `http-health` | validator | Same stub mechanism; default PASS. |
| `manual-approval` | validator | Always returns `AWAIT_SIGNAL`. |
| `log-notifier` | notifier | Writes the event to the worker log. |

`stub.after_attempt: N` means "return `outcome` for attempts < N, PASS afterwards", enabling retry/recovery demos.

### Registry (`plugins.yaml`)

```yaml
plugins:
  - { name: prometheus,      kind: validator, builtin: true }
  - { name: http-health,     kind: validator, builtin: true }
  - { name: manual-approval, kind: validator, builtin: true }
  - { name: mock-ramper,     kind: ramper,    builtin: true }
  - { name: log-notifier,    kind: notifier,  builtin: true }
  - { name: eval-team/csat,  kind: validator, endpoint: csat-plugin.eval:9000,
      timeout: 2m, params_schema: schemas/csat.json }
```

Loaded at startup by server and worker. Built-ins embed their own params schema. Changing the registry requires restarting both. Plugin metadata is frozen into the plan; the worker resolves plugin implementations by name and fails the activity (non-retryable) if a planned plugin is missing from its registry.

## 8. API and CLI

HTTP+JSON, bound to localhost; auth middleware hook reserved, no auth in v1.

| Endpoint | CLI | Behavior |
|---|---|---|
| `POST /v1/plans` `{policy, version}` | `rolloutctl plan -f policy.yaml --version v1.4.2` | Compile; return plan or 422 diagnostics. |
| `POST /v1/rollouts` `{policy, version}` | `rolloutctl start -f … --version …` | Compile; start `GlobalRollout`; 201 `{id}`; 409 if locked; 422 on diagnostics. |
| `GET /v1/rollouts/{id}` | `rolloutctl status <id> [--watch]` | `status` query; CLI renders the tree (`--watch` polls every 2s). |
| `POST /v1/rollouts/{id}/targets/{tid}/signal` `{signal, validation?}` | `rolloutctl approve\|reject\|resume\|retry\|abort <id> --target <tid>` | Validate against current status; send signal; 409 if not applicable. |
| `POST /v1/rollouts/{id}/abort` | `rolloutctl abort <id>` | Signal `abort` to `GlobalRollout`. |

Diagnostics shape: `{"errors": [{"path": "stages[2].waves[1].steps", "line": 41, "message": "steps must end at 100"}]}`.

## 9. Error handling summary

| Failure | Handling |
|---|---|
| Invalid policy | 422 with diagnostics; nothing starts. |
| Concurrent rollout (pipelining off) | 409 with the running rollout's ID. |
| Stage busy (pipelining on) | Queue; status `waiting_for_stage`. |
| Validator transport/plugin error | Activity retries → `INCONCLUSIVE` → decision rule. |
| Ramper error | Activity retries → target failure policy. |
| Notifier error | Retries → logged and ignored. |
| Worker crash/restart | Temporal replay; ramp idempotency makes re-execution safe. |
| Planned plugin missing on worker | Non-retryable activity error → target failure policy. |
| Inapplicable operator signal | Rejected by API (409); ignored by workflow. |

## 10. Testing

- **Unit:** `spec` parsing/validation (table tests incl. line numbers); `compiler` golden files (`testdata/*.yaml → *.plan.json`) including override precedence and every diagnostic; `decision` table tests over verdict/rule/attempt combinations.
- **Workflow:** Temporal `testsuite` (time-skipping) with mocked activities: happy path; fail → notify → pause → resume; pause → retry; pause → abort; pause timeout → hold; inconclusive retry → fail and → pass; manual approve/reject; global abort mid-bake; target held stops stage; wave parallelism bound respected; pipelining queue on busy stage; status tree correctness. Replay test against recorded histories in `testdata/histories/`.
- **Plugin:** remote adapter against an in-process fake gRPC server (success, timeout, error mapping); an exported contract test suite (`plugin/contracttest`) plugin teams can run against their implementation; built-in stub behavior.
- **API:** handler tests with a mocked Temporal client (201/409/422 paths, signal applicability).
- **E2E:** `make e2e` starts `temporal server start-dev`, server and worker; runs `examples/policy.yaml` via `rolloutctl` to success, and `examples/policy-failing.yaml` through pause → resume.

## 11. Future work (designed for, not built)

- **Layout C:** per-customer stage pipelines and `customer_groups`. Compiler-only change; plan tree and workflows already support arbitrary stage/wave/customer content.
- **Per-customer failure isolation:** e.g. `wave.failure_scope: customer` — a customer's failure holds only that customer's remaining targets. Hooks in at the target → stage boundary in `StageRollout`.
- **Rollback:** a `rollback` failure action implemented as `Ramper.SetPercent(0)` / restore of `Current()`'s previous version; optional cascade.
- **Real rampers:** Istio/Envoy weights, Argo Rollouts, feature-flag systems.
- **Real built-in validators:** Prometheus queries, HTTP health checks; Slack/PagerDuty notifiers.
- **Dynamic plugin registration** (`rolloutctl plugin register`), plugin mTLS/token auth, API authn/z.
- **GitOps trigger** as a thin client of `POST /v1/rollouts`.
- **Persistence** (rollout history beyond Temporal retention).
