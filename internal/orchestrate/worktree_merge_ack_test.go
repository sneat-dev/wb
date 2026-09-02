package orchestrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAcknowledgeWorktreeMergeReceiptCollisionIsAppendOnlyAndReplaySafe(t *testing.T) {
	fixture, receipt, options := collisionAcknowledgementFixture(t)
	receiptBefore, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBefore, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}

	dryRun, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options)
	if err != nil || dryRun.Status != "receipt_collision_acknowledged" || !dryRun.HistoricalValidationFailedOperatorAssertion {
		t.Fatalf("collision dry-run = %+v err=%v", dryRun, err)
	}
	if _, statErr := os.Stat(receiptCollisionAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run created acknowledgement: %v", statErr)
	}

	options.Apply, options.Actor, options.Reason = true, "reviewer", "audited historical prepare receipt collision"
	ack, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options)
	if err != nil || ack.ReceiptSHA256 != options.ExpectedReceiptSHA256 || ack.ImmutableClaimSHA256 != options.ExpectedImmutableClaimSHA256 {
		t.Fatalf("collision acknowledgement = %+v err=%v", ack, err)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
		t.Fatalf("collision receipt changed: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(claim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBefore) {
		t.Fatalf("collision claim changed: err=%v", readErr)
	}
	ackBefore, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{receipt.Sources[0].Worktree}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "may proceed only as --rebatch-receipt original") || blocked.ReceiptPath != receipt.ReceiptPath {
		t.Fatalf("ordinary prepare after collision acknowledgement = receipt %+v err=%v", blocked, err)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
		t.Fatalf("ordinary prepare changed collision receipt: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(claim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBefore) {
		t.Fatalf("ordinary prepare changed collision claim: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(ack.AcknowledgementPath); readErr != nil || !bytes.Equal(current, ackBefore) {
		t.Fatalf("ordinary prepare changed collision acknowledgement: err=%v", readErr)
	}
	replayed, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options)
	if err != nil || replayed.ID != ack.ID || replayed.AcknowledgementPath != ack.AcknowledgementPath || replayed.ReceiptSHA256 != ack.ReceiptSHA256 {
		t.Fatalf("collision acknowledgement replay = %+v err=%v", replayed, err)
	}
	extra := createMergeSource(t, fixture, "collision-extra", "feature/collision-extra", "extra.go", "package app\n\nfunc Extra() {}\n")
	if _, err := readReceiptCollisionAcknowledgement(receiptCollisionAcknowledgementPath(receipt.ReceiptPath), receipt); err != nil {
		t.Fatalf("collision acknowledgement changed before rebatch: %v", err)
	}
	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{receipt.Sources[0].Worktree, extra.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath,
	})
	if err != nil || replacement.RebatchOf != receipt.ReceiptPath || len(replacement.RebatchedCandidates) != 1 || replacement.RebatchedCandidates[0] != receipt.Candidate {
		t.Fatalf("collision rebatch = %+v err=%v", replacement, err)
	}
}

func TestReceiptCollisionAcknowledgementRebatchesAtDescendantTargetWithRootCompleteSource(t *testing.T) {
	fixture, receipt, options := collisionAcknowledgementFixture(t)
	receiptBefore, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBefore, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	options.Apply, options.Actor, options.Reason = true, "reviewer", "audited collision rebatch after target advance"
	ack, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	ackBefore, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}

	writeEngineFile(t, filepath.Join(fixture.canonical, "target-advance.go"), "package app\n\nfunc TargetAdvance() {}\n")
	runEngineGit(t, fixture.canonical, "add", "target-advance.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance collision rebatch target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	currentTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	rootComplete := createMergeSource(t, fixture, "collision-root-complete", "feature/collision-root-complete", "root_complete.go", "package app\n\nfunc RootComplete() {}\n")
	runEngineGit(t, rootComplete.WorktreeDir, "merge", "--no-edit", receipt.Candidate.SHA)
	rootCompleteHead := strings.TrimSpace(runEngineGit(t, rootComplete.WorktreeDir, "rev-parse", "HEAD"))
	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{receipt.Sources[0].Worktree, rootComplete.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.TargetSHA != currentTarget || replacement.RebatchOf != receipt.ReceiptPath || len(replacement.RebatchedCandidates) != 1 || replacement.RebatchedCandidates[0] != receipt.Candidate {
		t.Fatalf("advanced collision replacement = %+v, want target %s", replacement, currentTarget)
	}
	for _, root := range []string{receipt.TargetSHA, currentTarget, receipt.Candidate.SHA, receipt.Sources[0].SHA, rootCompleteHead} {
		contains, ancestorErr := isMergeAncestor(context.Background(), replacement.Candidate.Worktree, root, replacement.Candidate.SHA)
		if ancestorErr != nil || !contains {
			t.Fatalf("replacement omits required root %s: contains=%t err=%v", root, contains, ancestorErr)
		}
	}
	preparedRebatch, err := readPreparedWorktreeMergeRebatch(rebatchPath(receipt.ReceiptPath), receipt)
	if err != nil || preparedRebatch.CurrentTargetSHA != currentTarget || preparedRebatch.Replacement != replacement.Candidate {
		t.Fatalf("advanced collision rebatch acknowledgement = %+v err=%v", preparedRebatch, err)
	}
	for _, immutable := range []struct {
		path string
		want []byte
	}{
		{receipt.ReceiptPath, receiptBefore},
		{claim.ClaimPath, claimBefore},
		{ack.AcknowledgementPath, ackBefore},
	} {
		current, readErr := os.ReadFile(immutable.path)
		if readErr != nil || !bytes.Equal(current, immutable.want) {
			t.Fatalf("immutable evidence changed at %s: err=%v", immutable.path, readErr)
		}
	}
}

func TestReceiptCollisionAcknowledgementRebatchRefusesMissingOrChangedRoots(t *testing.T) {
	fixture, receipt, options := collisionAcknowledgementFixture(t)
	options.Apply, options.Actor, options.Reason = true, "reviewer", "audited collision rebatch root refusals"
	if _, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	rootComplete := createMergeSource(t, fixture, "collision-root-refusal", "feature/collision-root-refusal", "root_complete.go", "package app\n\nfunc RootComplete() {}\n")
	runEngineGit(t, rootComplete.WorktreeDir, "merge", "--no-edit", receipt.Candidate.SHA)
	extra := createMergeSource(t, fixture, "collision-extra-refusal", "feature/collision-extra-refusal", "extra.go", "package app\n\nfunc Extra() {}\n")
	extraSources, _, _, err := inspectWorktreeMergeSources(context.Background(), fixture.githubDir, []string{rootComplete.WorktreeDir, extra.WorktreeDir}, "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("missing immutable source ref", func(t *testing.T) {
		_, err := validatePreparedWorktreeMergeRebatch(context.Background(), fixture.githubDir, receipt.ReceiptPath, "acme/app", "main", extraSources)
		if err == nil || !strings.Contains(err.Error(), "removes immutable source ref") {
			t.Fatalf("missing root error = %v", err)
		}
	})
	t.Run("changed immutable source ref", func(t *testing.T) {
		changed := receipt.Sources[0]
		changed.SHA = receipt.TargetSHA
		_, err := validatePreparedWorktreeMergeRebatch(context.Background(), fixture.githubDir, receipt.ReceiptPath, "acme/app", "main", append([]WorktreeMergeSource{changed}, extraSources[:1]...))
		if err == nil || !strings.Contains(err.Error(), "not a descendant") {
			t.Fatalf("changed root error = %v", err)
		}
	})
}

