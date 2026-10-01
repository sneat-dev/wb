//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

type recycleEvidenceFixture struct {
	workLog    workLogCoverageBatchFixture
	prior      workLogProjection
	commit     string
	claimID    string
	runPath    string
	terminal   string
	claimPath  string
	outboxPath string
	projection []byte
}

func newRecycleEvidenceFixture(t *testing.T) recycleEvidenceFixture {
	t.Helper()
	fixture := newWorkLogCoverageBatchFixture(t, "recycle-evidence")
	prior, err := readWorkLogProjection(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	commit := gitTestOutput(t, fixture.worktree, "rev-parse", "HEAD")
	runDir, runPath, err := openWorkLogRun(fixture.home, prior.EffortID, prior.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sealWorkLogTerminal(fixture.home, runDir, worktreeclaims.TerminalSealRequest{
		Claim: fixture.outcome.claim, FinalCommit: commit, Disposition: "recycled",
	}); err != nil {
		_ = runDir.Close()
		t.Fatal(err)
	}
	if err := runDir.Close(); err != nil {
		t.Fatal(err)
	}
	recoveryID := successorWorkLogClaimID(prior.ClaimID, "wb-recycle-recovery", "recycle_failed")
	projectionPath := filepath.Join(fixture.worktree, workLogProjectionDirectory, workLogProjectionName)
	projection, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	return recycleEvidenceFixture{
		workLog: fixture, prior: prior, commit: commit, claimID: recoveryID, runPath: runPath,
		terminal:   filepath.Join(runPath, "terminals", prior.ClaimID+".json"),
		claimPath:  filepath.Join(runPath, "claims", recoveryID+".json"),
		outboxPath: filepath.Join(fixture.home, "worklogs", prior.EffortID, "outbox", prior.RunID+"-"+recoveryID+"-claimed.json"),
		projection: projection,
	}
}

func (fixture recycleEvidenceFixture) recover() error {
	return recoverFailedRecycleClaim(fixture.workLog.home, fixture.workLog.worktree, fixture.commit, fixture.prior)
}

func (fixture recycleEvidenceFixture) assertPriorProjectionAndHEAD(t *testing.T) {
	t.Helper()
	path := filepath.Join(fixture.workLog.worktree, workLogProjectionDirectory, workLogProjectionName)
	if after, err := os.ReadFile(path); err != nil || string(after) != string(fixture.projection) {
		t.Fatalf("failed recovery changed prior projection: %q, %v", after, err)
	}
	if head := gitTestOutput(t, fixture.workLog.worktree, "rev-parse", "HEAD"); head != fixture.commit {
		t.Fatalf("failed recovery changed live HEAD: %q, want %q", head, fixture.commit)
	}
}

//nolint:paralleltest // newWorkLogCoverageBatchFixture sets process environment for the real Git checkout.
func TestE2EFailedRecycleRecoveryPreservesTerminalAndPublishesOneSuccessor(t *testing.T) {
	fixture := newRecycleEvidenceFixture(t)
	terminalBefore, err := os.ReadFile(fixture.terminal)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.recover(); err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(fixture.claimPath)
	if err != nil {
		t.Fatal(err)
	}
	var successor workLogClaim
	if err := json.Unmarshal(claimBytes, &successor); err != nil {
		t.Fatal(err)
	}
	if successor.ClaimID != fixture.claimID || successor.ParentClaimID != fixture.prior.ClaimID ||
		successor.AcquiredVia != "recycle_failed" || successor.Lifecycle != "active" {
		t.Fatalf("recovery successor identity = %+v", successor)
	}
	outboxBytes, err := os.ReadFile(fixture.outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	var event workLogPublicEvent
	if err := json.Unmarshal(outboxBytes, &event); err != nil || event.ClaimID != fixture.claimID || event.Disposition != "recycle_failed" {
		t.Fatalf("recovery outbox event = %+v, %v", event, err)
	}
	projection, err := readWorkLogProjection(fixture.workLog.worktree)
	if err != nil || projection.ClaimID != fixture.claimID || projection.Lifecycle != "active" {
		t.Fatalf("recovery projection = %+v, %v", projection, err)
	}
	if err := fixture.recover(); err != nil {
		t.Fatalf("identical recovery retry: %v", err)
	}
	for path, want := range map[string][]byte{fixture.terminal: terminalBefore, fixture.claimPath: claimBytes, fixture.outboxPath: outboxBytes} {
		if after, err := os.ReadFile(path); err != nil || string(after) != string(want) {
			t.Fatalf("retry rewrote immutable evidence %s: %q, %v", path, after, err)
		}
	}
	if head := gitTestOutput(t, fixture.workLog.worktree, "rev-parse", "HEAD"); head != fixture.commit {
		t.Fatalf("recovery changed live HEAD: %q", head)
	}
}

//nolint:paralleltest // each real Git fixture sets process environment.
func TestE2EFailedRecycleRecoveryRefusesMissingOrConflictingEvidence(t *testing.T) {
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("missing claims directory", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		if err := os.RemoveAll(filepath.Join(fixture.runPath, "claims")); err != nil {
			t.Fatal(err)
		}
		if err := fixture.recover(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing claims error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
	})
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("corrupt original claim", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		path := filepath.Join(fixture.runPath, "claims", fixture.prior.ClaimID+".json")
		if err := os.WriteFile(path, []byte(`{"bad":}`), 0o600); err != nil {
			t.Fatal(err)
		}
		var syntax *json.SyntaxError
		if err := fixture.recover(); !errors.As(err, &syntax) {
			t.Fatalf("corrupt original claim error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
	})
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("missing terminals directory", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		if err := os.RemoveAll(filepath.Dir(fixture.terminal)); err != nil {
			t.Fatal(err)
		}
		if err := fixture.recover(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing terminals error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
		if _, err := os.Lstat(fixture.claimPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing terminals published recovery claim: %v", err)
		}
	})
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("missing terminal", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		if err := os.Remove(fixture.terminal); err != nil {
			t.Fatal(err)
		}
		if err := fixture.recover(); err == nil || !strings.Contains(err.Error(), "read failed-recycle terminal") {
			t.Fatalf("missing terminal error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
		if _, err := os.Lstat(fixture.claimPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing terminal published recovery claim: %v", err)
		}
	})
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("wrong terminal disposition", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		var terminal workLogTerminalRecord
		terminalBytes, err := os.ReadFile(fixture.terminal)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(terminalBytes, &terminal); err != nil {
			t.Fatal(err)
		}
		terminal.Disposition = "landed"
		wtLifeCovWriteJSON(t, fixture.terminal, terminal)
		if err := fixture.recover(); err == nil || !strings.Contains(err.Error(), "matching recycled terminal") {
			t.Fatalf("wrong terminal error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
		if _, err := os.Lstat(fixture.claimPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wrong terminal published recovery claim: %v", err)
		}
	})
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("conflicting successor bytes", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		before := []byte(`{"conflict":true}`)
		if err := os.WriteFile(fixture.claimPath, before, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.recover(); err == nil || !strings.Contains(err.Error(), "immutable file already exists") {
			t.Fatalf("conflicting successor error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
		if after, err := os.ReadFile(fixture.claimPath); err != nil || string(after) != string(before) {
			t.Fatalf("conflicting successor bytes changed: %q, %v", after, err)
		}
		if _, err := os.Lstat(fixture.outboxPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("claim conflict published recovery event: %v", err)
		}
	})
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("redirected outbox", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		outboxDir := filepath.Dir(fixture.outboxPath)
		retained := outboxDir + "-retained"
		if err := os.Rename(outboxDir, retained); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, outboxDir); err != nil {
			t.Fatal(err)
		}
		if err := fixture.recover(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("redirected outbox error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
		if _, err := os.Stat(fixture.claimPath); err != nil {
			t.Fatalf("claim should precede outbox open: %v", err)
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatalf("redirected outbox received bytes: %v, %v", entries, err)
		}
	})
	//nolint:paralleltest // each subtest uses a fixture with t.Setenv.
	t.Run("conflicting outbox bytes", func(t *testing.T) {
		fixture := newRecycleEvidenceFixture(t)
		before := []byte(`{"conflict":true}`)
		if err := os.WriteFile(fixture.outboxPath, before, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.recover(); err == nil || !strings.Contains(err.Error(), "immutable file already exists") {
			t.Fatalf("conflicting recovery outbox error = %v", err)
		}
		fixture.assertPriorProjectionAndHEAD(t)
		if _, err := os.Stat(fixture.claimPath); err != nil {
			t.Fatalf("claim should precede outbox write: %v", err)
		}
		if after, err := os.ReadFile(fixture.outboxPath); err != nil || string(after) != string(before) {
			t.Fatalf("conflicting outbox bytes changed: %q, %v", after, err)
		}
	})
}

type preparedExternalEvidence struct {
	fixture        *externalTargetFixture
	target         sessionmove.WorkLogReference
	claimPath      string
	projectionPath string
	claimBytes     []byte
	projection     []byte
	head           string
}

func prepareExternalEvidence(t *testing.T) preparedExternalEvidence {
	t.Helper()
	fixture := newExternalTargetFixture(t)
	if _, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options); err != nil {
		t.Fatal(err)
	}
	target, err := sessionmove.ExpectedTargetWorkLogReference(fixture.base.request, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.base.home, "worklogs", target.EffortID, "runs", target.RunID, "claims", target.ClaimID+".json")
	projectionPath := filepath.Join(fixture.worktree, workLogProjectionDirectory, workLogProjectionName)
	claimBytes, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	return preparedExternalEvidence{
		fixture: fixture, target: target, claimPath: claimPath, projectionPath: projectionPath,
		claimBytes: claimBytes, projection: projection,
		head: gitTestOutput(t, fixture.worktree, "rev-parse", "HEAD"),
	}
}

func (prepared preparedExternalEvidence) assertPrivateUnchanged(t *testing.T, checkHEAD bool) {
	t.Helper()
	for path, want := range map[string][]byte{prepared.claimPath: prepared.claimBytes, prepared.projectionPath: prepared.projection} {
		if after, err := os.ReadFile(path); err != nil || string(after) != string(want) {
			t.Fatalf("target claim evidence changed at %s: %q, %v", path, after, err)
		}
	}
	if checkHEAD {
		if head := gitTestOutput(t, prepared.fixture.worktree, "rev-parse", "HEAD"); head != prepared.head {
			t.Fatalf("target HEAD changed: %q, want %q", head, prepared.head)
		}
	}
}

//nolint:paralleltest // external target fixtures set process WB home and XDG variables.
func TestE2EExternalTargetClaimLoadRefusesCorruptEvidenceAndReleasesClaimFence(t *testing.T) {
	//nolint:paralleltest // shares the parent process environment.
	t.Run("home resolution", func(t *testing.T) {
		prepared := prepareExternalEvidence(t)
		cycle := filepath.Join(t.TempDir(), "cycle")
		if err := os.Symlink("cycle", cycle); err != nil {
			t.Fatal(err)
		}
		_, _, unlock, err := loadExternalTargetClaim(cycle, prepared.fixture.base.request, prepared.fixture.digest, prepared.fixture.worktree)
		if err == nil || unlock != nil {
			t.Fatalf("cyclic home root returned unlock=%t, err=%v", unlock != nil, err)
		}
		prepared.assertPrivateUnchanged(t, true)
	})
	//nolint:paralleltest // shares the parent process environment.
	t.Run("missing private claim", func(t *testing.T) {
		prepared := prepareExternalEvidence(t)
		if err := os.Remove(prepared.claimPath); err != nil {
			t.Fatal(err)
		}
		_, _, unlock, err := loadExternalTargetClaim(prepared.fixture.base.projectsRoot, prepared.fixture.base.request, prepared.fixture.digest, prepared.fixture.worktree)
		if !errors.Is(err, os.ErrNotExist) || unlock != nil {
			t.Fatalf("missing private claim returned unlock=%t, err=%v", unlock != nil, err)
		}
		if after, err := os.ReadFile(prepared.projectionPath); err != nil || string(after) != string(prepared.projection) {
			t.Fatalf("claim read error changed projection: %q, %v", after, err)
		}
	})
	//nolint:paralleltest // shares the parent process environment.
	t.Run("invalid repository remote", func(t *testing.T) {
		prepared := prepareExternalEvidence(t)
		request := prepared.fixture.base.request
		request.RepositoryRemote = "not-a-remote"
		_, _, unlock, err := loadExternalTargetClaim(prepared.fixture.base.projectsRoot, request, prepared.fixture.digest, prepared.fixture.worktree)
		if err == nil || unlock != nil || !strings.Contains(err.Error(), "repository remote must be an absolute local path or a supported URL") {
			t.Fatalf("invalid remote returned unlock=%t, err=%v", unlock != nil, err)
		}
		prepared.assertPrivateUnchanged(t, true)
		_, _, unlock, err = loadExternalTargetClaim(prepared.fixture.base.projectsRoot, prepared.fixture.base.request, prepared.fixture.digest, prepared.fixture.worktree)
		if err != nil || unlock == nil {
			t.Fatalf("failed load retained the claim fence: unlock=%t, err=%v", unlock != nil, err)
		}
		unlock()
	})
	//nolint:paralleltest // shares the parent process environment.
	t.Run("missing immutable manifest", func(t *testing.T) {
		prepared := prepareExternalEvidence(t)
		manifest := filepath.Join(prepared.fixture.worktree, journalRootDirectory, journalLocalDirectory, manifestName)
		if err := os.Remove(manifest); err != nil {
			t.Fatal(err)
		}
		_, _, unlock, err := loadExternalTargetClaim(prepared.fixture.base.projectsRoot, prepared.fixture.base.request, prepared.fixture.digest, prepared.fixture.worktree)
		if err == nil || unlock != nil || !strings.Contains(err.Error(), "manifest") {
			t.Fatalf("missing manifest returned unlock=%t, err=%v", unlock != nil, err)
		}
		prepared.assertPrivateUnchanged(t, true)
	})
	//nolint:paralleltest // shares the parent process environment.
	t.Run("live HEAD drift", func(t *testing.T) {
		prepared := prepareExternalEvidence(t)
		if err := os.WriteFile(filepath.Join(prepared.fixture.worktree, "drift.txt"), []byte("new commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitTest(t, prepared.fixture.worktree, "add", "drift.txt")
		gitTest(t, prepared.fixture.worktree, "-c", "user.name=claim-test", "-c", "user.email=claim@example.test", "commit", "-m", "drift")
		_, _, unlock, err := loadExternalTargetClaim(prepared.fixture.base.projectsRoot, prepared.fixture.base.request, prepared.fixture.digest, prepared.fixture.worktree)
		if err == nil || unlock != nil || !strings.Contains(err.Error(), "live pin") {
			t.Fatalf("live HEAD drift returned unlock=%t, err=%v", unlock != nil, err)
		}
		prepared.assertPrivateUnchanged(t, false)
		if head := gitTestOutput(t, prepared.fixture.worktree, "rev-parse", "HEAD"); head == prepared.head {
			t.Fatal("live HEAD did not drift before refusal")
		}
	})
}
