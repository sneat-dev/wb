package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// runWBHomeIsolated runs the built wb binary exactly like runWB, except with
// HOME (and so the retired legacy $HOME/.wb state directory) redirected to a
// private, empty per-call directory instead of this machine's real one.
//
// `wb layout migrate` reads live Work Log claims across every home WB
// resolves, including that legacy one (see
// internal/worktrees.ListActiveClaimSummaries), and a fixture repository name
// in this suite can otherwise coincidentally collide with a real claim this
// machine's fleet has recorded there, making the test's outcome depend on
// ambient state instead of the fixture. This is a dedicated helper, not a
// change to the shared runWB/runWBIn used across this package's other CLI
// suites, because some of those specifically exercise behavior that depends
// on HOME being genuinely absent or genuinely present.
func runWBHomeIsolated(t *testing.T, args ...string) smokeResult {
	t.Helper()
	return runWBWithHome(t, t.TempDir(), args...)
}

// runWBWithHome is runWBHomeIsolated, generalized to accept a specific HOME
// instead of always generating a fresh, empty one. A test that needs the
// retired legacy $HOME/.wb read layout (wbhome.Resolve) to actually resolve
// against a fixture it built there — rather than an empty, always-absent
// one — sets HOME to that fixture's directory for the whole test with
// t.Setenv and passes it here explicitly, so the CLI invocation sees the
// same HOME the fixture was built under instead of runWBHomeIsolated's own
// fresh substitute.
func runWBWithHome(t *testing.T, home string, args ...string) smokeResult {
	t.Helper()
	binary := buildWB(t)

	ctx, cancel := context.WithTimeout(context.Background(), smokeDeadline)
	defer cancel()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdin = devNull
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Env = append(os.Environ(), "TERM=dumb", "HOME="+home,
		"GIT_AUTHOR_NAME=wb-test", "GIT_AUTHOR_EMAIL=wb-test@example.com",
		"GIT_COMMITTER_NAME=wb-test", "GIT_COMMITTER_EMAIL=wb-test@example.com")

	runErr := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("wb %s did not exit within %s", strings.Join(args, " "), smokeDeadline)
	}

	result := smokeResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		result.exitCode = 0
	case errors.As(runErr, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("wb %s: %v", strings.Join(args, " "), runErr)
	}
	return result
}

