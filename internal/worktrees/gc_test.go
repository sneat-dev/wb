package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// installPerCommitPullRequestFixture serves GitHub's commit-to-pull-request
// index per commit, which is what the residue walk needs: a head GitHub has
// never seen answers 422, an ancestor answers with its merged pull request.
func installPerCommitPullRequestFixture(t *testing.T, payloads map[string]string, unknown ...string) {
	t.Helper()
	binDir := t.TempDir()
	indexDir := t.TempDir()
	for sha, payload := range payloads {
		if err := os.WriteFile(filepath.Join(indexDir, sha+".json"), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, sha := range unknown {
		if err := os.WriteFile(filepath.Join(indexDir, sha+".unknown"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
set -eu
if [ "$1 $2" != "api --paginate" ]; then
    echo "unexpected gh command: $*" >&2
    exit 2
fi
sha=$(basename "$(dirname "$3")")
if [ -f "$WB_TEST_PR_INDEX/$sha.json" ]; then
    cat "$WB_TEST_PR_INDEX/$sha.json"
    exit 0
fi
if [ -f "$WB_TEST_PR_INDEX/$sha.unknown" ]; then
    printf '{"message":"No commit found for SHA: %s","status":"422"}' "$sha"
    exit 1
fi
printf '[]'
`
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_PR_INDEX", indexDir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func mergedPullRequestPayload(t *testing.T, number int, headSHA, mergeSHA string, mergedAt time.Time) string {
	t.Helper()
	payload, err := json.Marshal([]map[string]any{{
		"number":           number,
		"html_url":         "https://github.com/acme/app/pull/" + itoa(number),
		"state":            "closed",
		"merged_at":        mergedAt.Format(time.RFC3339),
		"head":             map[string]any{"ref": "feature/landed", "sha": headSHA},
		"base":             map[string]any{"ref": "main", "sha": ""},
		"merge_commit_sha": mergeSHA,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func openPullRequestPayload(t *testing.T, number int, headSHA string) string {
	t.Helper()
	payload, err := json.Marshal([]map[string]any{{
		"number":   number,
		"html_url": "https://github.com/acme/app/pull/" + itoa(number),
		"state":    "open",
		"head":     map[string]any{"ref": "feature/open", "sha": headSHA},
		"base":     map[string]any{"ref": "main", "sha": ""},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func entryFor(t *testing.T, outcome GCOutcome, task string) GCEntry {
	t.Helper()
	for _, entry := range outcome.Entries {
		if entry.Task == task {
			return entry
		}
	}
	t.Fatalf("task %q is missing from the gc plan: %#v", task, outcome.Entries)
	return GCEntry{}
}

// A squash merge leaves no ancestry, so git reports the branch as unmerged
// forever. GC must classify it removable on pull-request evidence.
func TestGCClassifiesASquashMergedWorktreeRemovable(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-squash-merged")
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-squash-merged"},
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, "gc-squash-merged")
	if entry.Class != GCClassLandedClean || !entry.Eligible {
		t.Fatalf("entry = %#v, want an eligible landed-clean classification", entry)
	}
	if entry.Applied {
		t.Fatal("a dry run must not remove anything")
	}
	if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
		t.Fatalf("dry run removed the worktree: %v", statErr)
	}
	if outcome.Refused() != 0 {
		t.Fatalf("totals = %#v, want nothing refused", outcome.Totals)
	}
	if outcome.Reclaimable.ApparentBytes <= 0 || outcome.Reclaimable.UnsharedBytes <= 0 {
		t.Fatalf("reclaimable = %#v, want both an apparent and an unshared figure", outcome.Reclaimable)
	}
}

func TestGCApplyRetiresTheSquashMergedWorktreeAndKeepsTheWorkLog(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-apply-squash")
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})
	worklogs := filepath.Join(fixture.home, "worklogs")

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-apply-squash"}, Apply: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, "gc-apply-squash")
	if !entry.Applied {
		t.Fatalf("entry = %#v, want it retired", entry)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("worktree survived apply: %v", statErr)
	}
	if outcome.Reclaimed.UnsharedBytes <= 0 {
		t.Fatalf("reclaimed = %#v, want the unshared bytes it actually returned", outcome.Reclaimed)
	}
	if entries, readErr := os.ReadDir(worklogs); readErr != nil || len(entries) == 0 {
		t.Fatalf("work logs must never be deleted by gc: entries=%v err=%v", entries, readErr)
	}
}

func TestGCKeepsDirtyAndOpenPullRequestCheckoutsWithOwnerAgeAndSanctionedCommand(t *testing.T) {
	fixture := newGitFixture(t)
	dirty, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "gc-dirty",
		WorkLog: WorkLogOptions{Model: "unknown", AgentID: "lane-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty[0].WorktreeDir, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "gc-open-pr",
		WorkLog: WorkLogOptions{Model: "unknown", AgentID: "lane-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(open[0].WorktreeDir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, open[0].WorktreeDir, "add", "feature.txt")
	gitTest(t, open[0].WorktreeDir, "commit", "-m", "feature")
	openHead := gitTestOutput(t, open[0].WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, open[0].WorktreeDir, "push", "-u", "origin", open[0].Branch)
	installPerCommitPullRequestFixture(t, map[string]string{
		openHead: openPullRequestPayload(t, 91, openHead),
	})

	// A TTL is what tells an abandoned worktree from a paused one, so the
	// report must carry it beside the owner and the age.
	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, TTL: time.Hour, SkipSizes: true,
		Now: func() time.Time { return time.Now().Add(48 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	dirtyEntry := entryFor(t, outcome, "gc-dirty")
	if dirtyEntry.Class != GCClassDirty || dirtyEntry.Eligible {
		t.Fatalf("dirty entry = %#v", dirtyEntry)
	}
	if dirtyEntry.SanctionedCommand == "" || !strings.HasPrefix(dirtyEntry.SanctionedCommand, "wb ") {
		t.Fatalf("every refusal names a sanctioned wb command: %#v", dirtyEntry)
	}
	if dirtyEntry.Owner != "lane-a" || dirtyEntry.CreatedAt.IsZero() || dirtyEntry.TTLSeconds == 0 || !dirtyEntry.Expired {
		t.Fatalf("dirty entry lost its owner/age/TTL: %#v", dirtyEntry)
	}
	openEntry := entryFor(t, outcome, "gc-open-pr")
	if openEntry.Class != GCClassOpenPR || openEntry.Eligible || openEntry.PullRequest == nil {
		t.Fatalf("open-pr entry = %#v", openEntry)
	}
	if outcome.Refused() != 2 {
		t.Fatalf("totals = %#v, want two refusals", outcome.Totals)
	}
}

// Every pull-request review creates a detached checkout, and nothing in WB
// could see one, let alone retire it: the inventory showed 50 rows for 60
// checkouts.
func TestGCInventoriesAndRetiresADetachedReviewCheckout(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-review-source")
	reviewTask := "gc-review-checkout"
	reviewDir := filepath.Join(fixture.home, "worktrees", reviewTask, "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", reviewDir, head)
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{reviewTask}, SkipSizes: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, reviewTask)
	if !entry.Detached || entry.Class != GCClassDetachedReview || !entry.Eligible {
		t.Fatalf("detached review entry = %#v, want an eligible detached-review row", entry)
	}
	if entry.Branch != "" {
		t.Fatalf("a detached checkout has no branch: %#v", entry)
	}
	_ = result

	applied, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{reviewTask}, Apply: true, SkipSizes: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Entries[0].Error != "" || !applied.Entries[0].Applied {
		t.Fatalf("detached review checkout was not retired: %#v", applied.Entries[0])
	}
	if _, statErr := os.Stat(reviewDir); !os.IsNotExist(statErr) {
		t.Fatalf("detached review checkout survived: %v", statErr)
	}
}

func TestGCRefusesADetachedCheckoutWithNoLanding(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "unreviewed.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "unreviewed.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "unreviewed")
	detachedHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "reset", "--hard", head)
	reviewDir := filepath.Join(fixture.home, "worktrees", "gc-detached-unknown", "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", reviewDir, detachedHead)
	installPerCommitPullRequestFixture(t, nil, detachedHead)

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-detached-unknown"}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, "gc-detached-unknown")
	if entry.Class != GCClassDetachedUnknown || entry.Eligible {
		t.Fatalf("entry = %#v, want a refused detached-unknown row", entry)
	}
	if !strings.Contains(entry.Reason, "never pushed") {
		t.Fatalf("a detached head GitHub has never seen must say so: %q", entry.Reason)
	}
	if entry.SanctionedCommand == "" {
		t.Fatal("the refusal must name the sanctioned command")
	}
}

// The dominant false negative in the measured sweep: 7 of 11 refusals were
// demonstrably merged branches carrying one residual local commit, every one
// reported as a bare "awaiting push".
func TestGCReportsLandedWithResidueAndRetiresItOnlyWithAllowResidue(t *testing.T) {
	fixture, result, landedHead, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-residue")
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "residual.txt"), []byte("post-merge tidy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "residual.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "post-merge tidy")
	residualHead := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	installPerCommitPullRequestFixture(t, map[string]string{
		landedHead: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	}, residualHead)

	plan, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-residue"}, SkipSizes: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, plan, "gc-residue")
	if entry.Class != GCClassLandedResidue || entry.Eligible {
		t.Fatalf("entry = %#v, want a refused landed-residue row", entry)
	}
	if entry.Landing == nil || entry.Landing.LandedSHA != landedHead || len(entry.Landing.Residue) != 1 {
		t.Fatalf("landing evidence = %#v", entry.Landing)
	}
	if !strings.Contains(entry.Reason, "landed + residue") || !strings.Contains(entry.Reason, "post-merge tidy") {
		t.Fatalf("the residual commits are the thing to show: %q", entry.Reason)
	}
	if !strings.Contains(entry.SanctionedCommand, "--allow-residue") {
		t.Fatalf("sanctioned command = %q", entry.SanctionedCommand)
	}

	widened, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-residue"},
		AllowResidue: true, Apply: true, SkipSizes: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	widenedEntry := entryFor(t, widened, "gc-residue")
	if !widenedEntry.Applied || widenedEntry.Error != "" {
		t.Fatalf("--allow-residue must retire it: %#v", widenedEntry)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("worktree survived: %v", statErr)
	}
}

func TestGCPurgesTerminalArtefactsSilentlyOnItsOwnReadPath(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "gc-artefacts",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(fixture.home, "worktrees", "gc-artefacts")
	if err := os.Mkdir(filepath.Join(taskPath, testRetiredStage), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskPath, testRetiredLock), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	installPerCommitPullRequestFixture(t, nil, gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD"))

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-artefacts"}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	purgedPaths := map[string]bool{}
	for _, artefact := range outcome.Purged {
		if artefact.Kind != purgedRetiredStage && artefact.Kind != purgedRetiredLock {
			t.Fatalf("gc purged something that is not a terminal artefact: %#v", artefact)
		}
		purgedPaths[artefact.Path] = true
	}
	for _, name := range []string{testRetiredStage, testRetiredLock} {
		path := filepath.Join(taskPath, name)
		if !purgedPaths[path] {
			t.Fatalf("%s is missing from the purge receipt: %#v", name, outcome.Purged)
		}
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("%s survived a gc read path: %v", name, statErr)
		}
	}
}

// gc's refusals never escalate: --allow-residue is the only widening, and the
// type itself must not grow a force. This is a contract test, not a style
// preference — a force flag here would be a way to delete unpushed work.
func TestGCHasNoForceShapedOption(t *testing.T) {
	forbidden := []string{"force", "yes", "skipchecks", "noverify", "override"}
	value := reflect.TypeOf(GCOptions{})
	for index := 0; index < value.NumField(); index++ {
		name := strings.ToLower(value.Field(index).Name)
		for _, banned := range forbidden {
			if strings.Contains(name, banned) {
				t.Fatalf("GCOptions.%s is a force-shaped widening", value.Field(index).Name)
			}
		}
	}
}

// M1: a refusal must name a command that exists AND works on the shape it was
// named for. This executes the sanctioned command for a detached checkout with
// no landing association, on exactly that shape, and requires it to succeed.
func TestGCDetachedUnknownSanctionedCommandActuallyRetiresThatShape(t *testing.T) {
	fixture := newGitFixture(t)
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "unreviewed.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "unreviewed.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "unreviewed")
	detachedHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "reset", "--hard", base)
	const task = "gc-detached-sanctioned"
	reviewDir := filepath.Join(fixture.home, "worktrees", task, "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", reviewDir, detachedHead)
	installPerCommitPullRequestFixture(t, nil, detachedHead)

	plan, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, plan, task)
	if entry.Class != GCClassDetachedUnknown || entry.Eligible {
		t.Fatalf("entry = %#v", entry)
	}
	// This checkout was made with `git worktree add --detach`, exactly as a
	// pull-request review makes one, so WB holds no Work Log for it and must
	// not name a wb verb that would refuse.
	if entry.Managed {
		t.Fatalf("entry = %#v, want it recognised as one WB did not create", entry)
	}
	if strings.Contains(entry.SanctionedCommand, "wb worktree rescue") ||
		strings.Contains(entry.SanctionedCommand, "wb worktree adopt") {
		t.Fatalf("sanctioned command = %q, but rescue refuses a linked worktree and adopt refuses a detached HEAD",
			entry.SanctionedCommand)
	}
	// Run exactly what the refusal names, on exactly this shape.
	if err := runSanctionedCommand(t, entry.SanctionedCommand); err != nil {
		t.Fatalf("the command a refusal names must work on the shape it names it for: %v", err)
	}
	if _, statErr := os.Stat(reviewDir); !os.IsNotExist(statErr) {
		t.Fatalf("detached checkout survived its own sanctioned command: %v", statErr)
	}

	// A WB-created checkout keeps the audited path: abort seals its Work Log
	// and captures its bytes before anything is removed.
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "gc-managed-abort",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	managedPlan, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-managed-abort"}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	managedEntry := entryFor(t, managedPlan, "gc-managed-abort")
	if !managedEntry.Managed {
		t.Fatalf("a WB-created checkout must be recognised as managed: %#v", managedEntry)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "gc-managed-abort",
		Disposition: AbortDiscarded, Apply: true, DeleteRemote: true,
	})
	if err != nil {
		t.Fatalf("abort on a WB-created checkout: %v", err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone {
		t.Fatalf("abort = %#v", results)
	}
	_ = created
}

// M2: the same requirement for the `dirty` class, on the hardest shape — a
// checkout that is both dirty and detached, which the inventory used to drop.
func TestGCDirtySanctionedCommandWorksOnADirtyDetachedCheckout(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	const task = "gc-dirty-detached"
	worktree := filepath.Join(fixture.home, "worktrees", task, "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", worktree, head)
	if err := os.WriteFile(filepath.Join(worktree, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installPerCommitPullRequestFixture(t, nil, head)

	plan, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, plan, task)
	if entry.Class != GCClassDirty || entry.Eligible || entry.Managed {
		t.Fatalf("a dirty detached checkout = %#v, want a refused, unmanaged dirty row", entry)
	}
	// WB never created this checkout, so it holds no Work Log to seal and
	// `wb worktree abort` would refuse the dirty capture. Naming abort here
	// would hand the operator a command that fails on the exact shape it was
	// named for, which is the defect this rule exists to prevent.
	if strings.Contains(entry.SanctionedCommand, "wb worktree abort") {
		t.Fatalf("sanctioned command = %q, but abort cannot seal a checkout WB never recorded", entry.SanctionedCommand)
	}
	if !strings.Contains(entry.Reason, "no Work Log claim") {
		t.Fatalf("reason = %q, want it to say why WB will not delete this", entry.Reason)
	}
	// Prove the named command works on this exact shape.
	if err := runSanctionedCommand(t, entry.SanctionedCommand); err != nil {
		t.Fatalf("the command a refusal names must work on the shape it names it for: %v", err)
	}
	if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
		t.Fatalf("dirty detached checkout survived its own sanctioned command: %v", statErr)
	}

	// And abort remains correct on the managed dirty shape it IS named for.
	managed, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "gc-dirty-managed",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed[0].WorktreeDir, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedPlan, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-dirty-managed"}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	managedEntry := entryFor(t, managedPlan, "gc-dirty-managed")
	if !managedEntry.Managed || managedEntry.SanctionedCommand != "wb worktree abort gc-dirty-managed --apply" {
		t.Fatalf("managed dirty entry = %#v", managedEntry)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "gc-dirty-managed",
		Disposition: AbortDiscarded, Apply: true, DeleteRemote: true,
	})
	if err != nil {
		t.Fatalf("abort on the managed dirty checkout it is named for: %v", err)
	}
	if len(results) != 1 || !results[0].Applied || results[0].DirtyCapture == nil {
		t.Fatalf("abort on a managed dirty checkout = %#v", results)
	}
}

// runSanctionedCommand executes a printed sanctioned command literally, the way
// an operator or an agent would paste it. Only the git form is executable from
// here; a wb form is exercised through its own package entry point.
func runSanctionedCommand(t *testing.T, command string) error {
	t.Helper()
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] != "git" {
		t.Fatalf("not an executable sanctioned command: %q", command)
	}
	run := exec.Command(fields[0], fields[1:]...)
	if output, err := run.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v\n%s", command, err, output)
	}
	return nil
}

// S2: a live session outranks a landed head. Its work having landed makes it
// more likely to be mid-next-round, not less.
func TestGCKeepsAClaimedLiveCheckoutEvenWhenItsWorkLanded(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-live-owner")
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})
	if err := RecordCustody(result.WorktreeDir, "gc-live-owner", "test", AgentIdentity{
		AgentID: "lane-a", Model: "unknown", PID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-live-owner"}, SkipSizes: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, "gc-live-owner")
	if entry.Class != GCClassClaimedLive || entry.Eligible {
		t.Fatalf("entry = %#v, want a landed checkout kept because its session is live", entry)
	}
	if entry.SanctionedCommand != "wb worktree end gc-live-owner" {
		t.Fatalf("sanctioned command = %q", entry.SanctionedCommand)
	}
}

// S3: a repository the grace window held back is still a repository left
// behind, and a coordinated task has to name it.
func TestGCNamesARepositoryHeldBackByTheGraceWindow(t *testing.T) {
	fixture, _, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-grace")
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-grace"}, SkipSizes: true,
		OlderThan: 24 * time.Hour,
		Now:       func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, "gc-grace")
	if entry.Eligible || !strings.Contains(entry.Reason, "safety window") {
		t.Fatalf("entry = %#v, want it held back by the grace window", entry)
	}
	if !strings.Contains(entry.SanctionedCommand, "--older-than 0") {
		t.Fatalf("sanctioned command = %q", entry.SanctionedCommand)
	}
	if outcome.Refused() != 1 {
		t.Fatalf("a checkout held back by the window is a refusal: %#v", outcome.Totals)
	}
}

// S1: the detached-review label must be justified by what decided it.
func TestGCDetachedReviewRowNamesTheEvidenceThatDecidedIt(t *testing.T) {
	fixture, _, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-review-evidence-source")
	const task = "gc-review-evidence"
	reviewDir := filepath.Join(fixture.home, "worktrees", task, "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", reviewDir, head)
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, task)
	if entry.Class != GCClassDetachedReview || !entry.Eligible {
		t.Fatalf("entry = %#v", entry)
	}
	if !strings.Contains(entry.Reason, "pull/77") && !strings.Contains(entry.Reason, "landed at") {
		t.Fatalf("reason = %q, want it to name the evidence rather than assert intent", entry.Reason)
	}
}
