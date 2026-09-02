package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAcknowledgeRetiredLandedWorktreeMerge(t *testing.T) {
	fixture, receipt, landing := retiredPublishedMergeFixture(t, true)
	installWorktreeMergeMergedPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_TARGET_SHA", landing)

	// This is the real stale-resume boundary: a published receipt remains in
	// land/prepared while the candidate has already been retired. Land records a
	// conflict because it cannot chdir into that removed candidate. The later
	// acknowledgement must treat that conflict semantically, not match its text.
	failed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || failed.Status != WorktreeMergeConflict || !strings.Contains(failed.Failure, "chdir") {
		t.Fatalf("removed candidate resume = %+v err=%v", failed, err)
	}
	receipt, err = readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	fresh := createMergeSource(t, fixture, "retired-ack-fresh", "feature/retired-ack-fresh", "fresh.txt", "fresh\n")
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fresh.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "still owned") {
		t.Fatalf("stale retired receipt did not block prepare before acknowledgement: %v", err)
	}
	options := retiredLandedAcknowledgementOptions(t, fixture, receipt, landing)
	ackPath := retiredLandedAcknowledgementPath(receipt.ReceiptPath)
	planned, err := AcknowledgeRetiredLandedWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("dry-run acknowledgement: %v", err)
	}
	if planned.Status != "retired_landed_acknowledgement_planned" || planned.ID != "" {
		t.Fatalf("dry run was presented as durable evidence: %+v", planned)
	}
	if _, err := os.Stat(ackPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote acknowledgement: %v", err)
	}
	options.Apply = true
	ack, err := AcknowledgeRetiredLandedWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("apply acknowledgement: %v", err)
	}
	if ack.LandingSHA != landing || len(ack.ClaimSHA256) != 2 || ack.ClaimSHA256[0].TerminalSHA256 == "" {
		t.Fatalf("acknowledgement does not bind terminal evidence: %+v", ack)
	}
	after, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("acknowledgement rewrote immutable merge receipt")
	}
	ackBytes, err := os.ReadFile(ackPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, branch := range []string{receipt.Candidate.Branch, receipt.Sources[0].Branch} {
		claim, claimErr := ActiveMergeLaneClaim(fixture.githubDir, receipt.Repository, branch)
		if claimErr != nil || claim != nil {
			t.Fatalf("acknowledged retired branch %s remains claimed: %+v err=%v", branch, claim, claimErr)
		}
	}
	// The same preparation path that was blocked by the stale receipt is now
	// admitted; this is the red-to-green lane behavior, not merely a unit call.
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{fresh.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err != nil {
		t.Fatalf("acknowledged stale receipt still blocks prepare: %v", err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "advanced-after-ack.txt"), "advanced target\n")
	runEngineGit(t, fixture.canonical, "add", "advanced-after-ack.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target after acknowledgement")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	// Replay must not need GitHub, fetch, or the old target tip. The immutable
	// receipt/claim/terminal bytes and locally available graph are enough.
	runEngineGit(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/acme/app.git")
	replayed, err := AcknowledgeRetiredLandedWorktreeMerge(context.Background(), options)
	if err != nil || replayed.ID != ack.ID {
		t.Fatalf("offline acknowledgement replay = %+v err=%v", replayed, err)
	}
	replayedBytes, err := os.ReadFile(ackPath)
	if err != nil || string(replayedBytes) != string(ackBytes) {
		t.Fatalf("offline replay rewrote acknowledgement: err=%v", err)
	}
	if err := os.MkdirAll(receipt.Candidate.Worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeRetiredLandedWorktreeMerge(context.Background(), options); err == nil || !strings.Contains(err.Error(), "worktree path") {
		t.Fatalf("existing acknowledgement bypassed reused-path proof: %v", err)
	}
	if err := os.Remove(receipt.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	terminalPath := retiredTerminalPath(t, fixture, receipt.Sources[0].Task, ack.ClaimSHA256)
	terminalBytes, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(terminalPath, append(append([]byte(nil), terminalBytes...), ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeRetiredLandedWorktreeMerge(context.Background(), options); err == nil || !strings.Contains(err.Error(), "claim SHA256") {
		t.Fatalf("terminal-byte tampering was accepted by replay: %v", err)
	}
	if _, err := ActiveMergeLaneClaim(fixture.githubDir, receipt.Repository, receipt.Candidate.Branch); err == nil || !strings.Contains(err.Error(), "claim bytes changed") {
		t.Fatalf("terminal-byte tampering released active lane: %v", err)
	}
	if err := os.WriteFile(terminalPath, terminalBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	ack.Actor = "tampered"
	tampered, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ackPath, append(tampered, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ActiveMergeLaneClaim(fixture.githubDir, receipt.Repository, receipt.Candidate.Branch); err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
		t.Fatalf("tampered acknowledgement admitted lane release: %v", err)
	}
}

func TestAcknowledgeRetiredLandedWorktreeMergeRejectsTerminalFinalOutsideLanding(t *testing.T) {
	fixture, receipt, landing := retiredPublishedMergeFixture(t, true)
	installWorktreeMergeMergedPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	// The candidate is a valid ancestor of the current target but the advanced
	// source terminal final is not in that purported PR landing. The operation
	// must reject it rather than treating containment in later main as enough.
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	options := retiredLandedAcknowledgementOptions(t, fixture, receipt, receipt.Candidate.SHA)
	if _, err := AcknowledgeRetiredLandedWorktreeMerge(context.Background(), options); err == nil || !strings.Contains(err.Error(), "terminal source final") {
		t.Fatalf("terminal final outside exact PR landing was admitted: %v (actual landing %s)", err, landing)
	}
}

func TestAcknowledgeRetiredLandedWorktreeMergeRejectsDivergentTerminalFinal(t *testing.T) {
	fixture, receipt, landing := retiredPublishedMergeFixture(t, true)
	installWorktreeMergeMergedPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_TARGET_SHA", landing)
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		t.Fatal(err)
	}
	digests, err := worktrees.RemovedTerminalWorkLogClaimDigestsAllowingAdvancedFinalCommit(fixture.githubDir, expectations)
	if err != nil {
		t.Fatal(err)
	}
	terminalPath := retiredTerminalPath(t, fixture, receipt.Sources[0].Task, digests)
	unrelated := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", receipt.TargetSHA+"^{tree}", "-m", "unrelated terminal final"))
	rewriteJSONField(t, terminalPath, "final_commit", unrelated)
	rewriteJSONField(t, retiredTerminalOutboxPath(terminalPath), "final_commit", unrelated)
	options := retiredLandedAcknowledgementOptions(t, fixture, receipt, landing)
	if _, err := AcknowledgeRetiredLandedWorktreeMerge(context.Background(), options); err == nil || !strings.Contains(err.Error(), "receipted source to terminal final") {
		t.Fatalf("divergent terminal final was admitted: %v", err)
	}
}

func retiredPublishedMergeFixture(t *testing.T, advanceSource bool) (engineFixture, WorktreeMergeReceipt, string) {
	t.Helper()
	fixture := newEngineFixture(t)
	configureRetiredLandedFixtureOrigin(t, fixture)
	source := createMergeSource(t, fixture, "retired-ack-source", "feature/retired-ack-source", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(fixture.repository.CloneURL), "acme", "app.git")
	for _, path := range []string{receipt.Candidate.Worktree, source.WorktreeDir} {
		runEngineGit(t, path, "remote", "set-url", "origin", alias)
	}
	if advanceSource {
		writeEngineFile(t, filepath.Join(source.WorktreeDir, "later.txt"), "later terminal work\n")
		runEngineGit(t, source.WorktreeDir, "add", "later.txt")
		runEngineGit(t, source.WorktreeDir, "commit", "-m", "docs: later terminal work")
	}
	terminalSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", receipt.Candidate.SHA, "-m", "merge integration candidate")
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", terminalSource, "-m", "merge terminal source final")
	landing := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt.LandingSHA = landing
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", landing)
	for _, task := range sortedUniqueMergeTasks(receipt) {
		outcome, cleanupErr := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
			ProjectsRoot: fixture.githubDir, Task: task, Base: receipt.Target, ExactRepository: receipt.Repository,
			AbsorbedBy: landing, MergeReceiptProofs: worktreeMergeCleanupProofs(receipt, task), Apply: true, DeleteRemote: true, OlderThan: 0, Workers: 1,
		})
		if cleanupErr != nil || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
			t.Fatalf("retire %s = %+v err=%v", task, outcome, cleanupErr)
		}
	}
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergePrepared
	receipt.PullRequest = "https://example.test/acme/app/pull/17"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	receipt.LandingSHA = ""
	receipt.CleanedTasks = nil
	receipt.CleanupReports = nil
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return fixture, receipt, landing
}

func configureRetiredLandedFixtureOrigin(t *testing.T, fixture engineFixture) {
	t.Helper()
	alias := filepath.Join(filepath.Dir(fixture.repository.CloneURL), "acme", "app.git")
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.repository.CloneURL, alias); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "remote", "set-url", "origin", alias)
}

func retiredLandedAcknowledgementOptions(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, landing string) WorktreeMergeRetiredLandedAcknowledgementOptions {
	t.Helper()
	hash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		t.Fatal(err)
	}
	digests, err := worktrees.RemovedTerminalWorkLogClaimDigestsAllowingAdvancedFinalCommit(fixture.githubDir, expectations)
	if err != nil {
		t.Fatal(err)
	}
	expected := make([]string, len(digests))
	for i, digest := range digests {
		expected[i] = digest.Task + "=" + digest.SHA256
	}
	return WorktreeMergeRetiredLandedAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ExpectedReceiptSHA256: hash, ExpectedLandingSHA: landing, ExpectedClaimSHA256: expected, Actor: "test", Reason: "prove retired published landing"}
}

