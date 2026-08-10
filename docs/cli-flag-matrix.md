# WB root-flag support matrix

Generated from the persistent flags shown by `wb --help` on 2026-08-10 and
enforced by `cmd/wb/main.go`. A persistent flag is never accepted and ignored:
an unsupported combination exits with usage code `2` before the command starts.
This matrix covers inherited/root flags; command-specific flags are listed by
their own `wb <command> --help` and remain scoped to that command.

The machine-readable, checked-in capability × help × AI-skill view is
[`ai/capabilities.json`](../ai/capabilities.json). Its validator resolves every
advertised command/flag and checks the named skill anchor. The manifest carries
the planned extraction seam for a shared SpecScore capability artifact.

| Command surface | `--projects-root` | `--filter` | `--org` | `--non-interactive` |
|---|---:|---:|---:|---:|
| `sync` | yes | yes | command-local owner selector | yes |
| `run` | yes | yes | yes | yes |
| `migrate` | yes | rejected | rejected | yes |
| `deps` | yes | yes | yes | yes |
| `ci` | yes | yes | rejected | yes |
| `hooks` | yes | yes | rejected | yes |
| `coverage`, `verify`, `check`, `status` | yes | yes | rejected | yes |
| `worktree list`, `cleanup`, `rename` | yes | yes | rejected | yes |
| `worktree create`, `guard`, `abort` | yes | rejected | rejected | yes |
| `version`, `self-update` | rejected | rejected | rejected | yes |

## Precedence and non-interactive contract

- `--projects-root` overrides the default `<home>/projects` for the selected
  invocation. `WB_HOME` separately controls WB-managed worktree/journal state;
  it does not change clone discovery.
- Root `--org` is consumed only by fleet commands that query extra owners.
  `sync --org` is a command-local selector and is not the root fleet-owner
  setting.
- `--filter` selects only repository identities on the listed fleet/worktree
  inventory surfaces. It is intentionally rejected for a direct canonical
  worktree create/guard/abort, where silently skipping the requested repository
  would be unsafe.
- `--non-interactive` controls the terminal sync UI. `WB_NON_INTERACTIVE=1`
  has the same sync rendering effect; it is not a universal JSON mode.
- `--format`/`--json`, `--dry-run`, `--apply`, `--check`, and config flags are
  command-specific. They are never advertised as root flags. Commands that
  mutate default to their documented dry-run/plan behavior unless their own
  `--apply`/publication flag says otherwise.

## Audit procedure

Run `wb --help`, then `wb <command> --help` for every root command and nested
subcommand. For each inherited flag, add it to `persistentFlagSupport` only
when the command consumes it; otherwise the command must reject it. The unit
test `TestPersistentFlagsAreRejectedWhenTheSelectedCommandCannotUseThem`
guards the regression where an agent sees a valid-looking flag but the command
ignores it.
