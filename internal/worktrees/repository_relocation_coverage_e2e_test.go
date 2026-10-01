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
	"time"
)

//nolint:paralleltest // newGitFixture sets process-wide Git environment for these native refusals.
func TestE2ELegacyRepositoryRelocationRefusesMissingLiveProof(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	entry := ListResult{Task: "legacy-proof", Repository: "newco/renamed", CanonicalDir: fixture.canonical,
		WorktreeDir: fixture.canonical, Branch: "feature", HeadSHA: head, RemoteTargetSHA: head, IntegratedAtOrigin: true}
	check := func(label, want string, mutate func(*ListResult)) {
		t.Helper()
		candidate := entry
		mutate(&candidate)
		proved, err := legacyRepositoryRelocationForCleanup(context.Background(), fixture.home, fixture.projectsRoot, candidate, false, nil)
		if proved || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: proof=%t err=%v, want refusal %q", label, proved, err, want)
		}
	}
	check("open pull request", "open pull request", func(e *ListResult) { e.OpenPullRequest = &PullRequest{URL: "https://example.test/pr/1"} })
	check("missing fetched target", "exact immutable head", func(e *ListResult) { e.RemoteTargetSHA = "" })
	check("missing immutable head", "no exact immutable head", func(e *ListResult) { e.HeadSHA = "bad" })
	check("unfetched immutable head", "recheck exact immutable head containment", func(e *ListResult) { e.HeadSHA = strings.Repeat("f", 40) })
	if err := os.WriteFile(filepath.Join(fixture.canonical, "feature-proof.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "feature-proof.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "feature-only")
	feature := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	check("not contained", "no longer contained", func(e *ListResult) { e.HeadSHA = feature })
	proved, err := legacyRepositoryRelocationForCleanup(context.Background(), fixture.home, fixture.projectsRoot, entry, false, nil)
	if err != nil || proved {
		t.Fatalf("absent local projection: proof=%t err=%v", proved, err)
	}
	projectionDir := filepath.Join(fixture.canonical, workLogProjectionDirectory)
	if err := os.MkdirAll(projectionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectionDir, workLogProjectionName), []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	proved, err = legacyRepositoryRelocationForCleanup(context.Background(), fixture.home, fixture.projectsRoot, entry, false, nil)
	if proved || err == nil {
		t.Fatalf("malformed local projection: proof=%t err=%v", proved, err)
	}
	if _, statErr := os.Stat(fixture.canonical); statErr != nil {
		t.Fatalf("refusal changed canonical checkout: %v", statErr)
	}
}

type relocationLegacyProofFixture struct {
	repositoryTransferFixture
	entry     ListResult
	claim     workLogClaim
	claimPath string
	moved     string
}

func newRelocationLegacyProofFixture(t *testing.T) relocationLegacyProofFixture {
	t.Helper()
	fixture := newRepositoryTransferFixture(t)
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "second base commit")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	task := "legacy-relocation-proof"
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, _, claimPath, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.moveRemote(t)
	if err := os.MkdirAll(filepath.Dir(fixture.destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.canonical, fixture.destination); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(fixture.destination, ".worktrees", task)
	gitTest(t, fixture.destination, "remote", "set-url", "origin", fixture.newRemote)
	gitTest(t, fixture.destination, "remote", "set-url", "--push", "origin", fixture.newRemote)
	gitTest(t, fixture.destination, "worktree", "repair", moved)
	head := gitTestOutput(t, moved, "rev-parse", "HEAD")
	return relocationLegacyProofFixture{repositoryTransferFixture: fixture, claim: claim, claimPath: claimPath, moved: moved,
		entry: ListResult{Task: task, Repository: "newco/renamed", CanonicalDir: fixture.destination,
			WorktreeDir: moved, WorktreesRoot: filepath.Join(fixture.destination, ".worktrees"), Local: true,
			Branch: created[0].Branch, HeadSHA: head, RemoteTargetSHA: head, IntegratedAtOrigin: true}}
}

