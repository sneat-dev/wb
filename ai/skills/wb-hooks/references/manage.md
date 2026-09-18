# Install, check, and repair

Start with the smallest read-only check:

```sh
wb hooks check .
```

Exit 1 means drift or findings, not invalid syntax. Inspect the listed policy,
profiles, block order, managed path, and collisions.

Use `install` for a repository that has no WB shims:

```sh
wb hooks install .
```

Use `repair` for an installed repository whose shims are stale or missing:

```sh
wb hooks repair .
```

If WB refuses an unmanaged hook or conflicting `core.hooksPath`, inspect and
preserve it. Only then, with authority to replace it:

```sh
wb hooks repair . --force
```

WB backs up unmanaged collisions. Report the backup location.

`install` and `repair` persist the absolute `--projects-root` in managed shims,
so guards use the same policy when Git invokes them later. `WB_HOME` is retired:
a shim installed by an earlier release may still pin it, but it selects nothing
and WB warns on stderr when it is set.

That warning is deliberately suppressed on the agent-hook path (`wb hooks agent
...`, the entry a WB-generated harness settings file runs around every tool
call). WB itself planted `WB_HOME` in those shims, and their caller discards
stderr, so warning there would be invisible effort on the hottest path in the
product. Every other invocation — including the git-hook fan-out and
`wb hooks run` — still warns.

Worktree admission is enabled by default at post-checkout, pre-commit, and
pre-push. Post-checkout reports an unmanaged checkout after Git has already
made it and preserves the state; pre-commit and pre-push block work until it
is recovered. Run `wb worktree rescue <path>` first to move any uncommitted
work onto a branch. Only if WB cannot own
checkout policy, record the explicit exception in `.wb/hooks.yaml`, run repair,
and leave it visible to `hooks check`:

```yaml
version: 1
profiles:
  exclude: [worktree]
```

Do not run `wb hooks install` or `repair` through `go run`: the installer is
temporary and cannot establish a durable runtime. Generated shims do not store
that installer's path. At every Git invocation they prefer an explicit
`WB_EXECUTABLE`, otherwise resolve `wb` from `PATH`, require the result to be
an absolute regular executable, and reject relative or repository-local
candidates. Use an installed WB binary or build a durable candidate first.

GUI Git clients can start hooks with a reduced `PATH`. Set `WB_EXECUTABLE` in
that client's hook environment to an absolute installed launcher; do not edit
hundreds of generated shims to freeze the current package-manager target.

Fleet operations are explicit and local-only:

```sh
wb hooks check --fleet --filter <scope> --json
wb hooks repair --fleet --filter <scope>
```

Prefer one repository first. Fleet repair is appropriate only after its policy
has been validated and the selected scope is intentional.

After changing `.wb/hooks.yaml`, run `check`, `repair`, then `check` again.
Exercise the normal Git action when verifying behavior so hook arguments and
stdin match production use.
