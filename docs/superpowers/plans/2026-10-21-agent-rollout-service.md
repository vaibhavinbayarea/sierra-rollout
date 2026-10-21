# Agent Rollout Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go service that compiles agent rollout policies (YAML) into immutable plans and executes them as Temporal workflows with pluggable rampers, validators and notifiers.

**Architecture:** `spec` parses YAML → `compiler` resolves it (with `registry`) into a `plan.Plan` → the API starts `GlobalRollout`, which runs `StageRollout` children, which run one `TargetRollout` child per target. Targets call `Ramp`/`Validate`/`Notify` activities that resolve plugins (in-process built-in stubs or remote gRPC). A pure `decision` engine maps verdicts to next actions. Temporal is the only state store.

**Tech Stack:** Go 1.24, `go.temporal.io/sdk`, `gopkg.in/yaml.v3`, `github.com/santhosh-tekuri/jsonschema/v6`, `google.golang.org/grpc` + `google.golang.org/protobuf`, `github.com/bufbuild/buf` (as a `go tool`), `github.com/spf13/cobra`, `github.com/stretchr/testify`.

**Spec:** `docs/superpowers/specs/2026-10-21-agent-rollout-service-design.md`

## Global Constraints

- Go module path: `sierra-rollout`. `go 1.24` in `go.mod`. Code generators (`buf`, `protoc-gen-go`, `protoc-gen-go-grpc`) are declared as `tool` directives, so no global installs are needed except the `temporal` CLI for e2e (`brew install temporal`).
- No database. Temporal is the only state store; the registry is `plugins.yaml`.
- Policy `apiVersion` is exactly `rollout/v1`.
- Built-in defaults: `steps: [100]`, `bake: 0s`, `decision: {critical: all_pass, warning: notify, inconclusive: {retry: 3, backoff: 1m, then: fail}}`, `on_failure: [{pause: {timeout: 24h, on_timeout: hold}}]`, `max_parallel: 1`, `when: every_step`, `severity: critical`.
- Override precedence: wave > stage > `defaults` > built-in defaults. Fields: `ramper`, `steps`, `bake`, `decision`, `on_failure`, `regions`, `validate`.
- Workflow IDs: Global `ro/<agent>` (pipelining off) or `ro/<agent>/<version>` (on); Stage `ro/<agent>/stage/<stage>` in **both** modes (deviation from spec §6, see Review Focus #5); Target `<global-id>/<stage>/<wave>/<customer|_>/<region>`.
- Pipelined stage-busy retry interval: 30s workflow timer.
- Activity options: Ramp timeout = plugin timeout (default 5m), 5 attempts, exponential backoff from 5s. Validate timeout = plugin timeout (default 2m), 3 attempts, from 5s. Notify 30s, 3 attempts, failures logged and ignored.
- Terminal target/rollout statuses: `SUCCEEDED`, `HELD`, `ABORTED`; precedence when aggregating: `ABORTED` > `HELD` > `SUCCEEDED`.
- Target states: `pending, ramping, baking, validating, awaiting_approval, retrying, paused, succeeded, held, aborted`; rollout-level extra state `waiting_for_stage`.
- API binds `127.0.0.1:8080` by default; no auth. Remote plugin gRPC is plaintext with a `DialOptions` hook.
- `--watch` polls every 2s.

## Review Focus

1. **Restarting after a finished rollout** — `rolloutctl start` for the same agent after the previous rollout completed must succeed (ID reuse allowed after close), not 409. Test in Task 11 (`TestStartAfterCompletedRollout`).
2. **Early or duplicate operator signals** — an `approve` sent while a target is still baking, or a second `resume`, must be dropped, not buffered and consumed by a later wait. Test in Task 9 (`TestEarlyApproveIsIgnored`, `TestDuplicateResumeIgnored`).
3. **IDs containing `/` in URLs** — rollout IDs (`ro/support-agent`) and target IDs contain slashes; the CLI must path-escape them and the server must route them. Test in Task 11 (`TestSignalRouteWithSlashIDs`) and Task 12 (`TestClientEscapesIDs`).
4. **Abort while paused or awaiting approval** — a global abort must end a waiting target `ABORTED` immediately, not after the pause timeout. Test in Task 10 (`TestGlobalAbortWhilePaused`).
5. **Mixed pipelining modes for one agent** — if one rollout runs with `pipelining: true` (`ro/a/v2`) and a new one starts with it off (`ro/a`), both would run unserialized unless stage locks are shared. Stage IDs are therefore `ro/<agent>/stage/<stage>` in both modes. Test in Task 13 (`TestMixedModeSharesStageLock`, integration).

Also enforced (not in spec, implied by IDs): agent, stage, wave, customer and region names must match `^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$` — compile diagnostic otherwise (Task 4).

---

## File Structure

```
go.mod / go.sum / Makefile / buf.yaml / buf.gen.yaml / plugins.yaml
examples/policy.yaml, examples/policy-failing.yaml
internal/spec/        types.go, duration.go, parse.go, lines.go, diag.go   (+ _test.go, testdata/)
internal/registry/    registry.go, schemas/*.json (embedded builtins)
internal/plan/        plan.go (IR + Verdict/Outcome + status types)
internal/compiler/    compile.go (resolution), checks.go (semantic diagnostics), testdata/*.yaml|*.plan.json
internal/decision/    decision.go
internal/plugin/      plugin.go (interfaces), set.go (resolver)
internal/plugin/builtin/  stub.go, mockramper.go, notifier.go, approval.go
internal/plugin/remote/   remote.go (gRPC adapter)
proto/plugin/v1/      plugin.proto + generated *.pb.go
pkg/plugincontract/   contract.go (exported contract test helpers)
internal/activities/  activities.go
internal/workflows/   ids.go, signals.go, status.go, target.go, stage.go, global.go, register.go
internal/api/         server.go, handlers.go
internal/cli/         client.go, render.go, commands.go
cmd/server, cmd/worker, cmd/rolloutctl  main.go each
test/integration/     integration_test.go (build tag `integration`), e2e.sh
```

---

### Task 1: Module scaffold + policy types and parsing

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `internal/spec/types.go`, `internal/spec/duration.go`, `internal/spec/diag.go`, `internal/spec/lines.go`, `internal/spec/parse.go`
- Test: `internal/spec/parse_test.go`, `internal/spec/testdata/full.yaml` (copy of the spec §4 example)

**Interfaces:**
- Produces:
  - `type Duration time.Duration` with YAML unmarshal from `"5m"`/`"24h"`/`"0s"` strings; `func (d Duration) D() time.Duration`.
  - `type Diagnostic struct { Path string; Line int; Message string }` (JSON tags `path,line,message`); `func (d Diagnostic) Error() string`.
  - Types (yaml tags in snake_case matching spec §4): `Policy{APIVersion, Agent, Ramper *PluginCall, Concurrency{Pipelining bool}, Defaults Overrides, Validations map[string]Validation, Stages []Stage}`; `PluginCall{Plugin string; Params map[string]any}`; `Overrides{Ramper *PluginCall; Steps []int; Bake *Duration; Decision *Decision; OnFailure []FailureAction; Regions []string; Validate []string}`; `Stage{Name string; Overrides ",inline"; Waves []Wave}`; `Wave{Name string; Customers []string; MaxParallel *int; Overrides ",inline"}` (pointer so an explicit `0` is detectable); `Validation{Plugin string; Params map[string]any; Severity string; When string}`; `Decision{Critical string; Warning string; Inconclusive *Inconclusive}`; `Inconclusive{Retry int; Backoff Duration; Then string}`; `FailureAction{Notify string; Retry *RetryAction; Pause *PauseAction; Hold bool}` with `RetryAction{Max int; Backoff Duration}`, `PauseAction{Timeout Duration; OnTimeout string}`.
  - `func Parse(data []byte) (*Policy, *Lines, []Diagnostic)`; `type Lines` with `func (l *Lines) Line(path string) int` (returns 0 if unknown). Paths use the form `stages[2].waves[1].steps`, `validations.health.params`.

- [ ] **Step 1: Scaffold** — `go mod init sierra-rollout`, set `go 1.24`, add `.gitignore` (`/bin/`), `Makefile` targets `test` (`go test ./...`), `lint` (`go vet ./...`). Add yaml.v3 and testify.

- [ ] **Step 2: Write failing tests** in `parse_test.go`:
  - `TestParseFullExample`: parse `testdata/full.yaml`; no diagnostics; `p.Agent == "support-agent"`; `len(p.Stages) == 3`; `p.Stages[2].Waves[1].MaxParallel == 2`; `p.Stages[2].Waves[1].Steps == []int{25,100}`; `p.Defaults.Bake.D() == 5*time.Minute`; `p.Defaults.OnFailure[0].Notify == "log-notifier"`; `p.Defaults.OnFailure[1].Pause.Timeout.D() == 24*time.Hour`; `p.Validations["signoff"].When == "final_step"`.
  - `TestParseHoldScalar`: `on_failure: [hold]` and `on_failure: [{hold: true}]` both yield `FailureAction{Hold: true}`.
  - `TestParseUnknownField`: a stage with `regoins:` yields one diagnostic whose `Message` contains `regoins` and `Line` equals that line.
  - `TestParseBadDuration`: `bake: 5 minutes` → diagnostic with path `defaults.bake` and correct line.
  - `TestLinesLookup`: `lines.Line("stages[2].waves[1].steps")` equals the line of that key in `full.yaml`.
  - `TestParseMalformedYAML`: unbalanced brackets → one diagnostic, `Line > 0`, nil policy.

- [ ] **Step 3: Run** `go test ./internal/spec/...` — Expected: FAIL (undefined symbols).

- [ ] **Step 4: Implement.** Unmarshal once into `yaml.Node`; build `Lines` by walking the node tree (mapping key → `path`, sequence index → `path[i]`, storing the key node's line). Decode with `yaml.NewDecoder(...).KnownFields(true)` for unknown-field errors; convert `*yaml.TypeError` messages (`line N: ...`) into `Diagnostic`s by parsing the line number. `FailureAction.UnmarshalYAML` accepts scalar `hold` or a single-key mapping.

- [ ] **Step 5: Run** `go test ./internal/spec/...` — Expected: PASS.

- [ ] **Step 6: Commit** — `git add -A && git commit -m "feat(spec): policy types and YAML parsing with line diagnostics"`

---

### Task 2: Plugin registry

**Files:**
- Create: `internal/registry/registry.go`, `internal/registry/schemas/{prometheus,http-health,manual-approval,mock-ramper,log-notifier}.json`, `plugins.yaml` (exactly spec §7, with `eval-team/csat` commented out until Task 7 ships a fake), `internal/registry/testdata/csat.json`
- Test: `internal/registry/registry_test.go`

**Interfaces:**
- Produces:
  - `type Kind string` consts `KindRamper="ramper"`, `KindValidator="validator"`, `KindNotifier="notifier"`.
  - `type Entry struct { Name string; Kind Kind; Builtin bool; Endpoint string; Timeout time.Duration; ParamsSchema json.RawMessage }`.
  - `func Load(path string) (*Registry, error)` — `params_schema` paths are relative to the registry file; builtins get their embedded schema.
  - `func (r *Registry) Get(name string) (Entry, bool)`; `func (r *Registry) ValidateParams(name string, params map[string]any) error`.
- Built-in schemas (all `additionalProperties: false`, all allow optional `stub: {outcome: enum[PASS,FAIL,INCONCLUSIVE,AWAIT_SIGNAL], after_attempt: integer>=1}` except `mock-ramper` and `log-notifier`): `prometheus` `{query: string (required), max: number}`; `http-health` `{path: string (required)}`; `manual-approval` `{message: string}`; `mock-ramper` `{service: string}`; `log-notifier` `{}`.

- [ ] **Step 1: Write failing tests:**
  - `TestLoadBuiltinsAndRemote`: load a temp registry with all five builtins + a remote entry `{name: eval-team/csat, kind: validator, endpoint: localhost:9000, timeout: 2m, params_schema: csat.json}`; `Get("eval-team/csat")` has `Timeout == 2*time.Minute`, `Builtin == false`; `Get("prometheus").ParamsSchema` non-empty.
  - `TestLoadRejects`: unknown kind, duplicate name, builtin name not in the embedded set, remote without endpoint → each returns an error naming the entry.
  - `TestValidateParams`: `prometheus` with `{query: "x", max: 0.01}` ok; missing `query` → error; extra key `foo` → error; `stub: {outcome: MAYBE}` → error.

- [ ] **Step 2: Run** `go test ./internal/registry/...` — Expected: FAIL.

- [ ] **Step 3: Implement** with `//go:embed schemas/*.json`; compile schemas with `jsonschema/v6` at load time and cache the compiled schema per entry. Unknown plugin in `ValidateParams` returns an error.

- [ ] **Step 4: Run** tests — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(registry): static plugin registry with param schemas`

---

### Task 3: Plan IR and compiler resolution

**Files:**
- Create: `internal/plan/plan.go`, `internal/compiler/compile.go`
- Test: `internal/compiler/compile_test.go`, `internal/compiler/testdata/{full,minimal,staging-only}.yaml` + matching `*.plan.json` goldens, `internal/compiler/testdata/plugins.yaml`

**Interfaces:**
- Consumes: `spec.Parse`, `spec.Diagnostic`, `registry.Registry`.
- Produces in `internal/plan` (all with JSON tags, snake_case):
  - `Plan`, `StagePlan`, `WavePlan`, `CustomerPlan`, `TargetPlan` exactly as spec §5 (`TargetPlan.Ramper PluginCall`, `Bake time.Duration`).
  - `PluginRef{Name string; Kind string; Builtin bool; Endpoint string; Timeout time.Duration}` — `Timeout` filled with kind default (ramper 5m, validator 2m, notifier 30s) when the registry entry has none.
  - `PluginCall{Plugin string; Params map[string]any}`; `ValidationCall{Name, Plugin string; Params map[string]any; Severity string; When string}`.
  - `DecisionRule{Critical string; Warning string; WarningNotifier string; Inconclusive InconclusiveRule}`; `InconclusiveRule{Retry int; Backoff time.Duration; Then string}`.
  - `FailureAction{Kind string /* notify|retry|pause|hold */; Notifier string; Max int; Backoff time.Duration; Timeout time.Duration; OnTimeout string}`.
  - `type Outcome string` consts `PASS, FAIL, INCONCLUSIVE, AWAIT_SIGNAL`; `Verdict{Outcome Outcome; Summary string; Evidence map[string]any}`.
  - `func (p *Plan) Targets() []TargetPlan` (plan order).
  - `func GlobalID(agent, version string, pipelining bool) string`, `func StageID(agent, stage string) string`, `func TargetWorkflowID(globalID string, t TargetPlan) string` — formats per Global Constraints.
- Produces in `internal/compiler`: `func Compile(policy []byte, reg *registry.Registry, version string) (*plan.Plan, []spec.Diagnostic)`.

Resolution rules (the parts not obvious from the spec):
- `TargetPlan.ID = "<stage>/<wave>/<customer|_>/<region>"`; implicit wave name is `default`.
- `DecisionRule.WarningNotifier` = first `notify` action's notifier in the resolved `on_failure`, else `log-notifier`.
- `Plan.Plugins` contains only plugins referenced by some target (ramper, validations, notifiers, warning notifier).
- `PolicyHash` = hex sha256 of the raw policy bytes. `Plan.ID = GlobalID(...)`.
- `when: final_step` validations stay in `Validations`; filtering per step happens in the workflow.

- [ ] **Step 1: Write failing tests:**
  - `TestCompileGolden` (table over the three testdata files, version `v1.4.2`): marshal with `json.MarshalIndent`; compare against `*.plan.json`; `-update` flag rewrites goldens. Review the generated `full.plan.json` by hand before committing: 1 staging target, 2 pre-prod targets, prod canary 1 target, prod broad 4 targets (`globex`,`initech` × `us-east-1`,`eu-west-1`) with steps `[25,100]`.
  - `TestCompileDeterministic`: compiling `full.yaml` twice yields byte-identical JSON.
  - `TestOverridePrecedence`: defaults `bake: 5m`, stage `bake: 1m`, wave `bake: 10s` → wave target `Bake == 10*time.Second`; a sibling stage without override gets `5*time.Minute`; a policy with no bake anywhere gets `0`.
  - `TestBuiltinDefaults`: `minimal.yaml` (agent, ramper, one stage, one region) → `Steps == []int{100}`, `OnFailure == [{Kind: pause, Timeout: 24h, OnTimeout: hold}]`, `Decision.Inconclusive == {3, 1m, "fail"}`, `WarningNotifier == "log-notifier"`.
  - `TestIDs`: `GlobalID("a","v2",false)=="ro/a"`, `GlobalID("a","v2",true)=="ro/a/v2"`, `StageID("a","prod")=="ro/a/stage/prod"`, `TargetWorkflowID("ro/a", t{ID:"prod/broad/acme/us-east-1"})=="ro/a/prod/broad/acme/us-east-1"`.

- [ ] **Step 2: Run** `go test ./internal/compiler/... ./internal/plan/...` — Expected: FAIL.

- [ ] **Step 3: Implement** `plan.go` and `Compile` (parse → if diagnostics return them → resolve). Leave semantic checks to Task 4 (call a `check(p *spec.Policy, lines *spec.Lines, reg *registry.Registry) []spec.Diagnostic` stub returning nil).

- [ ] **Step 4: Run** with `-update`, inspect goldens, re-run without — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(compiler): plan IR and policy resolution`

---

### Task 4: Compiler semantic checks

**Files:**
- Create: `internal/compiler/checks.go`
- Test: `internal/compiler/checks_test.go`

**Interfaces:**
- Consumes: `spec.Lines.Line`, `registry.Registry.Get/ValidateParams`.
- Produces: `check(...)` filled in; `Compile` returns all diagnostics sorted by `(Line, Path)` and a nil plan if any exist.

- [ ] **Step 1: Write failing table test** `TestChecks` — each case is a small YAML snippet and the expected `(Path, substring of Message)`; assert `Line` equals the line of `Path` in the snippet:

| Case | Path | Message contains |
|---|---|---|
| `apiVersion: rollout/v2` | `apiVersion` | `unsupported apiVersion` |
| missing `agent` | `agent` | `required` |
| no ramper at any level for a target | `stages[0]` | `no ramper` |
| plugin `nope` | `validations.x.plugin` | `unknown plugin "nope"` |
| `log-notifier` used as validator | `validations.x.plugin` | `is a notifier, not a validator` |
| prometheus params missing `query` | `validations.x.params` | `query` |
| `steps: [10, 10, 100]` | `stages[0].steps` | `strictly increasing` |
| `steps: [10, 50]` | `stages[0].steps` | `must end at 100` |
| `steps: [0, 100]` | `stages[0].steps` | `between 1 and 100` |
| duplicate stage name | `stages[1].name` | `duplicate stage` |
| duplicate region in a list | `stages[0].regions[1]` | `duplicate region` |
| same customer in two waves of a stage | `stages[0].waves[1].customers[0]` | `duplicate customer` |
| `validate: [missing]` | `stages[0].validate[0]` | `undefined validation` |
| `on_failure: [hold, {notify: log-notifier}]` | `defaults.on_failure[1]` | `after hold` |
| `max_parallel: 0` (explicit) | `stages[0].waves[0].max_parallel` | `>= 1` |
| `bake: -1m` | `defaults.bake` | `negative` |
| `region: US_East` | `stages[0].regions[0]` | `invalid name` |
| stage with no regions anywhere | `stages[0].regions` | `required` |
| `severity: high` | `validations.x.severity` | `critical or warning` |
| `when: sometimes` | `validations.x.when` | `every_step or final_step` |
| `decision.warning: shout` | `defaults.decision.warning` | `notify, ignore or fail` |
| `pause.on_timeout: retry` | `defaults.on_failure[0].pause.on_timeout` | `hold or abort` |

  Also `TestChecksAccumulate`: a snippet with three independent errors returns three diagnostics sorted by line.

- [ ] **Step 2: Run** `go test ./internal/compiler/ -run TestChecks` — Expected: FAIL.

- [ ] **Step 3: Implement** the checks (`MaxParallel` nil → 1 is resolution, non-nil `< 1` is a diagnostic). Name regex: `^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$` for agent, stage, wave, customer, region.

- [ ] **Step 4: Run** `go test ./internal/...` — Expected: PASS (goldens unchanged).

- [ ] **Step 5: Commit** — `feat(compiler): semantic checks with line-referenced diagnostics`

---

### Task 5: Decision engine

**Files:**
- Create: `internal/decision/decision.go`
- Test: `internal/decision/decision_test.go`

**Interfaces:**
- Consumes: `plan.Verdict`, `plan.Outcome`, `plan.DecisionRule`.
- Produces:
  - `type NamedVerdict struct { Name string; Severity string; Verdict plan.Verdict }`
  - `type Action string` consts `Continue="CONTINUE"`, `RetryInconclusive="RETRY_INCONCLUSIVE"`, `Fail="FAIL"`.
  - `type Result struct { Action Action; Retry []string /* validation names to re-run */; Backoff time.Duration; NotifyWarnings []string /* names of warning fails to notify */; Reason string }`
  - `func Decide(vs []NamedVerdict, rule plan.DecisionRule, inconclusiveAttempt int) Result` — `inconclusiveAttempt` is the number of inconclusive retries already done for this step (0 on first evaluation).

Rules (in order): `AWAIT_SIGNAL` is treated as `INCONCLUSIVE`. An inconclusive verdict with `inconclusiveAttempt < rule.Inconclusive.Retry` is queued for retry; otherwise it becomes `rule.Inconclusive.Then` (`fail` → FAIL of its severity, `pass` → PASS). A critical FAIL → `Fail`. A warning FAIL → by `rule.Warning`: `ignore` nothing, `notify` add to `NotifyWarnings`, `fail` → `Fail`. Final action: `Fail` if any fail, else `RetryInconclusive` (with `Backoff = rule.Inconclusive.Backoff`) if any queued, else `Continue`. `NotifyWarnings` is populated regardless of action. `Reason` names the deciding validations.

- [ ] **Step 1: Write failing table test** `TestDecide` with cases: empty verdicts → Continue; all PASS → Continue; critical FAIL → Fail; warning FAIL + notify → Continue with `NotifyWarnings=["csat"]`; warning FAIL + ignore → Continue, no notify; warning FAIL + fail → Fail; critical INCONCLUSIVE attempt 0 retry 3 → RetryInconclusive `Retry=["error-rate"]` `Backoff=1m`; same at attempt 3 then fail → Fail; then pass → Continue; critical FAIL plus another INCONCLUSIVE → Fail (fail wins); AWAIT_SIGNAL at attempt 0 → RetryInconclusive; warning INCONCLUSIVE exhausted with `then: fail` and `warning: notify` → Continue with notify.

- [ ] **Step 2: Run** `go test ./internal/decision/...` — Expected: FAIL.

- [ ] **Step 3: Implement** `Decide`.

- [ ] **Step 4: Run** — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(decision): pure verdict → action engine`

---

### Task 6: Plugin interfaces, built-in stubs, resolver

**Files:**
- Create: `internal/plugin/plugin.go`, `internal/plugin/set.go`, `internal/plugin/builtin/{stub.go,mockramper.go,approval.go,notifier.go}`
- Test: `internal/plugin/builtin/builtin_test.go`, `internal/plugin/set_test.go`

**Interfaces:**
- Consumes: `registry.Registry`, `plan.Verdict`.
- Produces in `internal/plugin`:
  - `Target{Agent, Stage, Wave, Customer, Region string}`; `RampRequest{Target Target; Version string; Percent int; Params map[string]any}`; `RampResult{Percent int; Version string}`; `ValidateRequest{Target Target; Version string; Step, Percent, Attempt int; Params map[string]any}` (`Step` 0-based, `Attempt` 1-based); `Event{RolloutID string; Target Target; Kind, Message string; Details map[string]any}`.
  - `Ramper`, `Validator`, `Notifier` interfaces as spec §7 (Validator returns `plan.Verdict`).
  - `type Remote func(e registry.Entry) (any, error)` — constructs a remote plugin (wired to `remote.New` in Task 7; nil means remote entries error).
  - `func NewSet(reg *registry.Registry, remote Remote) (*Set, error)`; `func (s *Set) Ramper(name string) (Ramper, error)`; `Validator(name)`; `Notifier(name)`. Unknown or wrong-kind name returns `ErrPluginMissing` (exported sentinel, wrapped with name).
- Produces in `internal/plugin/builtin`: `func Register() map[string]any` returning name → instance for the five builtins; `func NewMockRamper() *MockRamper` with `func (m *MockRamper) History() []plugin.RampRequest` for tests; `log-notifier` writes via `slog`.

Stub semantics (`prometheus`, `http-health`): no `params.stub` → PASS. With `stub.outcome` and no `after_attempt` → always that outcome. With `after_attempt: N` → that outcome when `Attempt < N`, PASS otherwise. `Summary` = `"stub: <OUTCOME>"`. `manual-approval` always returns `AWAIT_SIGNAL` with `Summary` = `params.message` or `"awaiting approval"`.

- [ ] **Step 1: Write failing tests:**
  - `TestStubValidator` table: no stub → PASS; `{outcome: FAIL}` attempts 1,5 → FAIL; `{outcome: FAIL, after_attempt: 2}` attempt 1 → FAIL, attempt 2 → PASS.
  - `TestManualApproval`: → AWAIT_SIGNAL.
  - `TestMockRamperIdempotent`: `SetPercent(25)` twice → `Current` = 25, history length 2, no error; distinct targets tracked separately.
  - `TestSetResolves`: builtin lookups ok; `Validator("log-notifier")` → `errors.Is(err, plugin.ErrPluginMissing)`; remote entry with nil `Remote` → error.

- [ ] **Step 2: Run** `go test ./internal/plugin/...` — Expected: FAIL.

- [ ] **Step 3: Implement.** MockRamper guards its map with a mutex.

- [ ] **Step 4: Run** — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(plugin): interfaces, built-in stubs, resolver`

---

### Task 7: Remote plugin gRPC contract, adapter, contract tests

**Files:**
- Create: `proto/plugin/v1/plugin.proto`, generated `proto/plugin/v1/plugin.pb.go` + `plugin_grpc.pb.go`, `buf.yaml`, `buf.gen.yaml`, `internal/plugin/remote/remote.go`, `pkg/plugincontract/contract.go`
- Modify: `go.mod` (tool directives), `Makefile` (`proto: go tool buf generate`), `plugins.yaml` (uncomment `eval-team/csat`, keep schema `schemas/csat.json`; create that file: `{type: object, properties: {threshold: {type: number}}}`)
- Test: `internal/plugin/remote/remote_test.go`, `pkg/plugincontract/contract_test.go`

**Interfaces:**
- Produces:
  - Proto package `plugin.v1`, `go_package = "sierra-rollout/proto/plugin/v1;pluginv1"`. Services `RamperService{SetPercent, Current, Describe}`, `ValidatorService{Validate, Describe}`, `NotifierService{Notify, Describe}`. Messages mirror Task 6 types; `params`/`evidence`/`details` are `google.protobuf.Struct`; `Outcome` enum `OUTCOME_UNSPECIFIED, OUTCOME_PASS, OUTCOME_FAIL, OUTCOME_INCONCLUSIVE, OUTCOME_AWAIT_SIGNAL`; `DescribeResponse{name, kind, version, params_json_schema string}`.
  - `func remote.New(e registry.Entry, opts ...grpc.DialOption) (any, error)` — returns a `plugin.Ramper`/`Validator`/`Notifier` by `e.Kind`; each call wrapped in `context.WithTimeout(e.Timeout)`. `OUTCOME_UNSPECIFIED` → error. Default dial option `insecure.NewCredentials()`.
  - `pkg/plugincontract`: `func Validator(t *testing.T, addr string, sampleParams map[string]any)` and `func Ramper(t *testing.T, addr string)` — run subtests: `Describe` returns matching kind, non-empty name/version, and a schema that compiles; `Validate` with sample params returns a non-UNSPECIFIED outcome twice in a row; Ramper `SetPercent(10)` twice then `Current` = 10.

- [ ] **Step 1: Write the proto and generate** — add tools: `go get -tool github.com/bufbuild/buf/cmd/buf google.golang.org/protobuf/cmd/protoc-gen-go google.golang.org/grpc/cmd/protoc-gen-go-grpc`; `buf.gen.yaml` uses `local: ["go", "tool", "protoc-gen-go"]` style plugins with `paths=source_relative`. Run `make proto` — Expected: generated files exist, `go build ./...` succeeds.

- [ ] **Step 2: Write failing tests** in `remote_test.go` using `bufconn`: fake validator returning FAIL with evidence `{"p95": 1.2}` → adapter returns `plan.Verdict{Outcome: FAIL, Evidence: {"p95": 1.2}}`; fake sleeping past a 50ms entry timeout → error; fake returning `codes.Unavailable` → error; UNSPECIFIED → error; ramper round trip. In `contract_test.go` run `plugincontract.Validator` and `plugincontract.Ramper` against in-process fakes on a real localhost listener.

- [ ] **Step 3: Run** `go test ./internal/plugin/remote/... ./pkg/...` — Expected: FAIL.

- [ ] **Step 4: Implement** the adapter and contract helpers. Use `structpb.NewStruct` / `.AsMap()` for conversions.

- [ ] **Step 5: Run** — Expected: PASS.

- [ ] **Step 6: Commit** — `feat(plugin): remote gRPC contract, adapter and contract test kit`

---

### Task 8: Activities

**Files:**
- Create: `internal/activities/activities.go`
- Test: `internal/activities/activities_test.go`

**Interfaces:**
- Consumes: `plugin.Set`, `plugin.ErrPluginMissing`, plan types.
- Produces:
  - `type Activities struct { Plugins *plugin.Set }`
  - `RampInput{Ramper plan.PluginCall; Target plugin.Target; Version string; Percent int}`; `func (a *Activities) Ramp(ctx context.Context, in RampInput) error`
  - `ValidateInput{Validation plan.ValidationCall; Target plugin.Target; Version string; Step, Percent, Attempt int}`; `func (a *Activities) Validate(ctx context.Context, in ValidateInput) (plan.Verdict, error)`
  - `NotifyInput{Notifier string; Event plugin.Event}`; `func (a *Activities) Notify(ctx context.Context, in NotifyInput) error`
  - `const ErrTypePluginMissing = "PluginMissing"` — missing plugin returns `temporal.NewNonRetryableApplicationError(msg, ErrTypePluginMissing, err)`.
  - Activity names registered as `"Ramp"`, `"Validate"`, `"Notify"`.

- [ ] **Step 1: Write failing tests** with `testsuite.TestActivityEnvironment`: Ramp via builtin `mock-ramper` records the call; Validate with stub FAIL returns FAIL verdict; Validate with unknown plugin returns `*temporal.ApplicationError` with `Type() == "PluginMissing"` and `NonRetryable()`; Notify via `log-notifier` returns nil.

- [ ] **Step 2: Run** `go test ./internal/activities/...` — Expected: FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(activities): ramp, validate, notify activities`

---

### Task 9: TargetRollout workflow

**Files:**
- Create: `internal/workflows/signals.go`, `internal/workflows/status.go`, `internal/workflows/target.go`
- Test: `internal/workflows/target_test.go`

**Interfaces:**
- Consumes: `activities.*Input`, `activities.ErrTypePluginMissing`, `decision.Decide`, plan types.
- Produces:
  - `signals.go`: consts `SignalApprove="approve"`, `SignalReject="reject"`, `SignalResume="resume"`, `SignalRetry="retry"`, `SignalAbort="abort"`, `SignalStop="stop-after-step"` (stage → target), `SignalProgress="target-progress"` (target → global); `type OperatorSignal struct { Validation string }`.
  - `status.go`: target state consts listed in Global Constraints (`StatePending="pending"`, …); terminal status consts `Succeeded="SUCCEEDED"`, `Held="HELD"`, `Aborted="ABORTED"`; `type TargetStatus struct { ID, Stage, Wave, Customer, Region, State string; Step, Percent int; Verdicts map[string]plan.Verdict; AwaitingApproval []string; Message string; At time.Time }`; `func Worst(a, b string) string` (ABORTED > HELD > SUCCEEDED).
  - `target.go`: `type TargetInput struct { GlobalID, Agent, Version string; Target plan.TargetPlan; Plugins map[string]plan.PluginRef }`; `type TargetResult struct { Status string; Message string }`; `func TargetRollout(ctx workflow.Context, in TargetInput) (TargetResult, error)`.

Behavior beyond spec §6:
- Progress: on every state change, `workflow.SignalExternalWorkflow(ctx, in.GlobalID, "", SignalProgress, TargetStatus{...})` (fire-and-forget; ignore error).
- Operator signal handling: a single loop drains each signal channel with a selector. A signal that is not applicable in the current state is logged and dropped immediately — never left buffered (Review Focus #2). `approve`/`reject` apply only to a validation currently in `AwaitingApproval`; `resume`/`retry`/`abort` only while `paused`. Empty `OperatorSignal.Validation` on approve/reject applies to all awaiting validations.
- Attempt counter per validation per step, starting at 1, incremented on every re-run (inconclusive retry, `retry` action, pause→retry).
- Validate activity error: if `ApplicationError.Type() == "PluginMissing"` → step fails (go to failure policy) with that message; any other error → verdict INCONCLUSIVE with `Summary` = error text.
- Ramp activity error → failure policy; a `retry` failure action after a ramp failure re-attempts the ramp (since validation never ran).
- `NotifyWarnings` → `Notify` activity to `Decision.WarningNotifier` with event kind `warning`.
- `SignalStop` received → after the current step's decision is `Continue`, end `HELD` with message `stopped: sibling target failed`.
- Cancellation (global abort): catch `ctx.Done()` in every wait (bake timer, awaiting approval, paused, backoff); use `workflow.NewDisconnectedContext` to run the target's `notify` failure actions with event kind `aborted`, then return `TargetResult{Status: Aborted}` with nil error.
- Event kinds sent to notifiers: `failed`, `paused`, `held`, `aborted`, `warning`.

- [ ] **Step 1: Write failing tests** using `testsuite.WorkflowTestSuite` with registered activity mocks (`env.OnActivity("Validate", ...)`), a target with steps `[50,100]`, bake 5m, validations `health` (critical) and `signoff` (manual-approval, final_step). Use `env.RegisterDelayedCallback` to send signals at specific times; capture progress signals with `env.OnSignalExternalWorkflow`.
  - `TestTargetHappyPath`: Validate PASS; approve `signoff` after step 2 → SUCCEEDED; Ramp called with 50 then 100; `signoff` validated only once (final step).
  - `TestTargetFailPauseResume`: health FAIL on step 1, `on_failure: [notify log-notifier, pause 24h hold]`; Notify called with kind `failed` then `paused`; send `resume` at +1h → continues to step 2 → SUCCEEDED.
  - `TestTargetPauseRetry`: FAIL then PASS on Attempt 2; send `retry` → continues; assert second Validate input has `Attempt == 2`.
  - `TestTargetPauseAbortSignal`: `abort` while paused → ABORTED.
  - `TestTargetPauseTimeoutHold`: no signal → after 24h → HELD.
  - `TestTargetInconclusiveRetryThenFail`: Validate always INCONCLUSIVE, retry 3 backoff 1m, `on_failure: [hold]` → 4 Validate calls → HELD.
  - `TestTargetInconclusiveRecovers`: INCONCLUSIVE then PASS → SUCCEEDED.
  - `TestTargetValidateErrorIsInconclusive`: Validate returns plain error → treated as INCONCLUSIVE (retried per rule).
  - `TestTargetPluginMissingFails`: Validate returns PluginMissing app error → failure policy runs immediately (no inconclusive retries).
  - `TestTargetRejectFails`: `reject signoff` → failure policy.
  - `TestTargetWarningNotifies`: warning FAIL with `warning: notify` → Notify kind `warning`, target SUCCEEDED.
  - `TestTargetStopAfterStep`: send `stop-after-step` during step 1 bake → HELD after step 1 completes; Ramp never called with 100.
  - `TestTargetCancelled`: `env.CancelWorkflow()` during bake → ABORTED, Notify kind `aborted` called.
  - `TestEarlyApproveIsIgnored`: send `approve` during step 1 bake (before signoff is awaited) → at final step the workflow still waits; a delayed callback at +48h asserts the last progress signal is `awaiting_approval`, then calls `env.CancelWorkflow()`.
  - `TestDuplicateResumeIgnored`: send `resume` twice at the first pause; health fails again at step 2 → the target pauses again (the second resume did not pre-resume it).
  - `TestProgressSignals`: the sequence of states reported includes `ramping, baking, validating, awaiting_approval, succeeded`.

- [ ] **Step 2: Run** `go test ./internal/workflows/ -run 'TestTarget|TestEarly|TestDuplicate|TestProgress'` — Expected: FAIL.

- [ ] **Step 3: Implement** `TargetRollout` as a straight-line function over steps, with helpers `validateStep(...) decision.Result`, `runFailurePolicy(...) (outcome string)` where outcome ∈ `continue, revalidate, reramp, held, aborted`, and `awaitSignals(...)`. Activity options per Global Constraints, timeouts from `in.Plugins[name].Timeout`.

- [ ] **Step 4: Run** `go test ./internal/workflows/...` — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(workflows): TargetRollout state machine`

---

### Task 10: StageRollout and GlobalRollout

**Files:**
- Create: `internal/workflows/stage.go`, `internal/workflows/global.go`, `internal/workflows/register.go`
- Modify: `internal/workflows/status.go` (add rollout status)
- Test: `internal/workflows/stage_test.go`, `internal/workflows/global_test.go`

**Interfaces:**
- Consumes: `TargetRollout`, `plan.StageID`, `plan.TargetWorkflowID`.
- Produces:
  - `type RolloutStatus struct { ID, Agent, Version, State string; WaitingForStage string; Order []string; Targets map[string]TargetStatus; Message string }` — `State` ∈ `running, waiting_for_stage, SUCCEEDED, HELD, ABORTED`.
  - `const QueryStatus = "status"`.
  - `type StageInput struct { GlobalID, Agent, Version string; Stage plan.StagePlan; Plugins map[string]plan.PluginRef }`; `type StageResult struct { Status, Message string }`; `func StageRollout(ctx workflow.Context, in StageInput) (StageResult, error)`.
  - `func GlobalRollout(ctx workflow.Context, p plan.Plan) (RolloutStatus, error)`.
  - `func Register(w worker.Worker, a *activities.Activities)` registers all three workflows and the activities under their names.

Behavior:
- Stage: waves in order; within a wave start customers with `workflow.Go`, bounded by a buffered channel of size `MaxParallel`; per customer, targets sequentially as child workflows with ID `plan.TargetWorkflowID`. On the first non-SUCCEEDED target result: set `stopping`, send `SignalStop` to all running target children, start no further targets, wait for running children, return `Worst` of all results.
- Global: initialize `Targets` with every target in `pending`, `Order` = plan order; register `QueryStatus` handler; `SignalProgress` handler upserts `Targets[id]`. Stages sequentially as children with ID `plan.StageID` and `WorkflowIDReusePolicy: ALLOW_DUPLICATE`. If `GetChildWorkflowExecution().Get` returns `*temporal.ChildWorkflowExecutionAlreadyStartedError`: set `State=waiting_for_stage`, `WaitingForStage=<stage>`, sleep 30s, retry. Stop at the first non-SUCCEEDED stage. `SignalAbort` on global → cancel the stage context; children receive cancellation (`ParentClosePolicy` default terminate is fine because cancellation is requested first and awaited).
- Global returns the final `RolloutStatus`.

- [ ] **Step 1: Write failing tests** (activity mocks as in Task 9; real child workflows registered in the test env):
  - `TestStageSequentialRegions`: one customer, regions `[a,b]` → target `b` starts only after `a` succeeds (assert Ramp call order).
  - `TestStageWaveParallelism`: wave with 3 customers, `MaxParallel: 2`, bake 10m → at most 2 targets concurrently in `baking` (track via a mutex-guarded counter in the Ramp mock + timestamps).
  - `TestStageWavesSequential`: wave 2's first Ramp happens after all wave-1 targets succeed.
  - `TestStageTargetHeldStopsSiblings`: 2 parallel customers; customer A's target fails with `on_failure: [hold]`; customer B (steps `[25,100]`) receives stop and ends HELD without ramping to 100; stage result HELD; no wave-2 targets start.
  - `TestGlobalHappyPath`: compile `compiler/testdata/full.yaml` (with `signoff` replaced by `health` to avoid approvals) → `SUCCEEDED`; query `status` → all 8 targets `succeeded`.
  - `TestGlobalStopsAtHeldStage`: pre-prod held → prod targets remain `pending`, global `HELD`.
  - `TestGlobalAbortWhilePaused`: a target paused (24h timeout) → send `abort` to global at +1h → global `ABORTED` within the same simulated hour (assert `env` clock < 2h at completion).
  - `TestGlobalStatusQueryMidRun`: query during a bake shows that target `baking`, others `pending`.

- [ ] **Step 2: Run** `go test ./internal/workflows/...` — Expected: FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test ./internal/workflows/...` — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(workflows): stage and global rollout orchestration`

---

### Task 11: HTTP API server

**Files:**
- Create: `internal/api/server.go`, `internal/api/handlers.go`
- Test: `internal/api/handlers_test.go`

**Interfaces:**
- Consumes: `compiler.Compile`, `registry.Registry`, `workflows.QueryStatus`, signal consts, `plan.GlobalID`, Temporal `client.Client` (tests use `go.temporal.io/sdk/mocks.Client`).
- Produces:
  - `func NewServer(c client.Client, reg *registry.Registry, taskQueue string) http.Handler`.
  - Request/response types: `PlanRequest{Policy string; Version string}` (used by both plan and start); `StartResponse{ID string}`; `SignalRequest{Signal string; Validation string}`; error body `{"error": "..."}`; diagnostics body `{"errors": [spec.Diagnostic]}`.
  - Routes (Go 1.22 `ServeMux`): `POST /v1/plans`, `POST /v1/rollouts`, `GET /v1/rollouts/{id}`, `POST /v1/rollouts/{id}/targets/{tid}/signal`, `POST /v1/rollouts/{id}/abort`. `{id}` and `{tid}` are single path segments; clients send them `url.PathEscape`d (slashes as `%2F`); handlers read `r.PathValue`.
  - Task queue constant `rollout` (in `server.go`, `const TaskQueue = "rollout"`).

Behavior:
- Start: `ExecuteWorkflow` with `ID = plan.ID`, `WorkflowIDReusePolicy: ALLOW_DUPLICATE`, `WorkflowIDConflictPolicy: FAIL`, `WorkflowExecutionErrorWhenAlreadyStarted: true`; `*serviceerror.WorkflowExecutionAlreadyStarted` → 409 `{"error": "rollout ro/<agent> already running"}`.
- Signal: query status first; target unknown → 404; applicability: approve/reject require `awaiting_approval` (and, if `Validation` set, that it is in `AwaitingApproval`); resume/retry/abort require `paused`; else 409. Signal sent to `plan.TargetWorkflowID`-format ID `id + "/" + tid`. Unknown signal name → 400.
- Status: query → 200 JSON `RolloutStatus`; `serviceerror.NotFound` → 404.

- [ ] **Step 1: Write failing tests** (mock client):
  - `TestPlanReturnsPlan`: valid policy → 200 with 8 targets for `full.yaml`.
  - `TestPlanDiagnostics`: invalid policy → 422 with `errors[0].line > 0`.
  - `TestStartConflict`: mock `ExecuteWorkflow` returns `serviceerror.NewWorkflowExecutionAlreadyStarted` → 409.
  - `TestStartAfterCompletedRollout`: assert the `StartWorkflowOptions` passed has `ID == "ro/support-agent"` and `WorkflowIDReusePolicy == ALLOW_DUPLICATE` (Review Focus #1; behavior verified against a real server in Task 13).
  - `TestSignalRouteWithSlashIDs`: `POST /v1/rollouts/ro%2Fsupport-agent/targets/prod%2Fcanary%2Facme%2Fus-east-1/signal` with status showing that target `paused` and `{signal: resume}` → 200 and `SignalWorkflow` called with ID `ro/support-agent/prod/canary/acme/us-east-1`, signal `resume`.
  - `TestSignalNotApplicable`: target `baking`, `resume` → 409; unknown target → 404; signal `dance` → 400.
  - `TestStatusNotFound`: query returns `serviceerror.NewNotFound` → 404.
  - `TestAbort`: signals `abort` to the global ID.

- [ ] **Step 2: Run** `go test ./internal/api/...` — Expected: FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** — Expected: PASS.

- [ ] **Step 5: Commit** — `feat(api): HTTP API for plan, start, status, signals`

---

### Task 12: rolloutctl CLI

**Files:**
- Create: `internal/cli/client.go`, `internal/cli/render.go`, `internal/cli/commands.go`, `cmd/rolloutctl/main.go`
- Test: `internal/cli/client_test.go`, `internal/cli/render_test.go`, `internal/cli/testdata/status.golden`

**Interfaces:**
- Consumes: API types from Task 11, `workflows.RolloutStatus`, `plan.Plan`.
- Produces:
  - `type Client struct { BaseURL string; HTTP *http.Client }` with `Plan(ctx, policy []byte, version string) (*plan.Plan, []spec.Diagnostic, error)`, `Start(...) (string, []spec.Diagnostic, error)`, `Status(ctx, id string) (*workflows.RolloutStatus, error)`, `Signal(ctx, id, tid, signal, validation string) error`, `Abort(ctx, id string) error`.
  - `func RenderPlan(w io.Writer, p *plan.Plan)`; `func RenderStatus(w io.Writer, s *workflows.RolloutStatus)` — tree grouped stage → wave → customer → region, each line `<region>  <state>  step <n>/<total> @ <pct>%  <message>`; `func RenderDiagnostics(w io.Writer, file string, ds []spec.Diagnostic)` as `<file>:<line>: <path>: <message>`.
  - `func NewRootCmd() *cobra.Command` with commands `plan`, `start` (`-f`, `--version` required), `status <id> [--watch]`, `approve|reject <id> --target <tid> [--validation <name>]`, `resume|retry <id> --target <tid>`, `abort <id> [--target <tid>]` (with `--target` → target signal, else global abort). Global flag `--server` default `http://127.0.0.1:8080`, env `ROLLOUT_SERVER`.
  - Exit codes: 0 ok, 1 server/transport error, 2 diagnostics or usage error, 3 conflict (409).

- [ ] **Step 1: Write failing tests:**
  - `TestClientEscapesIDs`: against `httptest.Server`, `Signal(ctx, "ro/a", "prod/w/c/r", "resume", "")` hits raw path `/v1/rollouts/ro%2Fa/targets/prod%2Fw%2Fc%2Fr/signal` (assert `r.URL.RawPath`).
  - `TestClientMapsErrors`: 409 → error satisfying `errors.Is(err, cli.ErrConflict)`; 422 → diagnostics returned, nil error.
  - `TestRenderStatusGolden`: fixed `RolloutStatus` → `testdata/status.golden` (with `-update`).
  - `TestRenderDiagnostics`: `policy.yaml:41: stages[2].waves[1].steps: steps must end at 100`.

- [ ] **Step 2: Run** `go test ./internal/cli/...` — Expected: FAIL.

- [ ] **Step 3: Implement.** `--watch` clears the screen with `\033[H\033[2J` and re-renders every 2s until a terminal state.

- [ ] **Step 4: Run** — Expected: PASS; `go build ./cmd/rolloutctl` succeeds.

- [ ] **Step 5: Commit** — `feat(cli): rolloutctl`

---

### Task 13: Binaries, examples, integration and e2e

**Files:**
- Create: `cmd/server/main.go`, `cmd/worker/main.go`, `examples/policy.yaml`, `examples/policy-failing.yaml`, `test/integration/integration_test.go`, `test/integration/e2e.sh`, `internal/workflows/replay_test.go`, `internal/workflows/testdata/histories/` (generated)
- Modify: `Makefile` (`build`, `integration`, `e2e`, `dev-temporal` targets), `.gitignore`

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `cmd/server`: flags `--listen` (default `127.0.0.1:8080`), `--temporal` (default `127.0.0.1:7233`), `--registry` (default `plugins.yaml`).
  - `cmd/worker`: flags `--temporal`, `--registry`; builds `plugin.NewSet(reg, remote.New wrapper)`, registers via `workflows.Register`, task queue `api.TaskQueue`.
  - `examples/policy.yaml`: spec §4 example with `csat-drift` removed and `bake: 10s` so the e2e completes in minutes. `examples/policy-failing.yaml`: same, but pre-prod `error-rate` uses `stub: {outcome: FAIL, after_attempt: 2}` and `on_failure: [{notify: log-notifier}, {pause: {timeout: 1h, on_timeout: hold}}]`.
  - Makefile: `integration` = `go test -tags integration ./test/integration/...`; `e2e` = `./test/integration/e2e.sh`; `dev-temporal` = `temporal server start-dev`.

- [ ] **Step 1: Write failing integration tests** (`//go:build integration`) using `testsuite.StartDevServer` (downloads the Temporal CLI automatically), a real worker and `api.NewServer` behind `httptest.Server`, bake shortened via policy:
  - `TestE2EHappyPath`: start `examples/policy.yaml` (bake overridden to 1s), approve `signoff` for each prod target as it reaches `awaiting_approval` (poll status), final `SUCCEEDED`.
  - `TestStartAfterCompletedRollout`: after the above completes, start again with a new version → 201.
  - `TestStartWhileRunning`: second start for the same agent while running → 409.
  - `TestPipeliningQueues`: two policies with `pipelining: true`, versions `v1` and `v2`, staging bake 20s; start v1 then v2 → v2 status `waiting_for_stage` with `WaitingForStage == "staging"` while v1 is in staging; both eventually progress (stop after staging by making later stages trivial).
  - `TestMixedModeSharesStageLock`: start with `pipelining: true` (`ro/a/v1`) then `pipelining: false` (`ro/a`) → the second shows `waiting_for_stage` (Review Focus #5).
  - `TestFailingPauseResume`: `examples/policy-failing.yaml` → pre-prod target `paused`; signal `retry` via API → attempt 2 passes → continues.

- [ ] **Step 2: Run** `make integration` — Expected: FAIL (binaries' wiring missing).

- [ ] **Step 3: Implement** `cmd/server`, `cmd/worker` and the examples; extract worker wiring into a function the integration test reuses (`func workflows.NewWorker(c client.Client, reg *registry.Registry) (worker.Worker, error)` in `register.go`).

- [ ] **Step 4: Run** `make integration` — Expected: PASS.

- [ ] **Step 5: Determinism replay test.** Add a `-record` flag to `TestE2EHappyPath` that writes each finished workflow's history (`client.GetWorkflowHistory`) as JSON into `internal/workflows/testdata/histories/`. Run `go test -tags integration ./test/integration -run TestE2EHappyPath -record` once. Write `replay_test.go`: `worker.NewWorkflowReplayer()`, register the three workflows, `ReplayWorkflowHistoryFromJSONFile` for each file. Run `go test ./internal/workflows -run TestReplay` — Expected: PASS. Commit the histories.

- [ ] **Step 6: Write `e2e.sh`** — requires `temporal` on PATH (exit with a message naming `brew install temporal` otherwise); starts `temporal server start-dev --headless`, `go run ./cmd/worker`, `go run ./cmd/server` in background with a cleanup trap; runs `rolloutctl plan -f examples/policy.yaml --version v1` (expects exit 0), `start`, polls `status` and approves `signoff` targets until `SUCCEEDED`; then starts `examples/policy-failing.yaml` with `v2`, waits for `paused`, sends `retry`, polls to completion. Run `make e2e` — Expected: script prints `E2E OK` and exits 0.

- [ ] **Step 7: Full check** — `go vet ./... && go test ./... && make integration` — Expected: all PASS.

- [ ] **Step 8: Commit** — `feat: server and worker binaries, examples, integration and e2e tests`
