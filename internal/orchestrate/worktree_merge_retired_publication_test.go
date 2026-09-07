package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installRetiredPublicationGH fakes the exact `gh pr view` invocation
// proveRetiredPullRequest issues. Every invocation in this package's
// production code for this recovery passes an empty working directory, so
// this fake deliberately ignores cwd.
func installRetiredPublicationGH(t *testing.T, repository, pullRequest string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := "#!/bin/sh\nset -eu\ncase \"$*\" in\n  'pr view " + pullRequest + " --repo " + repository + ` --json state,closedAt,mergedAt,mergeCommit,headRefName,headRefOid')
    printf '{"state":"%s","closedAt":"%s","mergedAt":"%s","headRefName":"%s","headRefOid":"%s","mergeCommit":{"oid":"%s"}}\n' \
      "$WB_TEST_PR_STATE" "$WB_TEST_PR_CLOSED_AT" "$WB_TEST_PR_MERGED_AT" "$WB_TEST_PR_HEAD_REF" "$WB_TEST_PR_HEAD_SHA" "$WB_TEST_PR_MERGE_COMMIT_SHA" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// buildStuckRetiredPublicationReceipt reproduces the real stuck shape: an
// exact candidate was published in a pull request, the receipted target then
// advanced past that candidate, and WB's own resume refused to rewrite the
// published branch without force-push -- leaving a prepare-phase conflict
// receipt with a non-empty PullRequest and PublishedCandidateSHA and no
// LandingSHA.
func buildStuckRetiredPublicationReceipt(t *testing.T, fixture engineFixture, task, branch, name string) WorktreeMergeReceipt {
	t.Helper()
	source := createMergeSource(t, fixture, task, branch, name, name+"\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	receipt.PullRequest = "https://example.test/" + fixture.repository.Slug + "/pull/450"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	receipt.Failure = "target advanced after candidate " + receipt.Candidate.SHA + " was published in " + receipt.PullRequest + "; refusing to rewrite the published branch without force-push"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	// Simulate the actual publish: push the exact candidate branch to origin.
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.Branch)
	// Advance the target past the published candidate.
	writeEngineFile(t, filepath.Join(fixture.canonical, "advance-"+task+".txt"), "advance\n")
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target past published candidate")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	return receipt
}

func TestAcknowledgeRetiredPublicationProvesClosedPullRequestAndFreesLane(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-source", "feature/retired-pub", "retired")
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	// The operator retires the stale publication by hand: close the PR
	// without merging, and delete its remote candidate branch.
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "--delete", receipt.Candidate.Branch)

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")

	dryRun, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Status != "retired_publication_acknowledged" || dryRun.PullRequestState != "CLOSED" || dryRun.PullRequest != receipt.PullRequest {
		t.Fatalf("dry-run acknowledgement = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}

	ack, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "GitHub proves the PR closed unmerged and the branch is gone",
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

	ackAgain, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "retry",
	})
	if err != nil || ackAgain.ID != ack.ID {
		t.Fatalf("idempotent acknowledgement = %+v err=%v", ackAgain, err)
	}

	// The lane is freed: preparing the exact same sources again supersedes
	// the stuck receipt under a fresh operation, a fresh integration branch,
	// and an empty pull request, on the current (advanced) target.
	if len(receipt.Sources) != 1 {
		t.Fatalf("expected exactly one receipted source, got %+v", receipt.Sources)
	}
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{receipt.Sources[0].Worktree}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != WorktreeMergePrepared || next.Candidate.SHA == "" {
		t.Fatalf("new merge preflight = %+v", next)
	}
	if next.PullRequest != "" {
		t.Fatalf("new candidate should not inherit the retired pull request, got %+v", next)
	}
	if next.ReceiptPath == receipt.ReceiptPath {
		t.Fatalf("new candidate reused the stuck receipt path %s", next.ReceiptPath)
	}
	if next.Candidate.Branch == receipt.Candidate.Branch {
		t.Fatalf("new candidate reused the retired integration branch %s", next.Candidate.Branch)
	}
}

func TestAcknowledgeRetiredPublicationRefusesWhenMerged(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-merged-source", "feature/retired-pub-merged", "merged")

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", receipt.Candidate.SHA)

	if _, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "attempt",
	}); err == nil || !strings.Contains(err.Error(), "acknowledge-stranded-landing") {
		t.Fatalf("expected refusal pointing at acknowledge-stranded-landing, got %v", err)
	}
	if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refused acknowledgement wrote a file: %v", statErr)
	}
}

func TestAcknowledgeRetiredPublicationRefusesWhenStillOpen(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-open-source", "feature/retired-pub-open", "open")

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "OPEN")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")

	if _, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "attempt",
	}); err == nil || !strings.Contains(err.Error(), "want CLOSED") {
		t.Fatalf("expected refusal for an open pull request, got %v", err)
	}
	if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refused acknowledgement wrote a file: %v", statErr)
	}

	// The lane remains locked: an unrelated candidate for the same
	// (repository, target) lane is still refused at pre-flight.
	unrelated := createMergeSource(t, fixture, "retired-pub-open-unrelated", "feature/retired-pub-open-unrelated", "unrelated.txt", "unrelated\n")
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{unrelated.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "non-terminal receipt") {
		t.Fatalf("expected the lane to remain locked after a refused acknowledgement, got %v", err)
	}
}

func TestAcknowledgeRetiredPublicationRefusesWhenBranchStillPublished(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-branch-source", "feature/retired-pub-branch", "branch")
	// Deliberately do not delete the remote branch.

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")

	if _, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "attempt",
	}); err == nil || !strings.Contains(err.Error(), "still carries a remote ref") {
		t.Fatalf("expected refusal for a still-published branch, got %v", err)
	}
	if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refused acknowledgement wrote a file: %v", statErr)
	}
}

func TestAcknowledgeRetiredPublicationRefusesWhenCandidateLanded(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-landed-source", "feature/retired-pub-landed", "landed")

	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "--delete", receipt.Candidate.Branch)
	// The candidate actually landed as a fast-forward of main after all.
	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "--force", "origin", "main")

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")

	if _, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "attempt",
	}); err == nil || !strings.Contains(err.Error(), "already reachable from current target") {
		t.Fatalf("expected refusal for a landed candidate, got %v", err)
	}
	if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refused acknowledgement wrote a file: %v", statErr)
	}
}

func TestAcknowledgeRetiredPublicationRequiresActorAndReasonWithApply(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-noactor-source", "feature/retired-pub-noactor", "noactor")
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "--delete", receipt.Candidate.Branch)

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")

	if _, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
	}); err == nil || !strings.Contains(err.Error(), "--actor and --reason are required") {
		t.Fatalf("expected refusal for missing actor/reason, got %v", err)
	}
}

// TestRetiredPublicationAcknowledgementTamperingKeepsLaneBlocked reproduces a
// tampered sidecar: even though the receipt itself is untouched, a sidecar
// whose recorded fields do not match its own binding ID must fail closed,
// never silently freeing the lane.
func TestRetiredPublicationAcknowledgementTamperingKeepsLaneBlocked(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-tamper-source", "feature/retired-pub-tamper", "tamper")
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "--delete", receipt.Candidate.Branch)

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")

	ack, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "audited retirement",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with the sidecar's actor after the fact, without recomputing ID.
	contents, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(contents), `"actor": "reviewer"`, `"actor": "attacker"`, 1)
	if tampered == string(contents) {
		t.Fatal("tamper substitution did not match sidecar content")
	}
	if err := os.WriteFile(ack.AcknowledgementPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := hasRetiredPublicationAcknowledgement(receipt); err == nil {
		t.Fatal("expected the tampered sidecar to fail closed")
	}

	unrelated := createMergeSource(t, fixture, "retired-pub-tamper-unrelated", "feature/retired-pub-tamper-unrelated", "unrelated.txt", "unrelated\n")
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{unrelated.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil {
		t.Fatal("expected a tampered sidecar to keep the lane blocked, got success")
	}
}

func TestValidateRetiredPublicationReceiptRefusesWrongShape(t *testing.T) {
	base := func() WorktreeMergeReceipt {
		return WorktreeMergeReceipt{
			ID: "receipt-id", ReceiptPath: "/receipts/lane.json", Lane: worktreeMergeLaneID("acme/app", "main"),
			Repository: "acme/app", Target: "main", TargetSHA: strings.Repeat("a", 40),
			Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict,
			PullRequest: "https://example.test/acme/app/pull/1", PublishedCandidateSHA: strings.Repeat("b", 40),
			Sources:   []WorktreeMergeSource{{Task: "t", Worktree: "/w", Branch: "b", SHA: strings.Repeat("c", 40)}},
			Candidate: WorktreeMergeCandidate{Task: "t", Worktree: "/w", Branch: "b", SHA: strings.Repeat("b", 40)},
		}
	}
	if err := validateRetiredPublicationReceipt(base(), base().ReceiptPath); err != nil {
		t.Fatalf("baseline receipt should validate: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*WorktreeMergeReceipt)
		wantErr string
	}{
		{name: "wrong phase", mutate: func(r *WorktreeMergeReceipt) { r.Phase = "" }, wantErr: "want prepare or land"},
		{name: "wrong status", mutate: func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }, wantErr: "want conflict, validation_failed, or checks_failed"},
		{name: "already has a landing SHA", mutate: func(r *WorktreeMergeReceipt) { r.LandingSHA = strings.Repeat("d", 40) }, wantErr: "already recorded a landing SHA"},
		{name: "no pull request", mutate: func(r *WorktreeMergeReceipt) { r.PullRequest = "" }, wantErr: "no published pull request"},
		{name: "no published or preserved candidate", mutate: func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA, r.Candidate.SHA = "", "" }, wantErr: "no published or preserved candidate"},
		{name: "inconsistent lane", mutate: func(r *WorktreeMergeReceipt) { r.Lane = "some-other-lane" }, wantErr: "inconsistent immutable receipt identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receipt := base()
			tt.mutate(&receipt)
			err := validateRetiredPublicationReceipt(receipt, receipt.ReceiptPath)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRetiredPublicationReceipt() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestEffectiveUnpublishedConflictAcceptsValidRetiredPublicationAcknowledgement(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-pub-effective-source", "feature/retired-pub-effective", "effective")
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "--delete", receipt.Candidate.Branch)

	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")

	current, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if unpublished, unpublishedErr := effectiveUnpublishedConflict(current); unpublishedErr != nil || unpublished {
		t.Fatalf("effectiveUnpublishedConflict() before acknowledgement = %v, %v; want false, nil", unpublished, unpublishedErr)
	}

	if _, err := AcknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "audited retirement",
	}); err != nil {
		t.Fatal(err)
	}

	current, err = readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	unpublished, err := effectiveUnpublishedConflict(current)
	if err != nil || !unpublished {
		t.Fatalf("effectiveUnpublishedConflict() after acknowledgement = %v, %v; want true, nil", unpublished, err)
	}
}