func TestReceiptCollisionAcknowledgementRevalidatesClaimAtRebatchAndCleanup(t *testing.T) {
	fixture, receipt, options := collisionAcknowledgementFixture(t)
	options.Apply, options.Actor, options.Reason = true, "reviewer", "audited historical prepare receipt collision"
	if _, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(claimBytes, []byte("test-model"), []byte("other-model"), 1)
	if bytes.Equal(tampered, claimBytes) {
		t.Fatal("claim fixture has no model value to tamper")
	}
	if err := os.WriteFile(claim.ClaimPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	extra := createMergeSource(t, fixture, "collision-claim-extra", "feature/collision-claim-extra", "extra.go", "package app\n\nfunc Extra() {}\n")
	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{receipt.Sources[0].Worktree, extra.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: receipt.ReceiptPath,
	})
	if err == nil || !strings.Contains(err.Error(), "immutable claim SHA256") {
		t.Fatalf("rebatch after claim tamper error = %v", err)
	}
	if err := validateRebatchedWorktreeMergeCleanup(context.Background(), fixture.githubDir, WorktreeMergeReceipt{RebatchOf: receipt.ReceiptPath}); err == nil || !strings.Contains(err.Error(), "immutable claim SHA256") {
		t.Fatalf("cleanup after claim tamper error = %v", err)
	}
}

func TestAcknowledgeWorktreeMergeReceiptCollisionRefusesMismatchedEvidenceWithoutWrite(t *testing.T) {
	_, receipt, options := collisionAcknowledgementFixture(t)
	options.ExpectedCandidateSHA = strings.Repeat("f", 40)
	options.Apply, options.Actor, options.Reason = true, "reviewer", "must refuse mismatched candidate"
	if _, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options); err == nil || !strings.Contains(err.Error(), "do not match explicit expected identity") {
		t.Fatalf("mismatched collision acknowledgement error = %v", err)
	}
	if _, statErr := os.Stat(receiptCollisionAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("falsifier created acknowledgement: %v", statErr)
	}
}

func TestAcknowledgeWorktreeMergeReceiptCollisionNeverOverwritesConcurrentAcknowledgement(t *testing.T) {
	_, receipt, options := collisionAcknowledgementFixture(t)
	options.Apply, options.Actor, options.Reason = false, "reviewer", "audited historical prepare receipt collision"
	intended, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := intended
	conflicting.Actor, conflicting.Reason = "other-reviewer", "different audited recovery"
	conflicting.ID = receiptCollisionAcknowledgementID(conflicting)
	conflictingBytes, err := json.MarshalIndent(conflicting, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	conflictingBytes = append(conflictingBytes, '\n')
	path := receiptCollisionAcknowledgementPath(receipt.ReceiptPath)
	previousLink := linkReceiptCollisionAcknowledgement
	linkReceiptCollisionAcknowledgement = func(_, destination string) error {
		if destination != path {
			t.Fatalf("atomic create destination = %s, want %s", destination, path)
		}
		if err := os.WriteFile(destination, conflictingBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		return os.ErrExist
	}
	t.Cleanup(func() { linkReceiptCollisionAcknowledgement = previousLink })

	options.Apply = true
	if _, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options); err == nil || !strings.Contains(err.Error(), "binds different immutable evidence") {
		t.Fatalf("concurrent conflicting acknowledgement error = %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, conflictingBytes) {
		t.Fatalf("conflicting acknowledgement was replaced: err=%v", err)
	}

	linkReceiptCollisionAcknowledgement = previousLink
	replayed, err := AcknowledgeWorktreeMergeReceiptCollision(context.Background(), options)
	if err == nil || replayed.ID != "" {
		t.Fatalf("conflicting replay was accepted: acknowledgement=%+v err=%v", replayed, err)
	}
}

func collisionAcknowledgementFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, WorktreeMergeReceiptCollisionAcknowledgementOptions) {
	t.Helper()
	fixture := newEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: add collision validation fixture")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "collision-source", "feature/collision", "candidate.go", "package app\n\nfunc Candidate() { missingCandidate }\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err == nil || receipt.Status != WorktreeMergeValidationFailed {
		t.Fatalf("initial collision receipt = %+v err=%v", receipt, err)
	}
	historical := receipt.Sources[0]
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "candidate.go"), "package app\n\nfunc Candidate() {}\n")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "advance.txt"), "advance\n")
	runEngineGit(t, source.WorktreeDir, "add", "candidate.go", "advance.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "test: advance collision source")
	currentSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", currentSource)
	receipt.Sources[0].SHA, receipt.Sources[0].Merged = currentSource, true
	receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: time.Now().UTC(), Sources: []WorktreeMergeSource{historical}}}
	receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	receipt.Status, receipt.Failure = WorktreeMergePreparing, "historical validation failure asserted by operator"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, receipt, WorktreeMergeReceiptCollisionAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ExpectedReceiptSHA256: receiptHash,
		ExpectedImmutableClaimSHA256: hex.EncodeToString(claimDigest[:]), ExpectedTargetSHA: receipt.TargetSHA,
		ExpectedCandidateSHA: receipt.Candidate.SHA, ExpectedCurrentSourceSHA: currentSource, ExpectedHistoricalRefreshSourceSHA: historical.SHA,
	}
}

