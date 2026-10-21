# Dark Canary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-environment dark canary gate (mirrored traffic, validated, torn down) ahead of a stage's live waves.

**Architecture:** The compiler expands a stage's `dark:` block into a `<stage>-dark` StagePlan of `Mode: "dark"` targets that run in parallel; `TargetRollout` ramps those in shadow mode and always tears mirroring down; rampers receive a `Mode` in the plugin contract.

**Tech Stack:** unchanged (Go 1.24, Temporal SDK v1.40.0, gRPC/buf).

**Spec:** `docs/superpowers/specs/2026-10-21-dark-canary-design.md`

## Global Constraints

- Backward compatible: live targets issue identical workflow commands; existing replay histories must still pass; proto changes are additive with UNSPECIFIED = live.
- Mode strings: plan `TargetPlan.Mode` is `""` or `"dark"`; ramper mode is `"live"` or `"shadow"`; registry `modes` default `[live]`.
- Dark stage name `<stage>-dark`; dark target ID `<stage>-dark/<wave>/<customer|_>/<region>`.
- Teardown failure never changes a target's result.

## Review Focus

1. **Abort during a dark bake** must still tear mirroring down (disconnected context). Test: `TestDarkTargetTearsDownOnCancel` (Task 3).
2. **Stopped dark sibling** (another dark target held) must tear down too. Test: `TestDarkTargetTearsDownWhenStopped` (Task 3).
3. **Existing remote rampers** that ignore `mode` keep live behaviour: UNSPECIFIED maps to live. Test: `TestRampModeMapping` (Task 1).
4. **Stage with waves of different regions**: dark expansion must use each wave's resolved regions. Test: `TestDarkExpansionUsesWaveRegions` (Task 2).
5. **Dark on a stage whose ramper lacks shadow** must fail at compile time, not mid-rollout. Test: case `ramper without shadow` in `TestDarkChecks` (Task 2).

---

### Task 1: Ramper mode in the plugin contract

**Files:** `internal/registry/registry.go` (+`Modes []string`, yaml `modes`, builtin mock-ramper `[live, shadow]`), `internal/plan/plan.go` (`PluginRef.Modes`, `TargetPlan.Mode`), `internal/plugin/plugin.go` (`RampRequest.Mode`, `Current(ctx, t, mode)`), `internal/plugin/builtin/mockramper.go`, `proto/plugin/v1/plugin.proto` (+`RampMode`, `mode` fields, `DescribeResponse.modes`) and regenerated code, `internal/plugin/remote/remote.go`, `pkg/plugincontract/contract.go`, tests alongside.

- [ ] Failing tests: `TestLoadModes` (default `[live]`, mock-ramper `[live shadow]`, unknown mode rejected); `TestMockRamperSeparatesModes`; `TestRampModeMapping` (shadow→SHADOW, live/""→LIVE; server receiving UNSPECIFIED treated as live by fake); contract kit shadow check runs only when Describe lists shadow.
- [ ] Implement; `make proto`; `go test ./internal/... ./pkg/...` green.
- [ ] Commit `feat(plugin): ramper modes (live/shadow)`.

### Task 2: Schema, expansion and checks

**Files:** `internal/spec/types.go` (`Stage.Dark *Dark{Mirror []int; Bake *Duration; Validate []string; Decision *Decision; OnFailure []FailureAction; Ramper *PluginCall}`), `internal/compiler/compile.go` (expansion; `PluginRef.Modes`), `internal/compiler/checks.go`, `internal/compiler/testdata/dark.yaml` + golden.

- [ ] Failing tests: `TestCompileGolden` gains `dark`; `TestDarkExpansion` (stage order `[staging, prod-dark, prod]`, one wave `dark`, MaxParallel = target count, IDs, Mode, Steps=mirror, bake/ramper/on_failure inherited at stage level); `TestDarkExpansionUsesWaveRegions`; `TestDarkChecks` table for every §3 diagnostic.
- [ ] Implement; all compiler tests and existing goldens green.
- [ ] Commit `feat(compiler): dark canary stage expansion`.

### Task 3: Workflow

**Files:** `internal/activities/activities.go` (`RampInput.Mode`), `internal/workflows/target.go`, `internal/workflows/status.go` (`TargetStatus.Mode`), tests.

- [ ] Failing tests: `TestDarkTargetRampsShadowAndTearsDown` (ramps 50,100 shadow then 0 shadow; result SUCCEEDED), `TestDarkTargetTearsDownOnHold`, `TestDarkTargetTearsDownWhenStopped`, `TestDarkTargetTearsDownOnCancel`, `TestDarkTeardownFailureKeepsResult`, `TestGlobalDarkGatesProd` (held dark → prod skipped; passing dark → prod runs after); status carries `mode: dark`.
- [ ] Implement; full workflow suite and `TestReplay` green.
- [ ] Commit `feat(workflows): dark canary targets with guaranteed teardown`.

### Task 4: Visibility, demo, docs, integration

**Files:** `internal/cli/render.go`, `internal/api/ui/index.html`, `demo/policies/agent-foobar.yaml`, `demo/demo.sh` (Act 1 narration), `demo/policies_test.go` if target counts change, `docs/developer-guide.md`, `docs/plugins.md`, `docs/architecture.md`, `test/integration/integration_test.go`, recorded dark history.

- [ ] Failing tests: `TestRenderStatusGolden` includes a dark target rendered `@ 50% mirrored`; integration `TestDarkThenLive`, `TestFailingDarkSkipsProd` (record dark target history with `-record`); replay includes it.
- [ ] Implement; `go test ./...`, `make integration`, `make e2e`, demo unattended run green.
- [ ] Commit `feat: dark canary visibility, demo and docs`.
