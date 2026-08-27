package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPrepareWorktreeMergeCreatesIsolatedConsumableCandidate(t *testing.T) {
	fixture := newEngineFixture(t)
	canonicalHead := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	sourceA := createMergeSource(t, fixture, "merge-source-a", "feature/a", "a.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "merge-source-b", "feature/b", "b.txt", "b\n")
	sourceAHead := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD"))
	sourceBHead := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD"))

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{sourceA.WorktreeDir, sourceB.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != WorktreeMergePrepared || receipt.Phase != WorktreeMergePhasePrepare {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.Repository != "acme/app" || receipt.Target != "main" || receipt.TargetSHA != canonicalHead {
		t.Fatalf("target receipt = %+v, want acme/app main at %s", receipt, canonicalHead)
	}
	if receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "main" || receipt.Candidate.SHA == "" {
		t.Fatalf("candidate = %+v", receipt.Candidate)
	}
	if len(receipt.Sources) != 2 || receipt.Sources[0].SHA != sourceAHead || receipt.Sources[1].SHA != sourceBHead {
		t.Fatalf("sources = %+v", receipt.Sources)
	}
	for _, head := range []string{canonicalHead, sourceAHead, sourceBHead} {
		if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", "--is-ancestor", head, receipt.Candidate.SHA)); got != "" {
			t.Fatalf("unexpected merge-base output for %s: %q", head, got)
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != canonicalHead {
		t.Fatalf("canonical HEAD changed from %s to %s", canonicalHead, got)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD")); got != sourceAHead {
		t.Fatalf("source A changed from %s to %s", sourceAHead, got)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD")); got != sourceBHead {
		t.Fatalf("source B changed from %s to %s", sourceBHead, got)
	}
	if _, err := os.Stat(receipt.ReceiptPath); err != nil {
		t.Fatalf("durable receipt missing: %v", err)
	}
}

func TestLandWorktreeMergeDirectWalksExactRemoteJourney(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "direct-source", "feature/direct", "direct.txt", "direct\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeLanded || landed.Route.Route != WorktreeMergeRouteDirect || landed.LandingSHA != receipt.Candidate.SHA {
		t.Fatalf("landing receipt = %+v", landed)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != landed.LandingSHA {
		t.Fatalf("canonical target = %s, want exact remote landing %s", got, landed.LandingSHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(got, landed.LandingSHA+"\t") {
		t.Fatalf("remote target = %q, want %s", got, landed.LandingSHA)
	}
	reverted, err := PrepareWorktreeMergeRevert(context.Background(), fixture.githubDir, landed.ReceiptPath, time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reverted.Phase != WorktreeMergePhaseRevert || reverted.Status != WorktreeMergePrepared || reverted.Candidate.SHA == landed.Candidate.SHA {
		t.Fatalf("forward revert receipt = %+v", reverted)
	}
	if _, err := os.Stat(filepath.Join(reverted.Candidate.Worktree, "direct.txt")); !os.IsNotExist(err) {
		t.Fatalf("forward revert candidate retained landed file: %v", err)
	}
	t.Setenv("WB_TEST_TARGET_SHA", reverted.Candidate.SHA)
	revertLanded, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: reverted.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if revertLanded.Status != WorktreeMergeLanded || revertLanded.RevertOf == nil || revertLanded.RevertOf.LandingSHA != landed.LandingSHA {
		t.Fatalf("forward revert landing receipt = %+v", revertLanded)
	}
	if _, err := os.Stat(filepath.Join(fixture.canonical, "direct.txt")); !os.IsNotExist(err) {
		t.Fatalf("forward revert did not remove landed file from canonical target: %v", err)
	}
}

func TestLandWorktreeMergeCleanupTerminalizesExactRepositoryAssets(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "cleanup-source", "feature/cleanup", "cleanup.txt", "cleanup\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateWorktree := receipt.Candidate.Worktree
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Cleanup: true,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeComplete || len(landed.CleanedTasks) != 2 || len(landed.CleanupReports) != 2 {
		t.Fatalf("terminal cleanup receipt = %+v", landed)
	}
	for _, path := range []string{source.WorktreeDir, candidateWorktree} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("cleaned worktree still exists at %s: %v", path, statErr)
		}
	}
}

func TestLandWorktreeMergeRebasesUnpublishedCandidateOntoAdvancedTarget(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "rebase-source", "feature/rebase", "feature.txt", "feature\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	preparedCandidate := receipt.Candidate.SHA
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target advanced\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: advance target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advancedTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Rebase == nil || landed.Rebase.CandidateBefore != preparedCandidate || landed.Rebase.TargetBefore != receipt.TargetSHA ||
		landed.Rebase.TargetAfter != advancedTarget || landed.Rebase.CandidateAfter != landed.Candidate.SHA {
		t.Fatalf("rebase receipt = %+v, landing = %+v", landed.Rebase, landed)
	}
	if landed.Candidate.SHA == preparedCandidate {
		t.Fatal("candidate SHA did not change after target rebase")
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("rebased landing did not complete cleanup: %+v", landed)
	}
	for _, name := range []string{"feature.txt", "target.txt"} {
		if _, err := os.Stat(filepath.Join(fixture.canonical, name)); err != nil {
			t.Fatalf("landed canonical target lacks %s: %v", name, err)
		}
	}
	if _, statErr := os.Stat(source.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("rebased source worktree remains after cleanup: %v", statErr)
	}
}

func TestLandWorktreeMergeRebaseConflictAbortsWithoutChangingSources(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "rebase-conflict-source", "feature/rebase-conflict", "shared.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(fixture.canonical, "shared.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "shared.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: conflicting target advance")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	advancedTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))

	failed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts while rebasing") || failed.Status != WorktreeMergeConflict {
		t.Fatalf("rebase conflict receipt=%+v err=%v", failed, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != receipt.Candidate.SHA {
		t.Fatalf("candidate changed after aborted rebase: got %s want %s", got, receipt.Candidate.SHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != sourceHead {
		t.Fatalf("source changed after aborted rebase: got %s want %s", got, sourceHead)
	}
	if status := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "status", "--porcelain")); status != "" {
		t.Fatalf("candidate retained rebase conflict state: %q", status)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(got, advancedTarget+"\t") {
		t.Fatalf("remote target changed during failed rebase: %q", got)
	}
}

func TestLandWorktreeMergeRefusesToRewritePublishedCandidateForTargetDrift(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-drift-source", "feature/published-drift", "published.txt", "candidate\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.PullRequest = "https://example.test/acme/app/pull/23"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "advanced.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "advanced.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: advance published target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	installWorktreeMergeOpenPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)

	failed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to rewrite the published branch without force-push") || failed.Status != WorktreeMergeConflict {
		t.Fatalf("published target drift receipt=%+v err=%v", failed, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != receipt.Candidate.SHA {
		t.Fatalf("published candidate was rewritten: got %s want %s", got, receipt.Candidate.SHA)
	}
}

func TestLandWorktreeMergeResumesAfterSquashPRMergedBeforeReceiptPersisted(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "squash-resume-source", "feature/squash-resume", "squash.txt", "squash\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", receipt.Candidate.SHA+"^{tree}"))
	serverLanding := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", receipt.TargetSHA, "-m", "squash candidate"))
	runEngineGit(t, fixture.canonical, "push", "origin", serverLanding+":refs/heads/main")
	receipt.PullRequest = "https://example.test/acme/app/pull/17"
	receipt.Route = WorktreeMergeRouteDecision{Requested: WorktreeMergeRouteAuto, Route: WorktreeMergeRoutePullRequest}
	receipt.PreviousTargetSHA = receipt.TargetSHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeMergedPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_TARGET_SHA", serverLanding)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if landed.Status != WorktreeMergeLanded || landed.LandingSHA != serverLanding || landed.PreviousTargetSHA != receipt.TargetSHA {
		t.Fatalf("resumed squash receipt = %+v", landed)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != serverLanding {
		t.Fatalf("canonical target = %s, want resumed server landing %s", got, serverLanding)
	}
}

func TestPrepareWorktreeMergeConflictPreservesEverySource(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "conflict-source-a", "feature/a", "shared.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "conflict-source-b", "feature/b", "shared.txt", "b\n")
	sourceAHead := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD"))
	sourceBHead := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD"))

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{sourceA.WorktreeDir, sourceB.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting prepare error = %v, receipt=%+v", err, receipt)
	}
	if receipt.Status != WorktreeMergeConflict || receipt.ResumeArgs == nil {
		t.Fatalf("conflict receipt = %+v", receipt)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceA.WorktreeDir, "rev-parse", "HEAD")); got != sourceAHead {
		t.Fatalf("source A changed from %s to %s", sourceAHead, got)
	}
	if got := strings.TrimSpace(runEngineGit(t, sourceB.WorktreeDir, "rev-parse", "HEAD")); got != sourceBHead {
		t.Fatalf("source B changed from %s to %s", sourceBHead, got)
	}
	status := runEngineGit(t, receipt.Candidate.Worktree, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("candidate retained conflict state: %q", status)
	}
}

func TestPrepareWorktreeMergeUsesRemoteDefaultInsteadOfAssumingMain(t *testing.T) {
	fixture := newEngineFixtureOnBranch(t, "trunk")
	source := createMergeSourceOnBase(t, fixture, "default-target-source", "feature/default-target", "trunk", "trunk.txt", "trunk\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Target != "trunk" || receipt.TargetSHA != strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "origin/trunk")) {
		t.Fatalf("default target receipt = %+v", receipt)
	}
}

func TestPrepareWorktreeMergeKeepsOneExclusiveActiveTargetLane(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "lane-source-a", "feature/lane-a", "lane-a.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "lane-source-b", "feature/lane-b", "lane-b.txt", "b\n")
	first, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "still owned by non-terminal receipt") {
		t.Fatalf("second active lane prepare = receipt %+v err %v", blocked, err)
	}
	if blocked.ReceiptPath != first.ReceiptPath || blocked.Candidate.Worktree != first.Candidate.Worktree {
		t.Fatalf("lane blocker did not return its exact owner: first=%+v blocked=%+v", first, blocked)
	}
}

