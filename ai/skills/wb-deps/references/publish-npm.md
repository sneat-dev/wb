# Publish and propagate npm releases

`wb deps publish npm` is the one safe campaign command for an approved npm
release. It dispatches only the named repository-owned GitHub Actions workflow;
WB never accepts an npm token and never runs `npm publish`. The command is a
plan by default, and `--apply` is the explicit approval to dispatch workflows
and read registry evidence.

Downstream propagation runs in the shared dependency-wave engine, where
leaving `--parallel` at its default widens read-only pools (per-wave graph
discovery fetches and registry release observations) to a floor of 4 workers;
an explicit `--parallel` bounds every pool in both directions.

Plan mode validates the tuples and runs the existing downstream dependency-wave
engine in dry-run mode. It retains real fleet findings and the engine's durable
report, but never dispatches a release workflow, queries the npm registry, or
changes a downstream dependency file. Its wave report is isolated below
`<report-dir>/plan`, so a later plan cannot overwrite an apply/resume receipt.
For a Sneat Co. campaign, `--match 'sneat-co/*'` bounds downstream consumer
discovery to the intended organization rather than scanning unrelated fleet
repositories.

The plan mode is invoked as `wb deps publish npm --fleet --dry-run` after the
explicit release tuple flags are supplied.

Every repeatable `--repo`, `--workflow`, `--package`, and `--version` value is
one aligned tuple. For one package:

```sh
wb deps publish npm \
  --repo sneat-co/assetus \
  --workflow release-frontend.yml \
  --package @sneat/extension-assetus \
  --version 0.1.0 \
  --fleet --match 'sneat-co/*' --dry-run
```

For a provider whose one workflow publishes two packages, keep both tuples in
one campaign and scope each workflow input by its zero-based tuple index:

```sh
wb deps publish npm \
  --repo sneat-co/assetus \
  --repo sneat-co/eventius --repo sneat-co/eventius \
  --workflow release-frontend.yml \
  --workflow release-frontend.yml --workflow release-frontend.yml \
  --package @sneat/extension-assetus \
  --package @sneat/extension-eventius --package @sneat/extension-eventius-ui \
  --version 0.1.0 --version 0.0.1 --version 0.0.1 \
  --workflow-input 1:package=runtime \
  --workflow-input 2:package=ui \
  --fleet --match 'sneat-co/*' --apply
```

The apply report records the provider ref head, dispatch timestamp, exact
workflow run ID/URL/head, conclusion, and exact npm registry version before
handing all confirmed events to the recalculated `wb deps bump npm` engine.
Downstream changes remain a separate approval: add `--merge` only when the
consumer waves should be committed, pushed, reviewed, and merged.

The compact tuple-input form is
`wb deps publish npm --workflow-input 1:package=runtime --workflow-input 2:package=ui`.

After a workflow or registry timeout/failure, keep the report directory and
rerun the same tuples with `--resume --apply`. Receipted workflows are located
and observed again; WB does not dispatch a tuple that already has a dispatch
timestamp. Use `--format json` for machine consumers.
