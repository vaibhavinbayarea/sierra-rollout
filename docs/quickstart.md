# Quick start

Ten minutes from clone to a finished rollout: start the stack, write a
policy, roll it out, watch it on the dashboard, handle a failure, and put a
dark canary in front of prod.

Everything here runs locally with built-in stub plugins. Nothing real is
deployed.

## 1. Prerequisites

- Go 1.24
- The Temporal CLI: `brew install temporal`

## 2. Start the stack

```sh
make dev
```

This builds the binaries and starts a Temporal dev server, the rollout
worker and the API. When it's ready it prints:

```
  Rollout stack is up (logs in .dev-logs/):
    API          http://127.0.0.1:8080
    Dashboard    http://127.0.0.1:8080/ui
    Temporal UI  http://localhost:8233
```

Open the **dashboard** and keep it beside your terminal. Leave `make dev`
running, and use a second terminal for the rest of this guide.

## 3. Your first policy

A policy says how your agent rolls out. Here is
[`examples/quickstart.yaml`](../examples/quickstart.yaml):

```yaml
apiVersion: rollout/v1
agent: my-agent
ramper: { plugin: mock-ramper }        # moves traffic; a stub here

defaults:
  steps: [25, 100]                     # ramp to 25%, then 100%
  bake: 5s                             # wait after each ramp before validating
  on_failure:                          # what to do when a validation fails
    - { notify: log-notifier }
    - { pause: { timeout: 1h, on_timeout: hold } }

validations:                           # checks you can reuse by name
  health:     { plugin: http-health, params: { path: /healthz } }
  error-rate: { plugin: prometheus, params: { query: 'rate(errors[5m])', max: 0.01 } }
  signoff:    { plugin: manual-approval, when: final_step, params: { message: "ship to acme?" } }

stages:                                # run in order
  - name: staging
    regions: [us-east-1]
    steps: [100]
    validate: [health]
  - name: prod
    validate: [health, error-rate]
    waves:                             # customers, one wave after another
      - { name: canary, customers: [acme], regions: [us-east-1], validate: [health, error-rate, signoff] }
      - { name: broad, customers: [globex], regions: [us-east-1, eu-west-1] }
```

Each **target** (stage × customer × region) runs the same loop: ramp to
the next step, bake, run the validations, then continue, retry or apply
`on_failure`.

## 4. Plan, then start

`plan` is a dry run. It shows exactly what will happen, or points at the
line that's wrong:

```sh
bin/rolloutctl plan -f examples/quickstart.yaml --version v1
```

Start it:

```sh
bin/rolloutctl start -f examples/quickstart.yaml --version v1
# ro/my-agent
```

On the dashboard, staging fills to 100%, then prod's canary ramps 25% →
100%. Each card shows its state: `ramping`, `baking`, `validating`.

## 5. Approve the canary

The canary waits at `awaiting approval` for the `signoff` validation.
Click **Approve** on the dashboard, or:

```sh
bin/rolloutctl approve ro/my-agent --target prod/canary/acme/us-east-1
```

The broad wave (globex) then rolls out region by region, and the rollout
ends `SUCCEEDED`. From the terminal:

```sh
bin/rolloutctl status ro/my-agent
```

## 6. See a failure

Make `error-rate` fail its first check in each target by adding a stub to
its params, then roll out a new version:

```yaml
  error-rate: { plugin: prometheus, params: { query: 'rate(errors[5m])', max: 0.01, stub: { outcome: FAIL, after_attempt: 2 } } }
```

```sh
bin/rolloutctl start -f examples/quickstart.yaml --version v2
```

When prod's canary validates, it turns red and shows `paused`: the
failure policy notified (see `.dev-logs/worker.log`) and paused at 25%.
Nothing else moves. Click **Retry** (or `bin/rolloutctl retry ro/my-agent
--target prod/canary/acme/us-east-1`): the second attempt passes and the
target moves on. The stub fails the first check of *every* prod step, so
it pauses again at the next one. Retry a couple more times to watch it
recover, then click **Abort rollout**. Abort stops everything and leaves
traffic where it is.

Remove the `stub` again when you're done.

## 7. Add a dark canary

A dark canary sends a mirrored copy of real traffic to the new version and
throws its responses away, so you can validate it before any customer sees
it. Add a `dark` block to the prod stage:

```yaml
  - name: prod
    validate: [health, error-rate]
    dark: { mirror: [50, 100], validate: [error-rate] }
    waves: ...
```

```sh
bin/rolloutctl start -f examples/quickstart.yaml --version v3
```

A 🌘 `prod-dark` column appears before prod. Every prod target mirrors
50% → 100% at the same time (striped purple bars), then drops back to 0%
as mirroring is torn down. Only then do the live prod waves start. If the
dark phase fails, prod is never touched and its targets end `skipped`.

## 8. Stop

Press Ctrl-C in the `make dev` terminal. The dev server keeps nothing, so
the next `make dev` starts clean.

## Next steps

- `demo/demo.sh` gives a guided tour with several teams rolling out at once.
- The [developer guide](developer-guide.md#policy-reference) has the full
  policy reference and the HTTP API.
- To plug in your own ramper, validator or notifier, see the
  [plugin guide](plugins.md).
- How it works: [architecture](architecture.md).