func TestResolveWorktreeMergeAutoRouteUsesDirectOnlyForAuthoritativelyUnprotectedTarget(t *testing.T) {
	for _, test := range []struct {
		name       string
		branchJSON string
		rulesJSON  string
		want       WorktreeMergeRoute
	}{
		{name: "unprotected", branchJSON: `{"protected":false,"protection":{}}`, rulesJSON: `[[]]`, want: WorktreeMergeRouteDirect},
		{name: "classic pull request", branchJSON: `{"protected":true,"protection":{"required_pull_request_reviews":{}}}`, rulesJSON: `[[]]`, want: WorktreeMergeRoutePullRequest},
		{name: "ruleset pull request", branchJSON: `{"protected":true,"protection":{}}`, rulesJSON: `[[{"type":"pull_request","ruleset_id":7,"ruleset_source_type":"Repository","ruleset_source":"acme/app"}]]`, want: WorktreeMergeRoutePullRequest},
		{name: "incomplete branch policy", branchJSON: `{}`, rulesJSON: `[[]]`, want: WorktreeMergeRoutePullRequest},
		{name: "incomplete rules policy", branchJSON: `{"protected":false,"protection":{}}`, rulesJSON: `{}`, want: WorktreeMergeRoutePullRequest},
		{name: "merge queue unsupported", branchJSON: `{"protected":true,"protection":{}}`, rulesJSON: `[[{"type":"merge_queue","ruleset_id":9,"ruleset_source_type":"Repository","ruleset_source":"acme/app"}]]`, want: WorktreeMergeRouteUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			installWorktreeMergeGH(t, test.branchJSON, test.rulesJSON)
			decision, err := ResolveWorktreeMergeRoute(context.Background(), "acme/app", "main", WorktreeMergeRouteAuto)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Route != test.want {
				t.Fatalf("decision = %+v, want %s", decision, test.want)
			}
		})
	}
}

