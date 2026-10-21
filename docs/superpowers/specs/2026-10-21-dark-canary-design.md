# Dark Canary — Design

- **Date:** 2026-10-21
- **Status:** Approved in conversation; written review skipped at the owner's request
- **Extends:** `2026-10-21-agent-rollout-service-design.md`

## 1. Intent

Let an owner put a **dark canary gate** in front of an environment (typically prod): the new version receives a mirrored copy of real traffic, responses are discarded, and validations judge it before any customer sees it. Only if the whole dark phase passes do the environment's live waves start.

Decisions:
- One dark gate per environment, shadowing every target of that stage at once (not per-target dark steps).
- Declared as a `dark:` block on the stage; the compiler places it first.
- Mirroring is always torn down when the dark phase ends (pass, hold, stop, or abort).
- v1 mirroring is implemented by `mock-ramper`; real mirroring integrations are out of scope.

## 2. Policy schema

`dark` is allowed only on a stage:

```yaml
- name: prod
  dark:
    mirror: [10, 100]                 # % of live traffic mirrored; default [100]
    bake: 30m                         # default: the stage's resolved bake
    validate: [diff-rate, eval-delta] # required, ≥ 1
    decision: …                       # default: stage's resolved decision
    on_failure: …                     # default: stage's resolved on_failure
    ramper: …                         # default: stage's resolved ramper
  validate: [health, error-rate, signoff]
  waves: …
```

"Stage's resolved X" means resolution at stage level: stage > `defaults` > built-in (top-level `ramper` for the ramper). Wave overrides do not apply to the dark phase.

## 3. Compilation

- A stage with `dark` produces an extra `StagePlan` named `<stage>-dark`, inserted immediately before the stage.
- It has one wave named `dark` whose `MaxParallel` equals its number of targets; each (customer, region) of the live stage — across all its waves, using each wave's resolved regions — becomes its own `CustomerPlan` entry with one target, so every target runs concurrently.
- Dark target: `ID = <stage>-dark/<orig-wave>/<customer|_>/<region>`, `Stage = <stage>-dark`, `Wave = <orig-wave>`, `Mode = "dark"`, `Steps = mirror`, other fields from §2.
- Registry entries gain `modes: [live, shadow]` (default `[live]`); built-in `mock-ramper` declares both. Frozen into `PluginRef.Modes`.

New diagnostics:

| Condition | Path | Message contains |
|---|---|---|
| `dark.validate` empty or missing | `stages[i].dark.validate` | `needs at least one validation` |
| `<stage>-dark` equals another stage's name | `stages[i].dark` | `collides with stage` |
| invalid `mirror` | `stages[i].dark.mirror` | same messages as `steps` |
| resolved dark ramper lacks `shadow` | `stages[i].dark` | `does not support shadow` |
| undefined validation in `dark.validate` | `stages[i].dark.validate[j]` | `undefined validation` |

Plus the existing ramper/decision/on_failure/bake checks applied to the dark block's fields.

## 4. Ramper contract

- Go: `plugin.RampRequest.Mode string` — `"live"` or `"shadow"`; empty means live. `Ramper.Current` gains a mode argument: `Current(ctx, t Target, mode string)`.
- Proto (additive): `enum RampMode { RAMP_MODE_UNSPECIFIED = 0; RAMP_MODE_LIVE = 1; RAMP_MODE_SHADOW = 2; }`; field `mode` on `SetPercentRequest` and `CurrentRequest`. UNSPECIFIED is treated as live, so existing rampers keep working for live traffic. `DescribeResponse` gains `repeated string modes`.
- Contract kit: `plugincontract.Ramper` additionally checks shadow idempotency when `Describe().modes` contains `shadow`.
- `mock-ramper`: live and shadow state are tracked separately; logs include `mode=`.

## 5. Workflow

- `plan.TargetPlan.Mode` → `activities.RampInput.Mode` (`dark` → `shadow`).
- `TargetRollout` with `Mode == "dark"`: steps set the mirror percentage; bake/validate/decide/failure policy unchanged; status `percent` is the mirrored percentage.
- Teardown: whenever a dark target ends (SUCCEEDED, HELD, ABORTED — including stopped siblings), it runs `Ramp(0, shadow)` on a disconnected context before its terminal status. A failed teardown is logged and appended to the message (`mirror teardown failed: …`); the result is unchanged.
- Live targets issue exactly the same commands as before (existing histories replay unchanged).
- Gating is the existing stage order: a non-SUCCEEDED `prod-dark` stops the rollout; prod targets end `skipped`.

## 6. Visibility

- `TargetStatus.Mode` (`dark` or omitted).
- CLI status: `@ 50% mirrored` for dark targets.
- Dashboard: `<stage>-dark` is its own column; dark bars are striped purple and labelled "mirrored"; feed entries are prefixed `🌘`.

## 7. Demo and docs

- `demo/policies/agent-foobar.yaml` gets `dark: { mirror: [50, 100], bake: <short>, validate: [error-rate] }` on prod; Act 1 narrates the dark phase.
- Docs: policy reference (developer guide), ramper mode contract (plugins guide), architecture flow.

## 8. Testing

- Compiler: golden plan with `dark`; expansion IDs/parallelism/inheritance; every diagnostic in §3; registry `modes`.
- Workflow: dark target ramps in shadow mode and tears down on succeeded/held/stopped/aborted; failed teardown keeps result; `prod-dark` runs before `prod`; held dark → prod skipped.
- Plugins: remote adapter mode mapping incl. UNSPECIFIED; mock-ramper separates modes; contract kit shadow check gated on Describe.
- Replay: a recorded dark-phase target history.
- Integration: dark-then-live rollout succeeds; failing dark leaves prod untouched (`skipped`).

## 9. Out of scope

Real mirroring integrations, comparison validators, per-wave dark phases, keeping mirroring on while held.
