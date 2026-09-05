package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func installAdoptedCandidateResumeGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
head_sha="$(git --git-dir="$WB_TEST_REMOTE" rev-parse "refs/heads/$WB_TEST_PR_BRANCH")"
case "$*" in
  'pr view 7 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"OPEN","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$head_sha" ;;
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main') printf '%s\n' '{"protected":true,"protection":{"required_pull_request_reviews":{}}}' ;;
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[]' ;;
  'pr view 7 --repo acme/app --json state,headRefOid,baseRefName')
    printf '{"state":"OPEN","headRefOid":"%s","baseRefName":"main"}\n' "$head_sha" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installPublishedCandidateAdoptionGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'api repos/acme/app/pulls/7 --include'|'api repos/acme/app/pulls/7')
    printf '{"number":7,"state":"%s","head":{"ref":"%s","sha":"%s","repo":{"full_name":"%s"}},"base":{"ref":"%s","sha":"","repo":{"full_name":"%s"}}}\n' "$WB_TEST_PR_STATE" "$WB_TEST_PR_BRANCH" "$WB_TEST_PR_SHA" "$WB_TEST_PR_HEAD_REPO" "$WB_TEST_PR_BASE" "$WB_TEST_PR_BASE_REPO" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAdoptPublishedCandidatePreservesPRAcrossDescendantSourceRefresh(t *testing.T) {
	fixture := newEngineFixture(t)
	first := createMergeSource(t, fixture, "adopt-source-one", "feature/adopt-one", "one.txt", "one\n")
	second := createMergeSource(t, fixture, "adopt-source-two", "feature/adopt-two", "two.txt", "two\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
	// This is the real interrupted shape: GitHub has the exact candidate and
	// open PR, but WB lost the local publication fields before a failed check.
	receipt.Status, receipt.Phase, receipt.Failure = WorktreeMergeConflict, WorktreeMergePhasePrepare, "native Windows check failed after external publication"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(first.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, first.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, first.WorktreeDir, "commit", "-m", "fix: native check repair")
	installPublishedCandidateAdoptionGH(t)
	t.Setenv("WB_TEST_PR_STATE", "open")
	t.Setenv("WB_TEST_PR_BRANCH", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_HEAD_REPO", receipt.Repository)
	t.Setenv("WB_TEST_PR_BASE", receipt.Target)
	t.Setenv("WB_TEST_PR_BASE_REPO", receipt.Repository)
	dry, err := AdoptPublishedWorktreeMergeCandidate(context.Background(), WorktreeMergePublishedCandidateAdoptionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, PullRequest: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dry.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote acknowledgement: %v", err)
	}
	ack, err := AdoptPublishedWorktreeMergeCandidate(context.Background(), WorktreeMergePublishedCandidateAdoptionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, PullRequest: "7", Apply: true, Actor: "test", Reason: "recover lost local publication acknowledgement"})
	if err != nil {
		t.Fatal(err)
	}
	if now, err := os.ReadFile(receipt.ReceiptPath); err != nil || string(now) != string(original) {
		t.Fatalf("receipt changed: %v", err)
	}
	if _, err := os.Stat(ack.AcknowledgementPath); err != nil {
		t.Fatal(err)
	}
	ackBytes, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	var tampered WorktreeMergePublishedCandidateAdoption
	if err := json.Unmarshal(ackBytes, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Reason = "tampered"
	tamperedBytes, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ack.AcknowledgementPath, tamperedBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPublishedCandidateAdoption(ack.AcknowledgementPath, receipt); err == nil {
		t.Fatal("tampered acknowledgement was accepted")
	}
	if err := os.WriteFile(ack.AcknowledgementPath, ackBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if again, err := AdoptPublishedWorktreeMergeCandidate(context.Background(), WorktreeMergePublishedCandidateAdoptionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, PullRequest: "7", Apply: true, Actor: "other", Reason: "replay"}); err != nil || again.ID != ack.ID {
		t.Fatalf("idempotent replay=%+v err=%v", again, err)
	}
	refreshed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.PullRequest != "7" || refreshed.PublishedCandidateSHA != receipt.Candidate.SHA || refreshed.Candidate.SHA == receipt.Candidate.SHA {
		t.Fatalf("refresh lost adopted predecessor: %+v", refreshed)
	}
	remote := strings.TrimSpace(runEngineGit(t, refreshed.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+refreshed.Candidate.Branch))
	if !strings.HasPrefix(remote, receipt.Candidate.SHA+"\t") {
		t.Fatalf("prepare changed published remote ref: %q", remote)
	}
	installAdoptedCandidateResumeGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_PR_BRANCH", refreshed.Candidate.Branch)
	published, err := ResumeWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: refreshed.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("resume did not publish adopted candidate descendant: receipt=%+v err=%v", published, err)
	}
	if published.Status != WorktreeMergePublished || published.PullRequest != "7" || published.PublishedCandidateSHA != refreshed.Candidate.SHA {
		t.Fatalf("published descendant receipt = %+v", published)
	}
	if published.PushGate == nil || published.PushGate.Status != "passed" || published.PushGate.PreviousRemoteSHA != receipt.Candidate.SHA || published.PushGate.LocalSHA != refreshed.Candidate.SHA {
		t.Fatalf("published descendant push gate = %+v", published.PushGate)
	}
	remote = strings.TrimSpace(runEngineGit(t, refreshed.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+refreshed.Candidate.Branch))
	if !strings.HasPrefix(remote, refreshed.Candidate.SHA+"\t") {
		t.Fatalf("resume did not advance remote ref without force: %q", remote)
	}

	writeEngineFile(t, filepath.Join(second.WorktreeDir, "second-repair.txt"), "second repair\n")
	runEngineGit(t, second.WorktreeDir, "add", "second-repair.txt")
	runEngineGit(t, second.WorktreeDir, "commit", "-m", "fix: second native check repair")
	installPublishedCandidateAdoptionGH(t)
	t.Setenv("WB_TEST_PR_SHA", published.Candidate.SHA)
	secondRefresh, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatalf("prepare second descendant after published adoption = %v", err)
	}
	if secondRefresh.PullRequest != "7" || secondRefresh.PublishedCandidateSHA != published.Candidate.SHA || secondRefresh.Candidate.SHA == published.Candidate.SHA {
		t.Fatalf("second refresh lost published descendant: %+v", secondRefresh)
	}
	remote = strings.TrimSpace(runEngineGit(t, secondRefresh.Candidate.Worktree, "ls-remote", "origin", "refs/heads/"+secondRefresh.Candidate.Branch))
	if !strings.HasPrefix(remote, published.Candidate.SHA+"\t") {
		t.Fatalf("second prepare changed published remote ref: %q", remote)
	}
}