func TestAcknowledgeLandedValidationFailureLeavesHistoricalReceiptUntouched(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-source", "feature/ack", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	receipt.Failure = "candidate validation failed"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	// The candidate is the exact commit that the current target will contain.
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	dryRun, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Status != "landed_failure_acknowledged" || dryRun.CurrentTargetSHA != receipt.Candidate.SHA {
		t.Fatalf("dry-run acknowledgement = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "verified candidate landed before validation receipt became terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ID == "" || ack.AcknowledgementPath == "" {
		t.Fatalf("applied acknowledgement lacks identity: %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(original) {
		t.Fatal("historical merge receipt was rewritten")
	}
	if _, err := os.Stat(ack.AcknowledgementPath); err != nil {
		t.Fatalf("acknowledgement missing: %v", err)
	}
	ackAgain, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "retry",
	})
	if err != nil || ackAgain.ID != ack.ID {
		t.Fatalf("idempotent acknowledgement = %+v err=%v", ackAgain, err)
	}
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "acknowledged as a historical landed failure") {
		t.Fatalf("acknowledged receipt was replayable: %v", err)
	}

	// The old non-terminal receipt no longer owns the lane, so a new source
	// revision can pass normal prepare preflight without branch-only evidence.
	newSource := createMergeSource(t, fixture, "ack-source-new", "feature/ack-new", "new.txt", "new\n")
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{newSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != WorktreeMergePrepared || next.Candidate.SHA == "" {
		t.Fatalf("new merge preflight = %+v", next)
	}
}

func TestAcknowledgeLandedPostTargetCIFailureLeavesReceiptForFreshForwardRepair(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "contactus-source", "feature/contactus", "contactus.txt", "contactus\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce Contactus: the candidate was directly landed, then the exact
	// target CI receipt failed and left its immutable land receipt nonterminal.
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.LandingSHA = receipt.Candidate.SHA
	receipt.Checks = PullRequestWaitResult{Status: PullRequestWaitFailed, Head: receipt.LandingSHA, Reason: "required target check failed"}
	receipt.Failure = "post-target CI failed"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "Contactus candidate is in remote main and its post-target CI failure remains historical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ReceiptStatus != WorktreeMergePostTargetCIFailed || ack.ReceiptLandingSHA != receipt.LandingSHA || ack.CurrentTargetSHA != receipt.LandingSHA {
		t.Fatalf("post-target acknowledgement = %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(unchanged) != string(original) {
		t.Fatalf("post-target receipt changed: err=%v", err)
	}

	// A repair starts from an additive source revision on a different receipt;
	// it cannot replay or overwrite the historical failed landing.
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, source.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: repair Contactus target CI")
	repair, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repair.ReceiptPath == receipt.ReceiptPath || repair.Candidate.SHA == receipt.Candidate.SHA {
		t.Fatalf("forward repair reused immutable historical receipt: old=%+v new=%+v", receipt, repair)
	}
}

func TestAcknowledgeLandedFailureAcceptsOlderClaimBaseOnlyWhenItIsAnAncestor(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-older-base", "feature/ack-older-base", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
	if err != nil || claim.Claim == nil {
		t.Fatalf("load candidate claim: %+v err=%v", claim, err)
	}
	olderBase := claim.Claim.BaseSHA

	writeEngineFile(t, filepath.Join(fixture.canonical, "target-after-claim.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "target-after-claim.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance receipt target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	laterTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "fetch", "origin")
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", "origin/main")
	receipt.TargetSHA = laterTarget
	receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "candidate contains older immutable claim base and later receipt target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ClaimBaseSHA != olderBase || ack.ReceiptTargetSHA != laterTarget || ack.ClaimBaseSHA == ack.ReceiptTargetSHA {
		t.Fatalf("acknowledgement did not retain distinct ancestry roots: %+v", ack)
	}
}

func TestAcknowledgeLandedFailureRefusesNonAncestorClaimBaseAndIdentityMismatch(t *testing.T) {
	t.Run("non-ancestor claim base", func(t *testing.T) {
		fixture := newEngineFixture(t)
		initialTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		unrelatedWorktree := filepath.Join(t.TempDir(), "unrelated")
		runEngineGit(t, fixture.canonical, "worktree", "add", "-b", "test/ack-unrelated", unrelatedWorktree, initialTarget)
		writeEngineFile(t, filepath.Join(unrelatedWorktree, "unrelated.txt"), "unrelated\n")
		runEngineGit(t, unrelatedWorktree, "add", "unrelated.txt")
		runEngineGit(t, unrelatedWorktree, "commit", "-m", "test: unrelated claim base")
		unrelated := strings.TrimSpace(runEngineGit(t, unrelatedWorktree, "rev-parse", "HEAD"))
		source := createMergeSource(t, fixture, "ack-bad-base", "feature/ack-bad-base", "source.txt", "source\n")
		home, err := wbhome.EnsureRoot(fixture.githubDir)
		if err != nil {
			t.Fatal(err)
		}
		candidateTask := "ack-non-ancestor"
		candidateBranch := "wb/integration/main/ack-non-ancestor"
		candidateWorktree := filepath.Join(home, "worktrees", candidateTask, "acme", "app")
		if err := os.MkdirAll(filepath.Dir(candidateWorktree), 0o700); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, fixture.canonical, "worktree", "add", "-b", candidateBranch, candidateWorktree, initialTarget)
		runEngineGit(t, candidateWorktree, "merge", "--no-edit", source.Branch)
		candidateSHA := strings.TrimSpace(runEngineGit(t, candidateWorktree, "rev-parse", "HEAD"))
		prompt := filepath.Join(t.TempDir(), "prompt.txt")
		if err := os.WriteFile(prompt, []byte("authoritative non-ancestor claim fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = worktrees.EnsureWorkLogClaim(home, candidateTask, worktrees.CreateResult{
			Repository: fixture.repository.Slug, CanonicalDir: fixture.canonical, WorktreeDir: candidateWorktree,
			Branch: candidateBranch, Base: "main", BaseSHA: unrelated,
		}, worktrees.WorkLogOptions{
			EffortID: candidateTask, RunID: candidateTask + "-run", Initiator: "test", AgentID: candidateTask,
			AgentRuntime: "test", Model: "test-model", OriginalPrompt: prompt, RequireOriginalPrompt: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		sources := []WorktreeMergeSource{{Task: "ack-bad-base", Worktree: source.WorktreeDir, Branch: source.Branch, SHA: strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))}}
		lane := worktreeMergeLaneID(fixture.repository.Slug, "main")
		receipt := WorktreeMergeReceipt{
			SchemaVersion: 1, ID: worktreeMergeOperationID(lane, sources), Lane: lane,
			Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeValidationFailed,
			Repository: fixture.repository.Slug, Target: "main", TargetSHA: initialTarget,
			Sources: sources, Candidate: WorktreeMergeCandidate{Task: candidateTask, Worktree: candidateWorktree, Branch: candidateBranch, SHA: candidateSHA},
			ReceiptPath: filepath.Join(home, "reports", "worktree-merge", "non-ancestor-claim-base.json"), CreatedAt: now, UpdatedAt: now,
		}
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", candidateSHA)
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
		if err != nil || view.Claim != nil || !strings.Contains(strings.Join(view.Notes, "\n"), "live HEAD is not descended from claimed base") {
			t.Fatalf("invalid-base claim must be rejected by Work Log corroboration: %+v err=%v", view, err)
		}
		_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe claim base"})
		if err == nil || !strings.Contains(err.Error(), "matching the immutable receipt target and identity") {
			t.Fatalf("invalid-base public-path error = %v", err)
		}
		if err := requireCandidateContainsImmutableClaimBase(context.Background(), candidateWorktree, unrelated, candidateSHA); err == nil || !strings.Contains(err.Error(), "does not contain immutable claim base") {
			t.Fatalf("non-ancestor claim base predicate error = %v", err)
		}
	})

	t.Run("candidate identity mismatch", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "ack-identity", "feature/ack-identity", "source.txt", "source\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeValidationFailed
		receipt.Candidate.Task = "different-task"
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe identity"})
		if err == nil || !strings.Contains(err.Error(), "matching the immutable receipt target and identity") {
			t.Fatalf("identity mismatch error = %v", err)
		}
	})
}

