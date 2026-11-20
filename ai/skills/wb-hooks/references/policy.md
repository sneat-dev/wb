# Hook policy

Policy layers are conservative WB defaults, the user-owned `git_hooks:` section
of `~/.config/wb/wb.yaml`, then repository `.wb/hooks.yaml`.

Use built-in detection when conventional checks are wanted:

```yaml
git_hooks:
  version: 1
  profiles:
    auto: true
```

Worktree protection is installed by default for every repository:

```yaml
git_hooks:
  version: 1
  profiles: {}
```

The worktree profile guards `post-checkout`, `pre-commit`, and `pre-push`.
Git has no pre-checkout hook: post-checkout warns loudly after an unmanaged
checkout has happened, preserves it for inspection, and exits successfully.
`pre-commit` and `pre-push` are the blocking enforcement points. Preserve the
state; run `wb worktree rescue <path>` to move it onto a branch first.

Only when WB cannot own checkout policy, make the exception explicit and
auditable, then repair hooks:

```yaml
version: 1
profiles:
  exclude: [worktree]
```

Custom profiles should detect a stable repository marker and contribute the
smallest required hook:

```yaml
version: 1
profiles:
  definitions:
    product:
      order: 200
      detect:
        any_files:
          - product.yaml
      hooks:
        pre-push:
          template: templates/product/pre-push.sh
```

Resolve relative templates from the YAML file declaring them. Keep expensive
E2E work in `pre-push`, not `pre-commit`. Prefer one orchestrating command that
reuses results over multiple overlapping blocks.

The built-in Go and Node pre-push blocks are tiered by what is actually being
pushed, via `wb hooks push-tier` (the base/worktree/custom/metrics Tier 0
layer above always runs first, regardless of tier):

- **Tier 0** (always, sub-second): base `git diff --check`, worktree
  admission, canonical-clone guard, custom policy, metrics. Never skippable.
- **Tier 1** (static checks via `wb hooks lint`): runs on any push that is
  not a pure remote-ref deletion (40- or 64-zero-SHA) and not confined to the
  `refs/wb/checkpoints/*` checkpoint namespace. Go: `go vet ./...` and
  `golangci-lint run ./...` in every module the pushed commits touch, nested
  `go.mod` included. Node: lint only, only when the push changes JS/TS —
  `nx affected -t lint` against the push base in an Nx workspace, otherwise
  each touched package's own `lint` script. Every check runs through
  `wb run --`.
- **Tier 2** marks a *publication* push — the default branch, a tag, or a branch
  with an open pull request — but runs the same static checks as Tier 1. Tests,
  builds, coverage, and race belong to the landing receipt and CI. An
  unresolved PR status (no network, or the bounded `gh` lookup timed out or
  missed) degrades to Tier 1; CI remains the publication gate.

golangci-lint is the version the repository's CI pins: a
`golangci/golangci-lint-action` `version` input in `.github/workflows/*`, or in
a reusable workflow it calls (for example `strongo/cicd/...@<sha>`), read from
that repository's local clone at the pinned ref without fetching;
`v2.15.0` otherwise. It is built once per machine and Go toolchain into
`<user cache>/wb/golangci-lint/<go version>/<version>/` by the first pre-push
that needs it (seconds with a warm module cache, about a minute cold). Only a
finding blocks. A linter that cannot be built (offline), cannot read the
toolchain's export data (a pin older than the local Go), or crashes is
reported and left to CI; so are missing `node_modules`.

The Go pre-commit block also runs that linter over the staged packages only,
reporting issues on new or changed lines (`--new-from-rev=HEAD`). It never
builds the linter: until the first pre-push has, it says so and skips.
Pre-push still lints whole modules, because a changed-line lint can miss
cross-file issues. The Node pre-commit block already runs ESLint on the staged
JS/TS files with the repository's own config.

Every invocation prints one line naming the tier and the reason. There is no
Git-hook-bypass escape hatch: Tier 0 is mandatory on every push, including a
`refs/wb/checkpoints/*` checkpoint push (see `wb worktree log checkpoint` in
the wb-worktrees skill), which is Tier 0 only by classification, not by
skipping the hook. General secure-hook cache and durable metrics authority is
tracked in WB issue #61; do not treat this tiering as that broader fix.

After editing policy:

```sh
wb hooks check .
wb hooks repair .
wb hooks check .
```
