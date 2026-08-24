# Design: `wb remote` — fleet state across machines

**Date:** 2026-08-23
**Status:** Approved (design reviewed in conversation; implementation plan pending)

## Problem

`wb` knows everything about the fleet on the machine it runs on — which
clones are dirty, which have unpushed commits or divergence, which task
worktrees are live — and nothing about any other machine. A developer with a
laptop and a VM (or a team with twenty machines) cannot answer "is there
unpushed work anywhere?" or "who is on task-7?" without logging into every box.

The immediate goal is **reconciliation and audit**: a read-only, cross-machine
view of wb state with history. Coordination (claims) is the next step and
must not be designed out. Remote execution is explicitly out of scope — that
is Synchestra's job, and Synchestra can invoke `wb` on a host it already
manages.

## Goals

- Publish one machine's wb state to a shared store with a single command.
- Read every machine's published state from any machine, rendered as one
  worklist, with staleness made visible.
- Work with zero infrastructure: the shared store is a git repository.
- Keep the store pluggable. A hosted hub (Synchestra host registration, or a
  future `wb server` on a reachable box) must be addable as a second provider
  without touching commands or the snapshot model.
- Keep `wb` free of any dependency on Synchestra. The git provider is built
  in; any hub provider is a separate package behind the same interface.
- Support team scope: one state repo per team (or per person), keyed by
  GitHub login and machine name.

## Non-goals

- Claims / compare-and-swap coordination (next spec; see "Future: claims").
- Any daemon, background job, or `wb server` in this iteration.
- Triggering `wb` commands on another machine.
- Publishing automatically without an explicit opt-in.

## Snapshot model

Package `internal/remotestate`.

```go
type Snapshot struct {
    SchemaVersion       int               `yaml:"schema_version" json:"schema_version"` // 1
    Login               string            `yaml:"login" json:"login"`                   // GitHub login via discover.AuthUser
    Machine             string            `yaml:"machine" json:"machine"`               // from config; unique per login
    PublishedAt         time.Time         `yaml:"published_at" json:"published_at"`     // UTC
    WBVersion           string            `yaml:"wb_version" json:"wb_version"`
    ProjectsRoot        string            `yaml:"projects_root" json:"projects_root"`
    RepositoriesScanned int               `yaml:"repositories_scanned" json:"repositories_scanned"`
    Repositories        []RepositoryState `yaml:"repositories" json:"repositories"`
    Worktrees           []WorktreeState   `yaml:"worktrees" json:"worktrees"`
}

// RepositoryState is today's cmd/wb repositoryStatusInfo plus tracking.
type RepositoryState struct {
    Repository    string   `yaml:"repository"`            // owner/name
    Path          string   `yaml:"path"`
    Status        string   `yaml:"status"`
    Summary       string   `yaml:"summary,omitempty"`
    Branch        string   `yaml:"branch,omitempty"`
    Upstream      string   `yaml:"upstream,omitempty"`
    Ahead         int      `yaml:"ahead,omitempty"`
    Behind        int      `yaml:"behind,omitempty"`
    Modified      []string `yaml:"modified,omitempty"`
    Untracked     []string `yaml:"untracked,omitempty"`
    Conflicted    []string `yaml:"conflicted,omitempty"`
    Unpushed      []string `yaml:"unpushed,omitempty"`       // omitted when redacted
    UnpushedCount int      `yaml:"unpushed_count,omitempty"` // always set when Unpushed is non-empty or redacted
    Stashed       []string `yaml:"stashed,omitempty"`
    Error         string   `yaml:"error,omitempty"`
}

// WorktreeState has no CreatedAt: worktrees.ListResult does not expose a
// creation time, so there is nothing to carry here.
type WorktreeState struct {
    Task       string `yaml:"task"`
    Repository string `yaml:"repository"`
    Branch     string `yaml:"branch"`
    HeadSHA    string `yaml:"head_sha"`
    Dir        string `yaml:"dir"`
    OwnerState string `yaml:"owner_state,omitempty"` // active | orphaned | unknown
}
```