func TestSupersedeValidationFailedWorktreeMergeBindsReplacementWithoutRewritingReceipt(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "supersede-source-a", "feature/supersede-a", "a.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "supersede-source-b", "feature/supersede-b", "b.txt", "b\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir, sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	receipt.Failure = "historical validation failure"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, sourceA.WorktreeDir, "push", "origin", "feature/supersede-a")
	runEngineGit(t, sourceB.WorktreeDir, "push", "origin", "feature/supersede-b")
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target for replacement")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	currentTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	replacement := createMergeSource(t, fixture, "supersede-replacement", "feature/supersede-replacement", "replacement.txt", "replacement\n")
	runEngineGit(t, replacement.WorktreeDir, "fetch", "origin")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/feature/supersede-a")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/feature/supersede-b")
	replacementHead := strings.TrimSpace(runEngineGit(t, replacement.WorktreeDir, "rev-parse", "HEAD"))
	if contains, err := isMergeAncestor(context.Background(), replacement.WorktreeDir, receipt.Candidate.SHA, replacementHead); err != nil || contains {
		t.Fatalf("replacement unexpectedly contains failed candidate: contains=%t err=%v", contains, err)
	}

	dryRun, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.CurrentTargetSHA != currentTarget || dryRun.Replacement.SHA != replacementHead {
		t.Fatalf("dry-run supersession = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}
	ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "replacement contains immutable roots but not failed candidate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ID == "" || ack.ReceiptSHA256 == "" || ack.OriginalCandidate != receipt.Candidate || ack.Replacement.SHA != replacementHead {
		t.Fatalf("supersession acknowledgement = %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(unchanged) != string(originalReceipt) {
		t.Fatalf("failed receipt changed: err=%v", err)
	}
	if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
		t.Fatalf("supersession was not valid: superseded=%t err=%v", superseded, err)
	}
	if _, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath}); err == nil || !strings.Contains(err.Error(), "superseded by an audited replacement") {
		t.Fatalf("superseded receipt was replayable: %v", err)
	}
}

func TestSupersedeValidationFailedWorktreeMergeAcceptsOnlyRecordedSourceDescendant(t *testing.T) {
	t.Run("recorded source descendant retains every root", func(t *testing.T) {
		fixture, receipt, replacement := supersessionFixture(t)
		originalReceipt, originalCandidateClaim, replacementClaim := mergeSupersessionImmutableBytes(t, fixture, receipt, replacement)
		source := receipt.Sources[0]
		writeEngineFile(t, filepath.Join(source.Worktree, "source-descendant.txt"), "descendant\n")
		runEngineGit(t, source.Worktree, "add", "source-descendant.txt")
		runEngineGit(t, source.Worktree, "commit", "-m", "test: advance receipted source")
		advancedSource := strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD"))
		runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", advancedSource)

		ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
			Apply: true, Actor: "reviewer", Reason: "recorded source advanced while retaining immutable roots",
		})
		if err != nil {
			t.Fatal(err)
		}
		if ack.Replacement.SHA == "" {
			t.Fatalf("supersession acknowledgement has no replacement: %+v", ack)
		}
		for _, root := range []string{source.SHA, advancedSource, receipt.TargetSHA} {
			if contains, ancestorErr := isMergeAncestor(context.Background(), replacement.WorktreeDir, root, ack.Replacement.SHA); ancestorErr != nil || !contains {
				t.Fatalf("replacement root %s retained=%t err=%v", root, contains, ancestorErr)
			}
		}
		assertMergeSupersessionImmutableBytes(t, fixture, receipt, replacement, originalReceipt, originalCandidateClaim, replacementClaim)
	})

	t.Run("missing advanced source root refuses", func(t *testing.T) {
		fixture, receipt, replacement := supersessionFixture(t)
		source := receipt.Sources[0]
		writeEngineFile(t, filepath.Join(source.Worktree, "source-descendant.txt"), "descendant\n")
		runEngineGit(t, source.Worktree, "add", "source-descendant.txt")
		runEngineGit(t, source.Worktree, "commit", "-m", "test: advance receipted source")

		_, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
			Apply: true, Actor: "reviewer", Reason: "replacement omits advanced source",
		})
		if err == nil || !strings.Contains(err.Error(), "does not contain required immutable root") {
			t.Fatalf("missing advanced source root error = %v", err)
		}
		if _, statErr := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
			t.Fatalf("missing advanced source root wrote supersession: %v", statErr)
		}
	})

	t.Run("altered recorded source root refuses", func(t *testing.T) {
		fixture, receipt, replacement := supersessionFixture(t)
		source := receipt.Sources[0]
		runEngineGit(t, source.Worktree, "reset", "--hard", receipt.TargetSHA)

		_, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
			Apply: true, Actor: "reviewer", Reason: "rewritten recorded source",
		})
		if err == nil || !strings.Contains(err.Error(), "does not retain recorded source") {
			t.Fatalf("altered source root error = %v", err)
		}
		if _, statErr := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
			t.Fatalf("altered source root wrote supersession: %v", statErr)
		}
	})

	t.Run("generic sibling cannot impersonate recorded source", func(t *testing.T) {
		fixture, receipt, _ := supersessionFixture(t)
		source := receipt.Sources[0]
		sibling := createMergeSource(t, fixture, "supersession-source-sibling", "feature/supersession-source-sibling", "sibling.txt", "sibling\n")
		runEngineGit(t, sibling.WorktreeDir, "merge", "--no-edit", source.SHA)
		originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		impersonating := source
		impersonating.Worktree = sibling.WorktreeDir
		impersonating.Branch = sibling.Branch
		if _, err := validateValidationFailedSupersessionSource(context.Background(), fixture.githubDir, receipt, impersonating, originalClaim.BaseSHA); err == nil || !strings.Contains(err.Error(), "no matching active Work Log claim") {
			t.Fatalf("sibling source identity error = %v", err)
		}
	})
}

func TestSupersedeValidationFailedWorktreeMergeRoundTripsToNextPrepare(t *testing.T) {
	fixture, receipt, replacement := supersessionFixture(t)
	ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
		Apply: true, Actor: "reviewer", Reason: "audited replacement candidate",
	})
	if err != nil {
		t.Fatal(err)
	}

	// This is the production writer-to-next-reader boundary: the replacement
	// acknowledgement frees the lane, and a subsequent public prepare must scan
	// the same report directory without treating the acknowledgement as a
	// merge receipt.
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{replacement.WorktreeDir}, Target: receipt.Target,
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatalf("next prepare rejected valid supersession %s: %v", ack.AcknowledgementPath, err)
	}
	if next.Candidate.SHA == "" || next.Status != WorktreeMergePrepared {
		t.Fatalf("next prepare did not create a prepared candidate: %+v", next)
	}
}

func TestSupersedeValidationFailedWorktreeMergeRoundTripRefusesTamperedAcknowledgement(t *testing.T) {
	fixture, receipt, replacement := supersessionFixture(t)
	ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
		Apply: true, Actor: "reviewer", Reason: "audited replacement candidate",
	})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ack.AcknowledgementPath, []byte(strings.Replace(string(contents), ack.ID, "tampered", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{replacement.WorktreeDir}, Target: receipt.Target,
		Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
		t.Fatalf("tampered supersession was accepted: %v", err)
	}
}