func TestPublishedCandidateAdoptionRefusesDriftedPullRequestIdentity(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "adopt-negative", "feature/adopt-negative", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
	installPublishedCandidateAdoptionGH(t)
	for _, tc := range []struct{ name, state, branch, sha, headRepo, base, baseRepo string }{
		{"closed", "closed", receipt.Candidate.Branch, receipt.Candidate.SHA, receipt.Repository, receipt.Target, receipt.Repository},
		{"merged", "merged", receipt.Candidate.Branch, receipt.Candidate.SHA, receipt.Repository, receipt.Target, receipt.Repository},
		{"wrong branch", "open", "other", receipt.Candidate.SHA, receipt.Repository, receipt.Target, receipt.Repository},
		{"wrong head", "open", receipt.Candidate.Branch, strings.Repeat("a", 40), receipt.Repository, receipt.Target, receipt.Repository},
		{"wrong head repository", "open", receipt.Candidate.Branch, receipt.Candidate.SHA, "other/repo", receipt.Target, receipt.Repository},
		{"wrong target", "open", receipt.Candidate.Branch, receipt.Candidate.SHA, receipt.Repository, "other", receipt.Repository},
		{"wrong base repository", "open", receipt.Candidate.Branch, receipt.Candidate.SHA, receipt.Repository, receipt.Target, "other/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WB_TEST_PR_STATE", tc.state)
			t.Setenv("WB_TEST_PR_BRANCH", tc.branch)
			t.Setenv("WB_TEST_PR_SHA", tc.sha)
			t.Setenv("WB_TEST_PR_HEAD_REPO", tc.headRepo)
			t.Setenv("WB_TEST_PR_BASE", tc.base)
			t.Setenv("WB_TEST_PR_BASE_REPO", tc.baseRepo)
			if err := provePublishedCandidatePullRequest(context.Background(), receipt, "7"); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}

func TestPublishedCandidateAdoptionSourceProofRefusesDirtyMovedAndNonDescendant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, engineFixture, worktrees.CreateResult)
	}{
		{"dirty", func(t *testing.T, _ engineFixture, s worktrees.CreateResult) {
			writeEngineFile(t, filepath.Join(s.WorktreeDir, "dirty.txt"), "dirty\n")
		}},
		{"moved branch", func(t *testing.T, _ engineFixture, s worktrees.CreateResult) {
			runEngineGit(t, s.WorktreeDir, "checkout", "--detach")
		}},
		{"non-descendant", func(t *testing.T, f engineFixture, s worktrees.CreateResult) {
			runEngineGit(t, s.WorktreeDir, "reset", "--hard", strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEngineFixture(t)
			s := createMergeSource(t, f, "adopt-source-proof-"+strings.ReplaceAll(tc.name, " ", "-"), "feature/adopt-source-proof-"+strings.ReplaceAll(tc.name, " ", "-"), "source.txt", "source\n")
			r, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{s.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, f, s)
			if err := validatePublishedCandidateAdoptionSources(context.Background(), r); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}

func TestPersistPublishedCandidateAdoptionFailsClosedOnExclusivePublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ack.json")
	previous := linkPublishedCandidateAdoption
	linkPublishedCandidateAdoption = func(_, _ string) error { return os.ErrPermission }
	defer func() { linkPublishedCandidateAdoption = previous }()
	err := persistPublishedCandidateAdoption(path, WorktreeMergePublishedCandidateAdoption{SchemaVersion: 1, Status: "published_candidate_adopted"})
	if err == nil {
		t.Fatal("expected exclusive publish failure")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed publication left visible acknowledgement: %v", statErr)
	}
}

func TestPublishedCandidateAdoptionRejectsMalformedSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ack.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPublishedCandidateAdoption(path, WorktreeMergeReceipt{}); err == nil {
		t.Fatal("expected malformed sidecar refusal")
	}
}

func TestAdoptPublishedCandidateApplyRequiresActorAndReason(t *testing.T) {
	_, err := AdoptPublishedWorktreeMergeCandidate(context.Background(), WorktreeMergePublishedCandidateAdoptionOptions{ProjectsRoot: t.TempDir(), Receipt: "missing.json", PullRequest: "7", Apply: true})
	if err == nil || !strings.Contains(err.Error(), "--actor and --reason") {
		t.Fatalf("missing audit admission error = %v", err)
	}
}