func createMergeSource(t *testing.T, fixture engineFixture, task, branch, name, contents string) worktrees.CreateResult {
	return createMergeSourceOnBase(t, fixture, task, branch, "main", name, contents)
}

func createMergeSourceOnBase(t *testing.T, fixture engineFixture, task, branch, base, name, contents string) worktrees.CreateResult {
	t.Helper()
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("prepare merge fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := worktrees.Create(context.Background(), []string{fixture.repository.Slug}, worktrees.CreateOptions{
		ProjectsRoot: fixture.githubDir,
		Operation:    task,
		Branch:       branch,
		BranchChosen: true,
		Base:         base,
		WorkLog: worktrees.WorkLogOptions{
			EffortID: task, RunID: task + "-run", Initiator: "test", AgentID: task,
			AgentRuntime: "test", Model: "test-model", OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	writeEngineFile(t, filepath.Join(result.WorktreeDir, name), contents)
	runEngineGit(t, result.WorktreeDir, "add", name)
	runEngineGit(t, result.WorktreeDir, "commit", "-m", "feat: add "+name)
	return result
}

func installWorktreeMergeGH(t *testing.T, branchJSON, rulesJSON string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := "#!/bin/sh\nset -eu\n" +
		"case \"$*\" in\n" +
		"  'api repos/acme/app/branches/main') printf '%s\\n' \"$WB_TEST_BRANCH_JSON\" ;;\n" +
		"  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\\n' \"$WB_TEST_RULES_JSON\" ;;\n" +
		"  *) echo \"unexpected gh command: $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_BRANCH_JSON", branchJSON)
	t.Setenv("WB_TEST_RULES_JSON", rulesJSON)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergeDirectGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'api repos/acme/app/branches/main') printf '%s\n' '{"protected":false,"protection":{}}' ;;
  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[[]]' ;;
  'api repos/acme/app/git/ref/heads/main')
    target_sha="${WB_TEST_TARGET_SHA:-}"
    if [ -n "${WB_TEST_REMOTE:-}" ]; then target_sha="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"; fi
    printf '{"object":{"sha":"%s"}}\n' "$target_sha" ;;
  'api --paginate repos/acme/app/commits/'*'/pulls') printf '%s\n' '[]' ;;
  *'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergeMergedPRGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/17 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"MERGED","mergedAt":"2026-08-27T00:00:00Z","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":"%s"}}\n' "$WB_TEST_CANDIDATE_SHA" "$WB_TEST_TARGET_SHA" ;;
  'api repos/acme/app/branches/main') printf '%s\n' '{"protected":false,"protection":{}}' ;;
  'api --paginate --slurp repos/acme/app/rules/branches/main?per_page=100') printf '%s\n' '[[]]' ;;
  'api repos/acme/app/git/ref/heads/main') printf '{"object":{"sha":"%s"}}\n' "$WB_TEST_TARGET_SHA" ;;
  *'/check-runs?per_page=100') printf '%s\n' '{"total_count":0,"check_runs":[]}' ;;
  *'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installWorktreeMergeOpenPRGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
set -eu
case "$*" in
  'pr view https://example.test/acme/app/pull/23 --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName')
    printf '{"state":"OPEN","mergedAt":"","headRefOid":"%s","baseRefName":"main","mergeCommit":{"oid":""}}\n' "$WB_TEST_CANDIDATE_SHA" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
