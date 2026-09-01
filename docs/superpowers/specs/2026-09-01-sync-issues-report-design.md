# `wb sync` issues report

**Date:** 2026-09-01
**Status:** Approved
**Base:** `origin/main` @ `030889e`

## Problem

`wb sync` already classifies every repository it touches and prints the two
categories that need a human or an agent to act: `Needs attention` and
`Errors`. Then it throws them away. The findings live only in terminal
scrollback, mixed into a live TUI and a results browser.

That makes the natural next step — "hand this to an agent and let it work
through the list" — impossible without the operator manually re-reading the
screen and retyping what it said. Worse, the terminal rendering is lossy:
`fleetsync.Result` carries the local clone path, the branch, the upstream,
ahead/behind counts, the verbatim `archiveprune` reason, and the raw error
value, but the summary collapses each row to a single line.

A run that fails before it scans anything is lost entirely. When
`resolveSyncOwners` cannot authenticate to GitHub, sync prints to stderr and
returns; there is no artifact at all, even though "GitHub auth is broken" is
exactly the kind of issue an agent should be handed.

## Solution

`wb sync` always writes `~/.wb/last-sync-issues.md`: a Markdown report
containing only the repositories that need attention plus the errors, with
enough context for an agent to diagnose and resolve each one without
re-running discovery.

The file is overwritten every run and honours `$WB_HOME`.

### Why not the `reports/<operation>/<timestamp>/` convention

Four commands (`worktree-cleanup`, `branch-cleanup`, `deps-drift`, migrate
campaigns) write timestamped run directories under `~/.wb/reports/`. This
report deliberately does not.

Its entire purpose is to be handed to an agent by path. A stable path is a
usable instruction ("read `~/.wb/last-sync-issues.md` and fix what it lists");
a timestamped directory requires the operator to look up the newest run first,
every time. Sync's issue list is also inherently current-state — a week-old
list of repositories that needed attention is not evidence of anything, unlike
a cleanup receipt which records an irreversible mutation. History is not worth
the indirection here.

## Scope

### In scope

Every result the existing `fleetsync.Summary()` already places in the
`Needs attention` group, which on current `main` is five statuses:

| Status | Meaning |
|---|---|
| `Diverged` | Branch and upstream each hold commits the other lacks; no fast-forward, not pulled |
| `NoUpstream` | Checked-out branch tracks nothing; nowhere to pull from |
| `Unpushed` | Pull succeeded, but the clone holds commits on no remote |
| `ArchivedUnlandable` | Archived clone holding unpushed commits; the remote is read-only, so they can never be pushed |
| `ArchivedNotPruned` | Archived, but `--prune-archived` was not passed, so it was pulled like any other clone |

Plus the `Errors` group (`Failed`), and one new case the terminal has never
surfaced: a run that failed before scanning.

`ArchivedNotPruned` is informational rather than broken — nothing is wrong,
the operator simply did not ask for pruning. It renders in its own clearly
labelled subsection so an agent does not treat it as a defect to fix.

### Out of scope

- Any change to sync's exit codes, statuses, or classification logic
- The successful categories (Cloned, Pulled, Skipped, …) — this is an issues
  report, not a run log
- A JSON variant. Nothing consumes one yet; add it when something does.
- A `--report` / `--no-report` flag. The report is free to produce and the
  whole point is not having to remember it.
- Report history or rotation

## Architecture

Two files, one purpose each.

### `internal/fleetsync/issues.go` — pure renderer

```go
// RunMeta describes the run that produced a report.
type RunMeta struct {
	StartedAt     time.Time
	ProjectsRoot  string
	Scanned       int
	DryRun        bool
	PruneArchived bool
	// RunErr is set when the run failed before scanning anything, e.g.
	// GitHub authentication failed. Results is empty in that case.
	RunErr error
}

// IssuesMarkdown renders the attention and error groups as Markdown for a
// human or an AI agent. It performs no IO.
func IssuesMarkdown(meta RunMeta, results []Result) string
```

`fleetsync` already owns `Summary()`, `needsAttention()`, and the meaning of
every `Status`. Remediation text is a function of `Status`, so it belongs
beside them rather than in a package that would have to import all three back.
This mirrors `internal/deps/report.go`'s `Markdown()`, whose doc comment
already describes its output as "suitable for human or AI review".

Keeping the renderer pure means it is table-testable on strings alone — no
temp directories, no golden files.

`PruneArchived` is carried in `RunMeta` because the `ArchivedNotPruned`
entries cannot be explained without it.

### `cmd/wb/sync_report.go` — writer

```go
// writeSyncIssuesReport renders and writes the issues report, printing the
// path it wrote to out and any failure to errOut. It never fails a sync, so
// it returns nothing for a caller to check.
func writeSyncIssuesReport(meta fleetsync.RunMeta, results []fleetsync.Result,
	projectsRoot string, out, errOut io.Writer)
```

Resolves the path via `wbhome.EnsureRoot(projectsRoot)`, writes atomically
(temp file in the same directory, then `os.Rename`) so an agent can never read
a half-written report, and prints the path to `out`.

## Data flow

`finishSync` already establishes the exact precedent this needs:

```go
if !dryRun {
	refreshSyncedCheckoutMarkers(results, projectsRoot, errOut)
}
```