//nolint:paralleltest // the native transfer fixture sets process-wide Git environment.
func TestE2ELegacyRepositoryRelocationRejectsChangedPrivateEvidence(t *testing.T) {
	fixture := newRelocationLegacyProofFixture(t)
	ctx := context.Background()
	check := func(label, want string) {
		t.Helper()
		proved, err := legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, false, nil)
		if proved || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: proof=%t err=%v, want %q", label, proved, err, want)
		}
	}
	projectionPath := filepath.Join(fixture.moved, workLogProjectionDirectory, workLogProjectionName)
	projectionBytes, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var projection workLogProjection
	if err := json.Unmarshal(projectionBytes, &projection); err != nil {
		t.Fatal(err)
	}
	projection.Lifecycle = "terminal"
	changed, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectionPath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	check("terminalized local projection", "not active")
	if err := os.WriteFile(projectionPath, projectionBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	if err := os.Rename(runPath, runPath+".hidden"); err != nil {
		t.Fatal(err)
	}
	check("missing immutable run", "no such file")
	if err := os.Rename(runPath+".hidden", runPath); err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(fixture.claimPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := fixture.claim
	claim.Version = 999
	changed, err = json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.claimPath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	check("altered immutable claim", "invalid")
	if err := os.WriteFile(fixture.claimPath, claimBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	prior := fixture.entry
	prior.HeadSHA = gitTestOutput(t, fixture.destination, "rev-parse", fixture.entry.HeadSHA+"^")
	prior.RemoteTargetSHA = prior.HeadSHA
	if proved, err := legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, prior, false, nil); proved || err == nil || !strings.Contains(err.Error(), "not descended from the claimed base") {
		t.Fatalf("head before claimed base: proof=%t err=%v", proved, err)
	}
	proved, err := legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, false, nil)
	if err != nil || !proved {
		t.Fatalf("restored private evidence: proof=%t err=%v", proved, err)
	}
}

//nolint:paralleltest // the native transfer fixture sets process-wide Git environment.
func TestE2ELegacyRepositoryRelocationReplaysExactIntentBeforeReceipt(t *testing.T) {
	fixture := newRelocationLegacyProofFixture(t)
	ctx := context.Background()
	checkRefusal := func(label, want string, mutate func(*ListResult)) {
		t.Helper()
		candidate := fixture.entry
		mutate(&candidate)
		proved, err := legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, candidate, false, nil)
		if proved || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: proof=%t err=%v, want %q", label, proved, err, want)
		}
	}
	checkRefusal("claim branch mismatch", "immutable claim branch", func(e *ListResult) { e.Branch = "another-branch" })
	checkRefusal("unsupported external placement", "supported managed-worktree layout", func(e *ListResult) { e.External = true })
	gitTest(t, fixture.destination, "remote", "set-url", "origin", "git@github.com:other/repository.git")
	checkRefusal("changed origin", "origin does not identify", func(*ListResult) {})
	gitTest(t, fixture.destination, "remote", "set-url", "origin", fixture.newRemote)
	proved, err := legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, false, nil)
	if err != nil || !proved {
		t.Fatalf("read-only legacy transfer proof: proof=%t err=%v", proved, err)
	}
	if resolution, err := latestRelocationResolution(fixture.home, fixture.claim, fixture.moved); err != nil || resolution.receipt != nil {
		t.Fatalf("read-only proof published receipt: %+v, %v", resolution, err)
	}
	interrupted := errors.New("stop before immutable receipt")
	proved, err = legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, true, func() error { return interrupted })
	if proved || !errors.Is(err, interrupted) {
		t.Fatalf("interrupted after durable intent: proof=%t err=%v", proved, err)
	}
	intent, _, intentErr := pendingRelocationIntent(fixture.home, fixture.claim, fixture.moved, fixture.entry.Branch, fixture.entry.HeadSHA)
	resolution, err := latestRelocationResolution(fixture.home, fixture.claim, fixture.moved)
	if intentErr != nil || intent == nil || err != nil || resolution.receipt != nil {
		t.Fatalf("interrupted intent/receipt = intent=%+v resolution=%+v errors=%v/%v", intent, resolution, intentErr, err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	poisonedReceipt := filepath.Join(runPath, "relocations", relocationReceiptName(fixture.claim.ClaimID, intent.OperationID))
	proved, err = legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, true, func() error {
		return os.WriteFile(poisonedReceipt, []byte("{broken"), 0o600)
	})
	if proved || err == nil || !strings.Contains(err.Error(), "append legacy repository relocation receipt") {
		t.Fatalf("malformed receipt after durable intent: proof=%t err=%v", proved, err)
	}
	if err := os.Remove(poisonedReceipt); err != nil {
		t.Fatal(err)
	}
	proved, err = legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, true, nil)
	if err != nil || !proved {
		t.Fatalf("exact intent replay: proof=%t err=%v", proved, err)
	}
	resolution, err = latestRelocationResolution(fixture.home, fixture.claim, fixture.moved)
	if err != nil || resolution.receipt == nil {
		t.Fatalf("replayed receipt = %+v, %v", resolution, err)
	}
	proved, err = legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, true, nil)
	if err != nil || proved {
		t.Fatalf("completed replay must be a no-op: proof=%t err=%v", proved, err)
	}
}

