//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// The fixture uses a real bare Git remote and terminal WB cleanup proofs.
// Its GitHub shim computes ancestry from that remote rather than returning
// hard-coded containment answers.
//
//nolint:paralleltest // The native fixture and GitHub shim set process-wide environment variables.
func TestE2EAcknowledgeCleanedPullRequestPostTargetFailureRequiresMergedIdentityAndRecordedRoots(t *testing.T) {
	fixture, _, receipt, _ := landedTerminalCleanupFixture(t)
	runEngineGit(t, fixture.canonical, "commit", "--allow-empty", "-m", "server PR merge")
	landing := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	t.Setenv("WB_TEST_TARGET_SHA", landing)
	receipt.Route.Route = WorktreeMergeRoutePullRequest
	receipt.PullRequest = "https://example.test/acme/app/pull/168"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	receipt.LandingSHA = landing
	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.Checks.Status = githubchecks.PullRequestWaitFailed
	receipt.Checks.Head = landing
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	externallyTerminalizeMergeCleanup(t, fixture, &receipt)
	installStrandedLandingGH(t, receipt.PullRequest, fixture.repository.CloneURL)
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_MERGE_COMMIT_SHA", landing)
	options := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath}
	before, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	preview, err := AcknowledgeLandedMergeFailure(context.Background(), options)
	if err != nil || preview.ReceiptLandingSHA != landing || preview.CurrentTargetSHA != landing || preview.ClaimBaseSHA == "" {
		t.Fatalf("cleaned PR preview = %+v, error = %v", preview, err)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("preview wrote acknowledgement: %v", err)
	}

	for _, row := range []struct {
		name, want string
		mutate     func(*WorktreeMergeReceipt)
		prState    string
		mergeSHA   string
		prHeadSHA  string
	}{
		{name: "not merged", prState: "OPEN", want: "not MERGED"},
		{name: "wrong server merge", mergeSHA: receipt.Candidate.SHA, want: "does not prove exact recorded landing"},
		{name: "wrong PR head", prHeadSHA: receipt.TargetSHA, want: "does not contain exact receipted candidate"},
		{name: "wrong published candidate", mutate: func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.TargetSHA }, want: "exact published candidate"},
		{name: "wrong source", mutate: func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = r.TargetSHA }, want: "terminal cleanup receipt does not exactly match"},
	} {
		//nolint:paralleltest // Each case changes the process-wide GitHub shim environment with t.Setenv.
		t.Run(row.name, func(t *testing.T) {
			changed := receipt
			changed.Sources = append([]WorktreeMergeSource(nil), receipt.Sources...)
			if row.mutate != nil {
				row.mutate(&changed)
			}
			state, mergeSHA := "MERGED", landing
			if row.prState != "" {
				state = row.prState
			}
			if row.mergeSHA != "" {
				mergeSHA = row.mergeSHA
			}
			t.Setenv("WB_TEST_PR_STATE", state)
			t.Setenv("WB_TEST_MERGE_COMMIT_SHA", mergeSHA)
			t.Setenv("WB_TEST_PR_HEAD_SHA", row.prHeadSHA)
			_, err := acknowledgeLandedMergeFailure(context.Background(), options, defaultRunner, func(string) (WorktreeMergeReceipt, error) { return changed, nil }, persistLandedFailureAcknowledgement)
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("error = %v, want %q", err, row.want)
			}
			if _, statErr := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(statErr) {
				t.Fatalf("refusal wrote acknowledgement: %v", statErr)
			}
		})
	}
	driftRun := newAlreadyLandedScriptedRunner(map[string]string{
		"rev-parse --verify refs/remotes/origin/main^{commit}": receipt.TargetSHA + "\n",
	})
	if _, err := acknowledgeLandedMergeFailure(context.Background(), options, driftRun, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil || !strings.Contains(err.Error(), "changed since GitHub PR proof") {
		t.Fatalf("target changed between GitHub and git observations: %v", err)
	}
	runEngineGit(t, fixture.canonical, "push", "--force", "origin", receipt.TargetSHA+":main")
	if _, err := AcknowledgeLandedMergeFailure(context.Background(), options); err == nil {
		t.Fatal("rewound remote target accepted the recorded PR landing")
	}
	runEngineGit(t, fixture.canonical, "push", "--force", "origin", landing+":main")
	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "GitHub and exact terminal cleanup prove the recorded PR landing",
	})
	if err != nil || ack.ID == "" {
		t.Fatalf("cleaned PR apply = %+v, error = %v", ack, err)
	}
	after, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("historical failed receipt changed: %v", err)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, receipt); err != nil {
		t.Fatalf("read audited acknowledgement: %v", err)
	}
}
