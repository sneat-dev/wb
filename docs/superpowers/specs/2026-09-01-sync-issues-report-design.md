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
every time. Sync's issue list is also inherently current-state: a week-old list
of repositories that needed attention is not evidence of anything.

An earlier draft justified this by adding "unlike a cleanup receipt, which
records an irreversible mutation." That is wrong and is struck: `wb sync
--prune-archived` calls `os.RemoveAll` on local clones, which is exactly an
irreversible mutation, and it is the one WB command that deletes clones
**without** writing the receipt `wb archive clean` writes. The stable path is
still the right choice for *this* artifact, but it is not evidence that sync
needs no durable record. Two follow-ups stand on their own merits and are out
of scope here: a prune receipt for `--prune-archived`, and a timestamped copy
beside the stable path.

## Scope

### In scope

Every result the existing `fleetsync.Summary()` already places in the
`Needs attention` group. That group is **four statuses plus one flag**, and the
difference matters:

| Selector | Kind | Meaning |
|---|---|---|
| `Diverged` | Status | Branch and upstream each hold commits the other lacks; no fast-forward, not pulled |
| `NoUpstream` | Status | Checked-out branch tracks nothing, or HEAD is detached; nowhere to pull from |
| `Unpushed` | Status | Pull succeeded, but the clone holds commits on no remote |
| `ArchivedUnlandable` | Status | Archived clone holding unpushed commits; the remote is read-only, so they can never be pushed. **Only assigned when `--prune-archived` is passed** |
| `ArchivedNotPruned` | **Flag** | Archived, `--prune-archived` not passed. Set on top of *whatever* status the inner sync returned |

`ArchivedNotPruned` is a boolean field, not a `Status` constant. An earlier
draft of this document listed it as a fifth status, and that error propagated:
a renderer that switched on `Status` alone put a *failed* archived repository
into the informational bucket, describing a failure as "nothing is broken",
while it also appeared under Errors — one repository, two entries, both counts
inflated. Any selector must test the flag together with a benign status.

The same asymmetry means an archived repository holding unpushed commits is
`ArchivedUnlandable` only under `--prune-archived`; on the **default** path it
is a plain `Unpushed` with `Archived` true. A renderer keying on `Status` alone
therefore offers "push" and "discard" for commits that can never be pushed and
exist nowhere else. Every renderer must read `Archived`, not just `Status`.

Plus the `Errors` group (`Failed`), and one new case the terminal has never
surfaced: a run that failed before scanning.

`ArchivedNotPruned` **on a benign status** is informational rather than broken:
nothing is wrong, the operator simply did not ask for pruning. Those render in
their own clearly labelled subsection so an agent does not treat them as
defects to fix. The flag on any other status is not informational, per the
correction above, and keeps its real classification.

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

Dry-run detection is **not** identical to a real run — an earlier draft of this
document claimed it was, and that claim was false. In `syncActive`'s dry-run
branch the only classification performed is a dirty check and a tracking probe,
so it can produce `NoUpstream` (detached HEAD only) or `Pulled`, and nothing
else. `Diverged` is assigned only after a real `git pull` fails; `Unpushed` only
after a pull succeeds. **Both are structurally unreachable in a dry run.**

That makes a clean dry-run report actively dangerous if it is allowed to speak
like a real one: three of the five attention selectors cannot fire, so silence
is silence, not health — and the report would overwrite an accurate one from
the last real run with a false all-clear, on the command an operator reaches
for precisely to "check first".

So the report is still written on `--dry-run` (its findings, where it can make
them, are real), but under `DryRun` it may never emit "All repositories are in
sync". It states instead which categories a dry run cannot classify and that
their absence is not evidence of their absence.

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
- **Upstream:** none configured for this branch
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

## Corrections after adversarial review

This document was red-teamed after implementation. Four claims in the original
draft were factually wrong and are corrected above rather than quietly edited:

1. **`ArchivedNotPruned` listed as a fifth `Status`.** It is a boolean flag set
   on top of any status. This error propagated into the implementation and
   caused a failed archived repository to be reported as "nothing is broken".
2. **"Dry-run detection is read-only and identical to a real run."** False.
   `Diverged` and `Unpushed` are structurally unreachable in a dry run.
3. **A `NoUpstream` worked example showing a configured-but-deleted upstream.**
   `fleetsync` deliberately classifies that state as `Failed` so it keeps
   failing loudly; `NoUpstream` means no upstream configured, or detached HEAD.
4. **"Unlike a cleanup receipt, which records an irreversible mutation."**
   `wb sync --prune-archived` deletes local clones and writes no receipt.

Also found and fixed in implementation, none of which this design anticipated:
unescaped error text able to forge report structure; `git -C ''` targeting the
reader's own repository on a failed clone; unquoted branch names reaching a
shell command; a report written only after a blocking interactive browser; a
world-readable file able to contain a credentialed remote URL; and two
recommended commands that did not run or destroyed the report itself.

The common thread: this design reasoned about `Status` as the whole
classification and never enumerated the states the code can actually produce.

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