Rules:

1. **Only non-clean repositories are listed.** `RepositoriesScanned` records
   the full count so a reader can distinguish "nothing dirty" from "nothing
   scanned" — the same role `hidden_clean` plays in `wb status --json`.
   Non-clean means: dirty working tree, stash, unpushed commits, ahead/behind
   non-zero, missing or unconfigured upstream, or a scan error.
2. **Redaction is a publisher-side choice.** `publish.unpushed: subjects`
   (default) includes the `git log --oneline` lines; `counts` drops them and
   keeps `unpushed_count` only. Modified/untracked path lists are always
   included — they are the audit signal.
3. **`RepositoryState` is built from the same scan `wb status` uses**
   (`gitops.RepoStatus`, `gitops.TrackingState`). The scan
   (`gitops.Status`/`gitops.Tracking` over `qualityTargets`) is shared. The
   `wb status` row model is intentionally NOT unified with `RepositoryState`
   in this iteration: `wb status` attention ignores tracking, `remote`
   includes it, and changing `wb status` is out of scope. Follow-up: derive
   `repositoryStatusInfo` from `RepositoryState` when `wb status` gains
   tracking.
4. **Worktrees come from `worktrees.List`, unfiltered by owner state.** Every
   task worktree is published, active or orphaned, with its `owner_state`
   carried alongside — abandoned worktrees (sessions that exited without
   cleanup) are precisely what cross-machine reconciliation needs to see, so
   filtering them out of the snapshot would hide the thing it exists to
   surface. The snapshot records what is checked out, not the journal or
   prompts.

## Provider interface

```go
type Provider interface {
    // Publish overwrites the caller's own login/machine entry. Self-contained:
    // implementations refresh their own view of the store first.
    Publish(ctx context.Context, snapshot Snapshot) (PublishResult, error)
    // List returns every machine currently in the store, including the
    // caller's own last-published entry. Entries that fail to parse are
    // returned as Entry{Error: ...} with Login/Machine taken from the path.
    // Also self-contained: it refreshes the store view before reading.
    List(ctx context.Context) ([]Entry, error)
}

type PublishResult struct {
    Location string // e.g. commit SHA for git, URL for a hub
}

type Entry struct {
    Snapshot Snapshot
    Error    string // non-empty when the stored entry could not be decoded
}
```

Provider selection is `remote.provider` in config. `git` is the only value in
this iteration; the switch lives in one constructor so adding `hub` is a new
package plus one case.

## Git provider (`internal/remotestate/gitrepo`)

Store layout, one file per machine:

```
README.md                                   # generated once on first publish
machines/<login>/<machine>/snapshot.yaml
```

Because each machine rewrites only its own file, concurrent publishers never
produce content conflicts; only push races, which rebase resolves.

Clone location: `<projects-root>/<owner>/<repo>`, the canonical fleet
placement. If the clone is absent, `Publish`/`Fetch` clones it. From then on
`wb sync` keeps it current like any other fleet repo.

`Publish` sequence, all through `internal/gitops`:

1. `Fetch` (pull --rebase on the default branch).
2. Write `snapshot.yaml`; write `README.md` if missing.
3. `git add` those paths; if nothing changed, return without committing. In practice `published_at` changes on every run, so each publish commits; that is intentional — the timestamp is the staleness heartbeat.
4. Commit: `wb: publish <login>/<machine> @ <published_at RFC3339>`.
5. `git push`. On rejection: `pull --rebase`, push again, once. A second
   rejection returns an error; the local commit is kept so the next publish
   carries it.

`List` walks `machines/*/*/snapshot.yaml`. A file with an unknown newer
`schema_version` or a YAML error yields an `Entry` with `Error` set.

## Configuration

In `~/.config/wb/wb.yaml` — the file `wb run` already reads — a new
top-level `remote` section:

```yaml
remote:
  provider: git                 # only "git" for now
  repo: sneat-dev/wb-state      # owner/name on GitHub
  machine: vm-hetzner           # required; no hostname fallback
  publish:
    unpushed: subjects          # subjects | counts
```

