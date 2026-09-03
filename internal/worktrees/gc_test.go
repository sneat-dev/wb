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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-squash-merged"},
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-apply-squash"}, Apply: true,
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, TTL: time.Hour, SkipSizes: true,
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{reviewTask}, SkipSizes: true,
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
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{reviewTask}, Apply: true, SkipSizes: true,
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-detached-unknown"}, SkipSizes: true,
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-residue"}, SkipSizes: true,
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
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-residue"},
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-artefacts"}, SkipSizes: true,
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
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
	if entry.Management != ManagementUnknown {
		t.Fatalf("entry management = %q, want unknown: WB wrote no manifest for this checkout", entry.Management)
	}
	if strings.Contains(entry.SanctionedCommand, "wb worktree rescue") ||
		strings.Contains(entry.SanctionedCommand, "wb worktree adopt") {
		t.Fatalf("sanctioned command = %q, but rescue refuses a linked worktree and adopt refuses a detached HEAD",
			entry.SanctionedCommand)
	}
	// Run exactly what the refusal names, on exactly this shape.
	if err := runSanctionedCommand(t, fixture.projectsRoot, entry.SanctionedCommand); err != nil {
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
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-managed-abort"}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	managedEntry := entryFor(t, managedPlan, "gc-managed-abort")
	if managedEntry.Management != ManagementManaged {
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, plan, task)
	if entry.Class != GCClassDirty || entry.Eligible || entry.Management != ManagementUnknown {
		t.Fatalf("a dirty detached checkout = %#v, want a refused row WB does not claim to know", entry)
	}
	// WB never created this checkout, so it holds no Work Log to seal and
	// `wb worktree abort` would refuse the dirty capture. Naming abort here
	// would hand the operator a command that fails on the exact shape it was
	// named for, which is the defect this rule exists to prevent.
	if strings.Contains(entry.SanctionedCommand, "wb worktree abort") {
		t.Fatalf("sanctioned command = %q, but abort cannot seal a checkout WB never recorded", entry.SanctionedCommand)
	}
	// And it must not name a removal either. The changes are uncommitted: a
	// removal is the one thing that makes them unrecoverable, and printing it
	// as the resolution is an instruction to destroy them.
	if strings.Contains(entry.SanctionedCommand, "worktree remove") || strings.Contains(entry.SanctionedCommand, "--force") {
		t.Fatalf("sanctioned command = %q, but a dirty checkout must be captured before anything is removed", entry.SanctionedCommand)
	}
	if !strings.Contains(entry.Reason, "no Work Log") || !strings.Contains(entry.Reason, "capture") {
		t.Fatalf("reason = %q, want it to say what WB knows and what to do first", entry.Reason)
	}
	if len(entry.Warnings) == 0 || !strings.Contains(strings.Join(entry.Warnings, " "), "only copy") {
		t.Fatalf("warnings = %#v, want the row to state exactly what skipping the capture discards", entry.Warnings)
	}

	// Run exactly what the row printed, then prove the content survived it.
	if err := runSanctionedCommand(t, fixture.projectsRoot, entry.SanctionedCommand); err != nil {
		t.Fatalf("the command a refusal names must work on the shape it names it for: %v", err)
	}
	stashed := runGitTestOutput(t, worktree, "stash", "list")
	if !strings.Contains(stashed, "wb gc "+task) {
		t.Fatalf("the capture left no stash entry: %q", stashed)
	}
	if content := runGitTestOutput(t, worktree, "show", "stash@{0}^3:wip.txt"); !strings.Contains(content, "in progress") {
		t.Fatalf("the captured content is not recoverable: %q", content)
	}
	// The checkout is clean now, so the next sweep classifies it on its own
	// evidence and names a removal that Git itself would refuse if it were not.
	clean, err := GC(context.Background(), GCOptions{
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanEntry := entryFor(t, clean, task)
	if cleanEntry.Class == GCClassDirty {
		t.Fatalf("after the capture the checkout is clean: %#v", cleanEntry)
	}
	if cleanEntry.Eligible {
		// Nothing left to name: gc itself retires it.
		applied, applyErr := GC(context.Background(), GCOptions{
			SessionFreshness: DisableSessionFreshness,
			ProjectsRoot:     fixture.projectsRoot, Tasks: []string{task}, Apply: true, SkipSizes: true,
		})
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		if !entryFor(t, applied, task).Applied {
			t.Fatalf("gc did not retire the now-clean checkout: %#v", applied.Entries)
		}
	} else if err := runSanctionedCommand(t, fixture.projectsRoot, cleanEntry.SanctionedCommand); err != nil {
		t.Fatalf("the follow-up command must work too: %v", err)
	}
	if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
		t.Fatalf("checkout survived its own sanctioned command: %v", statErr)
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
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-dirty-managed"}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	managedEntry := entryFor(t, managedPlan, "gc-dirty-managed")
	if managedEntry.Management != ManagementManaged || managedEntry.SanctionedCommand != "wb worktree abort gc-dirty-managed --apply" {
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
// an operator or an agent would paste it. Both halves of the refusal table must
// be executable from here, or only half of it is actually validated: a `git`
// form runs as the process it names, and a `wb` form is dispatched to the same
// package entry point the CLI calls.
func runSanctionedCommand(t *testing.T, projectsRoot, command string) error {
	t.Helper()
	fields := strings.Fields(command)
	if len(fields) == 0 {
		t.Fatal("empty sanctioned command")
	}
	switch fields[0] {
	case "git":
		// Quoted arguments (a stash message) survive the split as separate
		// fields; rejoin them so the command runs exactly as printed.
		run := exec.Command(fields[0], unquoteFields(fields[1:])...)
		if output, err := run.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v\n%s", command, err, output)
		}
		return nil
	case "wb":
		return runSanctionedWBCommand(t, projectsRoot, fields[1:])
	default:
		t.Fatalf("not an executable sanctioned command: %q", command)
		return nil
	}
}

// runSanctionedWBCommand dispatches the wb verbs the gc refusal table names.
// An unrecognised one fails the test rather than passing silently: the table
// must not name a verb this harness cannot prove works.
func runSanctionedWBCommand(t *testing.T, projectsRoot string, fields []string) error {
	t.Helper()
	if len(fields) >= 3 && fields[0] == "worktree" && fields[1] == "abort" {
		options := AbortOptions{
			ProjectsRoot: projectsRoot, Task: fields[2],
			Disposition: AbortDiscarded, DeleteRemote: true,
		}
		for index := 3; index < len(fields); index++ {
			switch fields[index] {
			case "--apply":
				options.Apply = true
			case "--disposition":
				if index+1 < len(fields) {
					options.Disposition = AbortDisposition(fields[index+1])
					index++
				}
			}
		}
		results, err := Abort(context.Background(), options)
		if err != nil {
			return err
		}
		for _, result := range results {
			if !result.Applied {
				return fmt.Errorf("wb worktree abort %s did not apply: %s", fields[2], result.Reason)
			}
		}
		return nil
	}
	t.Fatalf("gc named a wb verb this harness cannot execute: wb %s", strings.Join(fields, " "))
	return nil
}

// unquoteFields rejoins the fields of a shell-quoted argument.
func unquoteFields(fields []string) []string {
	joined := strings.Join(fields, " ")
	arguments := make([]string, 0, len(fields))
	for len(joined) > 0 {
		joined = strings.TrimLeft(joined, " ")
		if joined == "" {
			break
		}
		if joined[0] == '"' {
			end := strings.IndexByte(joined[1:], '"')
			if end < 0 {
				arguments = append(arguments, joined[1:])
				break
			}
			arguments = append(arguments, joined[1:1+end])
			joined = joined[end+2:]
			continue
		}
		end := strings.IndexByte(joined, ' ')
		if end < 0 {
			arguments = append(arguments, joined)
			break
		}
		arguments = append(arguments, joined[:end])
		joined = joined[end:]
	}
	return arguments
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-grace"}, SkipSizes: true,
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
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
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

func runGitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(output)
}

// S10: absence of a manifest and a manifest that will not read are different
// facts, and only the second is a claim that the checkout is foreign.
func TestWorktreeManagementSeparatesAbsentFromInvalid(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "management",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := worktreeManagement(created[0].WorktreeDir); got != ManagementManaged {
		t.Fatalf("a WB-created checkout = %q, want managed", got)
	}
	bare := filepath.Join(t.TempDir(), "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := worktreeManagement(bare); got != ManagementUnknown {
		t.Fatalf("a checkout with no manifest = %q, want unknown", got)
	}
	manifest := filepath.Join(created[0].WorktreeDir, journalRootDirectory, journalLocalDirectory, manifestName)
	if _, statErr := os.Stat(manifest); statErr != nil {
		t.Fatalf("a created worktree must carry its manifest: %v", statErr)
	}
	if err := os.WriteFile(manifest, []byte("this: [is not: yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := worktreeManagement(created[0].WorktreeDir); got != ManagementUnmanaged {
		t.Fatalf("a manifest that will not read = %q, want unmanaged", got)
	}
}

// S11 and S12: the shell sweep is scoped to the tasks the run selected, and a
// dry run says what an apply would do.
func TestGCPlansEmptyShellsAndScopesThemToTheNamedTask(t *testing.T) {
	fixture := newGitFixture(t)
	worktreesRoot := filepath.Join(fixture.home, "worktrees")
	for _, task := range []string{"gc-shell-named", "gc-shell-other"} {
		if err := os.MkdirAll(filepath.Join(worktreesRoot, task, "acme", "app"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installPerCommitPullRequestFixture(t, nil)

	plan, err := GC(context.Background(), GCOptions{
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-shell-named"}, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Totals["eligible_shells"] != 1 {
		t.Fatalf("a dry run must state the shells an apply would retire: %#v", plan.Totals)
	}
	for _, shell := range plan.Shells {
		if shell.Task != "gc-shell-named" {
			t.Fatalf("the shell sweep reached outside the named task: %#v", shell)
		}
		if shell.Applied {
			t.Fatalf("a dry run retired a shell: %#v", shell)
		}
	}

	applied, err := GC(context.Background(), GCOptions{
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-shell-named"}, Apply: true, SkipSizes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Totals["retired_shells"] != 1 {
		t.Fatalf("apply did not retire the planned shell: %#v", applied.Totals)
	}
	if _, statErr := os.Stat(filepath.Join(worktreesRoot, "gc-shell-named")); !os.IsNotExist(statErr) {
		t.Fatalf("named task shell survived: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(worktreesRoot, "gc-shell-other")); statErr != nil {
		t.Fatalf("a task the run did not select was swept anyway: %v", statErr)
	}
}

// A live process id is not a heartbeat, and neither is the owner registration:
// RecordCustody deduplicates on identity, so a lane can run four verbs and
// advance nothing. Both mistakes were made in turn, and the second one nearly
// deleted a working lane's checkout. Freshness is measured from what the lane
// actually did.
func TestGCMeasuresFreshnessFromActivityNotFromAProcessId(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-activity")
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})
	if err := RecordCustody(result.WorktreeDir, "gc-activity", "test", AgentIdentity{
		AgentID: "lane-a", Model: "unknown", PID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}

	// A lane that only READS — no commit, no edit, no Work Log write — stays
	// live, because every wb invocation inside the checkout refreshes its
	// heartbeat. This is the case the previous rule got wrong: it saw four
	// verbs, one deduplicated owner record, and called a working lane recycled.
	TouchHeartbeat(result.WorktreeDir, "wb worktree summary")
	reading, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-activity"}, SkipSizes: true,
		Now: func() time.Time { return time.Now().Add(3 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry := entryFor(t, reading, "gc-activity"); entry.Class != GCClassClaimedLive || entry.Eligible {
		t.Fatalf("a lane three hours into its work = %#v, want it kept", entry)
	}

	// Seven hours with no activity of any kind: the process id means nothing,
	// the landing evidence decides, and the stale owner is named.
	stale, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-activity"}, SkipSizes: true,
		Now: func() time.Time { return time.Now().Add(7 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, stale, "gc-activity")
	if entry.Class != GCClassLandedClean || !entry.Eligible {
		t.Fatalf("a landed checkout untouched for seven hours = %#v", entry)
	}
	if len(entry.Warnings) == 0 || !strings.Contains(strings.Join(entry.Warnings, " "), "recycled") {
		t.Fatalf("warnings = %#v, want the stale owner named", entry.Warnings)
	}
}

// A lane that only edits files — no commits, no wb verbs — is working, and
// removing its checkout would take that work away.
func TestGCSeesAnEditedFileAsActivity(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-editing")
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})
	// Age every other signal out of the window, then edit one file.
	backdateTree(t, filepath.Join(result.WorktreeDir, journalRootDirectory), -9*time.Hour)
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "edited.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "gc-editing", Activity: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 {
		t.Fatalf("inventory = %#v", listed.Results)
	}
	if since := time.Since(listed.Results[0].LastActivityAt); since > time.Minute {
		t.Fatalf("an edited file must register as activity: last activity was %s ago", since)
	}
	if !checkoutIsInUse(listed.Results[0], GCOptions{}, time.Now().Add(time.Minute)) {
		t.Fatal("a checkout someone is editing is in use")
	}
	if checkoutIsInUse(listed.Results[0], GCOptions{}, time.Now().Add(9*time.Hour)) {
		t.Fatal("nine hours after the last edit it is not")
	}
}

// Zero disables the rule, exactly like --older-than 0.
func TestGCSessionFreshnessZeroDisablesTheInUseRule(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-freshness-off")
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})
	if err := RecordCustody(result.WorktreeDir, "gc-freshness-off", "test", AgentIdentity{
		AgentID: "lane-a", Model: "unknown", PID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}
	TouchHeartbeat(result.WorktreeDir, "wb worktree summary")

	outcome, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{"gc-freshness-off"}, SkipSizes: true,
		SessionFreshness: DisableSessionFreshness,
		Now:              func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry := entryFor(t, outcome, "gc-freshness-off"); entry.Class == GCClassClaimedLive {
		t.Fatalf("--session-freshness 0 must disable the rule: %#v", entry)
	}
}

// The heartbeat is keyed to the directory the command ran in, so a lane's own
// commands keep its checkout alive and a sweep run from elsewhere keeps nothing
// alive.
func TestHeartbeatIsScopedToTheDirectoryTheCommandRanIn(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "heartbeat-scope",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(created[0].WorktreeDir, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := worktreeRootOf(nested)
	if err != nil || root != created[0].WorktreeDir {
		t.Fatalf("worktreeRootOf(%s) = %q, %v", nested, root, err)
	}
	outside, err := worktreeRootOf(t.TempDir())
	if err != nil || outside != "" {
		t.Fatalf("a directory outside every worktree resolves to %q, %v", outside, err)
	}

	if before := HeartbeatAt(created[0].WorktreeDir); !before.IsZero() {
		t.Fatalf("a fresh checkout has no heartbeat yet: %s", before)
	}
	TouchHeartbeat(created[0].WorktreeDir, "wb worktree info")
	if at := HeartbeatAt(created[0].WorktreeDir); time.Since(at) > time.Minute {
		t.Fatalf("heartbeat = %s", at)
	}
}

func backdateTree(t *testing.T, path string, offset time.Duration) {
	t.Helper()
	at := time.Now().Add(offset)
	if err := filepath.Walk(path, func(entry string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chtimes(entry, at, at)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The first real sweep this verb ran found nine review checkouts it had proved
// landed and then refused to retire, because the Work Log claim recorded the
// branch the worktree was created on and a detached checkout has none. The
// refusal told the operator to `git branch -m` a HEAD that is not on a branch.
func TestGCRetiresAClaimedDetachedCheckoutWhoseClaimNamesABranch(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-claimed-detached")
	// Detach the worktree at its own head, the way a review checkout is left.
	gitTest(t, result.WorktreeDir, "checkout", "--detach", head)
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})

	outcome, err := GC(context.Background(), GCOptions{
		// This test is about classification, not about the in-use rule: the
		// fixture created its checkout moments ago, which is genuinely in use.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{"gc-claimed-detached"}, Apply: true, SkipSizes: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, "gc-claimed-detached")
	if entry.Class != GCClassDetachedReview || !entry.Applied {
		t.Fatalf("entry = %#v, want a detached review checkout retired on its landing evidence", entry)
	}
	if entry.Error != "" {
		t.Fatalf("a claim naming the branch the worktree was created on must not refuse a detached checkout: %s", entry.Error)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("checkout survived: %v", statErr)
	}
}

// The incident, as a test. A detached review checkout at a merged head, clean
// tree, someone working in it right now: the round-1 sweep removed exactly this
// while the reviewer was between rounds, and the round-2 guard did not cover it
// because the checkout carries no owner registration for a guard keyed to one.
func TestGCKeepsADetachedReviewCheckoutSomeoneIsUsing(t *testing.T) {
	fixture, _, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "gc-incident-source")
	const task = "gc-incident-review"
	reviewDir := filepath.Join(fixture.home, "worktrees", task, "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", reviewDir, head)
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 332, strings.Repeat("a", 40), squashSHA, mergedAt),
	})
	// Nobody registered an owner — a review checkout never does — and the
	// reviewer is working in it right now.
	TouchHeartbeat(reviewDir, "wb worktree list")

	inUse, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
		Now: func() time.Time { return time.Now().Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, inUse, task)
	if entry.Eligible || entry.Class != GCClassClaimedLive {
		t.Fatalf("a checkout someone is using = %#v, want it kept whatever its landing evidence says", entry)
	}
	if entry.OwnerState == "active" {
		t.Fatal("the fixture must have no live owner registration, or it does not reproduce the incident")
	}

	// Seven hours later nobody has touched it, and it retires normally.
	abandoned, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
		Now: func() time.Time { return time.Now().Add(7 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if abandoned := entryFor(t, abandoned, task); !abandoned.Eligible || abandoned.Class != GCClassDetachedReview {
		t.Fatalf("an untouched review checkout of a merged pull request = %#v, want it eligible", abandoned)
	}
}

// A clock skew or a restored archive must not pin a checkout open forever while
// reporting that it was used no time ago at all.
func TestActivityIgnoresATimestampInTheFuture(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "future-mtime",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(72 * time.Hour)
	path := filepath.Join(created[0].WorktreeDir, "from-the-future.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	backdateTree(t, filepath.Join(created[0].WorktreeDir, journalRootDirectory), -9*time.Hour)

	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "future-mtime", Activity: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 {
		t.Fatalf("inventory = %#v", listed.Results)
	}
	if listed.Results[0].LastActivityAt.After(time.Now().Add(2 * time.Minute)) {
		t.Fatalf("a future timestamp was taken as activity: %s", listed.Results[0].LastActivityAt)
	}
}

// A negative window is a mistake, and a silently inverted safety rule is the
// worst possible response to one.
func TestNegativeSessionFreshnessDisablesRatherThanInverts(t *testing.T) {
	result := ListResult{LastActivityAt: time.Now()}
	if checkoutIsInUse(result, GCOptions{SessionFreshness: -time.Hour}, time.Now()) {
		t.Fatal("a negative window must disable the rule, not invert it")
	}
	if checkoutIsInUse(result, GCOptions{SessionFreshness: DisableSessionFreshness}, time.Now()) {
		t.Fatal("the disable sentinel must disable the rule")
	}
	if !checkoutIsInUse(result, GCOptions{}, time.Now()) {
		t.Fatal("an unset window keeps the safe default")
	}
}
