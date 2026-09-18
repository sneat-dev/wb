package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wtLogCovBacklogRecord builds one valid managed backlog record whose paths
// match the strict layout validateLifecycleBacklog enforces.
func wtLogCovBacklogRecord(t *testing.T, disposition string) (string, string, lifecycleBacklogRecord) {
	t.Helper()
	projectsRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	result := ListResult{
		Task: "task", Repository: "acme/app",
		CanonicalDir:  filepath.Join(projectsRoot, "acme", "app"),
		WorktreesRoot: worktreesRoot,
		WorktreeDir:   filepath.Join(worktreesRoot, "task", "acme", "app"),
		Branch:        "wb/task", Base: "main", HeadSHA: strings.Repeat("a", 40),
	}
	record := newLifecycleBacklogRecord(projectsRoot, result, disposition)
	return projectsRoot, worktreesRoot, record
}

func TestWtLogCovValidateLifecycleBacklog(t *testing.T) {
	projectsRoot, worktreesRoot, record := wtLogCovBacklogRecord(t, "removed")
	if err := validateLifecycleBacklog(record); err != nil {
		t.Fatalf("valid backlog record rejected: %v", err)
	}

	local := record
	local.Local = true
	local.WorktreesRoot = filepath.Join(record.CanonicalDir, ".worktrees")
	local.WorktreeDir = filepath.Join(local.WorktreesRoot, record.Task)
	local.ID = newLifecycleBacklogRecord(projectsRoot, ListResult{Task: record.Task, Repository: record.Repository,
		CanonicalDir: record.CanonicalDir, WorktreesRoot: local.WorktreesRoot, WorktreeDir: local.WorktreeDir,
		Branch: record.Branch, HeadSHA: record.HeadSHA}, "removed").ID
	if err := validateLifecycleBacklog(local); err != nil {
		t.Fatalf("valid local backlog record rejected: %v", err)
	}

	external := record
	external.External = true
	external.WorktreesRoot = worktreesRoot
	external.WorktreeDir = filepath.Join(t.TempDir(), "adopted", "app")
	external.ID = newLifecycleBacklogRecord(projectsRoot, ListResult{Task: record.Task, Repository: record.Repository,
		CanonicalDir: record.CanonicalDir, WorktreesRoot: external.WorktreesRoot, WorktreeDir: external.WorktreeDir,
		Branch: record.Branch, HeadSHA: record.HeadSHA}, "removed").ID
	if err := validateLifecycleBacklog(external); err != nil {
		t.Fatalf("valid external backlog record rejected: %v", err)
	}

	legacy := record
	legacy.WorktreeDir = filepath.Join(worktreesRoot, record.Task, "app")
	legacy.ID = newLifecycleBacklogRecord(projectsRoot, ListResult{Task: record.Task, Repository: record.Repository,
		CanonicalDir: record.CanonicalDir, WorktreesRoot: legacy.WorktreesRoot, WorktreeDir: legacy.WorktreeDir,
		Branch: record.Branch, HeadSHA: record.HeadSHA}, "removed").ID
	if err := validateLifecycleBacklog(legacy); err != nil {
		t.Fatalf("valid legacy managed layout rejected: %v", err)
	}

	detached := record
	detached.Detached = true
	detached.Branch = ""
	detached.RemoteHeadSHA = ""
	detached.ID = newLifecycleBacklogRecord(projectsRoot, ListResult{Task: record.Task, Repository: record.Repository,
		CanonicalDir: record.CanonicalDir, WorktreesRoot: record.WorktreesRoot, WorktreeDir: record.WorktreeDir,
		Branch: "", HeadSHA: record.HeadSHA}, "removed").ID
	if err := validateLifecycleBacklog(detached); err != nil {
		t.Fatalf("valid detached backlog record rejected: %v", err)
	}

	createRecovery := record
	createRecovery.RecoveryKind = "create_work_log_failed"
	createRecovery.Disposition = "removed"
	createRecovery.Failure = "create failed"
	createRecovery.WorkLogEffort, createRecovery.WorkLogRun, createRecovery.WorkLogClaim = "effort", "run", strings.Repeat("b", 64)
	createRecovery.ID = newLifecycleBacklogRecord(projectsRoot, ListResult{Task: record.Task, Repository: record.Repository,
		CanonicalDir: record.CanonicalDir, WorktreesRoot: record.WorktreesRoot, WorktreeDir: record.WorktreeDir,
		Branch: record.Branch, HeadSHA: record.HeadSHA}, "removed").ID
	if err := validateLifecycleBacklog(createRecovery); err != nil {
		t.Fatalf("valid create-recovery record rejected: %v", err)
	}

	for name, mutate := range map[string]func(*lifecycleBacklogRecord){
		"version":            func(r *lifecycleBacklogRecord) { r.Version = 2 },
		"id":                 func(r *lifecycleBacklogRecord) { r.ID = "short" },
		"task":               func(r *lifecycleBacklogRecord) { r.Task = "../escape" },
		"repository":         func(r *lifecycleBacklogRecord) { r.Repository = "not-a-slug" },
		"relative root":      func(r *lifecycleBacklogRecord) { r.ProjectsRoot = "relative" },
		"canonical mismatch": func(r *lifecycleBacklogRecord) { r.CanonicalDir = filepath.Join(r.ProjectsRoot, "acme", "other") },
		"local layout":       func(r *lifecycleBacklogRecord) { r.Local = true; r.WorktreesRoot = filepath.Join(t.TempDir(), "x") },
		"external nested": func(r *lifecycleBacklogRecord) {
			r.External = true
			r.WorktreeDir = filepath.Join(r.WorktreesRoot, "nested", "app")
		},
		"managed layout": func(r *lifecycleBacklogRecord) {
			r.WorktreeDir = filepath.Join(r.WorktreesRoot, "wrong", "acme", "app")
		},
		"detached branch":   func(r *lifecycleBacklogRecord) { r.Detached = true },
		"branch":            func(r *lifecycleBacklogRecord) { r.Branch = "bad branch name" },
		"base":              func(r *lifecycleBacklogRecord) { r.Base = "bad branch name" },
		"head":              func(r *lifecycleBacklogRecord) { r.HeadSHA = "zzz" },
		"id evidence":       func(r *lifecycleBacklogRecord) { r.Disposition = "discarded" },
		"disposition":       func(r *lifecycleBacklogRecord) { r.Disposition = "elsewhere" },
		"recovery metadata": func(r *lifecycleBacklogRecord) { r.WorkLogClaim = strings.Repeat("b", 64) },
		"recovery kind":     func(r *lifecycleBacklogRecord) { r.RecoveryKind = "teleported" },
		"create disposition": func(r *lifecycleBacklogRecord) {
			r.RecoveryKind = "create_work_log_failed"
			r.Disposition = "discarded"
		},
		"create failure": func(r *lifecycleBacklogRecord) {
			r.RecoveryKind = "create_work_log_failed"
			r.Failure = "bad\x00detail"
		},
		"create partial": func(r *lifecycleBacklogRecord) { r.RecoveryKind = "create_work_log_failed"; r.WorkLogEffort = "effort" },
		"create bad claim": func(r *lifecycleBacklogRecord) {
			r.RecoveryKind = "create_work_log_failed"
			r.WorkLogEffort = "effort"
			r.WorkLogRun = "run"
			r.WorkLogClaim = "short"
		},
		"stage": func(r *lifecycleBacklogRecord) { r.Stage = "teleported" },
	} {
		mutated := record
		mutate(&mutated)
		if err := validateLifecycleBacklog(mutated); err == nil {
			t.Errorf("invalid lifecycle backlog %q was accepted", name)
		}
	}
}