func retiredTerminalPath(t *testing.T, fixture engineFixture, task string, digests []worktrees.TerminalWorkLogClaimDigest) string {
	t.Helper()
	var wanted string
	for _, digest := range digests {
		if digest.Task == task {
			wanted = digest.TerminalSHA256
		}
	}
	if wanted == "" {
		t.Fatalf("terminal digest for %s not found", task)
	}
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := os.ReadDir(filepath.Join(home, "worklogs", task, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if !run.IsDir() {
			continue
		}
		terminals := filepath.Join(home, "worklogs", task, "runs", run.Name(), "terminals")
		entries, readErr := os.ReadDir(terminals)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(terminals, entry.Name())
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			digest := sha256.Sum256(contents)
			if fmt.Sprintf("%x", digest[:]) == wanted {
				return path
			}
		}
	}
	t.Fatalf("terminal bytes for %s were not found", task)
	return ""
}

func retiredTerminalOutboxPath(terminalPath string) string {
	run := filepath.Base(filepath.Dir(filepath.Dir(terminalPath)))
	taskDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(terminalPath))))
	claimID := strings.TrimSuffix(filepath.Base(terminalPath), ".json")
	return filepath.Join(taskDir, "outbox", run+"-"+claimID+"-sealed.json")
}

func rewriteJSONField(t *testing.T, path, field, value string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	document[field] = value
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(updated, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
