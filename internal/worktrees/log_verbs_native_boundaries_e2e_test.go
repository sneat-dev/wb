//go:build e2e

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

func logVerbEventsPath(worktree string) string {
	return filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName)
}

func logVerbProjectionPath(worktree string) string {
	return filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogProjectionName)
}

func logVerbFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func logVerbAssertFileBytes(t *testing.T, label, path string, before []byte) {
	t.Helper()
	if after := logVerbFileBytes(t, path); string(after) != string(before) {
		t.Fatalf("%s changed %s: %q", label, path, after)
	}
}

//nolint:paralleltest // The Git fixture sets process-wide WB and Git environment variables.
func TestE2ELogIntegrateSelectsAutomaticStrategyFromPublishedRef(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "log-auto-strategy", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, err := LogCheckpoint(ctx, LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SkipRemote: true}); err != nil {
		t.Fatal(err)
	}
	local, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree})
	if err != nil || !local.Applied || local.Event == nil || local.Event.Target == nil || local.Event.Target.Strategy != "rebase" {
		t.Fatalf("unpublished automatic integration = %+v, %v", local, err)
	}
	gitTest(t, worktree, "push", "-u", "origin", "HEAD:refs/heads/"+created[0].Branch)
	gitTestOutput(t, worktree, "rev-parse", "--verify", "refs/remotes/origin/"+created[0].Branch)
	if _, err := LogCheckpoint(ctx, LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SkipRemote: true}); err != nil {
		t.Fatal(err)
	}
	published, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Strategy: "auto"})
	if err != nil || !published.Applied || published.Event == nil || published.Event.Target == nil || published.Event.Target.Strategy != "merge" {
		t.Fatalf("published automatic integration = %+v, %v", published, err)
	}
	if published.Event.Git == nil || published.Event.Git.Head != gitTestOutput(t, worktree, "rev-parse", "HEAD") {
		t.Fatalf("integration recorded a different HEAD: %+v", published.Event)
	}
}

