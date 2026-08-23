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
per machine, so history is the audit trail. Snapshots older than `--stale`
(default 24h) are flagged `STALE`. Entries that cannot be decoded are shown as
error rows and do not change the exit code. Exit `2` means the `remote`
section is missing; the message includes the snippet to add.