func TestWtLogCovLifecycleBacklogIdentityAndPaths(t *testing.T) {
	_, _, record := wtLogCovBacklogRecord(t, "removed")
	result := ListResult{Task: record.Task, Repository: record.Repository, CanonicalDir: record.CanonicalDir,
		WorktreeDir: record.WorktreeDir, Branch: record.Branch, HeadSHA: record.HeadSHA}
	if got := lifecycleBacklogID(result, "removed"); got != record.ID {
		t.Fatalf("lifecycleBacklogID = %q, want %q", got, record.ID)
	}
	if lifecycleBacklogID(result, "discarded") == record.ID {
		t.Fatal("backlog id must bind the disposition")
	}
	home := t.TempDir()
	if got := lifecycleBacklogDirectory(home); got != filepath.Join(home, "reports", "worktree-cleanup", "backlog") {
		t.Fatalf("backlog directory = %q", got)
	}
	if got := lifecycleBacklogPath(home, record.ID); got != filepath.Join(lifecycleBacklogDirectory(home), record.ID+".json") {
		t.Fatalf("backlog path = %q", got)
	}
	if record.Stage != lifecycleStageSealed || record.Version != lifecycleBacklogVersion || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		t.Fatalf("new record = %#v", record)
	}
}

func TestWtLogCovOpenLifecycleBacklogDirectory(t *testing.T) {
	home := t.TempDir()
	if _, err := openLifecycleBacklogDirectory(home, false); err == nil {
		t.Fatal("absent backlog directory was opened")
	}
	directory, err := openLifecycleBacklogDirectory(home, true)
	if err != nil {
		t.Fatal(err)
	}
	info, err := directory.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("backlog directory mode = %o", info.Mode().Perm())
	}
	_ = directory.Close()
	if _, err := openLifecycleBacklogDirectory(home, false); err != nil {
		t.Fatalf("reopening a created backlog directory failed: %v", err)
	}
	if _, err := openLifecycleBacklogDirectory(filepath.Join(home, "missing-parent"), true); err != nil {
		t.Fatalf("creating a backlog under an existing home failed: %v", err)
	}
}