func TestSupersedeValidationFailedWorktreeMergeIsIdempotentAndRefusesReplacementDrift(t *testing.T) {
	fixture, receipt, replacement := supersessionFixture(t)
	originalReceipt, originalCandidateClaim, replacementClaim := mergeSupersessionImmutableBytes(t, fixture, receipt, replacement)
	first, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "first audited supersession",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstAck, err := os.ReadFile(first.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "first audited supersession",
	})
	if err != nil || second.ID != first.ID || second.ReceiptSHA256 != first.ReceiptSHA256 || second.Replacement != first.Replacement || second.CurrentTargetSHA != first.CurrentTargetSHA {
		t.Fatalf("exact second apply = %+v err=%v, want immutable existing acknowledgement %+v", second, err, first)
	}
	secondAck, err := os.ReadFile(first.AcknowledgementPath)
	if err != nil || string(secondAck) != string(firstAck) {
		t.Fatalf("second apply rewrote acknowledgement: err=%v", err)
	}
	assertMergeSupersessionImmutableBytes(t, fixture, receipt, replacement, originalReceipt, originalCandidateClaim, replacementClaim)

	// A clean commit after acknowledgement changes the proposed replacement
	// identity. It must not overwrite or reuse the existing acknowledgement.
	writeEngineFile(t, filepath.Join(replacement.WorktreeDir, "replacement-drift.txt"), "drift\n")
	runEngineGit(t, replacement.WorktreeDir, "add", "replacement-drift.txt")
	runEngineGit(t, replacement.WorktreeDir, "commit", "-m", "test: advance replacement after acknowledgement")
	_, err = SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "unsafe replacement drift",
	})
	if err == nil || !strings.Contains(err.Error(), "binds different target or replacement evidence") {
		t.Fatalf("clean replacement drift error = %v", err)
	}
	ackAfterDrift, err := os.ReadFile(first.AcknowledgementPath)
	if err != nil || string(ackAfterDrift) != string(firstAck) {
		t.Fatalf("replacement drift rewrote acknowledgement: err=%v", err)
	}
}

func TestSupersedeValidationFailedWorktreeMergeRefusesDifferentReplacementWithoutMutation(t *testing.T) {
	fixture, receipt, replacement := supersessionFixture(t)
	originalReceipt, originalCandidateClaim, replacementClaim := mergeSupersessionImmutableBytes(t, fixture, receipt, replacement)
	first, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "first audited supersession",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstAck, err := os.ReadFile(first.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	secondReplacement := createMergeSource(t, fixture, "supersession-replacement-second", "feature/supersession-replacement-second", "second.txt", "second\n")
	runEngineGit(t, secondReplacement.WorktreeDir, "fetch", "origin")
	runEngineGit(t, secondReplacement.WorktreeDir, "merge", "--no-edit", "origin/feature/supersession-source")
	secondReplacementClaim, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: secondReplacement.WorktreeDir})
	if err != nil || secondReplacementClaim.Claim == nil {
		t.Fatalf("load second replacement claim: %+v err=%v", secondReplacementClaim, err)
	}
	secondReplacementClaimBytes, err := os.ReadFile(secondReplacementClaim.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: secondReplacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "unsafe different replacement",
	})
	if err == nil || !strings.Contains(err.Error(), "binds different target or replacement evidence") {
		t.Fatalf("different replacement error = %v", err)
	}
	ackAfter, err := os.ReadFile(first.AcknowledgementPath)
	if err != nil || string(ackAfter) != string(firstAck) {
		t.Fatalf("different replacement rewrote acknowledgement: err=%v", err)
	}
	assertMergeSupersessionImmutableBytes(t, fixture, receipt, replacement, originalReceipt, originalCandidateClaim, replacementClaim)
	if current, err := os.ReadFile(secondReplacementClaim.Claim.ClaimPath); err != nil || string(current) != string(secondReplacementClaimBytes) {
		t.Fatalf("different replacement Work Log changed: err=%v", err)
	}
}

func mergeSupersessionImmutableBytes(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, replacement worktrees.CreateResult) ([]byte, []byte, []byte) {
	t.Helper()
	receiptBytes, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
	if err != nil || candidateView.Claim == nil {
		t.Fatalf("load candidate claim: %+v err=%v", candidateView, err)
	}
	candidateBytes, err := os.ReadFile(candidateView.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	replacementView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: replacement.WorktreeDir})
	if err != nil || replacementView.Claim == nil {
		t.Fatalf("load replacement claim: %+v err=%v", replacementView, err)
	}
	replacementBytes, err := os.ReadFile(replacementView.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	return receiptBytes, candidateBytes, replacementBytes
}

func assertMergeSupersessionImmutableBytes(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, replacement worktrees.CreateResult, wantReceipt, wantCandidateClaim, wantReplacementClaim []byte) {
	t.Helper()
	gotReceipt, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(gotReceipt) != string(wantReceipt) {
		t.Fatalf("receipt changed: err=%v", err)
	}
	candidateView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
	if err != nil || candidateView.Claim == nil {
		t.Fatalf("load candidate claim: %+v err=%v", candidateView, err)
	}
	gotCandidateClaim, err := os.ReadFile(candidateView.Claim.ClaimPath)
	if err != nil || string(gotCandidateClaim) != string(wantCandidateClaim) {
		t.Fatalf("candidate Work Log changed: err=%v", err)
	}
	replacementView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: replacement.WorktreeDir})
	if err != nil || replacementView.Claim == nil {
		t.Fatalf("load replacement claim: %+v err=%v", replacementView, err)
	}
	gotReplacementClaim, err := os.ReadFile(replacementView.Claim.ClaimPath)
	if err != nil || string(gotReplacementClaim) != string(wantReplacementClaim) {
		t.Fatalf("replacement Work Log changed: err=%v", err)
	}
}

func TestSupersedeValidationFailedWorktreeMergeRefusesInvalidEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt, replacement worktrees.CreateResult)
		want   string
	}{
		{
			name: "wrong receipt status",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				receipt.Status = WorktreeMergePrepared
				if err := persistWorktreeMergeReceipt(*receipt); err != nil {
					t.Fatal(err)
				}
			},
			want: "want prepare validation_failed",
		},
		{
			name: "wrong receipt phase with validation failed status",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				receipt.Phase = WorktreeMergePhaseLand
				if err := persistWorktreeMergeReceipt(*receipt); err != nil {
					t.Fatal(err)
				}
			},
			want: "invalid prepare failure state",
		},
		{
			name: "missing original candidate claim",
			mutate: func(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
				if err != nil || view.Claim == nil {
					t.Fatalf("load original candidate claim: %+v err=%v", view, err)
				}
				if err := os.Remove(view.Claim.ClaimPath); err != nil {
					t.Fatal(err)
				}
			},
			want: "candidate has no active Work Log claim matching the immutable receipt target and identity",
		},
		{
			name: "identity mismatched original candidate claim",
			mutate: func(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
				if err != nil || view.Claim == nil {
					t.Fatalf("load original candidate claim: %+v err=%v", view, err)
				}
				contents, err := os.ReadFile(view.Claim.ClaimPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(view.Claim.ClaimPath, []byte(strings.Replace(string(contents), view.Claim.Task, "other-task", 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "matching the immutable receipt target and identity",
		},
		{
			name: "dirty old candidate",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "dirty.txt"), "dirty\n")
			},
			want: "candidate is not clean",
		},
		{
			name: "dirty replacement",
			mutate: func(t *testing.T, _ engineFixture, _ *WorktreeMergeReceipt, replacement worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(replacement.WorktreeDir, "dirty.txt"), "dirty\n")
			},
			want: "replacement is not clean",
		},
		{
			name: "drifted old candidate",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "advanced.txt"), "advanced\n")
				runEngineGit(t, receipt.Candidate.Worktree, "add", "advanced.txt")
				runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: advance failed candidate")
			},
			want: "does not match receipted candidate",
		},
		{
			name: "advanced receipted source missing replacement root",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				source := receipt.Sources[0]
				writeEngineFile(t, filepath.Join(source.Worktree, "advanced.txt"), "advanced\n")
				runEngineGit(t, source.Worktree, "add", "advanced.txt")
				runEngineGit(t, source.Worktree, "commit", "-m", "test: advance receipted source")
			},
			want: "does not contain required immutable root",
		},
		{
			name: "missing replacement claim",
			mutate: func(t *testing.T, fixture engineFixture, _ *WorktreeMergeReceipt, replacement worktrees.CreateResult) {
				view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: replacement.WorktreeDir})
				if err != nil || view.Claim == nil {
					t.Fatalf("load replacement claim: %+v err=%v", view, err)
				}
				if err := os.Remove(view.Claim.ClaimPath); err != nil {
					t.Fatal(err)
				}
			},
			want: "replacement has no authoritative active Work Log claim matching its identity",
		},
		{
			name: "remote target moved outside replacement",
			mutate: func(t *testing.T, fixture engineFixture, _ *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(fixture.canonical, "moved-target.txt"), "moved\n")
				runEngineGit(t, fixture.canonical, "add", "moved-target.txt")
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: move target")
				runEngineGit(t, fixture.canonical, "push", "origin", "main")
			},
			want: "does not contain required immutable root",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, receipt, replacement := supersessionFixture(t)
			test.mutate(t, fixture, &receipt, replacement)
			_, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "unsafe evidence"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("supersession error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
				t.Fatalf("refusal wrote supersession: %v", statErr)
			}
		})
	}
}