//nolint:paralleltest // the native transfer fixture sets process-wide Git environment.
func TestE2ELegacyRepositoryRelocationRejectsJournalAndPushDrift(t *testing.T) {
	fixture := newRelocationLegacyProofFixture(t)
	ctx := context.Background()
	check := func(label, want string) {
		t.Helper()
		proved, err := legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, false, nil)
		if proved || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: proof=%t err=%v, want refusal %q", label, proved, err, want)
		}
	}
	gitTest(t, fixture.destination, "remote", "set-url", "--push", "origin", "git@github.com:other/repository.git")
	check("changed push origin", "relocated repository origin")
	gitTest(t, fixture.destination, "remote", "set-url", "--push", "origin", fixture.newRemote)
	run, runPath, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	journalDir := filepath.Join(runPath, "relocations")
	if err := os.Mkdir(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(journalDir, fixture.claim.ClaimID+"-bogus.intent.json")
	if err := os.WriteFile(malformed, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("malformed immutable journal", "decode relocation journal")
	if err := os.Remove(malformed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree,
		filepath.Join(fixture.moved, "other"), workLogRelocationLegacyCheckout, fixture.entry.HeadSHA,
		fixture.claim.Repository, fixture.entry.Repository, fixture.newRemote, relocationPlacementRecord{}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if proved, err := legacyRepositoryRelocationForCleanup(ctx, fixture.home, fixture.projectsRoot, fixture.entry, false, nil); err != nil || !proved {
		t.Fatalf("unrelated pending relocation must not match: proof=%t err=%v", proved, err)
	}
	_, mismatchedPath, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree,
		fixture.moved, workLogRelocationLegacyCheckout, fixture.entry.HeadSHA,
		fixture.claim.Repository, fixture.entry.Repository, "git@github.com:newco/renamed.git", relocationPlacementRecord{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	check("pending intent with changed remote URL", "pending legacy checkout attestation does not match")
	if err := os.Remove(mismatchedPath); err != nil {
		t.Fatal(err)
	}
	matching, _, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree,
		fixture.moved, workLogRelocationLegacyCheckout, fixture.entry.HeadSHA,
		fixture.claim.Repository, fixture.entry.Repository, fixture.newRemote, relocationPlacementRecord{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	duplicate := *matching
	duplicate.At = matching.At.Add(time.Second)
	duplicate.OperationID = relocationOperationID(fixture.claim.ClaimID, duplicate.Source, duplicate.Destination, duplicate.HeadSHA, duplicate.At)
	encoded, err := json.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	duplicatePath := filepath.Join(journalDir, relocationIntentName(fixture.claim.ClaimID, duplicate.OperationID))
	if err := os.WriteFile(duplicatePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	check("multiple matching immutable intents", "multiple pending legacy checkout attestations")
}

//nolint:paralleltest // the native transfer fixture sets process-wide Git environment.
func TestE2ERepositoryTransferRefusesAmbiguousClaimAndRedirectedOwner(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-ownership-proof", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.moveRemote(t)
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main"}
	projectionPath := filepath.Join(created[0].WorktreeDir, workLogProjectionDirectory, workLogProjectionName)
	original, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectionPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused, err := RelocateRepository(context.Background(), options)
	if err != nil || refused.Eligible || !strings.Contains(refused.Reason, "WB claim is ambiguous") {
		t.Fatalf("malformed private claim projection: %+v, %v", refused, err)
	}
	if err := os.WriteFile(projectionPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	owner := filepath.Join(fixture.projectsRoot, "newco")
	if err := os.Symlink(outside, owner); err != nil {
		t.Fatal(err)
	}
	options.Apply = true
	refused, err = RelocateRepository(context.Background(), options)
	if err == nil || refused.Applied {
		t.Fatalf("redirected destination owner admitted: %+v, %v", refused, err)
	}
	if _, err := os.Stat(fixture.canonical); err != nil {
		t.Fatalf("refusal moved the source canonical: %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("redirected owner received files: %v, %v", entries, err)
	}
}

//nolint:paralleltest // the native transfer fixture sets process-wide Git environment.
func TestE2ERepositoryTransferFinalizationRejectsChangedIntentAndReplaysExactOne(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-finalize-proof", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.moveRemote(t)
	interrupted := errors.New("pause before Work Log completion")
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true,
		beforeWorkLogCompletion: func(string) error { return interrupted }}
	if _, err := RelocateRepository(context.Background(), options); !errors.Is(err, interrupted) {
		t.Fatalf("transfer did not stop after durable intent: %v", err)
	}
	moved := filepath.Join(fixture.destination, ".worktrees", "transfer-finalize-proof")
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("interrupted transfer lost moved checkout: %v", err)
	}
	options.beforeWorkLogCompletion = nil
	wrong := options
	wrong.RemoteURL = "git@github.com:newco/renamed.git"
	if receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "does not match repository transfer") || len(receipts) != 0 {
		t.Fatalf("different remote URL completed intent: %v, %v", receipts, err)
	}
	gitTest(t, fixture.destination, "remote", "set-url", "origin", "git@github.com:other/repository.git")
	if receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options); err == nil || !strings.Contains(err.Error(), "relocated repository origin") || len(receipts) != 0 {
		t.Fatalf("changed destination origin completed intent: %v, %v", receipts, err)
	}
	gitTest(t, fixture.destination, "remote", "set-url", "origin", fixture.newRemote)
	gitTest(t, fixture.destination, "remote", "set-url", "--push", "origin", "git@github.com:other/repository.git")
	if receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options); err == nil || !strings.Contains(err.Error(), "relocated repository origin") || len(receipts) != 0 {
		t.Fatalf("changed destination push origin completed intent: %v, %v", receipts, err)
	}
	gitTest(t, fixture.destination, "remote", "set-url", "--push", "origin", fixture.newRemote)
	intent, _, err := pendingRelocationIntent(fixture.home, claim, moved, claim.Branch, gitTestOutput(t, moved, "rev-parse", "HEAD"))
	if err != nil || intent == nil {
		t.Fatalf("pending intent before exact finalization: %+v, %v", intent, err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	poisonedReceipt := filepath.Join(runPath, "relocations", relocationReceiptName(claim.ClaimID, intent.OperationID))
	if err := os.WriteFile(poisonedReceipt, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options); err == nil || len(receipts) != 0 {
		t.Fatalf("tampered immutable receipt completed intent: %v, %v", receipts, err)
	}
	if err := os.Remove(poisonedReceipt); err != nil {
		t.Fatal(err)
	}
	receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("exact pending intent finalization: %v, %v", receipts, err)
	}
	if repeated, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options); err != nil || len(repeated) != 0 {
		t.Fatalf("repeated finalization: %v, %v", repeated, err)
	}
	if _, err := os.Stat(created[0].WorktreeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old checkout path reappeared: %v", err)
	}
}

//nolint:paralleltest // the native fixture sets process-wide Git environment.
func TestE2ERepositoryTransferRefusesBlockedRegistrationLock(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	lockPath := filepath.Join(fixture.canonical, ".git", repositoryRegistrationLockName)
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main"}
	result, err := RelocateRepository(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "lock source repository registration") || result.Applied {
		t.Fatalf("obstructed registration lock: %+v, %v", result, err)
	}
	if _, err := os.Stat(fixture.canonical); err != nil {
		t.Fatalf("lock refusal moved canonical repository: %v", err)
	}
}

//nolint:paralleltest // the native fixture sets process-wide Git environment.
func TestE2ERepositoryTransferRefusesBlockedPrivateJournal(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-private-journal", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	journalPath := filepath.Join(runPath, "relocations")
	if err := os.WriteFile(journalPath, []byte("obstructed"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.moveRemote(t)
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	result, err := RelocateRepository(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record repository relocation intent") || result.Applied {
		t.Fatalf("obstructed private journal: %+v, %v", result, err)
	}
	if _, err := os.Stat(fixture.canonical); err != nil {
		t.Fatalf("journal refusal moved canonical repository: %v", err)
	}
	if _, err := os.Stat(fixture.destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal refusal created destination: %v", err)
	}
}

//nolint:paralleltest // the native fixture sets process-wide Git environment.
func TestE2ERepositoryTransferReceiptFailureRetainsMovedCheckout(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-receipt-failure", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.moveRemote(t)
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	options.beforeWorkLogCompletion = func(moved string) error {
		intent, _, err := pendingRelocationIntent(fixture.home, claim, moved, claim.Branch, gitTestOutput(t, moved, "rev-parse", "HEAD"))
		if err != nil || intent == nil {
			t.Fatalf("missing durable intent before receipt: %+v, %v", intent, err)
		}
		run, runPath, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
		if err != nil {
			return err
		}
		_ = run.Close()
		return os.WriteFile(filepath.Join(runPath, "relocations", relocationReceiptName(claim.ClaimID, intent.OperationID)), []byte("{broken"), 0o600)
	}
	result, err := RelocateRepository(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record repository relocation receipt") || result.Applied {
		t.Fatalf("tampered receipt refusal: %+v, %v", result, err)
	}
	moved := filepath.Join(fixture.destination, ".worktrees", "transfer-receipt-failure")
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("receipt error rolled back moved checkout: %v", err)
	}
	if _, err := os.Stat(fixture.canonical); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt error restored old canonical path: %v", err)
	}
}