//nolint:paralleltest // The Git fixture sets process-wide WB and Git environment variables.
func TestE2ELogRefreshAndIntegrateRefuseChangedJournalAuthority(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	unclaimed := newJournalWorktree(t)
	if _, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: unclaimed}); err == nil || !strings.Contains(err.Error(), "active Work Log claim required") {
		t.Fatalf("integration accepted an unclaimed checkout: %v", err)
	}
	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "log-authority-refusals", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, err := LogCheckpoint(ctx, LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SkipRemote: true}); err != nil {
		t.Fatal(err)
	}
	projection, err := readLocalProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	eventsPath := logVerbEventsPath(worktree)
	projectionPath := logVerbProjectionPath(worktree)
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	beforeClaim := logVerbFileBytes(t, claimPath)
	originalEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents := originalEvents
	beforeHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	assertUntouched := func(label string) {
		t.Helper()
		logVerbAssertFileBytes(t, label, eventsPath, beforeEvents)
		logVerbAssertFileBytes(t, label, claimPath, beforeClaim)
		if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != beforeHead {
			t.Fatalf("%s moved HEAD from %s to %s", label, beforeHead, got)
		}
	}

	unresolved := projection
	unresolved.Conflict = "unrelated_conflict"
	wtLifeCovWriteJSON(t, logVerbProjectionPath(worktree), unresolved)
	beforeProjection := logVerbFileBytes(t, projectionPath)
	if _, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err == nil || !strings.Contains(err.Error(), "unresolved conflict") {
		t.Fatalf("unrelated conflict accepted: %v", err)
	}
	assertUntouched("unrelated conflict")
	logVerbAssertFileBytes(t, "unrelated conflict", projectionPath, beforeProjection)

	unresolved.Conflict = "integrate_conflict"
	unresolved.LastTarget = &LocalTargetEvidence{Ref: "origin/main", SHA: "not-a-commit"}
	wtLifeCovWriteJSON(t, logVerbProjectionPath(worktree), unresolved)
	beforeProjection = logVerbFileBytes(t, projectionPath)
	if _, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err == nil || !strings.Contains(err.Error(), "verify checkpointed integrate conflict resolution") {
		t.Fatalf("invalid target ancestry accepted: %v", err)
	}
	assertUntouched("invalid target ancestry")
	logVerbAssertFileBytes(t, "invalid target ancestry", projectionPath, beforeProjection)

	wtLifeCovWriteJSON(t, logVerbProjectionPath(worktree), projection)
	beforeProjection = logVerbFileBytes(t, projectionPath)
	if _, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "missing-target"}); err == nil || !strings.Contains(err.Error(), "missing-target") {
		t.Fatalf("integration did not refuse the missing target ref: %v", err)
	}
	assertUntouched("missing target")
	logVerbAssertFileBytes(t, "missing target", projectionPath, beforeProjection)

	corrupt := []byte("{invalid event JSON\n")
	if err := os.WriteFile(eventsPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeEvents = corrupt
	beforeProjection = logVerbFileBytes(t, projectionPath)
	if _, err := LogRefresh(ctx, LogRefreshOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main"}); err == nil {
		t.Fatal("refresh accepted a corrupt authoritative journal")
	}
	assertUntouched("refresh with corrupt journal")
	logVerbAssertFileBytes(t, "refresh with corrupt journal", projectionPath, beforeProjection)
	if _, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Strategy: "merge"}); err == nil {
		t.Fatal("integration accepted a corrupt authoritative journal")
	}
	assertUntouched("integrate with corrupt journal")
	logVerbAssertFileBytes(t, "integrate with corrupt journal", projectionPath, beforeProjection)

	if err := os.WriteFile(eventsPath, originalEvents, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeEvents = originalEvents
	if err := os.WriteFile(logVerbProjectionPath(worktree), []byte("{invalid projection"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeProjection = logVerbFileBytes(t, projectionPath)
	if _, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err == nil {
		t.Fatal("integration accepted a corrupt local projection")
	}
	assertUntouched("corrupt projection")
	logVerbAssertFileBytes(t, "corrupt projection", projectionPath, beforeProjection)
}

func TestE2ELogArchiveRequiresReadableProjectionAndEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := newJournalWorktree(t)
	if _, err := LogArchive(ctx, LogArchiveOptions{Worktree: root, Force: true}); err == nil || !strings.Contains(err.Error(), "local projection") {
		t.Fatalf("missing projection accepted: %v", err)
	}
	directory, err := openLocalWorkLogDir(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteJSON(t, logVerbProjectionPath(root), LocalWorkLogProjection{Version: 1, Lifecycle: "terminal"})
	if _, err := LogArchive(ctx, LogArchiveOptions{Worktree: root, Force: true}); err == nil || !strings.Contains(err.Error(), "requires local events") {
		t.Fatalf("empty journal accepted: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "foreign-events"), logVerbEventsPath(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := LogArchive(ctx, LogArchiveOptions{Worktree: root, Force: true}); err == nil || strings.Contains(err.Error(), "requires local events") {
		t.Fatalf("redirected event journal accepted: %v", err)
	}
}

func TestE2ELogArchiveUsesManifestOrUnknownEffortAndPreservesCopiedBytes(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"manifest", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			root := newJournalWorktree(t)
			if scenario == "manifest" {
				manifest := newCreatedManifest("archive-from-manifest")
				manifest.Worktree = root
				if err := WriteManifest(root, manifest); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := appendLocalEvent(root, LocalWorkLogEvent{Type: LocalEventInit, At: time.Now().Add(-8 * 24 * time.Hour), Message: "original event"}); err != nil {
				t.Fatal(err)
			}
			projection, err := readLocalProjection(root)
			if err != nil {
				t.Fatal(err)
			}
			projection.EffortID = ""
			projection.Lifecycle = "terminal"
			wtLifeCovWriteJSON(t, logVerbProjectionPath(root), projection)
			before, err := os.ReadFile(logVerbEventsPath(root))
			if err != nil {
				t.Fatal(err)
			}
			projectsRoot := t.TempDir()
			archived, err := LogArchive(ctx, LogArchiveOptions{ProjectsRoot: projectsRoot, Worktree: root, Apply: true})
			if err != nil || !archived.Applied || archived.Event == nil || archived.Event.Type != LocalEventArchive {
				t.Fatalf("archive = %+v, %v", archived, err)
			}
			destination, ok := archived.Event.Extra["archive_path"].(string)
			if !ok {
				t.Fatalf("archive event lacks destination: %+v", archived.Event)
			}
			effort := "unknown-effort"
			if scenario == "manifest" {
				effort = "archive-from-manifest"
			}
			if filepath.Base(filepath.Dir(filepath.Dir(destination))) != effort {
				t.Fatalf("archive destination %q did not select effort %q", destination, effort)
			}
			copied, err := os.ReadFile(filepath.Join(destination, worklogDirectory, localWorkLogEventsName))
			if err != nil || string(copied) != string(before) {
				t.Fatalf("copied journal differs from pre-archive bytes: %q, %v", copied, err)
			}
		})
	}
}

func TestE2ELogArchiveRefusesBlockedPrivateDestinationWithoutAppending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := newJournalWorktree(t)
	if _, _, err := appendLocalEvent(root, LocalWorkLogEvent{Type: LocalEventInit, Message: "source event"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(logVerbEventsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	beforeProjection := logVerbFileBytes(t, logVerbProjectionPath(root))
	beforeBranch := gitTestOutput(t, root, "symbolic-ref", "HEAD")
	projectsRoot := t.TempDir()
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "worklogs"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LogArchive(ctx, LogArchiveOptions{ProjectsRoot: projectsRoot, Worktree: root, Apply: true, Force: true}); err == nil || !strings.Contains(err.Error(), filepath.Join(home, "worklogs")) {
		t.Fatalf("archive did not report blocked private worklogs destination: %v", err)
	}
	if after, err := os.ReadFile(logVerbEventsPath(root)); err != nil || string(after) != string(before) {
		t.Fatalf("failed archive changed source journal: %q, %v", after, err)
	}
	logVerbAssertFileBytes(t, "blocked destination", logVerbProjectionPath(root), beforeProjection)
	if got := gitTestOutput(t, root, "symbolic-ref", "HEAD"); got != beforeBranch {
		t.Fatalf("blocked destination moved branch: %s to %s", beforeBranch, got)
	}
}

func TestE2ELogArchiveRejectsUnsafeHomeAndCopySourceWithoutAppending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := newJournalWorktree(t)
	if _, _, err := appendLocalEvent(root, LocalWorkLogEvent{Type: LocalEventInit, Message: "source event"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(logVerbEventsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	beforeProjection := logVerbFileBytes(t, logVerbProjectionPath(root))
	beforeBranch := gitTestOutput(t, root, "symbolic-ref", "HEAD")
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := LogArchive(ctx, LogArchiveOptions{ProjectsRoot: loop, Worktree: root, Apply: true, Force: true}); err == nil || !strings.Contains(err.Error(), "too many links") {
		t.Fatalf("archive accepted a looped projects root or failed elsewhere: %v", err)
	}
	if after, err := os.ReadFile(logVerbEventsPath(root)); err != nil || string(after) != string(before) {
		t.Fatalf("unsafe home changed source journal: %q, %v", after, err)
	}
	logVerbAssertFileBytes(t, "unsafe home", logVerbProjectionPath(root), beforeProjection)
	if got := gitTestOutput(t, root, "symbolic-ref", "HEAD"); got != beforeBranch {
		t.Fatalf("unsafe home moved branch: %s to %s", beforeBranch, got)
	}

	broken := filepath.Join(root, journalRootDirectory, journalLocalDirectory, "dangling-sidecar")
	if err := os.Symlink("missing-sidecar", broken); err != nil {
		t.Fatal(err)
	}
	if _, err := LogArchive(ctx, LogArchiveOptions{ProjectsRoot: t.TempDir(), Worktree: root, Apply: true, Force: true}); err == nil || !strings.Contains(err.Error(), broken) {
		t.Fatalf("archive did not report dangling sidecar copy failure: %v", err)
	}
	if after, err := os.ReadFile(logVerbEventsPath(root)); err != nil || string(after) != string(before) {
		t.Fatalf("copy failure changed source journal: %q, %v", after, err)
	}
	logVerbAssertFileBytes(t, "copy failure", logVerbProjectionPath(root), beforeProjection)
	if got := gitTestOutput(t, root, "symbolic-ref", "HEAD"); got != beforeBranch {
		t.Fatalf("copy failure moved branch: %s to %s", beforeBranch, got)
	}
}

func TestE2ELogArchiveCopyCannotHideBlockedJournalAppend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := newJournalWorktree(t)
	if _, _, err := appendLocalEvent(root, LocalWorkLogEvent{Type: LocalEventInit, Message: "source event"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(logVerbEventsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	beforeProjection := logVerbFileBytes(t, logVerbProjectionPath(root))
	beforeBranch := gitTestOutput(t, root, "symbolic-ref", "HEAD")
	lockPath := filepath.Join(root, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogLockName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LogArchive(ctx, LogArchiveOptions{ProjectsRoot: t.TempDir(), Worktree: root, Apply: true, Force: true}); err == nil || !strings.Contains(err.Error(), "local work-log journal lock") {
		t.Fatalf("archive did not report blocked journal append: %v", err)
	}
	if after, err := os.ReadFile(logVerbEventsPath(root)); err != nil || string(after) != string(before) {
		t.Fatalf("blocked append changed source journal: %q, %v", after, err)
	}
	logVerbAssertFileBytes(t, "blocked append", logVerbProjectionPath(root), beforeProjection)
	if got := gitTestOutput(t, root, "symbolic-ref", "HEAD"); got != beforeBranch {
		t.Fatalf("blocked append moved branch: %s to %s", beforeBranch, got)
	}
}
