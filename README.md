# sierra-rollout

A deployment service for agents and services. Owners describe their rollout
in a YAML policy: stages, regions, customer waves, ramp percentages,
validations and what to do on failure. The service compiles the policy
into an immutable plan and runs it as Temporal workflows. Ramping,
validation and notification are plugins, so other teams can provide their
own.

```
policy.yaml ──rolloutctl──▶ API ──compile──▶ plan ──▶ Temporal: GlobalRollout
                                                        └ StageRollout (staging → pre-prod → prod)
                                                            └ TargetRollout per stage/wave/customer/region
                                                                ramp % → bake → validate → decide
```

## Getting started

New here? The [quick start](docs/quickstart.md) takes you from clone to a
finished rollout, with a failure and a dark canary, in about ten minutes.

Requirements: Go 1.24 and the Temporal CLI (`brew install temporal`).

```sh
make build                       # bin/server, bin/worker, bin/rolloutctl
temporal server start-dev        # terminal 1 (UI on http://localhost:8233)
bin/worker                       # terminal 2: runs workflows and plugins
bin/server                       # terminal 3: HTTP API on 127.0.0.1:8080
```

Then, in another terminal:

```sh
bin/rolloutctl plan  -f examples/policy.yaml --version v1   # dry run: print the resolved plan
bin/rolloutctl start -f examples/policy.yaml --version v1   # prints the rollout ID: ro/support-agent
bin/rolloutctl status ro/support-agent --watch
```

Prod targets in the example wait for a manual sign-off. Approve them as
they reach `awaiting_approval`:

```sh
bin/rolloutctl approve ro/support-agent --target prod/canary/acme/us-east-1
```

To see failure handling, start `examples/policy-failing.yaml`. A pre-prod
validation fails, the target pauses, and you can `retry`, `resume` or
`abort` it:

```sh
bin/rolloutctl start -f examples/policy-failing.yaml --version v2
bin/rolloutctl retry ro/support-agent --target pre-prod/default/_/us-east-1
```

To watch rollouts move stage by stage, open the live dashboard at
<http://127.0.0.1:8080/ui>. It shows each rollout as a pipeline with ramp
bars, highlights state changes, and has Approve / Retry / Abort buttons.

For a guided tour of multi-team rollouts, a prod dark canary, failures and
guardrails, run `demo/demo.sh` (`DEMO_AUTO=1` skips the pauses). It opens
the dashboard for you.

## rolloutctl

| Command | What it does |
|---|---|
| `plan -f FILE --version V` | Compile and print the plan, or `file:line` errors |
| `start -f FILE --version V` | Compile and start the rollout; prints its ID |
| `status ID [--watch]` | Show the rollout tree; `--watch` refreshes every 2s |
| `approve\|reject ID --target T [--validation N]` | Answer a manual approval |
| `resume\|retry ID --target T` | Act on a paused target |
| `abort ID [--target T]` | Abort the whole rollout, or one paused target |

`--server` (or `ROLLOUT_SERVER`) sets the API address. Exit codes: `0` ok,
`1` server or transport error, `2` policy errors or bad usage, `3` conflict
(for example, the agent already has a rollout running).

## A policy at a glance

```yaml
apiVersion: rollout/v1
agent: support-agent
ramper: { plugin: mock-ramper }
defaults:
  steps: [10, 25, 50, 100]          # ramp percentages
  bake: 5m                          # wait after each ramp before validating
  on_failure: [{ notify: log-notifier }, { pause: { timeout: 24h, on_timeout: hold } }]
validations:
  health:  { plugin: http-health, params: { path: /healthz } }
  signoff: { plugin: manual-approval, when: final_step }
stages:
  - { name: staging,  regions: [us-east-1], steps: [100], validate: [health] }
  - { name: pre-prod, regions: [us-east-1, eu-west-1], validate: [health] }
  - name: prod
    validate: [health, signoff]
    waves:
      - { name: canary, customers: [acme], regions: [us-east-1] }
      - { name: broad, customers: [globex, initech], max_parallel: 2, regions: [us-east-1, eu-west-1] }
```

To put a **dark canary** in front of an environment, add a `dark:` block to
its stage. Before any live prod traffic, every prod target first receives
mirrored traffic (responses discarded), which is validated and then torn
down:

```yaml
  - name: prod
    dark: { mirror: [50, 100], validate: [error-rate] }
```

The full schema is in the [developer guide](docs/developer-guide.md#policy-reference).

## Documentation

- [Quick start](docs/quickstart.md): first rollout in ten minutes
- [Architecture](docs/architecture.md): components, workflow hierarchy, state machine, diagrams
- [Developer guide](docs/developer-guide.md): building, testing, code layout, policy reference, API
- [Plugin guide](docs/plugins.md): writing a ramper, validator or notifier, and registering it
- [Contributing](CONTRIBUTING.md): how to propose changes
- [Design spec](docs/superpowers/specs/2026-10-21-agent-rollout-service-design.md)
