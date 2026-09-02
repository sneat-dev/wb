# Sync Issues Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `wb sync` always write `~/.wb/last-sync-issues.md` — a Markdown report of only the repositories needing attention plus errors — so it can be handed to an AI agent by a stable path.

**Architecture:** A pure renderer in `internal/fleetsync/issues.go` turns `[]fleetsync.Result` plus a `RunMeta` into a Markdown string with no IO. A thin writer in `cmd/wb/sync_report.go` resolves the path through `wbhome.EnsureRoot`, writes atomically, and prints the path. `finishSync` calls the writer before its error short-circuit, so a run with errors — the run whose report matters most — still produces one.

**Tech Stack:** Go 1.27, stdlib only (`fmt`, `strings`, `regexp`, `time`, `os`, `path/filepath`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-01-sync-issues-report-design.md`

## Global Constraints

- Go toolchain from `go.mod` (Go 1.27). Build: `go build ./...` · Test: `go test ./...` · Lint: `golangci-lint run`
- Total statement coverage must stay at or above `--minimum=58`, enforced in `.github/workflows/go-ci.yml`. Do not reduce approved scope to satisfy it — say so instead.
- Exit codes are contract: `0` success, `1` findings, `2` usage. **This feature must never change a sync exit code.**
- **No new CLI flags.** The report is unconditional, so `persistentFlagSupport` in `cmd/wb/main.go` and `docs/cli-flag-matrix.md` are not touched.
- **Every test that exercises the writer MUST set `t.Setenv("WB_HOME", t.TempDir())`.** Without it the test writes into the developer's real `~/.wb`.
- Report filename is exactly `last-sync-issues.md`, directly under WB home.
- Work happens in the worktree `/home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb` on branch `sync-issues-report`. The canonical clone at `~/projects/sneat-dev/wb` is `writable: false` — never edit or commit there.
- Run `gofmt -l .` before every commit; it must print nothing.

## File Structure

| File | Responsibility |
|---|---|
| `internal/fleetsync/issues.go` (create) | `RunMeta` type and `IssuesMarkdown` — pure rendering, zero IO |
| `internal/fleetsync/issues_test.go` (create) | Table tests over the rendered string |
| `cmd/wb/sync_report.go` (create) | Path resolution, atomic write, "never fails a sync" policy |
| `cmd/wb/sync_report_test.go` (create) | Writer tests under a temporary `WB_HOME` |
| `cmd/wb/sync.go` (modify) | Build `RunMeta`; report on the two early-failure paths |
| `cmd/wb/remote_test.go` (modify) | Existing `finishSync` callers gain the new parameter |
| `ai/capabilities.json` (modify) | `wb.sync` runtime mode + notes mention the report |
| `ai/skills/wb-fleet/references/sync.md` (modify) | Tell agents to read the report |

---

### Task 1: `RunMeta` and the report skeleton

Header, the clean-run body, and the dry-run stamp. No issue entries yet.

**Files:**
- Create: `internal/fleetsync/issues.go`
- Test: `internal/fleetsync/issues_test.go`

**Interfaces:**
- Consumes: `Result`, `Summary`, `SummaryGroupByLabel` from `internal/fleetsync/summary.go`; `discover.Repo.Slug()`.
- Produces: `RunMeta` struct and `func IssuesMarkdown(meta RunMeta, results []Result) string`, used by Tasks 2–4 and by `cmd/wb/sync_report.go` in Task 5.

- [ ] **Step 1: Write the failing test**

Create `internal/fleetsync/issues_test.go`:

```go
package fleetsync

import (
	"strings"
	"testing"
	"time"
)

func testMeta() RunMeta {
	return RunMeta{
		StartedAt:    time.Date(2026, 9, 1, 10, 15, 0, 0, time.UTC),
		ProjectsRoot: "/home/ai/projects",
		Scanned:      214,
	}
}

func TestIssuesMarkdownCleanRunSaysNothingRequiresAttention(t *testing.T) {
	got := IssuesMarkdown(testMeta(), nil)
	for _, want := range []string{
		"# WB sync issues",
		"2026-09-01T10:15:00Z",
		"/home/ai/projects",
		"**Scanned:** 214 repositories",
		"**Issues:** none",
		"All repositories are in sync. Nothing requires attention.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Needs attention") {
		t.Errorf("clean run must not render an attention section:\n%s", got)
	}
}

func TestIssuesMarkdownStampsDryRun(t *testing.T) {
	meta := testMeta()
	meta.DryRun = true
	got := IssuesMarkdown(meta, nil)
	if !strings.Contains(got, "**Dry run:**") {
		t.Errorf("dry run not stamped:\n%s", got)
	}
	if !strings.Contains(got, "the fleet was not modified") {
		t.Errorf("dry run stamp must explain the consequence:\n%s", got)
	}
}

func TestIssuesMarkdownIsDeterministic(t *testing.T) {
	first := IssuesMarkdown(testMeta(), nil)
	second := IssuesMarkdown(testMeta(), nil)
	if first != second {
		t.Fatal("two renders of identical input differ")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/fleetsync/ -run TestIssuesMarkdown -v`
Expected: FAIL — `undefined: RunMeta`, `undefined: IssuesMarkdown`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/fleetsync/issues.go`:

```go
package fleetsync

import (
	"fmt"
	"strings"
	"time"
)

// RunMeta describes the sync run that produced an issues report. It carries
// what the results themselves cannot say: when the run happened, whether it
// was a dry run, whether archived pruning was requested, and whether the run
// failed before it scanned anything at all.
type RunMeta struct {
	StartedAt    time.Time
	ProjectsRoot string
	Scanned      int
	DryRun       bool
	// PruneArchived records whether --prune-archived was passed. Without it
	// the ArchivedNotPruned entries cannot be explained.
	PruneArchived bool
	// RunErr is set when the run failed before scanning anything — a GitHub
	// authentication or discovery failure. Results is empty in that case, and
	// the failure is itself the issue worth reporting.
	RunErr error
}

// IssuesMarkdown renders the attention and error groups as Markdown for a
// human or an AI agent to act on. It performs no IO and is deterministic:
// identical input always renders identical bytes.
func IssuesMarkdown(meta RunMeta, results []Result) string {
	var out strings.Builder
	out.WriteString("# WB sync issues\n\n")
	writeIssuesHeader(&out, meta)
	out.WriteString("All repositories are in sync. Nothing requires attention.\n")
	return out.String()
}

// writeIssuesHeader renders the run's provenance and counts. Later tasks widen
// it with the counts; this first version reports the clean run only.
func writeIssuesHeader(out *strings.Builder, meta RunMeta) {
	fmt.Fprintf(out, "**Run:** %s · **Projects root:** %s\n",
		meta.StartedAt.UTC().Format(time.RFC3339), meta.ProjectsRoot)
	fmt.Fprintf(out, "**Scanned:** %d repositories · **Issues:** none\n\n", meta.Scanned)
	if meta.DryRun {
		out.WriteString("**Dry run:** the fleet was not modified. Findings are real, but " +
			"unpushed-commit detection normally runs after a pull and may be incomplete.\n\n")
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/fleetsync/ -run TestIssuesMarkdown -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
gofmt -l .
git add internal/fleetsync/issues.go internal/fleetsync/issues_test.go
git commit -m "feat(sync): render the issues report header and clean-run body"
```

---

### Task 2: Attention entries

The four defect statuses, each with facts, read-only inspection commands, and remediation options. Plus the informational `ArchivedNotPruned` section, kept separate so an agent does not treat it as a defect.

**Files:**
- Modify: `internal/fleetsync/issues.go`
- Test: `internal/fleetsync/issues_test.go`

**Interfaces:**
- Consumes: `RunMeta`, `IssuesMarkdown` from Task 1; `Result.Tracking` (`gitops.TrackingState`, with `.Summary()`), `Result.Detail` (`gitops.RepoStatus`, with `.Summary()`), `Result.Repo.Path`, `Result.Reason`, `Result.ArchivedNotPruned`.
- Produces: `splitAttention(results []Result) (defects, informational []Result)` and `shellQuote(value string) string`, both used by Task 3.

- [ ] **Step 1: Write the failing test**

Append to `internal/fleetsync/issues_test.go`:

```go
func TestIssuesMarkdownRendersDivergedEntry(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "sneat-co", Name: "competios", Path: "/home/ai/projects/sneat-co/competios"},
		Status: Diverged,
		Tracking: gitops.TrackingState{
			Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 5, Configured: true,
		},
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"## Needs attention",
		"### sneat-co/competios — diverged",
		"**Clone:** `/home/ai/projects/sneat-co/competios`",
		"main is 2 ahead, 5 behind origin/main",
		"not pulled",
		"**Inspect**",
		"git -C /home/ai/projects/sneat-co/competios log --oneline --left-right main...origin/main",
		"**Resolve**",
		"wb worktree create",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownRendersNoUpstreamEntry(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "sneat-dev", Name: "wb", Path: "/home/ai/projects/sneat-dev/wb"},
		Status: NoUpstream,
		Tracking: gitops.TrackingState{
			Branch: "fix/auth", Configured: true,
		},
		Detail: gitops.RepoStatus{Unpushed: []string{"2fb7069 fix(sync): fail on auth"}},
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"### sneat-dev/wb — no upstream",
		"fix/auth tracks an upstream that no longer resolves",
		"git push -u origin fix/auth",
		"git -C /home/ai/projects/sneat-dev/wb log --oneline origin/main..HEAD",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownRendersUnpushedEntry(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"},
		Status: Unpushed,
		Detail: gitops.RepoStatus{Unpushed: []string{"abc1234 wip"}},
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"### o/r — unpushed commits",
		"pulled, but holds 1 unpushed commit",
		"git -C /p/o/r log --oneline --branches --not --remotes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownRendersArchivedUnlandableEntryWithReason(t *testing.T) {
	results := []Result{{
		Repo:     discover.Repo{Org: "o", Name: "old", Path: "/p/o/old"},
		Status:   ArchivedUnlandable,
		Archived: true,
		Detail:   gitops.RepoStatus{Unpushed: []string{"abc1234 wip", "def5678 more"}},
		Reason:   "2 unpushed commits on branch main",
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"### o/old — archived, holds unpushed commits",
		"can never be pushed",
		"2 unpushed commits on branch main",
		"unarchive",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownSeparatesArchivedNotPrunedFromDefects(t *testing.T) {
	results := []Result{{
		Repo:              discover.Repo{Org: "o", Name: "stale", Path: "/p/o/stale"},
		Status:            Pulled,
		Archived:          true,
		ArchivedNotPruned: true,
	}}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "## Archived, not pruned") {
		t.Errorf("missing informational section:\n%s", got)
	}
	if !strings.Contains(got, "Nothing is broken") {
		t.Errorf("informational section must say nothing is broken:\n%s", got)
	}
	if !strings.Contains(got, "--prune-archived") {
		t.Errorf("informational section must name the flag:\n%s", got)
	}
	if strings.Contains(got, "## Needs attention") {
		t.Errorf("an archived-not-pruned repo is not a defect:\n%s", got)
	}
}

func TestIssuesMarkdownQuotesPathsNeedingIt(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "o", Name: "r", Path: "/p/with space/o/r"},
		Status: Unpushed,
		Detail: gitops.RepoStatus{Unpushed: []string{"abc1234 wip"}},
	}}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "git -C '/p/with space/o/r'") {
		t.Errorf("path with a space must be shell-quoted:\n%s", got)
	}
}
```

Add the imports at the top of the test file:

```go
import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/fleetsync/ -run TestIssuesMarkdown -v`
Expected: FAIL — the clean-run body renders instead of any entry; `report missing "## Needs attention"`.

- [ ] **Step 3: Write the implementation**

Replace `IssuesMarkdown` in `internal/fleetsync/issues.go` and add the helpers below it:

```go
// IssuesMarkdown renders the attention and error groups as Markdown for a
// human or an AI agent to act on. It performs no IO and is deterministic:
// identical input always renders identical bytes.
func IssuesMarkdown(meta RunMeta, results []Result) string {
	var out strings.Builder
	out.WriteString("# WB sync issues\n\n")

	groups := Summary(results)
	attention, _ := SummaryGroupByLabel(groups, "Needs attention")
	defects, informational := splitAttention(attention.Results)

	writeIssuesHeader(&out, meta, len(defects), len(informational))

	if len(defects) == 0 && len(informational) == 0 {
		out.WriteString("All repositories are in sync. Nothing requires attention.\n")
		return out.String()
	}

	out.WriteString(inspectFirstNote)

	if len(defects) > 0 {
		out.WriteString("## Needs attention\n\n")
		for _, result := range defects {
			writeAttentionEntry(&out, result)
		}
	}
	if len(informational) > 0 {
		writeArchivedNotPruned(&out, informational)
	}
	return out.String()
}

// inspectFirstNote is the standing instruction to whoever reads the report.
// Resolution options are deliberately prose rather than a ready-to-paste
// command: rebasing or resetting the wrong clone is not recoverable from a
// report.
const inspectFirstNote = "> Inspection commands are read-only and safe to run as-is. Resolution " +
	"options are deliberately not copy-paste ready: inspect first, then choose.\n\n"

// splitAttention divides the attention group into genuine defects and the
// merely informational. An archived repository that was not pruned is in the
// attention group so it stays visible, but nothing about it is broken — the
// operator simply did not pass --prune-archived. Rendering it beside real
// defects would invite an agent to "fix" a repository that is fine.
//
// The two are mutually exclusive by construction: needsAttention matches the
// four defect statuses first and only falls through to ArchivedNotPruned in
// its default branch.
func splitAttention(results []Result) (defects, informational []Result) {
	for _, result := range results {
		switch result.Status {
		case Diverged, NoUpstream, Unpushed, ArchivedUnlandable:
			defects = append(defects, result)
		default:
			informational = append(informational, result)
		}
	}
	return defects, informational
}

func writeIssuesHeader(out *strings.Builder, meta RunMeta, defects, informational int) {
	fmt.Fprintf(out, "**Run:** %s · **Projects root:** %s\n",
		meta.StartedAt.UTC().Format(time.RFC3339), meta.ProjectsRoot)
	if defects == 0 && informational == 0 {
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Issues:** none\n\n", meta.Scanned)
	} else {
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Needs attention:** %d · **Archived, not pruned:** %d\n\n",
			meta.Scanned, defects, informational)
	}
	if meta.DryRun {
		out.WriteString("**Dry run:** the fleet was not modified. Findings are real, but " +
			"unpushed-commit detection normally runs after a pull and may be incomplete.\n\n")
	}
}

// writeAttentionEntry renders one defect: what it is, where it is, how to look
// at it, and what the options are.
func writeAttentionEntry(out *strings.Builder, result Result) {
	fmt.Fprintf(out, "### %s — %s\n\n", result.Repo.Slug(), result.Status)
	writeClone(out, result)

	switch result.Status {
	case Diverged, NoUpstream:
		fmt.Fprintf(out, "- **Tracking:** %s\n", result.Tracking.Summary())
		out.WriteString("- **Impact:** not pulled\n")
	case Unpushed:
		fmt.Fprintf(out, "- **Impact:** pulled, but holds %s\n", result.Detail.Summary())
	case ArchivedUnlandable:
		fmt.Fprintf(out, "- **Impact:** archived on GitHub, so its %s can never be pushed\n",
			result.Detail.Summary())
	}
	if result.Reason != "" {
		fmt.Fprintf(out, "- **Detail:** %s\n", result.Reason)
	}

	out.WriteString("\n**Inspect**\n\n```sh\n")
	for _, command := range inspectCommands(result) {
		out.WriteString(command + "\n")
	}
	out.WriteString("```\n\n**Resolve** — choose after inspecting:\n\n")
	for _, option := range resolveOptions(result) {
		out.WriteString("- " + option + "\n")
	}
	out.WriteString("\n")
}

func writeClone(out *strings.Builder, result Result) {
	if result.Repo.Path == "" {
		out.WriteString("- **Clone:** not present locally\n")
		return
	}
	fmt.Fprintf(out, "- **Clone:** `%s`\n", result.Repo.Path)
}

// inspectCommands returns read-only commands that show the reader exactly what
// state the repository is in. Nothing here mutates a repository.
func inspectCommands(result Result) []string {
	at := "git -C " + shellQuote(result.Repo.Path)
	switch result.Status {
	case Diverged:
		branch := result.Tracking.Branch
		return []string{
			fmt.Sprintf("%s log --oneline --left-right %s...%s", at, branch, result.Tracking.Upstream),
			fmt.Sprintf("%s cherry -v %s %s", at, result.Tracking.Upstream, branch),
			at + " status -sb",
		}
	case NoUpstream:
		return []string{
			at + " log --oneline origin/main..HEAD",
			at + " status -sb",
			at + " branch -vv",
		}
	case Unpushed, ArchivedUnlandable:
		return []string{
			at + " log --oneline --branches --not --remotes",
			at + " status -sb",
		}
	default:
		return []string{at + " status -sb"}
	}
}

// resolveOptions describes the ways out, with their consequences. A canonical
// clone is expected to sit on its base branch with nothing unpushed, so every
// option ends by pointing real work at a worktree rather than the clone.
func resolveOptions(result Result) []string {
	slug := result.Repo.Slug()
	worktree := fmt.Sprintf("Move real work off the canonical clone: `wb worktree create <task> %s`", slug)
	switch result.Status {
	case Diverged:
		return []string{
			"If the local commits are unlanded work, replay them onto the upstream: " +
				fmt.Sprintf("`git -C %s rebase %s`", shellQuote(result.Repo.Path), result.Tracking.Upstream),
			"If `git cherry` above marked every commit `-`, they already landed upstream under different SHAs; " +
				"reset the clone to its upstream instead of rebasing",
			worktree,
		}
	case NoUpstream:
		branch := result.Tracking.Branch
		return []string{
			fmt.Sprintf("Publish the branch: `git -C %s push -u origin %s`", shellQuote(result.Repo.Path), branch),
			"Or, if the work already landed upstream under a squashed commit, return the clone to its " +
				"base branch and delete the leftover branch",
			"Or `wb repo init-remote " + shellQuote(result.Repo.Path) + "` if the branch was never published at all",
			worktree,
		}
	case Unpushed:
		return []string{
			"Push the commits if they are finished work",
			"Or open a PR from a worktree if they still need review",
			"Or discard them if they were superseded — confirm with the log above first",
			worktree,
		}
	case ArchivedUnlandable:
		return []string{
			"Unarchive the repository on GitHub if the commits must land",
			"Or discard the commits and let `wb sync --prune-archived` remove the clone",
			"Never force the push: the remote is read-only while archived",
		}
	default:
		return []string{worktree}
	}
}

// writeArchivedNotPruned renders the informational section: archived
// repositories that were pulled like any other clone because pruning was not
// requested.
func writeArchivedNotPruned(out *strings.Builder, results []Result) {
	out.WriteString("## Archived, not pruned\n\n")
	out.WriteString("Informational: these are archived on GitHub and were pulled like any other clone " +
		"because `--prune-archived` was not passed. Nothing is broken.\n\n")
	for _, result := range results {
		fmt.Fprintf(out, "- `%s` — pass `--prune-archived` to evaluate it for cleanup\n", result.Repo.Slug())
	}
	out.WriteString("\n")
}

// safeShellWord matches a path that needs no quoting, so the common case reads
// as a plain command a human can scan.
var safeShellWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote renders value as a single POSIX shell word.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if safeShellWord.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
```

Add `"regexp"` to the import block in `internal/fleetsync/issues.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/fleetsync/ -run TestIssuesMarkdown -v`
Expected: PASS (9 tests).

- [ ] **Step 5: Commit**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
gofmt -l .
git add internal/fleetsync/issues.go internal/fleetsync/issues_test.go
git commit -m "feat(sync): render attention entries with inspection and resolution guidance"
```

---

### Task 3: Errors and the failed-run case

The `Errors` group, plus a run that died before scanning anything.

**Note on the intermediate state:** after Task 2 the renderer knowingly ignores
`Failed` results — a run with only errors still renders "All repositories are
in sync". That is wrong, and this task is what fixes it. Do not ship Task 2
without Task 3.

**Files:**
- Modify: `internal/fleetsync/issues.go`
- Test: `internal/fleetsync/issues_test.go`

**Interfaces:**
- Consumes: `splitAttention`, `shellQuote`, `writeClone`, `inspectFirstNote` from Task 2; `RunMeta.RunErr`.
- Produces: complete `IssuesMarkdown` behaviour — Task 5 needs no further renderer changes.

- [ ] **Step 1: Write the failing test**

Append to `internal/fleetsync/issues_test.go`:

```go
func TestIssuesMarkdownRendersErrorsVerbatim(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "o", Name: "broken", Path: "/p/o/broken"},
		Status: Failed,
		Err:    errors.New("git pull: could not read Username for 'https://github.com'"),
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"## Errors",
		"### o/broken — failed",
		"could not read Username for 'https://github.com'",
		"gh auth status -h github.com",
		"gh auth login -h github.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownCountsErrorsInHeader(t *testing.T) {
	results := []Result{
		{Repo: discover.Repo{Org: "o", Name: "a", Path: "/p/o/a"}, Status: Failed, Err: errors.New("boom")},
		{Repo: discover.Repo{Org: "o", Name: "b", Path: "/p/o/b"}, Status: Unpushed,
			Detail: gitops.RepoStatus{Unpushed: []string{"abc1234 wip"}}},
	}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "**Needs attention:** 1") {
		t.Errorf("attention count wrong:\n%s", got)
	}
	if !strings.Contains(got, "**Errors:** 1") {
		t.Errorf("error count wrong:\n%s", got)
	}
	if strings.Contains(got, "**Issues:** none") {
		t.Errorf("a run with issues must not claim none:\n%s", got)
	}
}

func TestIssuesMarkdownReportsRunThatFailedBeforeScanning(t *testing.T) {
	meta := testMeta()
	meta.Scanned = 0
	meta.RunErr = errors.New("GitHub authentication failed: gh: not logged in")
	got := IssuesMarkdown(meta, nil)
	for _, want := range []string{
		"## Run failed",
		"gh: not logged in",
		"no repository was scanned",
		"gh auth login -h github.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "All repositories are in sync") {
		t.Errorf("a failed run must never claim the fleet is in sync:\n%s", got)
	}
}
```

Add `"errors"` to the test file's import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/fleetsync/ -run TestIssuesMarkdown -v`
Expected: FAIL — `report missing "## Errors"`, and the failed-run case wrongly renders "All repositories are in sync".

- [ ] **Step 3: Write the implementation**

In `internal/fleetsync/issues.go`, replace `IssuesMarkdown` and `writeIssuesHeader`, and add the two new writers:

```go
// IssuesMarkdown renders the attention and error groups as Markdown for a
// human or an AI agent to act on. It performs no IO and is deterministic:
// identical input always renders identical bytes.
func IssuesMarkdown(meta RunMeta, results []Result) string {
	var out strings.Builder
	out.WriteString("# WB sync issues\n\n")

	groups := Summary(results)
	attention, _ := SummaryGroupByLabel(groups, "Needs attention")
	failures, _ := SummaryGroupByLabel(groups, "Errors")
	defects, informational := splitAttention(attention.Results)

	writeIssuesHeader(&out, meta, len(defects), len(informational), len(failures.Results))

	// A run that never scanned cannot say anything about the fleet, so it
	// reports only its own failure. Claiming "in sync" here would be a lie an
	// agent would act on.
	if meta.RunErr != nil {
		writeRunFailure(&out, meta)
		return out.String()
	}

	if len(defects) == 0 && len(informational) == 0 && len(failures.Results) == 0 {
		out.WriteString("All repositories are in sync. Nothing requires attention.\n")
		return out.String()
	}

	out.WriteString(inspectFirstNote)

	if len(defects) > 0 {
		out.WriteString("## Needs attention\n\n")
		for _, result := range defects {
			writeAttentionEntry(&out, result)
		}
	}
	if len(informational) > 0 {
		writeArchivedNotPruned(&out, informational)
	}
	if len(failures.Results) > 0 {
		out.WriteString("## Errors\n\n")
		for _, result := range failures.Results {
			writeErrorEntry(&out, result)
		}
	}
	return out.String()
}

func writeIssuesHeader(out *strings.Builder, meta RunMeta, defects, informational, failures int) {
	fmt.Fprintf(out, "**Run:** %s · **Projects root:** %s\n",
		meta.StartedAt.UTC().Format(time.RFC3339), meta.ProjectsRoot)
	switch {
	case meta.RunErr != nil:
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Run failed before scanning**\n\n", meta.Scanned)
	case defects == 0 && informational == 0 && failures == 0:
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Issues:** none\n\n", meta.Scanned)
	default:
		fmt.Fprintf(out,
			"**Scanned:** %d repositories · **Needs attention:** %d · **Archived, not pruned:** %d · **Errors:** %d\n\n",
			meta.Scanned, defects, informational, failures)
	}
	if meta.DryRun {
		out.WriteString("**Dry run:** the fleet was not modified. Findings are real, but " +
			"unpushed-commit detection normally runs after a pull and may be incomplete.\n\n")
	}
}

// writeErrorEntry renders one failed repository. The error value is reproduced
// verbatim: a re-worded error is a different error, and the reader may need to
// match it against Git's or gh's own output.
func writeErrorEntry(out *strings.Builder, result Result) {
	fmt.Fprintf(out, "### %s — failed\n\n", result.Repo.Slug())
	writeClone(out, result)
	fmt.Fprintf(out, "- **Error:** `%v`\n", result.Err)

	at := "git -C " + shellQuote(result.Repo.Path)
	out.WriteString("\n**Inspect**\n\n```sh\n")
	out.WriteString("gh auth status -h github.com\n")
	out.WriteString(at + " config --get remote.origin.url\n")
	out.WriteString(at + " status -sb\n")
	out.WriteString("```\n\n**Resolve** — choose after inspecting:\n\n")
	out.WriteString("- Re-authenticate if the error is about credentials: `gh auth login -h github.com`\n")
	out.WriteString("- Switch the remote to SSH if this clone was created outside WB with an HTTPS URL\n")
	out.WriteString("- Re-run the single repository to see the full Git output: " +
		fmt.Sprintf("`wb sync --filter %s`\n", result.Repo.Name))
	out.WriteString("\n")
}

// writeRunFailure reports a run that failed before scanning. Broken GitHub
// authentication leaves every clone unmanaged, so it is exactly the finding
// worth handing to an agent — and until now it existed only on stderr.
func writeRunFailure(out *strings.Builder, meta RunMeta) {
	out.WriteString("## Run failed\n\n")
	out.WriteString("The sync failed before it reached the fleet, so no repository was scanned and " +
		"nothing below reflects the state of any clone.\n\n")
	fmt.Fprintf(out, "- **Error:** `%v`\n", meta.RunErr)
	out.WriteString("\n**Inspect**\n\n```sh\ngh auth status -h github.com\n```\n\n")
	out.WriteString("**Resolve** — choose after inspecting:\n\n")
	out.WriteString("- Re-authenticate: `gh auth login -h github.com`\n")
	out.WriteString("- Then re-run `wb sync`; every clone is unmanaged until it succeeds\n")
}
```

- [ ] **Step 4: Run the full package suite**

Run: `go test ./internal/fleetsync/ -v`
Expected: PASS — the 12 `TestIssuesMarkdown*` tests plus every pre-existing `fleetsync` test.

- [ ] **Step 5: Commit**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
gofmt -l .
git add internal/fleetsync/issues.go internal/fleetsync/issues_test.go
git commit -m "feat(sync): report failed repositories and runs that never scanned"
```

---

### Task 4: The report writer

Path resolution, atomic write, and the "never fails a sync" policy.

**Files:**
- Create: `cmd/wb/sync_report.go`
- Test: `cmd/wb/sync_report_test.go`

**Interfaces:**
- Consumes: `fleetsync.RunMeta`, `fleetsync.IssuesMarkdown` from Tasks 1–3; `wbhome.EnsureRoot(projectsRoot string) (string, error)`.
- Produces: `func writeSyncIssuesReport(meta fleetsync.RunMeta, results []fleetsync.Result, projectsRoot string, out, errOut io.Writer)` and `const syncIssuesReportName = "last-sync-issues.md"`, both used by Task 5.

- [ ] **Step 1: Write the failing test**

Create `cmd/wb/sync_report_test.go`:

```go
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
)

// syncReportHome pins WB_HOME at a temporary directory. Every test here must
// use it: without it the writer targets the developer's real ~/.wb.
func syncReportHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	return home
}

func syncReportMetaForTest() fleetsync.RunMeta {
	return fleetsync.RunMeta{
		StartedAt:    time.Date(2026, 9, 1, 10, 15, 0, 0, time.UTC),
		ProjectsRoot: "/home/ai/projects",
		Scanned:      3,
	}
}

func TestWriteSyncIssuesReportWritesToWBHome(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer

	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	path := filepath.Join(home, "last-sync-issues.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	if !strings.Contains(string(contents), "# WB sync issues") {
		t.Errorf("unexpected contents:\n%s", contents)
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("path not announced on stdout: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr: %q", errOut.String())
	}
}

func TestWriteSyncIssuesReportOverwritesRatherThanAppends(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer
	path := filepath.Join(home, "last-sync-issues.md")

	results := []fleetsync.Result{{
		Repo:   discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"},
		Status: fleetsync.Failed,
		Err:    errors.New("boom"),
	}}
	writeSyncIssuesReport(syncReportMetaForTest(), results, "/home/ai/projects", &out, &errOut)
	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if strings.Contains(string(contents), "boom") {
		t.Errorf("second run must replace the first, not append:\n%s", contents)
	}
	if strings.Count(string(contents), "# WB sync issues") != 1 {
		t.Errorf("report written more than once:\n%s", contents)
	}
}

func TestWriteSyncIssuesReportLeavesNoTemporaryFile(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer

	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-sync-issues-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestWriteSyncIssuesReportWarnsWithoutFailingWhenHomeIsUnwritable(t *testing.T) {
	home := syncReportHome(t)
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	var out, errOut bytes.Buffer

	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	if !strings.Contains(errOut.String(), "sync issues report not written") {
		t.Errorf("failure not warned about: %q", errOut.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/wb/ -run TestWriteSyncIssuesReport -v`
Expected: FAIL — `undefined: writeSyncIssuesReport`.

- [ ] **Step 3: Write the implementation**

Create `cmd/wb/sync_report.go`:

```go
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// syncIssuesReportName is the stable filename under WB home. It is stable on
// purpose: the path is the instruction handed to an agent ("read
// ~/.wb/last-sync-issues.md and fix what it lists"), which a timestamped
// directory could not be.
const syncIssuesReportName = "last-sync-issues.md"

// writeSyncIssuesReport renders and writes the issues report, printing the
// path it wrote to out and any failure to errOut. It never fails a sync, so it
// returns nothing for a caller to check: sync's exit code reflects sync, never
// its reporting — the same policy finishSync already applies to a failed
// --publish and to refreshSyncedCheckoutMarkers.
func writeSyncIssuesReport(
	meta fleetsync.RunMeta,
	results []fleetsync.Result,
	projectsRoot string,
	out, errOut io.Writer,
) {
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "sync issues report not written:", err)
		return
	}
	path := filepath.Join(home, syncIssuesReportName)
	if err := writeSyncIssuesFile(path, fleetsync.IssuesMarkdown(meta, results)); err != nil {
		_, _ = fmt.Fprintln(errOut, "sync issues report not written:", err)
		return
	}
	_, _ = fmt.Fprintf(out, "Issues report: %s\n", path)
}

// writeSyncIssuesFile replaces the report through a temporary file in the same
// directory. An agent reads this path unprompted, so it must never observe a
// half-written report: rename is atomic, a partial write is not.
func writeSyncIssuesFile(path, contents string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".wb-sync-issues-*")
	if err != nil {
		return fmt.Errorf("stage a replacement for %s: %w", path, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/wb/ -run TestWriteSyncIssuesReport -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
gofmt -l .
git add cmd/wb/sync_report.go cmd/wb/sync_report_test.go
git commit -m "feat(sync): write the issues report atomically under WB home"
```

---

### Task 5: Wire the report into `wb sync`

**Files:**
- Modify: `cmd/wb/sync.go` (`runSync` at ~line 122, `finishSync` at ~line 170)
- Modify: `cmd/wb/remote_test.go` (existing `finishSync` callers)
- Test: `cmd/wb/sync_report_test.go`

**Interfaces:**
- Consumes: `writeSyncIssuesReport` from Task 4; `fleetsync.RunMeta` from Task 1.
- Produces: `finishSync` gains a leading `meta fleetsync.RunMeta` parameter. Any other caller must pass one.

- [ ] **Step 1: Write the failing test**

Append to `cmd/wb/sync_report_test.go`:

```go
func TestFinishSyncWritesReportEvenWhenARepositoryFailed(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer

	results := []fleetsync.Result{{
		Repo:   discover.Repo{Org: "o", Name: "broken", Path: "/p/o/broken"},
		Status: fleetsync.Failed,
		Err:    errors.New("git pull: transport failure"),
	}}
	code := finishSync(syncReportMetaForTest(), results, false, false, remoteDeps{},
		t.TempDir(), "", 1, &out, &errOut)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	contents, err := os.ReadFile(filepath.Join(home, "last-sync-issues.md"))
	if err != nil {
		t.Fatalf("a run with errors must still produce a report: %v", err)
	}
	if !strings.Contains(string(contents), "transport failure") {
		t.Errorf("error not reported:\n%s", contents)
	}
}

func TestFinishSyncReportFailureDoesNotChangeExitCode(t *testing.T) {
	home := syncReportHome(t)
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	var out, errOut bytes.Buffer

	code := finishSync(syncReportMetaForTest(), nil, false, false, remoteDeps{},
		t.TempDir(), "", 1, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0: an unwritable report must not fail a clean sync", code)
	}
	if !strings.Contains(errOut.String(), "sync issues report not written") {
		t.Errorf("failure not warned about: %q", errOut.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/wb/ -run 'TestFinishSync' -v`
Expected: FAIL to compile — `too many arguments in call to finishSync`.

- [ ] **Step 3: Change `finishSync` to write the report first**

In `cmd/wb/sync.go`, replace the `finishSync` signature and the top of its body:

```go
// finishSync maps sync results to an exit code, writes the issues report, and,
// when asked, publishes this machine's state. A publish failure is reported to
// errOut and never changes the sync exit code. dryRun short-circuits publish
// entirely: a `--dry-run --publish` sync changed nothing, so publishing its
// (unreal) outcome would be a lie.
func finishSync(meta fleetsync.RunMeta, results []fleetsync.Result, publish, dryRun bool, deps remoteDeps, projectsRoot, filter string, workers int, out, errOut io.Writer) int {
	// Written before the error short-circuit below, because a run WITH errors
	// is exactly the run whose report matters most. Unlike the checkout
	// markers, this also runs for a dry run: dry-run detection is read-only
	// and identical, so its findings are real — IssuesMarkdown stamps the
	// report so the reader knows the fleet was not actually pulled.
	writeSyncIssuesReport(meta, results, projectsRoot, out, errOut)

	hasErrors := false
	for _, res := range results {
		if res.Status == fleetsync.Failed {
			hasErrors = true
		}
	}

	if hasErrors {
		return 1
	}
	// … rest of finishSync unchanged …
```

- [ ] **Step 4: Build `RunMeta` in `runSync` and cover the early-failure paths**

In `cmd/wb/sync.go`, replace the head of `runSync`:

```go
func runSync(ctx context.Context, projectsRoot, filter string, only []string, workers int, dryRun, publish, pruneArchived bool, deps remoteDeps) int {
	startedAt := time.Now().UTC()
	meta := func(scanned int, runErr error) fleetsync.RunMeta {
		return fleetsync.RunMeta{
			StartedAt:     startedAt,
			ProjectsRoot:  projectsRoot,
			Scanned:       scanned,
			DryRun:        dryRun,
			PruneArchived: pruneArchived,
			RunErr:        runErr,
		}
	}

	owners, err := syncOwners(only)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wb: %v\nRe-authenticate with: gh auth login -h github.com\n", err)
		// Broken authentication leaves every clone unmanaged. That is a
		// finding worth handing to an agent, not something to leave on stderr.
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, os.Stdout, os.Stderr)
		return exitFindings
	}
	repos, err := fleet(projectsRoot, filter, func() []string { return owners })
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, os.Stdout, os.Stderr)
		return 1
	}
```

and its final line:

```go
	return finishSync(meta(len(results), nil), results, publish, dryRun, deps, projectsRoot, filter, workers, os.Stdout, os.Stderr)
}
```

Add `"time"` to the import block in `cmd/wb/sync.go`.

- [ ] **Step 5: Update the existing `finishSync` callers**

There are exactly four, all in `cmd/wb/remote_test.go` — currently at lines 503, 514, 526 and 545:

| Line | Test |
|---|---|
| 503 | `TestFinishSyncPublishFailureIsReportedButExitStaysZero` |
| 514 | `TestFinishSyncPublishesAfterCleanSync` |
| 526 | `TestFinishSyncSkipsPublishWhenSyncFailed` |
| 545 | `TestFinishSyncDryRunSkipsPublish` |

Confirm the list before editing:

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
grep -rn "finishSync(" cmd/wb/ --include="*_test.go"
```

Insert `fleetsync.RunMeta{}` as the first argument of each, e.g. line 514 becomes:

```go
	if code := finishSync(fleetsync.RunMeta{}, nil, true, false, f.deps("alice", time.Now().UTC()), f.projectsRoot, "", 1, &out, &errOut); code != 0 {
```

**No `t.Setenv` is needed here:** all four build their fixture with `newRemoteFixture(t, "laptop")`, which already pins `WB_HOME` to a temporary directory. The report lands there harmlessly.

These four tests' existing assertions still hold — the writer prints `Issues report: <path>` to `out`, which contains neither `"published"` nor `"skipping remote publish"`, and writes nothing to `errOut` on success. Do not weaken any of them.

- [ ] **Step 6: Run the full suite**

Run: `go build ./... && go test ./cmd/wb/ ./internal/fleetsync/`
Expected: PASS. If any `cmd/wb` test now writes to a real home, it will show up as a stray `~/.wb/last-sync-issues.md` — check with `ls -la ~/.wb/last-sync-issues.md` and add the missing `t.Setenv`.

- [ ] **Step 7: Commit**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
gofmt -l .
git add cmd/wb/sync.go cmd/wb/sync_report_test.go cmd/wb/remote_test.go
git commit -m "feat(sync): write the issues report on every sync run"
```

---

### Task 6: Capability manifest and agent skill documentation

`cmd/wb/skills_test.go` enforces the manifest; the skill reference is how an agent learns the report exists.

**Files:**
- Modify: `ai/capabilities.json` (the `wb.sync` entry, ~line 5654)
- Modify: `ai/skills/wb-fleet/references/sync.md`

**Interfaces:**
- Consumes: the behaviour built in Tasks 1–5. No Go code changes.

- [ ] **Step 1: Run the gate test to see it pass before the edit**

Run: `go test ./cmd/wb/ -run TestCapabilit -v`
Expected: PASS. This is the baseline — the manifest must still validate after the edit.

- [ ] **Step 2: Add the report to the `wb sync` runtime modes and notes**

In `ai/capabilities.json`, inside the `"id": "wb.sync"` entry, append this string to `surfaces.runtime.commands[0].modes`:

```json
"Every run writes ~/.wb/last-sync-issues.md (honouring $WB_HOME): a Markdown report of only the repositories needing attention plus errors, with read-only inspection commands and remediation options. It is written even on a clean run (stating no issues), on --dry-run (stamped), and when the run fails before scanning; a report that cannot be written is warned about and never changes the sync exit code."
```

and replace the entry's `notes` value with:

```json
"Canonical clones are always resolved under the deterministic owner/repository layout; dirty or unpushed clones are preserved. Archived-repository deletion is opt-in via --prune-archived and always defers to internal/archiveprune's predicate. The issues report at ~/.wb/last-sync-issues.md is the stable path to hand an agent; it is overwritten every run and never accumulates history."
```

- [ ] **Step 3: Register the new tests in the manifest**

In the same entry, append to `surfaces.tests.references`:

```json
{
  "path": "internal/fleetsync/issues_test.go",
  "name": "TestIssuesMarkdownRendersDivergedEntry",
  "kind": "unit"
},
{
  "path": "cmd/wb/sync_report_test.go",
  "name": "TestFinishSyncWritesReportEvenWhenARepositoryFailed",
  "kind": "integration"
}
```

- [ ] **Step 4: Tell agents to read the report**

Append to `ai/skills/wb-fleet/references/sync.md`:

```markdown
## Read the issues report

Every `wb sync` writes `~/.wb/last-sync-issues.md` (or `$WB_HOME/last-sync-issues.md`).
It lists only the repositories that need attention plus the errors — never the
successful ones — with the local clone path, the exact state, read-only
inspection commands, and the resolution options for each.

```sh
cat ~/.wb/last-sync-issues.md
```

The path is stable and the file is overwritten every run, so it always
describes the most recent sync and never a stale one. A clean run still writes
it, saying explicitly that there are no issues; a run that failed before
scanning reports that failure instead. Read it before deciding what to fix —
and run the inspection commands before any resolution command, because the
report deliberately does not spell out a ready-to-paste rebase or reset.
```

- [ ] **Step 5: Verify the manifest still validates**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
python3 -c "import json; json.load(open('ai/capabilities.json')); print('valid JSON')"
go test ./cmd/wb/ -run TestCapabilit -v
```
Expected: `valid JSON`, then PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
git add ai/capabilities.json ai/skills/wb-fleet/references/sync.md
git commit -m "docs(sync): advertise the issues report in the capability manifest and fleet skill"
```

---

### Task 7: Full verification

**Files:** none — this task only runs gates.

- [ ] **Step 1: Format check**

Run: `gofmt -l .`
Expected: no output.

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: no output.

- [ ] **Step 3: Full test suite**

Run: `go test ./...`
Expected: all packages `ok` or `no test files`.

- [ ] **Step 4: Lint**

Run: `golangci-lint run`
Expected: no findings.

- [ ] **Step 5: Coverage floor**

Run:
```bash
go run ./cmd/wb coverage . --test-shards 8 --shard-package ./internal/worktrees \
  --coverage-profile profile.cov --minimum=58 --format json --non-interactive --timeout 4m30s
```
Expected: exit 0. If below 58, add tests — do not lower the floor.

- [ ] **Step 6: Confirm no test polluted the real WB home**

Run: `ls -la ~/.wb/last-sync-issues.md 2>/dev/null || echo "clean — no stray report"`
Expected: `clean — no stray report`. If the file exists, a test is missing `t.Setenv("WB_HOME", t.TempDir())`; find it, fix it, and delete the stray file.

- [ ] **Step 7: Smoke-test against the real fleet**

Run: `wb sync --dry-run 2>&1 | tail -20` using the freshly built binary (`go build -o /tmp/wb-test ./cmd/wb && /tmp/wb-test sync --dry-run`), then:
```bash
cat ~/.wb/last-sync-issues.md
```
Expected: the report exists, is stamped as a dry run, and its findings match the terminal summary exactly.

- [ ] **Step 8: Clean up and push**

```bash
cd /home/ai/.wb/worktrees/sync-issues-report/sneat-dev/wb
rm -f profile.cov /tmp/wb-test
git status --short
git push -u origin sync-issues-report
```

---

## Self-Review Notes

**Spec coverage:** stable path (T4) · issues-only content (T2, T3) · always written including clean runs (T1, T3) · dry-run stamped (T1, T5) · run-failed-early (T3, T5) · five attention statuses with `ArchivedNotPruned` separated (T2) · errors verbatim (T3) · atomic write (T4) · never changes exit code (T4, T5) · `$WB_HOME` honoured (T4) · no new flags (Global Constraints) · capability manifest and skill docs (T6).

**Known deviation from the spec:** the spec's `RunMeta` example showed the header with a `Machine:` field; it is omitted because `RunMeta` has no machine identity and adding one would pull `internal/remotestate` config into a pure renderer. The spec's own output sample already omits it.

**Extension beyond the spec's literal text:** the spec's failed-run example shows only the `syncOwners` path. Task 5 also reports a `fleet()` discovery failure through the same `RunErr` field — both are "a run that failed before scanning", which is how the spec frames the case.