func TestSupersedeValidationFailedWorktreeMergeRefusesMissingSourceAncestryAndTampering(t *testing.T) {
	t.Run("missing receipted source ancestry", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "missing-source", "feature/missing-source", "source.txt", "source\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeValidationFailed
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
		runEngineGit(t, fixture.canonical, "add", "target.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "test: target without source")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		replacement := createMergeSource(t, fixture, "missing-source-replacement", "feature/missing-source-replacement", "replacement.txt", "replacement\n")
		_, err = SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "missing source ancestry"})
		if err == nil || !strings.Contains(err.Error(), "does not contain required immutable root") {
			t.Fatalf("missing source ancestry error = %v", err)
		}
	})

	t.Run("tampered acknowledgement and receipt", func(t *testing.T) {
		fixture, receipt, replacement := supersessionFixture(t)
		ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "valid supersession"})
		if err != nil {
			t.Fatal(err)
		}
		ackContents, err := os.ReadFile(ack.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ack.AcknowledgementPath, []byte(strings.Replace(string(ackContents), ack.ID, "tampered", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
			t.Fatalf("tampered acknowledgement error = %v", err)
		}
		if err := os.WriteFile(ack.AcknowledgementPath, ackContents, 0o600); err != nil {
			t.Fatal(err)
		}
		receiptContents, err := os.ReadFile(receipt.ReceiptPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receipt.ReceiptPath, append(receiptContents, ' '), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
			t.Fatalf("tampered receipt error = %v", err)
		}
	})
}

func supersessionFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, worktrees.CreateResult) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "supersession-source", "feature/supersession-source", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "push", "origin", "feature/supersession-source")
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	replacement := createMergeSource(t, fixture, "supersession-replacement", "feature/supersession-replacement", "replacement.txt", "replacement\n")
	runEngineGit(t, replacement.WorktreeDir, "fetch", "origin")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/feature/supersession-source")
	return fixture, receipt, replacement
}

func TestSupersedeValidationFailedWorktreeMergeRefusesSelfReplacementWithoutMutation(t *testing.T) {
	fixture, receipt, _ := supersessionFixture(t)
	receiptBytes, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: receipt.Candidate.Worktree, Apply: true, Actor: "reviewer", Reason: "must refuse self replacement",
	})
	if err == nil || !strings.Contains(err.Error(), "distinct from the failed receipt candidate") {
		t.Fatalf("self replacement error = %v", err)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBytes) {
		t.Fatalf("receipt changed on self replacement refusal: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(claim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBytes) {
		t.Fatalf("claim changed on self replacement refusal: err=%v", readErr)
	}
	if _, statErr := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("self replacement refusal wrote supersession: %v", statErr)
	}
}

