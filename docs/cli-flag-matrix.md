# WB root-flag support matrix

Generated from the persistent flags shown by `wb --help` on 2026-08-28 and
enforced by `cmd/wb/main.go`. A persistent flag is never accepted and ignored:
an unsupported combination exits with usage code `2` before the command starts.
Leaf help hides inherited selectors that the selected command would reject.
This matrix covers inherited/root flags; command-specific flags are listed by
their own `wb <command> --help` and remain scoped to that command.

Mutation admission flags are command-specific: `worktree adopt`,
`worktree rename`, and the recovery leaves `worktree merge
acknowledge-landed-failed`/`acknowledge-stranded-landing`/`acknowledge-receipt-collision`/`seal-validation-failed`/`supersede-validation-failed`/`prepare-published-forward-repair` expose `--mode` and
`--initiator` (only `--apply` requires admission); `worktree own` and
`worktree correct-identity` always mutate and therefore use the same flags.
Work Log mutation leaves inherit these flags from `worktree log`; `show`, and
`recover`/`archive` without `--apply`, remain read-only. `--mode agent`
requires a live registered session, while `--mode manual` requires
`--initiator`.

The machine-readable, checked-in capability × help × AI-skill × tests view is
[`ai/capabilities.json`](../ai/capabilities.json). It conforms to the exact
checked-in SpecScore schema at
[`ai/cli-capability-delivery.schema.json`](../ai/cli-capability-delivery.schema.json).
That validation input is digest-pinned to the canonical SpecScore commit and
its provenance/update contract is documented in [`ai/README.md`](../ai/README.md).
Its validator resolves every runtime command/flag, renders help anchors, parses
skill examples, resolves executable tests, and enforces sorted `wb.` IDs.

| Command surface | `--projects-root` | `--filter` | `--org` | `--non-interactive` |
|---|---:|---:|---:|---:|
| `sync` | yes | yes | yes; both root and command-local spellings restrict owners | yes |
| `run` | yes | yes | yes | yes |
| `migrate` | yes | rejected | rejected | yes |
| `deps graph`, `deps set`, `deps drift` | yes | yes | `--fleet` only | yes |
| `deps bump` | yes | yes | yes (`--fleet` is mandatory) | yes |
| `deps publish npm` | yes | yes | yes (`--fleet` is mandatory) | yes |
| `ci audit` | `--fleet` only | `--fleet` only | rejected | yes |
| `ci wait` | rejected | rejected | rejected | yes |
| `hooks install`, `check`, `repair` | yes | `--fleet` only | rejected | yes |
| hidden `hooks run` | yes | rejected | rejected | yes |
| `hooks agent pre-tool-use`, `hooks agent install` | yes | rejected | rejected | yes |
| `hooks metrics` | rejected | rejected | rejected | yes |
| `coverage`, `verify`, `check` | `--fleet` only | `--fleet` only | rejected | yes |
| `status` | no-path default fleet only | no-path default fleet only | rejected | yes |
| `fleet`, `fleet overview`, `fleet stats`, `fleet status` | yes | yes | rejected | yes |
| `fleet prs` | rejected | rejected | yes | yes |
| `remote publish`, `remote status`, `remote machines` | yes | `remote publish` only | rejected | yes |
| `remote claim`, `remote release`, `remote claims` | yes | rejected | rejected | yes |
| `session register`, `list`, `prune`, `move`, `receive`, `park`, `resume` | yes | rejected | rejected | yes |
| `layout audit`, `layout clean` | yes | rejected | rejected | yes |
| `archive clean` | yes | yes | rejected | yes |
| `repo status` | rejected | rejected | rejected | yes |
| `worktree list`, `cleanup`, `rename`, `summary` | yes | yes | rejected | yes |
| `worktree marker`, `worktree rescue` | yes | yes | rejected | yes |
| `worktree abort` | yes | yes | rejected | yes |
| `worktree create`, `guard`, `log`, `info` | yes | rejected | rejected | yes |
| `worktree merge`, `merge prepare` (including `--rebatch-receipt`), `merge land`, `merge resume` (including PR-only `--stop-before-merge`), `merge revert`, `merge acknowledge-landed-failed`, `merge acknowledge-stranded-landing`, `merge acknowledge-receipt-collision`, `merge seal-validation-failed`, `merge supersede-validation-failed`, `merge prepare-published-forward-repair` | yes | rejected | rejected | yes |
| `worktree log init`, `steer`, `show`, `checkpoint`, `refresh`, `integrate`, `handoff`, `recover`, `finalize`, `sync`, `archive` | yes | rejected | rejected | yes |
| `worktree orphans`, `backfill` | yes | rejected | rejected | yes |
| `worktree checkpoint-fetch` | rejected | rejected | rejected | yes |
| `worktree set` | rejected | rejected | rejected | yes |
| `branch list`, `cleanup` | yes | yes | rejected | yes |
| `version`, `self-update` | rejected | rejected | rejected | yes |
| `skills sync`, `skills hook print`, `skills hook install` | rejected | rejected | rejected | yes |
| hidden `skills hook run` | rejected | rejected | rejected | yes |
| `commands` | rejected | rejected | rejected | yes |

