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
so guards use the same policy when Git invokes them later.

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