func TestCorrectValidationFailedSelfSupersessionIsAppendOnlyAndReplaySafe(t *testing.T) {
	fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
	receiptBytes, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionBytes, err := os.ReadFile(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err == nil || superseded {
		t.Fatalf("uncorrected self supersession = superseded=%t err=%v", superseded, err)
	}
	options := WorktreeMergeSelfSupersessionCorrectionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: claimHash,
		Apply: true, Actor: "reviewer", Reason: "correct historical self supersession",
	}
	correction, err := CorrectValidationFailedSelfSupersession(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if correction.CorrectedReplacement.Worktree != replacement.WorktreeDir || correction.CorrectedReplacement.SHA == receipt.Candidate.SHA || correction.CorrectedReplacement.SHA != strings.TrimSpace(runEngineGit(t, replacement.WorktreeDir, "rev-parse", "HEAD")) {
		t.Fatalf("corrected replacement = %+v", correction.CorrectedReplacement)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBytes) {
		t.Fatalf("receipt changed by correction: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(claim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBytes) {
		t.Fatalf("claim changed by correction: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || !bytes.Equal(current, supersessionBytes) {
		t.Fatalf("self supersession changed by correction: err=%v", readErr)
	}
	if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
		t.Fatalf("corrected self supersession = superseded=%t err=%v", superseded, err)
	}
	if active, err := activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(receipt.ReceiptPath), receipt.Lane); err != nil || active != nil {
		t.Fatalf("active lane after correction = %+v err=%v", active, err)
	}
	replayed, err := CorrectValidationFailedSelfSupersession(context.Background(), options)
	if err != nil || replayed.ID != correction.ID {
		t.Fatalf("correction replay = %+v err=%v", replayed, err)
	}
	correctionBytes, err := os.ReadFile(correction.CorrectionPath)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := options
	conflicting.Reason = "a different correction must not overwrite the first"
	if _, err := CorrectValidationFailedSelfSupersession(context.Background(), conflicting); err == nil || !strings.Contains(err.Error(), "binds different immutable evidence") {
		t.Fatalf("conflicting correction error = %v", err)
	}
	if current, readErr := os.ReadFile(correction.CorrectionPath); readErr != nil || !bytes.Equal(current, correctionBytes) {
		t.Fatalf("conflicting correction replaced existing record: err=%v", readErr)
	}
	if err := os.WriteFile(correction.CorrectionPath, []byte(strings.Replace(string(correctionBytes), correction.ID, "tampered", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err == nil || superseded {
		t.Fatalf("tampered correction = superseded=%t err=%v", superseded, err)
	}
}

func TestCorrectValidationFailedSelfSupersessionRefusesFailedCandidateReplacementWithoutMutation(t *testing.T) {
	fixture, receipt, _, supersession, claimHash := selfSupersessionFixture(t)
	receiptBefore, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBefore, err := os.ReadFile(originalClaim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionBefore, err := os.ReadFile(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: receipt.Candidate.Worktree,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: claimHash,
		Apply: true, Actor: "reviewer", Reason: "must refuse failed candidate as corrected replacement",
	})
	if err == nil || !strings.Contains(err.Error(), "distinct from the failed receipt candidate") {
		t.Fatalf("self corrected-replacement error = %v", err)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
		t.Fatalf("self corrected-replacement changed failed receipt: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(originalClaim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBefore) {
		t.Fatalf("self corrected-replacement changed failed claim: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || !bytes.Equal(current, supersessionBefore) {
		t.Fatalf("self corrected-replacement changed self-supersession acknowledgement: err=%v", readErr)
	}
	if _, statErr := os.Stat(selfSupersessionCorrectionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("self corrected-replacement leaked correction artifact: %v", statErr)
	}
}

func TestCorrectValidationFailedSelfSupersessionRefusesConcurrentConflictingCreate(t *testing.T) {
	fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergeSelfSupersessionCorrectionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: claimHash,
		Apply: true, Actor: "reviewer", Reason: "the intended correction",
	}
	dryRun := options
	dryRun.Apply = false
	competing, err := CorrectValidationFailedSelfSupersession(context.Background(), dryRun)
	if err != nil {
		t.Fatal(err)
	}
	competing.Reason = "a concurrently published correction"
	competing.ID = selfSupersessionCorrectionID(competing)
	competingBytes, err := json.MarshalIndent(competing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	competingBytes = append(competingBytes, '\n')
	previousLink := linkSelfSupersessionCorrection
	linkSelfSupersessionCorrection = func(_, path string) error {
		if err := os.WriteFile(path, competingBytes, 0o600); err != nil {
			return err
		}
		return os.ErrExist
	}
	t.Cleanup(func() { linkSelfSupersessionCorrection = previousLink })
	if _, err := CorrectValidationFailedSelfSupersession(context.Background(), options); err == nil || !strings.Contains(err.Error(), "concurrent self-supersession correction") {
		t.Fatalf("concurrent correction error = %v", err)
	}
	current, err := os.ReadFile(selfSupersessionCorrectionPath(receipt.ReceiptPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, competingBytes) {
		t.Fatal("concurrent correction was overwritten")
	}
}

func TestCorrectValidationFailedSelfSupersessionRefusesUnsafeHistoricalEvidence(t *testing.T) {
	t.Run("malformed receipt state or landing", func(t *testing.T) {
		for _, mutate := range []struct {
			name  string
			apply func(*WorktreeMergeReceipt)
		}{
			{name: "state", apply: func(receipt *WorktreeMergeReceipt) { receipt.Status = WorktreeMergePrepared }},
			{name: "landing", apply: func(receipt *WorktreeMergeReceipt) { receipt.LandingSHA = receipt.TargetSHA }},
		} {
			t.Run(mutate.name, func(t *testing.T) {
				fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
				mutate.apply(&receipt)
				if err := persistWorktreeMergeReceipt(receipt); err != nil {
					t.Fatal(err)
				}
				receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				// Keep the historical acknowledgement byte-consistent with the
				// mutated receipt. Before the exact-state guard, this made the
				// malformed receipt reach the self-supersession correction path.
				supersession.ReceiptSHA256 = receiptHash
				supersession.ID = validationFailureSupersessionID(supersession)
				if err := persistValidationFailureSupersession(supersession.AcknowledgementPath, supersession); err != nil {
					t.Fatal(err)
				}
				ackBefore, err := os.ReadFile(supersession.AcknowledgementPath)
				if err != nil {
					t.Fatal(err)
				}
				ackHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
					ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
					ExpectedSupersessionSHA256: ackHash, ExpectedImmutableClaimSHA256: claimHash, Apply: true, Actor: "reviewer", Reason: "must refuse malformed receipt",
				})
				if err == nil || !strings.Contains(err.Error(), "validation_failed") {
					t.Fatalf("malformed receipt correction error = %v", err)
				}
				if current, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || !bytes.Equal(current, ackBefore) {
					t.Fatalf("historical acknowledgement changed: err=%v", readErr)
				}
				if _, statErr := os.Stat(selfSupersessionCorrectionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
					t.Fatalf("malformed receipt wrote correction: %v", statErr)
				}
			})
		}
	})

	t.Run("self acknowledgement claim bases differ", func(t *testing.T) {
		fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
		supersession.ReplacementClaimBaseSHA = "1111111111111111111111111111111111111111"
		supersession.ID = validationFailureSupersessionID(supersession)
		if err := persistValidationFailureSupersession(supersession.AcknowledgementPath, supersession); err != nil {
			t.Fatal(err)
		}
		ackHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
			ExpectedSupersessionSHA256: ackHash, ExpectedImmutableClaimSHA256: claimHash, Apply: true, Actor: "reviewer", Reason: "must refuse mismatched historical claim bases",
		})
		if err == nil || !strings.Contains(err.Error(), "exact self-supersession") {
			t.Fatalf("claim-base mismatch correction error = %v", err)
		}
	})

	t.Run("replacement lacks historical current target", func(t *testing.T) {
		fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
		currentTarget, err := fetchExactMergeTarget(context.Background(), replacement.WorktreeDir, receipt.Target)
		if err != nil {
			t.Fatal(err)
		}
		candidateSHA := strings.TrimSpace(runEngineGit(t, replacement.WorktreeDir, "rev-parse", "HEAD"))
		if contains, err := isMergeAncestor(context.Background(), replacement.WorktreeDir, currentTarget, candidateSHA); err != nil || !contains {
			t.Fatalf("replacement does not contain fresh current target: contains=%t err=%v", contains, err)
		}
		tree := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", currentTarget+"^{tree}"))
		sideCommit := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", currentTarget, "-m", "test: unreceipted historical target side commit"))
		if contains, err := isMergeAncestor(context.Background(), replacement.WorktreeDir, sideCommit, candidateSHA); err != nil || contains {
			t.Fatalf("side commit unexpectedly belongs to replacement: contains=%t err=%v", contains, err)
		}
		supersession.CurrentTargetSHA = sideCommit
		supersession.ID = validationFailureSupersessionID(supersession)
		if err := persistValidationFailureSupersession(supersession.AcknowledgementPath, supersession); err != nil {
			t.Fatal(err)
		}
		ackHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
			ExpectedSupersessionSHA256: ackHash, ExpectedImmutableClaimSHA256: claimHash, Apply: true, Actor: "reviewer", Reason: "must require historical current target ancestry",
		})
		if err == nil || !strings.Contains(err.Error(), "does not contain required immutable root") {
			t.Fatalf("historical current target ancestry error = %v", err)
		}
	})
}

func TestCorrectedSelfSupersessionClaimTamperBlocksLandAndActiveLaneBeforeTerminalization(t *testing.T) {
	fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
	ackHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
		ExpectedSupersessionSHA256: ackHash, ExpectedImmutableClaimSHA256: claimHash, Apply: true, Actor: "reviewer", Reason: "establish valid correction before lifecycle wiring falsifier",
	}); err != nil {
		t.Fatal(err)
	}
	if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
		t.Fatalf("valid corrected supersession = superseded=%t err=%v", superseded, err)
	}
	receiptBefore, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claim.ClaimPath, append(claimBytes, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath}); err == nil || !strings.Contains(err.Error(), "immutable claim SHA256") {
		t.Fatalf("land after claim tamper error = %v", err)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
		t.Fatalf("land after claim tamper terminalized receipt: err=%v", readErr)
	}
	if _, err := activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(receipt.ReceiptPath), receipt.Lane); err == nil || !strings.Contains(err.Error(), "immutable claim SHA256") {
		t.Fatalf("active-lane cleanup gate after claim tamper error = %v", err)
	}
}

