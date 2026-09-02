package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installStrandedLandingGH fakes the exact `gh` invocations
// proveStrandedPullRequestLanding issues: a PR-view read (state, target head,
// remote comparisons), all resolved against the fixture's real bare remote so
// ancestry proofs are genuine git computations, never hard-coded booleans.
// Every invocation in this package's production code passes an empty working
// directory for these reads, so this fake deliberately ignores cwd.
func installStrandedLandingGH(t *testing.T, pullRequest, remoteGitDir string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := "#!/bin/sh\nset -eu\ncase \"$*\" in\n  'pr view " + pullRequest + ` --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    if [ "$WB_TEST_PR_STATE" = MERGED ]; then
      printf '{"state":"MERGED","mergedAt":"2026-09-01T00:00:00Z","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":"%s"}}\n' "$WB_TEST_CANDIDATE_SHA" "$WB_TEST_MERGE_COMMIT_SHA"
    else
      printf '{"state":"%s","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$WB_TEST_PR_STATE" "$WB_TEST_CANDIDATE_SHA"
    fi ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main')
    sha="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"
    printf '{"object":{"sha":"%s"}}\n' "$sha" ;;
  'api repos/acme/app/compare/'*'...'*)
    pair="${2#*compare/}"
    base="${pair%%...*}"
    candidate="${pair#*...}"
    merge_base="$(git --git-dir="$WB_TEST_REMOTE" merge-base "$base" "$candidate" 2>/dev/null || true)"
    if [ "$base" = "$candidate" ]; then
      status="identical"
    elif git --git-dir="$WB_TEST_REMOTE" merge-base --is-ancestor "$base" "$candidate" 2>/dev/null; then
      status="ahead"
    elif git --git-dir="$WB_TEST_REMOTE" merge-base --is-ancestor "$candidate" "$base" 2>/dev/null; then
      status="behind"
    else
      status="diverged"
    fi
    printf '{"status":"%s","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' "$status" "$base" "$merge_base" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_REMOTE", remoteGitDir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAcknowledgeStrandedPullRequestLandingProvesRemoteContainmentAndFreesLane(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "stranded-source", "feature/stranded", "stranded.txt", "stranded\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reproduce the exact stuck shape: a resume observed a published PR but
	// failed on pure I/O before it could confirm the landing, leaving a
	// land/conflict receipt with no recorded LandingSHA.
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergeConflict
	receipt.PullRequest = "https://example.test/acme/app/pull/91"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	receipt.Failure = "read pull-request landing receipt: gh pr view https://example.test/acme/app/pull/91 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName: chdir " + receipt.Candidate.Worktree + ": no such file or directory"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	// The candidate actually landed as a fast-forward of main.
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	// Remove the candidate worktree entirely: this receipt shape is defined
	// by that infrastructure already being gone, and the new verb must not
	// depend on it (unlike acknowledge-landed-failed, which requires it).
	if err := os.RemoveAll(receipt.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}

	installStrandedLandingGH(t, receipt.PullRequest, fixture.repository.CloneURL)
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_MERGE_COMMIT_SHA", receipt.Candidate.SHA)

	dryRun, err := AcknowledgeStrandedPullRequestLanding(context.Background(), WorktreeMergeStrandedLandingAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Status != "stranded_landing_acknowledged" || dryRun.ProvedLandingSHA != receipt.Candidate.SHA || dryRun.CurrentTargetSHA != receipt.Candidate.SHA {
		t.Fatalf("dry-run acknowledgement = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}

	ack, err := AcknowledgeStrandedPullRequestLanding(context.Background(), WorktreeMergeStrandedLandingAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "GitHub proves the PR merged and the target still contains the exact candidate",
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

	ackAgain, err := AcknowledgeStrandedPullRequestLanding(context.Background(), WorktreeMergeStrandedLandingAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "retry",
	})
	if err != nil || ackAgain.ID != ack.ID {
		t.Fatalf("idempotent acknowledgement = %+v err=%v", ackAgain, err)
	}

	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "acknowledged as a proved stranded landing") {
		t.Fatalf("acknowledged receipt was replayable: %v", err)
	}

	// The old non-terminal receipt no longer owns the lane, so a fresh source
	// can pass normal prepare preflight.
	newSource := createMergeSource(t, fixture, "stranded-source-new", "feature/stranded-new", "new.txt", "new\n")
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

func TestAcknowledgeStrandedPullRequestLandingRefusesWhenNotProvenMerged(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "stranded-open-source", "feature/stranded-open", "open.txt", "open\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergeConflict
	receipt.PullRequest = "https://example.test/acme/app/pull/92"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	receipt.Failure = "read pull-request landing receipt: chdir: no such file or directory"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	// The remote target never advanced: the PR is genuinely still open.
	installStrandedLandingGH(t, receipt.PullRequest, fixture.repository.CloneURL)
	t.Setenv("WB_TEST_PR_STATE", "OPEN")
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_MERGE_COMMIT_SHA", "")

	if _, err := AcknowledgeStrandedPullRequestLanding(context.Background(), WorktreeMergeStrandedLandingAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "attempt",
	}); err == nil || !strings.Contains(err.Error(), "not MERGED") {
		t.Fatalf("expected refusal for an unmerged pull request, got %v", err)
	}
	if _, statErr := os.Stat(strandedLandingAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refused acknowledgement wrote a file: %v", statErr)
	}

	// The lane remains locked: an unrelated candidate for the same
	// (repository, target) lane is still refused at pre-flight, exactly
	// reproducing the real stuck-lane symptom.
	unrelated := createMergeSource(t, fixture, "stranded-open-unrelated", "feature/stranded-open-unrelated", "unrelated.txt", "unrelated\n")
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{unrelated.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "non-terminal receipt") {
		t.Fatalf("expected the lane to remain locked after a refused acknowledgement, got %v", err)
	}
}

func TestAcknowledgeStrandedPullRequestLandingRefusesDivergedRemoteTarget(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "stranded-diverged-source", "feature/stranded-diverged", "diverged.txt", "diverged\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergeConflict
	receipt.PullRequest = "https://example.test/acme/app/pull/93"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	receipt.Failure = "read pull-request landing receipt: chdir: no such file or directory"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	// Push the candidate's objects to the bare remote under a throwaway ref
	// (without ever advancing main to it), so the fake compare endpoint can
	// compute a genuine merge-base instead of failing on an unknown object.
	runEngineGit(t, fixture.canonical, "push", "origin", receipt.Candidate.SHA+":refs/heads/stranded-diverged-candidate")

	// GitHub reports MERGED, but the remote target instead advanced with an
	// unrelated commit that diverges from the candidate rather than
	// containing it. The proof must catch this rather than trust the
	// PR-view state alone.
	writeEngineFile(t, filepath.Join(fixture.canonical, "diverged.txt"), "diverged\n")
	runEngineGit(t, fixture.canonical, "add", "diverged.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: diverged target history")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	installStrandedLandingGH(t, receipt.PullRequest, fixture.repository.CloneURL)
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_MERGE_COMMIT_SHA", receipt.Candidate.SHA)

	if _, err := AcknowledgeStrandedPullRequestLanding(context.Background(), WorktreeMergeStrandedLandingAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "attempt",
	}); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("expected refusal for a diverged remote target, got %v", err)
	}
	if _, statErr := os.Stat(strandedLandingAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refused acknowledgement wrote a file: %v", statErr)
	}
}

func TestValidateStrandedLandingReceiptRefusesWrongShape(t *testing.T) {
	base := func() WorktreeMergeReceipt {
		return WorktreeMergeReceipt{
			ID: "receipt-id", ReceiptPath: "/receipts/lane.json", Lane: worktreeMergeLaneID("acme/app", "main"),
			Repository: "acme/app", Target: "main", TargetSHA: strings.Repeat("a", 40),
			Phase: WorktreeMergePhaseLand, Status: WorktreeMergeConflict,
			PullRequest: "https://example.test/acme/app/pull/1",
			Candidate:   WorktreeMergeCandidate{Task: "t", Worktree: "/w", Branch: "b", SHA: strings.Repeat("b", 40)},
		}
	}
	baseline := base()
	baseline.PublishedCandidateSHA = baseline.Candidate.SHA
	if err := validateStrandedLandingReceipt(baseline, baseline.ReceiptPath); err != nil {
		t.Fatalf("baseline receipt should validate: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*WorktreeMergeReceipt)
		wantErr string
	}{
		{name: "wrong phase", mutate: func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhasePrepare }, wantErr: "want land conflict"},
		{name: "wrong status", mutate: func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergeValidationFailed }, wantErr: "want land conflict"},
		{name: "already has a landing SHA", mutate: func(r *WorktreeMergeReceipt) { r.LandingSHA = strings.Repeat("c", 40) }, wantErr: "already recorded a landing SHA"},
		{name: "no pull request", mutate: func(r *WorktreeMergeReceipt) { r.PullRequest = "" }, wantErr: "no published pull request"},
		{name: "published candidate mismatch", mutate: func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = strings.Repeat("d", 40) }, wantErr: "does not match its exact preserved candidate"},
		{name: "inconsistent lane", mutate: func(r *WorktreeMergeReceipt) { r.Lane = "some-other-lane" }, wantErr: "inconsistent immutable receipt identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receipt := base()
			receipt.PublishedCandidateSHA = receipt.Candidate.SHA
			tt.mutate(&receipt)
			err := validateStrandedLandingReceipt(receipt, receipt.ReceiptPath)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateStrandedLandingReceipt() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
