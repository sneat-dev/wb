# Set an exact dependency

Use when the desired version is already known and only existing references
should change.

Preview one repository:

```sh
wb deps set go <module>@<version> . --dry-run
wb deps set github-actions <owner/action>@<version> . --dry-run
```

Preview a selected fleet:

```sh
wb deps set go <module>@<version> --fleet \
  --match '<owner>/*' --dry-run
```

Then run the same scope without `--dry-run`. Without publication flags, WB
leaves verified changes in isolated operation worktrees for review.

For a provider-first rollout:

```sh
wb deps set go <module>@<version> --fleet \
  --dependency-order --merge
```

Use `--layer N` or `N-M` only when deliberately staging layers. Use
`--propagate` to delegate the exact Go release event to bump waves.

Local verification is on by default. Tune it with `--checks`,
`--timeout`, and `--retry`. For a CI-authoritative fleet update,
`--validation=fast --merge --parallel <N>` retains repository push hooks,
opens independent PRs concurrently, and requires passing exact PR-head GitHub
checks before merge. The legacy `--no-verify` flag remains a separate explicit
escape hatch, not the fast mode.
A semantic downgrade requires `--allow-downgrade`.

For private modules, repeat `--go-private <pattern>`. Configure credentials
outside WB; WB scopes Go privacy environment variables but does not store
credentials.
