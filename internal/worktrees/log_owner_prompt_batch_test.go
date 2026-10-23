package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestPreparedWorkLogPromptRejectsChangedRunArchive(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	prompt := filepath.Join(t.TempDir(), "request.txt")
	if err := os.WriteFile(prompt, []byte("original request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := WorkLogOptions{EffortID: "coverage", RunID: "run-1", Model: "unknown", OriginalPrompt: prompt, RequireOriginalPrompt: true}
	prepared, err := PrepareWorkLogOptions(projects, "coverage", options)
	if err != nil || string(prepared.snapshot.Contents) != "original request\n" {
		t.Fatalf("prepared prompt = %#v, %v", prepared, err)
	}
	home, err := wbhome.Root(projects)
	if err != nil {
		t.Fatal(err)
	}
	run, path, err := openWorkLogRun(home, "coverage", "run-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Close(); err != nil {
		t.Fatalf("close private run before verifying its archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "original-prompt.txt"), []byte("original request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWorkLogOptions(projects, "coverage", options); err != nil {
		t.Fatalf("same prompt refused: %v", err)
	}
	if err := os.WriteFile(prompt, []byte("changed request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWorkLogOptions(projects, "coverage", options); err == nil || !strings.Contains(err.Error(), "different original prompt bytes") {
		t.Fatalf("changed prompt = %v", err)
	}
}

func TestSnapshotOriginalPromptRequiresRegularNonemptyFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, prompt string
		require      bool
		want         string
	}{
		{"required absent", "", true, "required"},
		{"missing", filepath.Join(t.TempDir(), "missing"), false, "open original prompt"},
		{"directory", t.TempDir(), false, "regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := WorkLogOptions{OriginalPrompt: tc.prompt, RequireOriginalPrompt: tc.require}
			if err := snapshotOriginalPrompt(&options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("snapshot error = %v", err)
			}
		})
	}
	file := filepath.Join(t.TempDir(), "blank.txt")
	if err := os.WriteFile(file, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := WorkLogOptions{OriginalPrompt: file}
	if err := snapshotOriginalPrompt(&options); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("blank prompt = %v", err)
	}
	if err := os.WriteFile(file, []byte("exact bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshotOriginalPrompt(&options); err != nil || string(options.snapshot.Contents) != "exact bytes\n" {
		t.Fatalf("snapshot = %#v, %v", options, err)
	}
	options.snapshot.Digest = "wrong"
	if err := snapshotOriginalPrompt(&options); err == nil || !strings.Contains(err.Error(), "internally inconsistent") {
		t.Fatalf("tampered snapshot = %v", err)
	}
}

func TestNormalizeWorkLogOptionsRejectsUnsafeIdentifiers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		task    string
		options WorkLogOptions
		want    string
	}{
		{"bad/task", WorkLogOptions{Model: "unknown"}, "effort id"},
		{"safe", WorkLogOptions{Model: "unknown", RunID: "bad/run"}, "run id"},
	} {
		if _, _, err := normalizeWorkLogOptions(tc.task, tc.options, time.Now()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("normalize %q = %v", tc.task, err)
		}
	}
	effort, run, err := normalizeWorkLogOptions("safe", WorkLogOptions{Model: "unknown"}, time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil || effort != "safe" || !strings.HasPrefix(run, "wb-20260927T120000") {
		t.Fatalf("normalized = %q, %q, %v", effort, run, err)
	}
}

func TestPromptArchiveReservationValidatesImmutableBytesAndIndex(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	options, err := (WorkLogOptions{EffortID: "campaign", RunID: "run-a", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("stable prompt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reserveOriginalPromptArchive(home, "campaign", options); err != nil {
		t.Fatal(err)
	}
	if err := reserveOriginalPromptArchive(home, "campaign", options); err != nil {
		t.Fatalf("identical reservation: %v", err)
	}
	run, path, err := openWorkLogRun(home, "campaign", "run-a", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run.Close(); err != nil {
			t.Errorf("close prompt reservation run: %v", err)
		}
	})
	if err := validateReservationPrompt(run, options.snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	if err := validateReservationPrompt(run, "different"); err == nil {
		t.Fatal("accepted mismatched reservation digest")
	}
	if err := ensureWorkLogRunIndex(run, "campaign", "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkLogRunIndex(run, "campaign", "run-a"); err != nil {
		t.Fatalf("same index: %v", err)
	}
	if err := ensureWorkLogRunIndex(run, "other", "run-a"); err == nil {
		t.Fatal("accepted conflicting run index")
	}
	changed, err := options.WithOriginalPromptFromStdin([]byte("changed prompt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reserveOriginalPromptArchive(home, "campaign", changed); err == nil {
		t.Fatal("accepted changed immutable prompt")
	}
	if got, err := os.ReadFile(filepath.Join(path, "original-prompt.txt")); err != nil || string(got) != "stable prompt\n" {
		t.Fatalf("archived bytes = %q, %v", got, err)
	}
}

func TestUnclaimedPromptReservationDetectsClaimAndRunEvidence(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	run, path, err := openWorkLogRun(home, "campaign", "run-a", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run.Close(); err != nil {
			t.Errorf("close unclaimed prompt run: %v", err)
		}
	})
	if hasWorkLogClaimsOrTerminals(run) || legacyUnclaimedPromptReservation(run) {
		t.Fatal("empty run has evidence")
	}
	if err := os.WriteFile(filepath.Join(path, "original-prompt.txt"), []byte("prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !legacyUnclaimedPromptReservation(run) {
		t.Fatal("orphaned prompt not recognized")
	}
	claims, err := openPrivateChild(run, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "claims", "claim.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claims.Close(); err != nil {
		t.Fatalf("close private claims directory before reading it: %v", err)
	}
	if !hasWorkLogClaimsOrTerminals(run) {
		t.Fatal("claim was not detected")
	}
	if err := os.WriteFile(filepath.Join(path, "run.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if legacyUnclaimedPromptReservation(run) {
		t.Fatal("indexed run classified as unclaimed")
	}
}

func TestCountLegacyWorkLogProjectionsMatchesExactRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(dir, filename, effort, run string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"effort_id":"` + effort + `","run_id":"` + run + `"}`
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "a"), legacyWorkLogProjectionName, "campaign", "run-a")
	write(filepath.Join(root, "b", workLogProjectionDirectory), workLogProjectionName, "campaign", "run-a")
	write(filepath.Join(root, "c"), legacyWorkLogProjectionName, "campaign", "other")
	write(filepath.Join(root, "d"), legacyWorkLogProjectionName, "campaign", "run-a")
	if err := os.WriteFile(filepath.Join(root, "d", legacyWorkLogProjectionName), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := countLegacyWorkLogProjections(root, "campaign", "run-a"); err != nil || got != 2 {
		t.Fatalf("projection count = %d, %v", got, err)
	}
}

//nolint:paralleltest // newGitFixture uses t.Setenv to scope WB and Git configuration for its subprocesses.
func TestActiveClaimReadersPreserveImmutableIdentity(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "claim-reader-batch",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	home, err := wbhome.Root(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	claim, projection, claimPath, err := activeClaimPorts().ActiveWorkLogClaimWithMode(home, worktree, true)
	if err != nil || claim.ClaimID == "" || claimPath == "" || projection.Lifecycle != "active" {
		t.Fatalf("active claim = %#v, %#v, %q, %v", claim, projection, claimPath, err)
	}
	if _, err := readWorkLogProjectionForClaim(home, worktree); err != nil {
		t.Fatal(err)
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
		t.Fatal(err)
	}
	if terminal, err := readWorkLogTerminalRecordWithMode(home, worktree, true); err != nil || terminal != nil {
		t.Fatalf("active terminal = %#v, %v", terminal, err)
	}
	identity, err := currentExecutionIdentity(home, claim)
	if err != nil || identity.Model != "unknown" {
		t.Fatalf("execution identity = %#v, %v", identity, err)
	}
	if err := validateResumeWorkLogRequest(home, WorkLogOptions{Model: "different"}, claim); err == nil || !strings.Contains(err.Error(), "different model") {
		t.Fatalf("changed model = %v", err)
	}
	extended, err := workLogOptionsForClaimExtension(home, WorkLogOptions{Model: "unknown"}, claim)
	if err != nil || extended.EffortID != claim.EffortID || extended.RunID != claim.RunID {
		t.Fatalf("claim extension = %#v, %v", extended, err)
	}
	if err := corroborateClaimAtPath(home, worktree, "wrong-head", projection, claim); err == nil {
		t.Fatal("accepted wrong terminal commit")
	}
	if err := corroborateClaim(filepath.Join(t.TempDir(), "other"), claim.BaseSHA, projection, claim); err == nil || !strings.Contains(err.Error(), "identity/path mismatch") {
		t.Fatalf("moved claim = %v", err)
	}
	if err := writeWorkLogProjection(worktree, workLogProjection{}); err == nil {
		t.Fatal("accepted invalid projection")
	}
}
