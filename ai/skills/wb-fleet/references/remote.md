# Share fleet state across machines

Configure once in `~/.config/wb/wb.yaml`:

```yaml
remote:
  provider: git
  repo: <owner>/wb-state        # team or personal private repository
  machine: <unique-name>        # required; unique per GitHub login
  publish:
    unpushed: subjects          # or counts, to hide commit subjects
```

| Need | Command |
|---|---|
| Publish this machine's attention repositories and worktrees | `wb remote publish` |
| Preview without publishing | `wb remote publish --dry-run` |
| Cross-machine worklist | `wb remote status` |
| Only flag machines idle over 12h | `wb remote status --stale 12h` |
| One line per machine with publish age | `wb remote machines --json` |
| Publish after syncing | `wb sync --publish` |

The store is a git repository: one `machines/<login>/<machine>/snapshot.yaml`
per machine, so history is the audit trail. Staleness keys off the effective
heartbeat: the later of the machine's publish and its claim activity
(`last_seen_at`, stamped by claim/refresh/release/take-over) — a machine
that runs a long claims campaign without re-publishing stays fresh. `wb
remote machines` shows both: PUBLISHED is the raw publish age, SEEN is the
effective-heartbeat age STALE actually keys off. Entries older than
`--stale` (default 24h) are flagged `STALE`. Entries that cannot be decoded
are shown as error rows and do not change the exit code. Exit `2` means the
`remote` section is missing; the message includes the snippet to add.

Create the store with `gh repo create <owner>/wb-state --private` (no README
needed; the first publish creates `main`), and SSH access to GitHub is
required — the clone URL is `git@github.com:<owner>/<name>.git`.

## Task claims

The same store reserves `claims/<task>.yaml` — the file's existence IS the
claim; releasing deletes it, so git history is the audit trail (no `state:`
field, no tombstones).

| Need | Command |
|---|---|
| Claim a task for this login/machine | `wb remote claim task-7 --note rehearsal` |
| Take over a stale claim | `wb remote claim task-7 --take-over` |
| Take over any claim, loudly | `wb remote claim task-7 --force` |
| Release your own claim | `wb remote release task-7` |
| Release someone else's claim | `wb remote release task-7 --force` |
| List every claim with staleness | `wb remote claims --stale 12h` |

Staleness has no separate TTL: a claim is stale exactly when its holder
machine's effective heartbeat (the later of `published_at` and
`last_seen_at`) is stale, or the holder never published at all. `--take-over`
only replaces a stale claim; `--force` replaces any claim and prints who is
being overridden — never used by any automatic path. Same `login` on a
different `machine` is still another holder (only the wording softens to
"you").

`wb worktree create <task>` claims best-effort automatically when `remote:`
is configured: it prints the outcome (acquired, refreshed, held by another
— warn and proceed, held by you elsewhere — warn and proceed, took over a
stale claim, or skipped because the store is unreachable) and includes it
as the `remote_claim` field of `--format json` output; it never fails the
command. Pass `--no-claim` to skip the attempt. `wb worktree cleanup
--apply` and `wb worktree abort --apply` (discarded or handoff dispositions
only — not_landed keeps the claim because the task is expected to resume)
release the claim best-effort the same way, printing `release skipped: …`
on failure without changing the exit code.