// migrateWorktreeJSON is one repointed linked worktree in a decoded report.
type migrateWorktreeJSON struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// migrateRelocationJSON is one managed task checkout's relocation plan or
// outcome in a decoded report.
type migrateRelocationJSON struct {
	Task        string `json:"task"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
}

// migrateCloneJSON is one clone's migration plan or outcome in a decoded
// report.
type migrateCloneJSON struct {
	Repository  string                  `json:"repository"`
	Source      string                  `json:"source"`
	Destination string                  `json:"destination"`
	Status      string                  `json:"status"`
	Reason      string                  `json:"reason"`
	Worktrees   []migrateWorktreeJSON   `json:"worktrees"`
	Relocations []migrateRelocationJSON `json:"relocations"`
}

// migrateReportJSON is the subset of layout.MigrateReport this suite decodes.
type migrateReportJSON struct {
	ManifestID string             `json:"manifest_id"`
	Clones     []migrateCloneJSON `json:"clones"`
}

func findRelocation(clone migrateCloneJSON, task string) (int, bool) {
	for index, relocation := range clone.Relocations {
		if relocation.Task == task {
			return index, true
		}
	}
	return 0, false
}

func decodeMigrateReport(t *testing.T, stdout string) migrateReportJSON {
	t.Helper()
	var report migrateReportJSON
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode migrate report: %v\n%s", err, stdout)
	}
	return report
}

func findMigrateClone(report migrateReportJSON, repository string) (int, bool) {
	for index, clone := range report.Clones {
		if clone.Repository == repository {
			return index, true
		}
	}
	return 0, false
}

// TestLayoutMigratePlansThenMovesAndRepoints encodes
// projects-root-layout#ac:migrate-plans-then-moves-and-repoints: a dry run
// changes nothing, and --apply moves the clone with its uncommitted changes
// intact, repoints both a nested and an external linked worktree, removes the
// emptied legacy owner directory, and a repeated --apply is a no-op.
func TestLayoutMigratePlansThenMovesAndRepoints(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := filepath.Join(root, "dal-go", "dalgo")
	initOriginRepository(t, legacy, "dal-go/dalgo")
	if err := os.WriteFile(filepath.Join(legacy, "uncommitted.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(legacy, ".worktrees", "t1")
	runGit(t, legacy, "worktree", "add", "-b", "t1", nested)
	external := filepath.Join(root, "worktrees", "t2", "dal-go", "dalgo")
	if err := os.MkdirAll(filepath.Dir(external), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, legacy, "worktree", "add", "-b", "t2", external)

	destination := filepath.Join(root, "github.com", "dal-go", "dalgo")
	destinationNested := filepath.Join(destination, ".worktrees", "t1")

	dry := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--format", "json")
	if dry.exitCode != exitOK {
		t.Fatalf("dry-run exit = %d, want ok; stderr=%s stdout=%s", dry.exitCode, dry.stderr, dry.stdout)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("dry run must not move the clone")
	}
	plan := decodeMigrateReport(t, dry.stdout)
	index, found := findMigrateClone(plan, "dal-go/dalgo")
	if !found {
		t.Fatalf("dry run plan missing dal-go/dalgo: %+v", plan)
	}
	if plan.Clones[index].Status != "planned" || plan.Clones[index].Destination != destination {
		t.Fatalf("dry run plan = %+v, want planned -> %s", plan.Clones[index], destination)
	}
	if len(plan.Clones[index].Worktrees) != 2 {
		t.Fatalf("dry run plan worktrees = %+v, want 2", plan.Clones[index].Worktrees)
	}

	applied := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--apply", "--format", "json")
	if applied.exitCode != exitOK {
		t.Fatalf("apply exit = %d, want ok; stderr=%s stdout=%s", applied.exitCode, applied.stderr, applied.stdout)
	}
	report := decodeMigrateReport(t, applied.stdout)
	index, found = findMigrateClone(report, "dal-go/dalgo")
	if !found || report.Clones[index].Status != "done" {
		t.Fatalf("apply report = %+v, want done", report.Clones)
	}
	if report.ManifestID == "" {
		t.Fatal("apply must record a manifest id")
	}

	if _, err := os.Stat(filepath.Join(root, "dal-go")); !os.IsNotExist(err) {
		t.Fatalf("emptied legacy owner directory must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "uncommitted.txt")); err != nil {
		t.Fatal("uncommitted change must survive the move")
	}
	for _, worktree := range []string{destination, destinationNested, external} {
		out := gitOutput(t, worktree, "status", "--porcelain=v1")
		_ = out // status succeeding without error is the assertion
	}
	listOutput := gitOutput(t, destination, "worktree", "list", "--porcelain")
	if strings.Contains(listOutput, "prunable") {
		t.Fatalf("worktree list reports a prunable entry:\n%s", listOutput)
	}
	for _, marker := range []string{destination, destinationNested, external} {
		contents, err := os.ReadFile(filepath.Join(marker, ".worktree.md"))
		if err != nil {
			t.Fatalf("marker missing at %s: %v", marker, err)
		}
		if !strings.Contains(string(contents), destination) {
			t.Fatalf("marker at %s does not name new canonical path %s:\n%s", marker, destination, contents)
		}
	}

	audit := runWBHomeIsolated(t, "layout", "audit", "--projects-root", root, "--format", "json")
	if audit.exitCode != exitOK {
		t.Fatalf("layout audit after migrate = %d, stderr=%s", audit.exitCode, audit.stderr)
	}

	again := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--apply", "--format", "json")
	if again.exitCode != exitOK {
		t.Fatalf("second apply exit = %d, want ok; stderr=%s", again.exitCode, again.stderr)
	}
	secondReport := decodeMigrateReport(t, again.stdout)
	index, found = findMigrateClone(secondReport, "dal-go/dalgo")
	if !found || secondReport.Clones[index].Status != "already_done" {
		t.Fatalf("second apply report = %+v, want already_done", secondReport.Clones)
	}
}

// TestLayoutMigrateSkipsUnsafeClones encodes
// projects-root-layout#ac:migrate-skips-unsafe-clones: an owner mismatch, an
// occupied destination, a rebase in progress, and a live Work Log claim are
// each left in place with a finding, the clean clone still migrates, and the
// command exits with the findings code.
func TestLayoutMigrateSkipsUnsafeClones(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Clean clone: migrates normally.
	cleanPath := filepath.Join(root, "acme", "clean")
	initOriginRepository(t, cleanPath, "acme/clean")

	// Owner mismatch: origin names a different owner than the clone's path.
	mismatchPath := filepath.Join(root, "acme", "mismatch")
	initOriginRepository(t, mismatchPath, "other/mismatch")

	// Destination already exists.
	occupiedPath := filepath.Join(root, "acme", "occupied")
	initOriginRepository(t, occupiedPath, "acme/occupied")
	occupiedDestination := filepath.Join(root, "github.com", "acme", "occupied")
	if err := os.MkdirAll(occupiedDestination, 0o755); err != nil {
		t.Fatal(err)
	}

	// Rebase in progress.
	rebasePath := filepath.Join(root, "acme", "rebasing")
	initOriginRepository(t, rebasePath, "acme/rebasing")
	if err := os.MkdirAll(filepath.Join(rebasePath, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Live Work Log claim on a linked worktree. worktrees.Create fetches and
	// verifies against the clone's actual origin, so it needs a real,
	// reachable remote; the clone is repointed to its eventual GitHub origin
	// only after the claim exists, exactly as a machine would look once
	// cloned from GitHub and later adopted by WB.
	claimedPath := filepath.Join(root, "acme", "claimed")
	claimedRemote := filepath.Join(root, "claimed-origin.git")
	testenv.InitBareRemoteForTest(t, claimedRemote)
	runGit(t, root, "clone", claimedRemote, claimedPath)
	runGit(t, claimedPath, "config", "user.email", "wb@example.test")
	runGit(t, claimedPath, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(claimedPath, "README.md"), []byte("acme/claimed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, claimedPath, "add", ".")
	runGit(t, claimedPath, "commit", "-m", "init")
	runGit(t, claimedPath, "push", "-u", "origin", "main")
	created, err := worktrees.Create(context.Background(), []string{"acme/claimed"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "claim-task",
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create claimed worktree: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("create claimed worktree returned %d results", len(created))
	}
	runGit(t, claimedPath, "remote", "set-url", "origin", "git@github.com:acme/claimed.git")

	result := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--apply", "--format", "json")
	if result.exitCode != exitFindings {
		t.Fatalf("apply exit = %d, want findings; stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
	}
	report := decodeMigrateReport(t, result.stdout)

	expectSkipped := map[string]string{
		"acme/mismatch": "owner",
		"acme/occupied": "destination",
		"acme/rebasing": "rebase",
		"acme/claimed":  "claim",
	}
	for repository, wantSubstring := range expectSkipped {
		index, found := findMigrateClone(report, repository)
		if !found {
			t.Fatalf("report missing %s: %+v", repository, report.Clones)
		}
		clone := report.Clones[index]
		if clone.Status != "skipped" {
			t.Fatalf("%s status = %q, want skipped (reason=%q)", repository, clone.Status, clone.Reason)
		}
		if !strings.Contains(strings.ToLower(clone.Reason), wantSubstring) {
			t.Fatalf("%s reason = %q, want it to mention %q", repository, clone.Reason, wantSubstring)
		}
		if _, err := os.Stat(clone.Source); err != nil {
			t.Fatalf("%s must be left in place: %v", repository, err)
		}
	}
	index, found := findMigrateClone(report, "acme/clean")
	if !found || report.Clones[index].Status != "done" {
		t.Fatalf("clean clone = %+v, want done", report.Clones)
	}
	if _, err := os.Stat(filepath.Join(root, "github.com", "acme", "clean")); err != nil {
		t.Fatal("clean clone must have moved to the host level")
	}
}

// TestLayoutMigrateIsReversible encodes
// projects-root-layout#ac:migrate-is-reversible: undo reverses every clone the
// manifest records as done, and every checkout is left in working order.
func TestLayoutMigrateIsReversible(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := filepath.Join(root, "dal-go", "dalgo")
	initOriginRepository(t, legacy, "dal-go/dalgo")
	nested := filepath.Join(legacy, ".worktrees", "t1")
	runGit(t, legacy, "worktree", "add", "-b", "t1", nested)

	applied := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--apply", "--format", "json")
	if applied.exitCode != exitOK {
		t.Fatalf("apply exit = %d; stderr=%s", applied.exitCode, applied.stderr)
	}
	report := decodeMigrateReport(t, applied.stdout)
	if report.ManifestID == "" {
		t.Fatal("apply did not record a manifest id")
	}
	destination := filepath.Join(root, "github.com", "dal-go", "dalgo")
	if _, err := os.Stat(destination); err != nil {
		t.Fatal("apply must have moved the clone")
	}

	undone := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--undo", report.ManifestID, "--apply", "--format", "json")
	if undone.exitCode != exitOK {
		t.Fatalf("undo exit = %d, want ok; stderr=%s stdout=%s", undone.exitCode, undone.stderr, undone.stdout)
	}
	undoReport := decodeMigrateReport(t, undone.stdout)
	index, found := findMigrateClone(undoReport, "dal-go/dalgo")
	if !found || undoReport.Clones[index].Status != "reversed" {
		t.Fatalf("undo report = %+v, want reversed", undoReport.Clones)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("undo must restore the legacy clone path")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("undo must remove the host-level path")
	}
	for _, checkout := range []string{legacy, nested} {
		gitOutput(t, checkout, "status", "--porcelain=v1")
	}
}

// TestLayoutMigrateUndoPrintsPartialReportOnManifestWriteFailure covers
// MINOR #F of the second review round: when a manifest write fails partway
// through `--undo`, the operator must still see what was already reversed
// before the failure, not just a bare error. The manifest's own directory is
// made read-only (but still readable and traversable) after a successful
// `--apply`, so `--undo` can still READ the manifest but cannot write its
// updated outcome back — the exact failure this guards against — and the
// clone it reversed just before that write must still appear in the
// command's JSON output on stdout.
func TestLayoutMigrateUndoPrintsPartialReportOnManifestWriteFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := filepath.Join(root, "acme", "app")
	initOriginRepository(t, legacy, "acme/app")

	applied := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--apply", "--format", "json")
	if applied.exitCode != exitOK {
		t.Fatalf("apply exit = %d; stderr=%s", applied.exitCode, applied.stderr)
	}
	report := decodeMigrateReport(t, applied.stdout)
	if report.ManifestID == "" {
		t.Fatal("apply did not record a manifest id")
	}
	manifestDir := filepath.Join(root, ".wb", "layout-migrations", report.ManifestID)
	if _, err := os.Stat(filepath.Join(manifestDir, "manifest.json")); err != nil {
		t.Fatalf("manifest file missing: %v", err)
	}
	// Read-only + execute: --undo can still open and read manifest.json (an
	// existing file needs no directory write permission to read), but cannot
	// create the temp file its atomic writeManifest needs to record the
	// reversal outcome, which needs write permission on the directory itself.
	if err := os.Chmod(manifestDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(manifestDir, 0o755); err != nil {
			t.Fatal(err)
		}
	})

	undone := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--undo", report.ManifestID, "--apply", "--format", "json")
	if undone.exitCode == exitOK {
		t.Fatalf("undo with an unwritable manifest directory must not exit ok; stdout=%s", undone.stdout)
	}
	undoReport := decodeMigrateReport(t, undone.stdout)
	index, found := findMigrateClone(undoReport, "acme/app")
	if !found {
		t.Fatalf("undo's partial report must still be printed to stdout despite the manifest-write error; stdout=%s stderr=%s", undone.stdout, undone.stderr)
	}
	if undoReport.Clones[index].Status != "reversed" {
		t.Fatalf("partial report clone = %+v, want reversed (the move-back itself succeeded before the manifest write failed)", undoReport.Clones[index])
	}
	// The move-back is a real filesystem change independent of the manifest
	// write that failed: the clone really is back at its legacy path.
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("the reversed clone must actually be back at its legacy path even though the manifest write failed")
	}
}

// dalgoFetchableFixture builds a legacy dal-go/dalgo clone with a real,
// fetchable local origin (a bare repository), which worktrees.Create needs to
// fetch and verify against. The origin is later repointed to its eventual
// GitHub identity, exactly as TestLayoutMigrateSkipsUnsafeClones's claimed
// worktree does: a machine clones for real, WB creates its managed task
// checkouts against that real origin, and only afterwards does the clone's
// remote get rewritten to the conceptual forge URL migrate keys off of.
func dalgoFetchableFixture(t *testing.T, root string) (legacy string) {
	t.Helper()
	return dalgoFetchableFixtureAt(t, root, filepath.Join(root, "dal-go", "dalgo"))
}

// dalgoFetchableFixtureAt is dalgoFetchableFixture, generalized to clone
// directly at an arbitrary path -- in particular, directly at the host-level
// destination a migrated clone would occupy, so a test can exercise the
// "already at the host level, moved by this run or earlier" clone class
// (REQ: migration-relocates-managed-worktrees) without migrate having to move
// anything first.
func dalgoFetchableFixtureAt(t *testing.T, root, clonePath string) string {
	t.Helper()
	origin := filepath.Join(root, "dalgo-origin.git")
	if _, statErr := os.Stat(origin); statErr != nil {
		testenv.InitBareRemoteForTest(t, origin)
	}
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(clonePath), "clone", origin, filepath.Base(clonePath))
	runGit(t, clonePath, "config", "user.email", "wb@example.test")
	runGit(t, clonePath, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(clonePath, "README.md"), []byte("dal-go/dalgo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clonePath, "add", ".")
	runGit(t, clonePath, "commit", "-m", "init")
	runGit(t, clonePath, "push", "-u", "origin", "main")
	return clonePath
}

// dalgo2sqlFetchableFixtureAt is dalgoFetchableFixtureAt for a second,
// distinct repository (dal-go/dalgo2sql), so a test can hold two independent
// clones at once without sharing one bare origin between two different
// repository identities.
func dalgo2sqlFetchableFixtureAt(t *testing.T, root, clonePath string) string {
	t.Helper()
	origin := filepath.Join(root, "dalgo2sql-origin.git")
	if _, statErr := os.Stat(origin); statErr != nil {
		testenv.InitBareRemoteForTest(t, origin)
	}
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(clonePath), "clone", origin, filepath.Base(clonePath))
	runGit(t, clonePath, "config", "user.email", "wb@example.test")
	runGit(t, clonePath, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(clonePath, "README.md"), []byte("dal-go/dalgo2sql\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clonePath, "add", ".")
	runGit(t, clonePath, "commit", "-m", "init")
	runGit(t, clonePath, "push", "-u", "origin", "main")
	return clonePath
}

// moveWorklogTaskToLegacyHome relocates one task's entire Work Log tree from
// fromHome/worklogs/<task> to toHome/worklogs/<task>, simulating a claim
// recorded under the retired default state directory ($HOME/.wb) before the
// projects-root layout existed. The checkout itself is untouched: only the
// claim's private state moves, so resolving it depends on
// claimForRelocationAcrossHomes actually searching every home wbhome.Resolve
// reports, not only the projects root's own <root>/.wb.
func moveWorklogTaskToLegacyHome(t *testing.T, fromHome, toHome, task string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(toHome, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(toHome, "worklogs", task)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(fromHome, "worklogs", task), dst); err != nil {
		t.Fatal(err)
	}
}

// TestLayoutMigrateRelocatesManagedWorktrees encodes
// projects-root-layout#ac:migrate-relocates-managed-worktrees, under the
// founder's decision (2026-09-18) and its 2026-09-18 redesign: a finished
// task's checkout is relocated (no live session depends on its path any
// more, so it is the safe case), an active task's checkout is left in place
// with a finding, and every relocation candidate gets its own per-checkout
// safety recheck immediately before it moves, independent of the
// clone-level refusal, which stays exactly as it always has been.
//
// Clone A (dal-go/dalgo) is a genuine legacy clone this run moves, holding
// two finished checkouts and one unmanaged one, exercising both the forward
// clone move (relocateOneCheckout matches each by its exact path from
// clone A's own `git worktree list`) and the dry-run/apply agreement Fix #5
// names. t1's Work Log claim lives under the projects root's own home; t2's
// was moved to the retired legacy $HOME/.wb layout after being recorded, so
// resolving it depends on claimForRelocationAcrossHomes actually searching
// every home wbhome.Resolve reports, not only the write home. Clone B
// (dal-go/dalgo2sql) is a separate repository already at its host-level
// placement, holding one still-active checkout (t3): the clone-level
// live-claim refusal (liveClaimReason) matches by repository slug, so t3's
// active claim never refuses clone A, and t3 itself is left in place with a
// finding rather than relocated.
func TestLayoutMigrateRelocatesManagedWorktrees(t *testing.T) {
	// Not t.Parallel(): this test uses t.Setenv (HOME, XDG_CONFIG_HOME) to
	// select the claim home and store mode, which testing forbids alongside
	// t.Parallel.
	root := t.TempDir()
	// legacyHome simulates the retired default state directory ($HOME/.wb):
	// it is passed explicitly to the migrate CLI invocations below (via
	// runWBWithHome) as their HOME, without changing this test process's own
	// HOME — buildWB below shells out to `go build`, which must keep using
	// this machine's real module/build cache, not one seeded under a
	// TempDir that t.TempDir() then cannot clean up (its cache entries are
	// read-only).
	legacyHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacyHome, ".wb", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}

	cloneA := filepath.Join(root, "dal-go", "dalgo")
	dalgoFetchableFixtureAt(t, root, cloneA)
	destinationA := filepath.Join(root, "github.com", "dal-go", "dalgo")

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "wb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "worktrees.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	finalize := func(worktree string) {
		t.Helper()
		if _, err := worktrees.LogFinalize(context.Background(), worktrees.LogFinalizeOptions{
			ProjectsRoot: root, Worktree: worktree, Result: "success", Apply: true,
		}); err != nil {
			t.Fatalf("finalize %s: %v", worktree, err)
		}
	}

	// t1: in-clone, finished, claim recorded under the projects root's own
	// home (<root>/.wb).
	createdT1, err := worktrees.Create(context.Background(), []string{"dal-go/dalgo"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t1", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create t1: %v", err)
	}
	finalize(createdT1[0].WorktreeDir)

	// t2: in-clone, finished, but its claim is relocated to the legacy
	// $HOME/.wb layout afterward, simulating one recorded before the
	// projects-root layout existed.
	createdT2, err := worktrees.Create(context.Background(), []string{"dal-go/dalgo"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t2", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create t2: %v", err)
	}
	finalize(createdT2[0].WorktreeDir)
	moveWorklogTaskToLegacyHome(t, filepath.Join(root, ".wb"), filepath.Join(legacyHome, ".wb"), "t2")

	// unmanaged: a plain linked worktree with no WB task identity, nested in
	// clone A so it moves along with the clone rename.
	unmanaged := filepath.Join(cloneA, ".worktrees", "adopted")
	runGit(t, cloneA, "worktree", "add", "-b", "adopted", unmanaged)

	// Clone B: a separate repository already at its host-level placement,
	// holding one still-active checkout.
	cloneB := filepath.Join(root, "github.com", "dal-go", "dalgo2sql")
	dalgo2sqlFetchableFixtureAt(t, root, cloneB)
	createdT3, err := worktrees.Create(context.Background(), []string{"dal-go/dalgo2sql"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t3", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create t3: %v", err)
	}
	t3Path := createdT3[0].WorktreeDir // active: never finalized

	// Reset to today's machine config: default central store.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	// The fetchable local origins above were only needed for Create's own
	// verification; repoint them to the conceptual GitHub identities migrate
	// keys the host level off of, now that every claim already exists.
	runGit(t, cloneA, "remote", "set-url", "origin", "git@github.com:dal-go/dalgo.git")
	runGit(t, cloneB, "remote", "set-url", "origin", "git@github.com:dal-go/dalgo2sql.git")

	t1Destination := filepath.Join(root, ".worktrees", "t1", "github.com", "dal-go", "dalgo")
	t2Destination := filepath.Join(root, ".worktrees", "t2", "github.com", "dal-go", "dalgo")
	unmanagedDestination := filepath.Join(destinationA, ".worktrees", "adopted")

	dry := runWBWithHome(t, legacyHome, "layout", "migrate", "--projects-root", root, "--format", "json")
	if dry.exitCode != exitFindings {
		t.Fatalf("dry-run exit = %d, want findings (t3 is active); stderr=%s stdout=%s", dry.exitCode, dry.stderr, dry.stdout)
	}
	plan := decodeMigrateReport(t, dry.stdout)
	planIndexA, found := findMigrateClone(plan, "dal-go/dalgo")
	if !found || plan.Clones[planIndexA].Status != "planned" {
		t.Fatalf("dry run clone A = %+v, want planned", plan.Clones)
	}
	for _, task := range []string{"t1", "t2"} {
		if relIndex, found := findRelocation(plan.Clones[planIndexA], task); !found || plan.Clones[planIndexA].Relocations[relIndex].Status != "planned" {
			t.Fatalf("dry run relocations = %+v, want %s planned", plan.Clones[planIndexA].Relocations, task)
		}
	}
	planIndexB, found := findMigrateClone(plan, "dal-go/dalgo2sql")
	if !found || plan.Clones[planIndexB].Status != "already_done" {
		t.Fatalf("dry run clone B = %+v, want already_done", plan.Clones)
	}
	if relIndex, found := findRelocation(plan.Clones[planIndexB], "t3"); !found || plan.Clones[planIndexB].Relocations[relIndex].Status != "skipped" ||
		!strings.Contains(plan.Clones[planIndexB].Relocations[relIndex].Reason, "active task") {
		t.Fatalf("dry run t3 relocation = %+v, want skipped naming the active task", plan.Clones[planIndexB].Relocations)
	}

	applied := runWBWithHome(t, legacyHome, "layout", "migrate", "--projects-root", root, "--apply", "--format", "json")
	if applied.exitCode != exitFindings {
		t.Fatalf("apply exit = %d, want findings (t3 is active); stderr=%s stdout=%s", applied.exitCode, applied.stderr, applied.stdout)
	}
	report := decodeMigrateReport(t, applied.stdout)
	if report.ManifestID == "" {
		t.Fatal("apply did not record a manifest id")
	}
	cloneIndexA, found := findMigrateClone(report, "dal-go/dalgo")
	if !found || report.Clones[cloneIndexA].Status != "done" {
		t.Fatalf("apply clone A = %+v, want done", report.Clones)
	}
	cloneAResult := report.Clones[cloneIndexA]
	for task, want := range map[string]string{"t1": t1Destination, "t2": t2Destination} {
		relIndex, found := findRelocation(cloneAResult, task)
		if !found || cloneAResult.Relocations[relIndex].Status != "done" {
			t.Fatalf("relocations = %+v, want %s done", cloneAResult.Relocations, task)
		}
		if cloneAResult.Relocations[relIndex].Destination != want {
			t.Fatalf("%s relocated to %q, want %q", task, cloneAResult.Relocations[relIndex].Destination, want)
		}
	}
	var unmanagedFound bool
	for _, relocation := range cloneAResult.Relocations {
		if relocation.Task == "" && relocation.Status == "unmanaged" {
			unmanagedFound = true
			if relocation.Source != unmanagedDestination {
				t.Fatalf("unmanaged relocation source = %q, want %q", relocation.Source, unmanagedDestination)
			}
		}
	}
	if !unmanagedFound {
		t.Fatalf("relocations = %+v, want an unmanaged entry", cloneAResult.Relocations)
	}
	cloneIndexB, found := findMigrateClone(report, "dal-go/dalgo2sql")
	if !found || report.Clones[cloneIndexB].Status != "already_done" {
		t.Fatalf("apply clone B = %+v, want already_done", report.Clones)
	}
	if relIndex, found := findRelocation(report.Clones[cloneIndexB], "t3"); !found || report.Clones[cloneIndexB].Relocations[relIndex].Status != "skipped" ||
		!strings.Contains(report.Clones[cloneIndexB].Relocations[relIndex].Reason, "active task") {
		t.Fatalf("t3 relocation = %+v, want skipped naming the active task", report.Clones[cloneIndexB].Relocations)
	}

	if _, err := os.Stat(t1Destination); err != nil {
		t.Fatalf("t1 must be relocated to the store: %v", err)
	}
	if _, err := os.Stat(t2Destination); err != nil {
		t.Fatalf("t2 must be relocated to the store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationA, ".worktrees", "t1")); !os.IsNotExist(err) {
		t.Fatalf("the moved clone must no longer hold t1 in-clone, err=%v", err)
	}
	if _, err := os.Stat(t3Path); err != nil {
		t.Fatalf("t3 (active) must be left exactly where it was: %v", err)
	}
	for _, checkout := range []string{destinationA, t1Destination, t2Destination, t3Path, unmanagedDestination} {
		gitOutput(t, checkout, "status", "--porcelain=v1")
	}

	// --clones-only, run standalone against a matching fixture: clone A
	// moves, every worktree is repointed, and nothing is relocated.
	t.Run("clones-only", func(t *testing.T) {
		onlyRoot := t.TempDir()
		onlyLegacy := dalgoFetchableFixture(t, onlyRoot)
		onlyConfigHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", onlyConfigHome)
		onlyConfigDir := filepath.Join(onlyConfigHome, "wb")
		if err := os.MkdirAll(onlyConfigDir, 0o755); err != nil {
			t.Fatal(err)
		}
		onlyConfigPath := filepath.Join(onlyConfigDir, "worktrees.yaml")
		if err := os.WriteFile(onlyConfigPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		createdOnlyT1, err := worktrees.Create(context.Background(), []string{"dal-go/dalgo"}, worktrees.CreateOptions{
			ProjectsRoot: onlyRoot, Operation: "t1", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
		})
		if err != nil {
			t.Fatalf("create t1: %v", err)
		}
		if _, err := worktrees.LogFinalize(context.Background(), worktrees.LogFinalizeOptions{
			ProjectsRoot: onlyRoot, Worktree: createdOnlyT1[0].WorktreeDir, Result: "success", Apply: true,
		}); err != nil {
			t.Fatalf("finalize t1: %v", err)
		}
		if err := os.Remove(onlyConfigPath); err != nil {
			t.Fatal(err)
		}
		runGit(t, onlyLegacy, "remote", "set-url", "origin", "git@github.com:dal-go/dalgo.git")

		onlyDestination := filepath.Join(onlyRoot, "github.com", "dal-go", "dalgo")
		onlyApplied := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", onlyRoot, "--apply", "--clones-only", "--format", "json")
		if onlyApplied.exitCode != exitOK {
			t.Fatalf("apply exit = %d, want ok; stderr=%s stdout=%s", onlyApplied.exitCode, onlyApplied.stderr, onlyApplied.stdout)
		}
		onlyReport := decodeMigrateReport(t, onlyApplied.stdout)
		onlyIndex, found := findMigrateClone(onlyReport, "dal-go/dalgo")
		if !found || onlyReport.Clones[onlyIndex].Status != "done" {
			t.Fatalf("clones-only apply clone = %+v, want done", onlyReport.Clones)
		}
		if len(onlyReport.Clones[onlyIndex].Relocations) != 0 {
			t.Fatalf("clones-only must not relocate anything: %+v", onlyReport.Clones[onlyIndex].Relocations)
		}
		if _, err := os.Stat(onlyDestination); err != nil {
			t.Fatalf("clone must have moved to its host-level path: %v", err)
		}
		if _, err := os.Stat(filepath.Join(onlyDestination, ".worktrees", "t1")); err != nil {
			t.Fatalf("t1 must still be repointed in-clone (repository-local mode), not relocated: %v", err)
		}
	})

	// Undo round trip: finalize t3 too (the task finishes sometime after the
	// migration ran), so migrateUndo's per-relocation refuseClone recheck no
	// longer refuses reversing t1 and t2's (already-finished-task)
	// relocations. t3 itself was never relocated, so there is nothing to
	// reverse for it; it stays exactly where it always was.
	finalize(t3Path)
	undone := runWBWithHome(t, legacyHome, "layout", "migrate", "--projects-root", root, "--undo", report.ManifestID, "--apply", "--format", "json")
	if undone.exitCode != exitOK {
		t.Fatalf("undo exit = %d, want ok; stderr=%s stdout=%s", undone.exitCode, undone.stderr, undone.stdout)
	}
	undoReport := decodeMigrateReport(t, undone.stdout)
	undoIndexA, found := findMigrateClone(undoReport, "dal-go/dalgo")
	if !found || undoReport.Clones[undoIndexA].Status != "reversed" {
		t.Fatalf("undo clone A = %+v, want reversed", undoReport.Clones)
	}
	for _, task := range []string{"t1", "t2"} {
		relIndex, found := findRelocation(undoReport.Clones[undoIndexA], task)
		if !found || undoReport.Clones[undoIndexA].Relocations[relIndex].Status != "done" {
			t.Fatalf("undo relocations = %+v, want %s done", undoReport.Clones[undoIndexA].Relocations, task)
		}
	}
	// Clone B was never itself moved by the forward run -- only its t3
	// relocation was recorded (skipped, active task, never actually
	// relocated) -- so undo reports it "reversed", not "skipped": nothing
	// here failed, and a clone whose only manifest entry is a relocation
	// record must not make the whole undo run exit with findings.
	undoIndexB, found := findMigrateClone(undoReport, "dal-go/dalgo2sql")
	if !found || undoReport.Clones[undoIndexB].Status != "reversed" ||
		!strings.Contains(undoReport.Clones[undoIndexB].Reason, "already at the host level") {
		t.Fatalf("undo clone B = %+v, want reversed naming the clone as already at the host level", undoReport.Clones)
	}
	if len(undoReport.Clones[undoIndexB].Relocations) != 0 {
		t.Fatalf("undo relocations for clone B = %+v, want none (t3 was never itself relocated, so there is nothing to reverse)", undoReport.Clones[undoIndexB].Relocations)
	}
	// Clone A's own manifest entry recorded a completed forward move
	// ("done"), so undo reverses that too, after reversing its relocations:
	// it ends back at its original legacy path, carrying t1 and t2 with it.
	if _, err := os.Stat(destinationA); !os.IsNotExist(err) {
		t.Fatalf("clone A must be moved back off its host-level path, err=%v", err)
	}
	if _, err := os.Stat(cloneA); err != nil {
		t.Fatalf("clone A must be restored to its original legacy path: %v", err)
	}
	t1Source := filepath.Join(cloneA, ".worktrees", "t1")
	t2Source := filepath.Join(cloneA, ".worktrees", "t2")
	if _, err := os.Stat(t1Source); err != nil {
		t.Fatalf("undo must restore t1 to its pre-relocation path: %v", err)
	}
	if _, err := os.Stat(t2Source); err != nil {
		t.Fatalf("undo must restore t2 to its pre-relocation path: %v", err)
	}
	if _, err := os.Stat(t1Destination); !os.IsNotExist(err) {
		t.Fatalf("undo must remove t1 from its relocated path, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".worktrees", "t1")); !os.IsNotExist(err) {
		t.Fatalf("undo must remove the emptied <root>/.worktrees/t1 directory, err=%v", err)
	}
	if _, err := os.Stat(t2Destination); !os.IsNotExist(err) {
		t.Fatalf("undo must remove t2 from its relocated path, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".worktrees", "t2")); !os.IsNotExist(err) {
		t.Fatalf("undo must remove the emptied <root>/.worktrees/t2 directory, err=%v", err)
	}
	if _, err := os.Stat(t3Path); err != nil {
		t.Fatalf("t3 must remain exactly where it was throughout undo: %v", err)
	}
	for _, checkout := range []string{cloneA, t1Source, t2Source, t3Path} {
		gitOutput(t, checkout, "status", "--porcelain=v1")
	}
}