`machine` is deliberately required. Hostnames collide across users and
change over time; a wrong or duplicate machine name silently overwrites
someone else's entry, which is worse than a hard error.

Config loading moves from `cmd/wb/run.go` into a small shared loader so
`recipes` and `remote` are parsed from the same file. Existing
`wb run --config` behaviour is unchanged.

## Commands

All live under a new `wb remote` group (`cmd/wb/remote.go`, one file per
verb per the UI-file rule).

- `wb remote publish [--dry-run] [--json]`
  Scan the fleet (same selection as `wb status`, honouring `--projects-root`
  and `--filter`), list worktrees, build the snapshot, call `Publish`.
  `--dry-run` prints the snapshot and makes no git changes. Output: machine
  key, counts (repos scanned / non-clean / worktrees), and the commit SHA.

- `wb remote status [--json] [--stale 24h] [--machine <login>/<machine>]`
  `Fetch` then `List`; render one table grouped by machine: repository,
  branch, ahead/behind, dirty summary, worktree tasks. Any machine whose
  `published_at` is older than `--stale` is flagged `STALE`. The local
  machine is shown from the store, not from a live scan, so the reader sees
  exactly what everyone else sees; `wb status` remains the live local view.
  Exit code 0 even when entries have errors; errors are rows, not failures.

- `wb remote machines [--json]`
  One line per machine: `login/machine`, `published_at`, age, wb version,
  non-clean repo count, worktree count. This is the audit entry point.

- `wb sync --publish`
  Opt-in flag: after a successful sync, run the equivalent of
  `wb remote publish`. A publish failure is reported but does not change the
  sync exit code.

## Failure handling

| Situation | Behaviour |
|---|---|
| `remote` section missing, or `repo`/`machine` unset | exit 2 with the exact YAML snippet to add |
| `gh` not authenticated (login unknown) | exit 2; publishing needs a login to key the entry |
| State repo clone absent | clone it; failure to clone is exit 1 |
| Push rejected twice | exit 1; local commit retained |
| Another machine's file unparseable / newer schema | rendered as an error row; others render normally |
| Scan error in one fleet repo | that repo appears with `error` set, as in `wb status` today |

The git provider never writes to any fleet repository other than the state
repo clone. Publishing is the only network write; `status`/`machines` only
pull.

## Future: claims (not in this spec)

The layout reserves `claims/<task>.yaml` at the store root, written by a
future `wb worktree create` / `wb remote claim` and keyed by
`login/machine`. With the git provider, push rejection on the same path is
the compare-and-swap; with a hub provider it is an HTTP conditional write.
Nothing in this spec must be changed for that — which is the reason the
snapshot carries worktree `Task`s already.

## Testing

- `internal/remotestate`: table-driven tests for snapshot construction from
  `gitops.RepoStatus`/`TrackingState` (non-clean filter, redaction,
  `RepositoriesScanned`), and for YAML round-trip including unknown newer
  `schema_version`.
- `internal/remotestate/gitrepo`: tests against a bare local repository as
  origin: first publish creates README + snapshot; re-publish with no change
  makes no commit; two simulated machines publishing concurrently exercise
  the rebase-and-retry path; `List` surfaces a corrupt entry as an error
  entry.
- `cmd/wb`: smoke tests in the existing `cli_smoke_test.go` style —
  unconfigured error with snippet, `publish` → `status` round trip against a
  temporary bare origin, `--stale` flagging, `--json` shape.
- Coverage: the repo's CI gate is a total-coverage threshold
  (`MINIMUM_COVERAGE` in `.github/workflows/go-ci.yml`, currently 58%). New
  packages target full coverage of their own statements so the total only
  rises.

## Open decisions (resolved in conversation)

- Store is a git repository first; a reachable hub is a later provider.
- One store per team (or person), keyed `machines/<login>/<machine>`.
- No daemon; explicit `publish`, with `wb sync --publish` as the only
  automation hook, and `wb hooks` available for more.
