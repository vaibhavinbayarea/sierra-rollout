# Contributing

## What kind of change are you making?

| Change | Where to start |
|---|---|
| A new remote plugin (ramper, validator, notifier) | [docs/plugins.md](docs/plugins.md). Your code lives in your own repo; here you only change `plugins.yaml` and add a schema. |
| A new built-in plugin | [docs/plugins.md#adding-a-built-in-platform-team-only](docs/plugins.md#adding-a-built-in-platform-team-only) |
| Policy schema, compiler, workflows, API, CLI | [docs/developer-guide.md](docs/developer-guide.md) and [docs/architecture.md](docs/architecture.md) |
| A larger design change | Write a short design note under `docs/superpowers/specs/` and get it reviewed before you write code |

## Registry PRs (plugin teams)

A PR that adds or changes a plugin entry should include:

- **The `plugins.yaml` entry.** The `name` is prefixed with your team
  (`eval-team/csat`), and the entry sets `kind`, `endpoint` and a `timeout`
  that fits your service.
- **`schemas/<plugin>.json`.** It must match what your `Describe` RPC
  returns.
- **The PR description** says:
  - an owner or on-call contact
  - that `plugincontract` passes against the deployed endpoint
  - an example `validations:` (or `ramper:`) snippet
- **Confirmation that `make test` passes.** `TestExamplesCompile` loads the
  root `plugins.yaml`.

Removing or renaming a plugin breaks policies and rollouts that already
reference it. Announce it first, and wait until running rollouts that use
it have finished.

## Code changes

1. Branch from `main`.
2. Write the failing test first, then the code. Match the style of the
   surrounding package.
3. Run `make lint test`. If you touched workflows, the API or plugin
   wiring, also run `make integration`.
4. Workflow changes must follow the determinism rules in the
   [developer guide](docs/developer-guide.md#changing-workflows) and pass
   `TestReplay`. Gate changes in command order with `workflow.GetVersion`.
5. Keep commits focused, with a conventional prefix (`feat:`, `fix:`,
   `docs:`).
6. Update docs and examples in the same PR when behaviour changes.
   `examples/*.yaml` must keep compiling.

## Generated code

After editing `proto/plugin/v1/plugin.proto`, run `make proto` and commit
the generated files. Contract changes must be backward compatible, because
deployed plugins don't upgrade in lockstep: add fields, never renumber or
remove them.