## `archive clean` command flags

`wb archive clean` plans by default. `--apply` deletes an archived clone only
after its normal safety predicate passes. Untracked paths are itemized in every
plan; `--apply` alone refuses them. `--delete-untracked` is valid only as the
separate narrow authority alongside `--apply`: it rereads the exact manifest,
refuses drift, symlinks, and traversal, records the durable itemized receipt,
and then allows the normal archive prune. It is not a cache-name exception or
a general force flag.

## Precedence and non-interactive contract

- `--projects-root` overrides the default `<home>/projects` for the selected
  invocation. `WB_HOME` separately controls WB-managed worktree/journal state;
  it does not change clone discovery. CI audit, coverage, verify, and check
  consume it only with `--fleet`; status consumes it only in no-path default
  fleet mode. Supplying it with a direct repository path is rejected.
  `wb fleet` / `overview` / `stats` / `status` always consume `--projects-root`
  and `--filter`. `wb repo status` rejects both because it targets one path.
- Root `--org` is consumed only by fleet commands that query owners. For sync,
  `wb --org acme sync` and `wb sync --org acme` both restrict selection to the
  named owner; the command-local spelling intentionally shadows the root flag.
- `--filter` selects repository identities on the listed fleet/worktree
  inventory surfaces. CI, hook, coverage, verify, and check commands require
  explicit `--fleet`; status requires its no-path default fleet mode; the
  `wb fleet …` leaves always accept it. The flag
  is intentionally rejected for a direct canonical
  worktree create/guard, where silently skipping the requested repository
  would be unsafe: those commands have no per-repository report for an
  excluded repository to appear in. `worktree abort` is the one exception
  (#170): it narrows which repositories in a coordinated task are touched,
  but every excluded repository is still reported via `AbortResult.Excluded`
  — never silently dropped — and the task remains non-terminal (its remote
  claim, if any, is not released) until a later abort call resolves it too.
  This lets one repository blocked on something abort cannot fix stop
  blocking the rest of the task without ever widening into #76's cross-repo
  blast-radius concern.
- `--non-interactive` disables every live terminal UI and progress line,
  including sync, status, fleet quality checks, CI waits, dependency campaigns,
  npm publication, remote publication, and hierarchical migration.
  `WB_NON_INTERACTIVE=1` has the same rendering effect; it is not a universal
  JSON mode. Structured command output remains on stdout while interactive
  progress uses stderr.
- `--format`/`--json`, `--dry-run`, `--apply`, `--check`, and config flags are
  command-specific. They are never advertised as root flags. Commands that
  mutate default to their documented dry-run/plan behavior unless their own
  `--apply`/publication flag says otherwise.

## Audit procedure

Run `wb --help`, then `wb <command> --help` for every root command and nested
subcommand. For each inherited flag, add it to `persistentFlagSupport` only
when the command consumes it; otherwise the command must reject it. The
conformance test `TestPersistentFlagMatrix` exercises every root-flag ×
leaf-command cell; focused negative cases remain in
`TestPersistentFlagsAreRejectedWhenTheSelectedCommandCannotUseThem`.