func TestWtLogCovPersistLifecycleBacklog(t *testing.T) {
	home := t.TempDir()
	if err := persistLifecycleBacklog(home, nil, lifecycleStageSealed); err == nil {
		t.Fatal("nil backlog record was accepted")
	}
	_, _, record := wtLogCovBacklogRecord(t, "removed")
	if err := persistLifecycleBacklog(home, &record, lifecycleStageComplete); err != nil {
		t.Fatal(err)
	}
	directory, err := openLifecycleBacklogDirectory(home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	var stored lifecycleBacklogRecord
	if err := readJSONAt(directory, record.ID+".json", &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Stage != lifecycleStageComplete || stored.ID != record.ID || stored.Disposition != "removed" {
		t.Fatalf("stored record = %#v", stored)
	}
	invalid := record
	invalid.Version = 9
	if err := persistLifecycleBacklog(home, &invalid, lifecycleStageComplete); err == nil {
		t.Fatal("invalid backlog record was persisted")
	}
}

func TestWtLogCovCompleteVacantLifecycleBacklog(t *testing.T) {
	fixture := newGitFixture(t)
	head := strings.Repeat("a", 40)
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	worktreeDir := filepath.Join(worktreesRoot, "task", "acme", "app")
	result := ListResult{Task: "task", Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: worktreesRoot, WorktreeDir: worktreeDir, Branch: "wb/missing", Base: "main", HeadSHA: head}
	record := newLifecycleBacklogRecord(fixture.projectsRoot, result, "removed")
	lockErr := errors.New("task namespace is gone")
	if err := completeVacantLifecycleBacklog(context.Background(), fixture.home, &record, lockErr); err != nil {
		t.Fatalf("vacant backlog was not completed: %v", err)
	}
	directory, err := openLifecycleBacklogDirectory(fixture.home, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	var stored lifecycleBacklogRecord
	if err := readJSONAt(directory, record.ID+".json", &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Stage != lifecycleStageComplete {
		t.Fatalf("stored stage = %q", stored.Stage)
	}

	// A checkout that still exists keeps the record open with the original lock error.
	present := record
	present.Stage = lifecycleStageSealed
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := completeVacantLifecycleBacklog(context.Background(), fixture.home, &present, lockErr); !errors.Is(err, lockErr) {
		t.Fatalf("existing worktree error = %v", err)
	}
	// A live remote branch keeps the record open too.
	if err := os.RemoveAll(worktreeDir); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "push", "origin", "HEAD:refs/heads/wb/missing")
	remote := record
	remote.Stage = lifecycleStageSealed
	if err := completeVacantLifecycleBacklog(context.Background(), fixture.home, &remote, lockErr); !errors.Is(err, lockErr) {
		t.Fatalf("live remote branch error = %v", err)
	}
	// Preserving the local branch excuses a surviving remote.
	// PreserveLocalBranch is create-recovery metadata, so a preserving record
	// must carry that recovery kind.
	preserved := remote
	preserved.PreserveLocalBranch = true
	preserved.RecoveryKind = "create_work_log_failed"
	if err := completeVacantLifecycleBacklog(context.Background(), fixture.home, &preserved, lockErr); err != nil {
		t.Fatalf("preserved branch backlog was not completed: %v", err)
	}
	// An unreadable canonical repository keeps the record open.
	if err := completeVacantLifecycleBacklog(context.Background(), fixture.home, &lifecycleBacklogRecord{}, lockErr); !errors.Is(err, lockErr) {
		t.Fatalf("invalid canonical error = %v", err)
	}
}

func TestWtLogCovDetachedAwareRemoteBranchHead(t *testing.T) {
	detached := &lifecycleBacklogRecord{Detached: true}
	if head, err := detachedAwareRemoteBranchHead(context.Background(), detached); err != nil || head != "" {
		t.Fatalf("detached record = %q/%v", head, err)
	}
	fixture := newGitFixture(t)
	record := &lifecycleBacklogRecord{CanonicalDir: fixture.canonical, Branch: "wb/absent"}
	if head, err := detachedAwareRemoteBranchHead(context.Background(), record); err != nil || head != "" {
		t.Fatalf("absent remote = %q/%v", head, err)
	}
	gitTest(t, fixture.canonical, "push", "origin", "HEAD:refs/heads/wb/present")
	record.Branch = "wb/present"
	head, err := detachedAwareRemoteBranchHead(context.Background(), record)
	if err != nil || head == "" {
		t.Fatalf("present remote = %q/%v", head, err)
	}
	if head != gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD") {
		t.Fatalf("remote head = %q", head)
	}
}

func TestWtLogCovSealCreateFailureBacklogClaim(t *testing.T) {
	fixture := newGitFixture(t)
	head := strings.Repeat("a", 40)
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", "task", "acme", "app")
	claim := workLogClaim{Version: 1, EffortID: "effort", RunID: "run", Task: "task", Repository: "acme/app",
		Worktree: worktreeDir, Branch: "wb/task", Base: "main", BaseSHA: head, Lifecycle: "active",
		RecordedAt: time.Now().UTC(), Model: "unknown", ModelProvenance: modelProvenanceUnknown}
	claim.ClaimID = workLogClaimID(claim.EffortID, CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree,
		Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	runDir, _, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", claim, false); err != nil {
		t.Fatal(err)
	}
	_ = claims.Close()
	defer func() { _ = runDir.Close() }()

	record := lifecycleBacklogRecord{Task: "task", Repository: "acme/app", WorktreeDir: worktreeDir, Branch: "wb/task",
		Base: "main", HeadSHA: head, WorkLogEffort: claim.EffortID, WorkLogRun: claim.RunID, WorkLogClaim: claim.ClaimID,
		RecoveryKind: "create_work_log_failed", Disposition: "removed"}
	if err := sealCreateFailureBacklogClaim(fixture.home, record); err != nil {
		t.Fatalf("sealing failed-create claim: %v", err)
	}
	terminals, err := openPrivateChild(runDir, "terminals", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = terminals.Close() }()
	var terminal workLogTerminalRecord
	if err := readJSONAt(terminals, claim.ClaimID+".json", &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Disposition != "create_failed" || terminal.FinalCommit != head || terminal.Lifecycle != "terminal" {
		t.Fatalf("terminal = %#v", terminal)
	}

	mismatch := record
	mismatch.Branch = "wb/other"
	if err := sealCreateFailureBacklogClaim(fixture.home, mismatch); err == nil {
		t.Fatal("mismatched backlog claim was sealed")
	}
	missing := record
	missing.WorkLogRun = "other-run"
	if err := sealCreateFailureBacklogClaim(fixture.home, missing); err == nil {
		t.Fatal("missing run was sealed")
	}
}
