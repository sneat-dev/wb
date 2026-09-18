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
	binary := buildWB(t)

	ctx, cancel := context.WithTimeout(context.Background(), smokeDeadline)
	defer cancel()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	home := t.TempDir()

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
	runGit(t, root, "init", "--bare", "-b", "main", claimedRemote)
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
	// A live Work Log claim confined to a linked worktree no longer refuses
	// the whole clone (projects-root-layout#req:migration-relocates-managed-worktrees):
	// the clone still migrates and its claimed checkout relocates to the
	// store alongside it.
	claimedIndex, found := findMigrateClone(report, "acme/claimed")
	if !found || report.Clones[claimedIndex].Status != "done" {
		t.Fatalf("acme/claimed = %+v, want done", report.Clones[claimedIndex])
	}
	if relocations := report.Clones[claimedIndex].Relocations; len(relocations) != 1 || relocations[0].Status != "done" {
		t.Fatalf("acme/claimed relocations = %+v, want one done relocation", relocations)
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
	legacy = filepath.Join(root, "dal-go", "dalgo")
	origin := filepath.Join(root, "dalgo-origin.git")
	runGit(t, root, "init", "--bare", "-b", "main", origin)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(legacy), "clone", origin, "dalgo")
	runGit(t, legacy, "config", "user.email", "wb@example.test")
	runGit(t, legacy, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(legacy, "README.md"), []byte("dal-go/dalgo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, legacy, "add", ".")
	runGit(t, legacy, "commit", "-m", "init")
	runGit(t, legacy, "push", "-u", "origin", "main")
	return legacy
}

// TestLayoutMigrateRelocatesManagedWorktrees encodes
// projects-root-layout#ac:migrate-relocates-managed-worktrees: after the
// clone moves, migrate relocates every managed task checkout whose placement
// differs from the machine's current store-mode placement, by calling the
// existing `wb worktree relocate` implementation, and leaves an unmanaged
// linked worktree repointed only. It also proves the undo round trip
// reverses relocations before the clone move they depend on.
//
// t1 stands in for the AC's in-clone managed checkout: it is created while
// repository-local store mode is selected, landing inside the clone exactly
// like `<root>/dal-go/dalgo/.worktrees/t1`. t2 stands in for the AC's
// external managed checkout at a placement the machine no longer points to:
// it is created against a configured store root outside the projects root,
// then the machine's configuration is reset to today's default central
// store before migrate runs — the same "placement differs from today's
// config" condition a genuine `~/.wb/worktrees/...` legacy checkout has,
// reached through the public Create/config surface rather than by hand
// constructing a Work Log claim.
func TestLayoutMigrateRelocatesManagedWorktrees(t *testing.T) {
	// Not t.Parallel(): this test uses t.Setenv (XDG_CONFIG_HOME) to select
	// store mode at each Create call, which testing forbids alongside
	// t.Parallel.
	root := t.TempDir()
	legacy := dalgoFetchableFixture(t, root)

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "wb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "worktrees.yaml")
	writeConfig := func(content string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeConfig("version: 1\nworktrees:\n  store: repository-local\n")
	if _, err := worktrees.Create(context.Background(), []string{"dal-go/dalgo"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t1", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatalf("create t1: %v", err)
	}

	externalStore := t.TempDir()
	writeConfig("version: 1\nworktrees:\n  root: " + externalStore + "\n")
	if _, err := worktrees.Create(context.Background(), []string{"dal-go/dalgo"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t2", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatalf("create t2: %v", err)
	}

	// Reset to today's machine config: default central store.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	unmanaged := filepath.Join(root, "adopted-elsewhere")
	runGit(t, legacy, "worktree", "add", "-b", "adopted", unmanaged)

	// The fetchable local origin above was only needed for Create's own
	// verification; repoint it to the conceptual GitHub identity migrate
	// keys the host level off of, now that every claim already exists.
	runGit(t, legacy, "remote", "set-url", "origin", "git@github.com:dal-go/dalgo.git")

	destination := filepath.Join(root, "github.com", "dal-go", "dalgo")
	t1Destination := filepath.Join(root, ".worktrees", "t1", "github.com", "dal-go", "dalgo")
	t2Destination := filepath.Join(root, ".worktrees", "t2", "github.com", "dal-go", "dalgo")
	unmanagedDestination := unmanaged

	dry := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--format", "json")
	if dry.exitCode != exitOK {
		t.Fatalf("dry-run exit = %d; stderr=%s stdout=%s", dry.exitCode, dry.stderr, dry.stdout)
	}
	plan := decodeMigrateReport(t, dry.stdout)
	planIndex, found := findMigrateClone(plan, "dal-go/dalgo")
	if !found || plan.Clones[planIndex].Status != "planned" {
		t.Fatalf("dry run plan = %+v, want planned", plan.Clones)
	}
	for _, task := range []string{"t1", "t2"} {
		if relIndex, found := findRelocation(plan.Clones[planIndex], task); !found || plan.Clones[planIndex].Relocations[relIndex].Status != "planned" {
			t.Fatalf("dry run relocations = %+v, want %s planned", plan.Clones[planIndex].Relocations, task)
		}
	}

	applied := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--apply", "--format", "json")
	if applied.exitCode != exitOK {
		t.Fatalf("apply exit = %d; stderr=%s stdout=%s", applied.exitCode, applied.stderr, applied.stdout)
	}
	report := decodeMigrateReport(t, applied.stdout)
	if report.ManifestID == "" {
		t.Fatal("apply did not record a manifest id")
	}
	cloneIndex, found := findMigrateClone(report, "dal-go/dalgo")
	if !found || report.Clones[cloneIndex].Status != "done" {
		t.Fatalf("apply clone = %+v, want done", report.Clones)
	}
	clone := report.Clones[cloneIndex]
	for task, want := range map[string]string{"t1": t1Destination, "t2": t2Destination} {
		relIndex, found := findRelocation(clone, task)
		if !found || clone.Relocations[relIndex].Status != "done" {
			t.Fatalf("relocations = %+v, want %s done", clone.Relocations, task)
		}
		if clone.Relocations[relIndex].Destination != want {
			t.Fatalf("%s relocated to %q, want %q", task, clone.Relocations[relIndex].Destination, want)
		}
	}
	var unmanagedFound bool
	for _, relocation := range clone.Relocations {
		if relocation.Task == "" && relocation.Status == "unmanaged" {
			unmanagedFound = true
			if relocation.Source != unmanagedDestination {
				t.Fatalf("unmanaged relocation source = %q, want %q", relocation.Source, unmanagedDestination)
			}
		}
	}
	if !unmanagedFound {
		t.Fatalf("relocations = %+v, want an unmanaged entry", clone.Relocations)
	}

	if _, err := os.Stat(t1Destination); err != nil {
		t.Fatalf("t1 must be relocated to the store: %v", err)
	}
	if _, err := os.Stat(t2Destination); err != nil {
		t.Fatalf("t2 must be relocated to the store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".worktrees", "t1")); !os.IsNotExist(err) {
		t.Fatalf("the moved clone must no longer hold t1 in-clone, err=%v", err)
	}
	for _, checkout := range []string{destination, t1Destination, t2Destination, unmanagedDestination} {
		gitOutput(t, checkout, "status", "--porcelain=v1")
	}

	// --clones-only, run standalone against a matching fixture, must not
	// relocate anything.
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
		if _, err := worktrees.Create(context.Background(), []string{"dal-go/dalgo"}, worktrees.CreateOptions{
			ProjectsRoot: onlyRoot, Operation: "t1", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
		}); err != nil {
			t.Fatalf("create t1: %v", err)
		}
		if err := os.Remove(onlyConfigPath); err != nil {
			t.Fatal(err)
		}
		runGit(t, onlyLegacy, "remote", "set-url", "origin", "git@github.com:dal-go/dalgo.git")

		// --clones-only reproduces exactly PR #579's pre-relocation behaviour
		// (REQ: migration-relocates-managed-worktrees), which includes its
		// live-Work-Log-claim clone refusal: t1's claim is still live, so the
		// clone itself is left in place, exactly as
		// TestLayoutMigrateSkipsUnsafeClones's "claimed" case proves for the
		// unqualified command.
		onlyApplied := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", onlyRoot, "--apply", "--clones-only", "--format", "json")
		if onlyApplied.exitCode != exitFindings {
			t.Fatalf("apply exit = %d, want findings; stderr=%s stdout=%s", onlyApplied.exitCode, onlyApplied.stderr, onlyApplied.stdout)
		}
		onlyReport := decodeMigrateReport(t, onlyApplied.stdout)
		onlyIndex, found := findMigrateClone(onlyReport, "dal-go/dalgo")
		if !found || onlyReport.Clones[onlyIndex].Status != "skipped" || !strings.Contains(strings.ToLower(onlyReport.Clones[onlyIndex].Reason), "claim") {
			t.Fatalf("clones-only apply clone = %+v, want skipped naming the live claim", onlyReport.Clones)
		}
		if len(onlyReport.Clones[onlyIndex].Relocations) != 0 {
			t.Fatalf("clones-only must not relocate anything: %+v", onlyReport.Clones[onlyIndex].Relocations)
		}
		if _, err := os.Stat(onlyLegacy); err != nil {
			t.Fatalf("refused clone must be left in place: %v", err)
		}
	})

	undone := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--undo", report.ManifestID, "--apply", "--format", "json")
	if undone.exitCode != exitOK {
		t.Fatalf("undo exit = %d; stderr=%s stdout=%s", undone.exitCode, undone.stderr, undone.stdout)
	}
	undoReport := decodeMigrateReport(t, undone.stdout)
	undoIndex, found := findMigrateClone(undoReport, "dal-go/dalgo")
	if !found || undoReport.Clones[undoIndex].Status != "reversed" {
		t.Fatalf("undo clone = %+v, want reversed", undoReport.Clones)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("undo must restore the legacy clone path")
	}
	t1Source := filepath.Join(legacy, ".worktrees", "t1")
	// t2 was created against the clone's still-legacy, two-level on-disk
	// address (before this run's clone move), so its pre-relocation store
	// path has no host segment — only relocateClones's own later placement
	// resolution, computed after the clone reached the host level, does.
	t2Source := filepath.Join(externalStore, "t2", "dal-go", "dalgo")
	if _, err := os.Stat(t1Source); err != nil {
		t.Fatalf("undo must restore t1 to its pre-relocation path: %v", err)
	}
	if _, err := os.Stat(t2Source); err != nil {
		t.Fatalf("undo must restore t2 to its pre-relocation path: %v", err)
	}
	if _, err := os.Stat(t1Destination); !os.IsNotExist(err) {
		t.Fatalf("undo must remove t1 from its relocated path, err=%v", err)
	}
	for _, checkout := range []string{legacy, t1Source, t2Source} {
		gitOutput(t, checkout, "status", "--porcelain=v1")
	}
}