— a non-fatal side effect that writes files after the run and never changes
the exit code. The report writer sits beside it, with one deliberate
difference: it also runs on `--dry-run`.

Dry-run detection is read-only and identical to a real run, so its findings are
real and worth reporting. The report is stamped `Dry run: true` so an agent
knows the fleet was not actually pulled and that `Unpushed` detection, which
normally runs after a pull, may be incomplete.

The run-failed-early path in `runSync` writes a report too, before returning
`exitFindings`:

```go
owners, err := syncOwners(only)
if err != nil {
	fmt.Fprintf(os.Stderr, "wb: %v\n…", err)
	writeSyncIssuesReport(fleetsync.RunMeta{RunErr: err, …}, nil, …)
	return exitFindings
}
```

## Error handling

A report that cannot be written is warned about on stderr and changes nothing
else — same policy `finishSync` already applies to a failed `--publish` and to
`refreshSyncedCheckoutMarkers`. Sync's exit code reflects sync, never its
reporting.

A clean run still writes the file, with an explicit "no issues" body. Leaving a
stale report in place would let an agent read yesterday's already-fixed
findings and try to re-fix them; deleting the file would force every agent to
treat a missing path as a success case.

## Output shape

```markdown
# WB sync issues

**Run:** 2026-09-01T10:15:00Z · **Projects root:** /home/ai/projects
**Scanned:** 214 repositories · **Needs attention:** 2 · **Errors:** 1

> Inspection commands are read-only and safe to run as-is. Resolution options
> are deliberately not copy-paste ready: inspect first, then choose.

## Needs attention

### sneat-dev/wb — no upstream

- **Clone:** `/home/ai/projects/sneat-dev/wb`
- **Branch:** `fix/sync-authentication-failure`
- **Upstream:** `origin/fix/sync-authentication-failure` is configured but gone from the remote
- **Impact:** not pulled; 1 commit exists on no remote

**Inspect**

    git -C /home/ai/projects/sneat-dev/wb log --oneline origin/main..HEAD
    git -C /home/ai/projects/sneat-dev/wb status -sb

**Resolve** — choose after inspecting:

- Publish the branch: `git push -u origin fix/sync-authentication-failure`
- Or, if the work already landed upstream under a squashed commit, return the
  clone to its default branch and delete the leftover branch
- Or `wb repo init-remote .` if the branch was never published at all

## Archived, not pruned

Informational: these are archived on GitHub and were pulled like any other
clone because `--prune-archived` was not passed. Nothing is broken.

- `owner/old-repo` — pass `--prune-archived` to evaluate it for cleanup

## Errors

### owner/example — failed

- **Clone:** `/home/ai/projects/owner/example`
- **Error:** `git pull: could not read Username for 'https://github.com'`

**Inspect**

    gh auth status -h github.com
    git -C /home/ai/projects/owner/example config --get remote.origin.url

**Resolve** — choose after inspecting:

- Re-authenticate: `gh auth login -h github.com`
- Or switch the remote to SSH if this clone was created outside WB
```

The clean-run body:

```markdown
# WB sync issues

**Run:** 2026-09-01T10:15:00Z · **Projects root:** /home/ai/projects
**Scanned:** 214 repositories · **Issues:** none

All repositories are in sync. Nothing requires attention.
```

## Testing

`internal/fleetsync/issues_test.go` — table tests over `IssuesMarkdown`:

- One case per attention status, asserting the clone path, branch, and
  status-specific remediation appear
- An error case, asserting the verbatim `Err` text is reproduced
- The clean run: no issue headings, explicit "none"
- The dry-run stamp
- The run-failed-early case with empty results
- `ArchivedNotPruned` renders in its own section, not among the defects
- Determinism: two renders of the same input are byte-identical

`cmd/wb/sync_report_test.go` — with `WB_HOME` set to `t.TempDir()`:

- The file lands at `<WB_HOME>/last-sync-issues.md`
- A second run overwrites rather than appends
- A clean run still writes the file
- A write failure (read-only home) warns and leaves the exit code untouched
- No temp file is left behind

## Repository gates

CI enforces these; the change is not complete without them.

- `ai/capabilities.json` — the `wb.sync` entry's `notes` and `ai_skill`
  surface must mention the report
- `ai/skills/wb-fleet/references/sync.md` — the agent-facing instruction to
  read `~/.wb/last-sync-issues.md`
- No new flags, so `cmd/wb/main.go`'s `persistentFlagSupport` allowlist is not
  involved
- `~/.wb/README.md` already states the directory holds "command reports"; no
  change needed

## Decisions

| Decision | Rationale |
|---|---|
| Stable path, not timestamped | The path is the instruction handed to an agent |
| Name `last-sync-issues.md`, not `last-sync.md` | Honest about holding only issues, not a run log |
| Always written, including clean runs | A stale report is worse than a boring one |
| Written on `--dry-run`, stamped | Detection is read-only and identical; findings are real |
| Report a run that failed before scanning | Broken GitHub auth is exactly what an agent should be handed |
| Remediation as options, not commands | An agent must inspect before rebasing or force-pushing |
| No flag | Free to produce; remembering a flag defeats the purpose |
