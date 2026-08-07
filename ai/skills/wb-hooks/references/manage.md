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

`install` and `repair` persist the absolute `--projects-root` and resolved WB
home in managed shims, so guards use the same policy when Git invokes them
later. A default-home shim keeps legacy worktrees readable during migration;
an explicitly selected `WB_HOME` remains authoritative.

Do not run `wb hooks install` or `repair` through `go run`: its executable is
temporary and must never be written into a persistent Git shim. Use an
installed WB binary or build a durable candidate binary first.

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