func TestCorrectedSelfSupersessionReaderRefusesLiveEvidenceDrift(t *testing.T) {
	newCorrected := func(t *testing.T) (engineFixture, WorktreeMergeReceipt, WorktreeMergeSelfSupersessionCorrection, *worktrees.WorkLogClaimView) {
		t.Helper()
		fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
		ackHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		correction, err := CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
			ExpectedSupersessionSHA256: ackHash, ExpectedImmutableClaimSHA256: claimHash, Apply: true, Actor: "reviewer", Reason: "establish correction before live-drift falsifier",
		})
		if err != nil {
			t.Fatal(err)
		}
		claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		return fixture, receipt, correction, claim
	}

	t.Run("original immutable claim bytes", func(t *testing.T) {
		fixture, receipt, _, claim := newCorrected(t)
		claimBytes, err := os.ReadFile(claim.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(claim.ClaimPath, append(claimBytes, ' '), 0o600); err != nil {
			t.Fatal(err)
		}
		if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err == nil || superseded || !strings.Contains(err.Error(), "immutable claim SHA256") {
			t.Fatalf("claim-byte drift = superseded=%t err=%v", superseded, err)
		}
	})

	t.Run("advanced target missing from replacement", func(t *testing.T) {
		fixture, receipt, _, _ := newCorrected(t)
		writeEngineFile(t, filepath.Join(fixture.canonical, "target-drift.txt"), "target drift\n")
		runEngineGit(t, fixture.canonical, "add", "target-drift.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "test: drift target after correction")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err == nil || superseded || !strings.Contains(err.Error(), "does not contain recorded immutable root") {
			t.Fatalf("target drift = superseded=%t err=%v", superseded, err)
		}
	})

	t.Run("recorded replacement descendant", func(t *testing.T) {
		fixture, receipt, correction, _ := newCorrected(t)
		writeEngineFile(t, filepath.Join(correction.CorrectedReplacement.Worktree, "replacement-drift.txt"), "replacement drift\n")
		runEngineGit(t, correction.CorrectedReplacement.Worktree, "add", "replacement-drift.txt")
		runEngineGit(t, correction.CorrectedReplacement.Worktree, "commit", "-m", "test: drift replacement after correction")
		if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
			t.Fatalf("replacement descendant = superseded=%t err=%v", superseded, err)
		}
	})

	t.Run("target and replacement descendants retain every root", func(t *testing.T) {
		fixture, receipt, correction, _ := newCorrected(t)
		writeEngineFile(t, filepath.Join(fixture.canonical, "target-descendant.go"), "package app\n\nfunc TargetDescendant() {}\n")
		runEngineGit(t, fixture.canonical, "add", "target-descendant.go")
		runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance corrected target")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		currentTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		runEngineGit(t, correction.CorrectedReplacement.Worktree, "merge", "--no-edit", currentTarget)
		if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
			t.Fatalf("root-complete descendants = superseded=%t err=%v", superseded, err)
		}
	})

	t.Run("sibling replacement remains an exact identity refusal", func(t *testing.T) {
		fixture, receipt, correction, _ := newCorrected(t)
		sibling := createMergeSource(t, fixture, "self-supersession-sibling", "feature/self-supersession-sibling", "sibling.go", "package app\n\nfunc Sibling() {}\n")
		// Make the sibling ancestry-complete first. The correction writer must
		// still reject a different managed identity after all root checks pass.
		runEngineGit(t, sibling.WorktreeDir, "merge", "--no-edit", correction.CorrectedReplacement.SHA)
		supersessionHash, err := worktreeMergeReceiptSHA256(correction.SupersessionPath)
		if err != nil {
			t.Fatal(err)
		}
		originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		claimBytes, err := os.ReadFile(originalClaim.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
		claimDigest := sha256.Sum256(claimBytes)
		_, err = CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: sibling.WorktreeDir,
			ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: hex.EncodeToString(claimDigest[:]),
			Apply: true, Actor: "reviewer", Reason: "sibling must not replace corrected identity",
		})
		if err == nil || !strings.Contains(err.Error(), "binds different immutable evidence") {
			t.Fatalf("sibling replacement correction = %v", err)
		}
	})

	t.Run("historical source remains effective after its live worktree advances", func(t *testing.T) {
		fixture, receipt, _, _ := newCorrected(t)
		source := receipt.Sources[0]
		writeEngineFile(t, filepath.Join(source.Worktree, "source-drift.txt"), "source drift\n")
		runEngineGit(t, source.Worktree, "add", "source-drift.txt")
		runEngineGit(t, source.Worktree, "commit", "-m", "test: drift source after correction")
		if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
			t.Fatalf("advanced historical source = superseded=%t err=%v", superseded, err)
		}
	})
}

func selfSupersessionFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, worktrees.CreateResult, WorktreeMergeValidationFailureSupersession, string) {
	t.Helper()
	fixture, receipt, replacement := supersessionFixture(t)
	originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(originalClaim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)
	claimHash := hex.EncodeToString(claimDigest[:])
	currentTarget, err := fetchExactMergeTarget(context.Background(), replacement.WorktreeDir, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ackPath := validationFailureSupersessionPath(receipt.ReceiptPath)
	ack := WorktreeMergeValidationFailureSupersession{
		SchemaVersion: worktreeMergeValidationFailureSupersessionSchemaVersion, Status: "validation_failure_superseded", ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: ackPath,
		ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, ReceiptStatus: receipt.Status, Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: currentTarget,
		OriginalCandidate: receipt.Candidate, OriginalClaimBaseSHA: originalClaim.BaseSHA, Replacement: receipt.Candidate, ReplacementClaimBaseSHA: originalClaim.BaseSHA,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...), Actor: "historical-operator", Reason: "historical self supersession", RecordedAt: time.Now().UTC(),
	}
	ack.ID = validationFailureSupersessionID(ack)
	if err := persistValidationFailureSupersession(ackPath, ack); err != nil {
		t.Fatal(err)
	}
	return fixture, receipt, replacement, ack, claimHash
}

func TestAcknowledgeLandedValidationFailureRefusesMissingTargetContainment(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-refuse", "feature/ack-refuse", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe test",
	})
	if err == nil || !strings.Contains(err.Error(), "does not contain receipted candidate") {
		t.Fatalf("missing target containment error = %v", err)
	}
	if _, statErr := os.Stat(landedFailureAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refusal left acknowledgement: %v", statErr)
	}
}

func TestAcknowledgeLandedValidationFailureRefusesChangedReceiptedSource(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-source-drift", "feature/ack-source-drift", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "changed-after-receipt.txt"), "changed\n")
	runEngineGit(t, source.WorktreeDir, "add", "changed-after-receipt.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: advance receipted source")

	_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe source drift",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed receipted source acknowledgement error = %v", err)
	}
}
