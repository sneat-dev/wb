package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func seedUmbrellaClaim(t *testing.T, projectsRoot, task, repo, branch, umbrellaRef string, prNum int) (string, worktreeclaims.Claim) {
	t.Helper()
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(projectsRoot, ".worktrees", task, repo)
	if err := os.MkdirAll(filepath.Join(wtDir, ".wb", "local"), 0o700); err != nil {
		t.Fatal(err)
	}
	baseSHA := strings.Repeat("a", 40)
	claimID := worktreeclaims.WorkLogClaimID(task, worktreeclaims.CreationResult{
		Repository: repo, WorktreeDir: wtDir, Branch: branch, Base: "main", BaseSHA: baseSHA,
	})
	claim := worktreeclaims.Claim{
		Version: 2, EffortID: task, RunID: "run-1", ClaimID: claimID, Task: task,
		Repository: repo, Worktree: wtDir, Branch: branch, Base: "main", BaseSHA: baseSHA,
		Lifecycle: "active", RecordedAt: time.Now().UTC(), Umbrella: umbrellaRef,
	}
	claimsDir := filepath.Join(home, "worklogs", task, "runs", "run-1", "claims")
	if err := os.MkdirAll(claimsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claimsDir, claimID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "version: 1\neffort_id: " + task + "\neffort_kind: feature\nrepository: " + repo + "\nworktree: " + wtDir + "\nbranch: " + branch + "\nbase: main\nbase_sha: " + baseSHA + "\ncreated_at: 2026-11-28T00:00:00Z\numbrella: " + umbrellaRef + "\nprovenance: created\n"
	if err := os.WriteFile(filepath.Join(wtDir, ".wb", "local", "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if prNum > 0 {
		prRaw, err := json.Marshal(worktreeclaims.ClaimPullRequestBinding{
			Repository: repo, PullRequest: prNum,
			URL:        "https://github.com/" + repo + "/pull/846",
			RecordedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(claimsDir, claimID+worktreeclaims.PullRequestBindingSuffix), prRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return wtDir, claim
}

func TestUmbrellaCommitTrailersAndPullRequestBodyPartOf(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	wtDir, _ := seedUmbrellaClaim(t, projectsRoot, "cross-repo", "sneat-co/backstage", "cross-repo", "github:sneat-co/backstage#845", 846)

	// Commit message carries Wb-Umbrella trailer without copying private text.
	gotMsg := commitMessageWithUmbrella(projectsRoot, wtDir, "feat: add rule")
	wantMsg := "feat: add rule\n\nWb-Umbrella: github:sneat-co/backstage#845"
	if gotMsg != wantMsg {
		t.Fatalf("commitMessageWithUmbrella = %q, want %q", gotMsg, wantMsg)
	}
	if got := commitMessageWithUmbrella(projectsRoot, t.TempDir(), "plain"); got != "plain" {
		t.Fatalf("no-umbrella commit message = %q", got)
	}
	if got := commitMessageWithTaskUmbrella(projectsRoot, "cross-repo", "revert: x"); !strings.Contains(got, "Wb-Umbrella: github:sneat-co/backstage#845") {
		t.Fatalf("commitMessageWithTaskUmbrella = %q", got)
	}
	if got := commitMessageWithTaskUmbrella(projectsRoot, "missing", "revert: x"); got != "revert: x" {
		t.Fatalf("missing task message = %q", got)
	}

	// PR body adds Part of <link> rendered by provider and is idempotent.
	providers := umbrella.DefaultProviders(nil)
	body, refStr, err := pullRequestBodyWithUmbrella(projectsRoot, wtDir, "Change summary", providers)
	if err != nil {
		t.Fatalf("pullRequestBodyWithUmbrella: %v", err)
	}
	if refStr != "github:sneat-co/backstage#845" || body != "Part of sneat-co/backstage#845\n\nChange summary" {
		t.Fatalf("pullRequestBodyWithUmbrella = (%q, %q)", body, refStr)
	}
	if got, gotRef, err := pullRequestBodyWithUmbrella(projectsRoot, t.TempDir(), "Plain", providers); err != nil || got != "Plain" || gotRef != "" {
		t.Fatalf("no-umbrella body = (%q, %q, %v)", got, gotRef, err)
	}

	// Pull request merge commit body trailer.
	mergedBody := appendPullRequestUmbrellaTrailer(projectsRoot, "sneat-co/backstage", 846, "cross-repo", "Pull request: sneat-co/backstage#846\n")
	if !strings.Contains(mergedBody, "Wb-Umbrella: github:sneat-co/backstage#845") {
		t.Fatalf("appendPullRequestUmbrellaTrailer = %q", mergedBody)
	}
	if got := appendPullRequestUmbrellaTrailer(projectsRoot, "other/repo", 1, "b", "plain\n"); got != "plain\n" {
		t.Fatalf("unmatched PR trailer = %q", got)
	}

	// Unsupported registry stored on manifest fails RenderLink cleanly.
	wtBad, _ := seedUmbrellaClaim(t, projectsRoot, "bad-reg", "o/r", "b", "synchestra:eff-1", 0)
	if _, _, err := pullRequestBodyWithUmbrella(projectsRoot, wtBad, "Body", providers); err == nil || !strings.Contains(err.Error(), "synchestra") {
		t.Fatalf("synchestra RenderLink error = %v", err)
	}
	// Malformed umbrella stored on manifest returns parse error for body and leaves commit message untouched.
	wtMalformed, _ := seedUmbrellaClaim(t, projectsRoot, "bad-fmt", "o/r2", "b2", "github:not-valid", 0)
	if got := commitMessageWithUmbrella(projectsRoot, wtMalformed, "msg"); got != "msg" {
		t.Fatalf("malformed umbrella commit message = %q", got)
	}
	if got := commitMessageWithTaskUmbrella(projectsRoot, "bad-fmt", "msg"); got != "msg" {
		t.Fatalf("malformed umbrella task message = %q", got)
	}
	if _, _, err := pullRequestBodyWithUmbrella(projectsRoot, wtMalformed, "Body", providers); err == nil {
		t.Fatal("malformed umbrella body succeeded")
	}
	if _, err := pullRequestCreateBody(PullRequestCreateOptions{ProjectsRoot: projectsRoot, Body: "Body", UmbrellaProviders: providers}, wtMalformed, nil); err == nil {
		t.Fatal("pullRequestCreateBody accepted malformed umbrella")
	}
}

func TestReportPullRequestAndWorktreeMergeLandedUmbrella(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	wt1, _ := seedUmbrellaClaim(t, projectsRoot, "cross-repo", "sneat-co/backstage", "cross-repo", "github:sneat-co/backstage#845", 846)

	var posted []string
	providers := umbrella.DefaultProviders(func(_ context.Context, args ...string) umbrella.CommandResult {
		posted = append(posted, strings.Join(args, " "))
		return umbrella.CommandResult{Stdout: []byte(`{"id":1}`)}
	})

	// Nil result guard and unmatched PR guard.
	reportPullRequestLandedUmbrella(context.Background(), PullRequestLandOptions{}, nil, "846")
	unmatchedPR := PullRequestLandResult{Repository: "other/repo", PullRequest: 99, HeadRef: "other", MergeSHA: strings.Repeat("9", 40), Evidence: map[string]string{}}
	reportPullRequestLandedUmbrella(context.Background(), PullRequestLandOptions{ProjectsRoot: projectsRoot, Repository: "other/repo", UmbrellaProviders: providers}, &unmatchedPR, "99")
	if unmatchedPR.Umbrella != "" {
		t.Fatalf("unmatched PR unexpectedly got umbrella %q", unmatchedPR.Umbrella)
	}

	// PR land posts status line, records LandedSHA, and offers close command once all members landed!
	prResult := PullRequestLandResult{
		Repository:  "sneat-co/backstage",
		PullRequest: 846,
		HeadRef:     "cross-repo",
		MergeSHA:    strings.Repeat("9", 40),
		Evidence:    map[string]string{},
	}
	reportPullRequestLandedUmbrella(context.Background(), PullRequestLandOptions{
		ProjectsRoot:      projectsRoot,
		Repository:        "sneat-co/backstage",
		UmbrellaProviders: providers,
	}, &prResult, "846")
	if prResult.Umbrella != "github:sneat-co/backstage#845" {
		t.Fatalf("prResult.Umbrella = %q", prResult.Umbrella)
	}
	if prResult.Evidence["umbrella_status"] != "posted" {
		t.Fatalf("umbrella_status = %q", prResult.Evidence["umbrella_status"])
	}
	if prResult.UmbrellaCloseCommand != "wb umbrella close sneat-co/backstage#845 --apply" {
		t.Fatalf("UmbrellaCloseCommand = %q", prResult.UmbrellaCloseCommand)
	}
	if len(posted) != 1 || !strings.Contains(posted[0], "repos/sneat-co/backstage/issues/845/comments") || !strings.Contains(posted[0], "PR #846") {
		t.Fatalf("posted comments = %v", posted)
	}

	// Failing provider records error in Evidence without losing Umbrella ref.
	failProviders := umbrella.DefaultProviders(func(context.Context, ...string) umbrella.CommandResult {
		return umbrella.CommandResult{Stderr: []byte("network down"), Err: errors.New("exit 1")}
	})
	prFail := PullRequestLandResult{
		Repository:  "sneat-co/backstage",
		PullRequest: 846,
		HeadRef:     "cross-repo",
		MergeSHA:    strings.Repeat("9", 40),
		Evidence:    map[string]string{},
	}
	reportPullRequestLandedUmbrella(context.Background(), PullRequestLandOptions{
		ProjectsRoot:      projectsRoot,
		Repository:        "sneat-co/backstage",
		UmbrellaProviders: failProviders,
	}, &prFail, "846")
	if !strings.Contains(prFail.Evidence["umbrella_status_error"], "network down") {
		t.Fatalf("expected umbrella_status_error, got %+v", prFail.Evidence)
	}

	// Worktree merge land posts status line and records finding on failure.
	posted = nil
	reportWorktreeMergeLandedUmbrella(context.Background(), projectsRoot, providers, nil)
	wtBadOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wtBadOnly, ".wb", "local"), 0o700); err != nil {
		t.Fatal(err)
	}
	badManifest := "version: 1\neffort_id: bad-only\neffort_kind: feature\nrepository: o/r\nworktree: " + wtBadOnly + "\nbranch: b\nbase: main\nbase_sha: " + strings.Repeat("a", 40) + "\ncreated_at: 2026-11-28T00:00:00Z\numbrella: github:invalid\nprovenance: created\n"
	if err := os.WriteFile(filepath.Join(wtBadOnly, ".wb", "local", "manifest.yaml"), []byte(badManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := WorktreeMergeReceipt{
		Repository:  "sneat-co/backstage",
		PullRequest: "https://github.com/sneat-co/backstage/pull/846",
		LandingSHA:  strings.Repeat("9", 40),
		Sources: []WorktreeMergeSource{
			{Task: "cross-repo", Worktree: wt1},
			{Task: "cross-repo", Worktree: wt1}, // duplicate deduped
			{Task: "no-umbrella", Worktree: t.TempDir()},
			{Task: "bad-only", Worktree: wtBadOnly},
		},
	}
	reportWorktreeMergeLandedUmbrella(context.Background(), projectsRoot, providers, &receipt)
	if receipt.Umbrella != "github:sneat-co/backstage#845" || receipt.UmbrellaCloseCommand != "wb umbrella close sneat-co/backstage#845 --apply" {
		t.Fatalf("receipt umbrella fields = (%q, %q)", receipt.Umbrella, receipt.UmbrellaCloseCommand)
	}
	if len(posted) != 1 {
		t.Fatalf("worktree merge posted %d comments, want 1", len(posted))
	}

	// Worktree merge failure records finding without aborting.
	receiptFail := WorktreeMergeReceipt{
		Repository: "sneat-co/backstage",
		LandingSHA: strings.Repeat("9", 40),
		Sources:    []WorktreeMergeSource{{Task: "cross-repo", Worktree: wt1}},
	}
	reportWorktreeMergeLandedUmbrella(context.Background(), projectsRoot, failProviders, &receiptFail)
	if len(receiptFail.Findings) != 1 || receiptFail.Findings[0].Code != "umbrella-status-error" {
		t.Fatalf("expected umbrella-status-error finding, got %+v", receiptFail.Findings)
	}

	// Verify resolveUmbrellaProviders nil fallback returns non-nil default registry.
	if reg := resolveUmbrellaProviders(nil); reg == nil || len(reg.Supported()) != 1 {
		t.Fatalf("resolveUmbrellaProviders(nil) = %+v", reg)
	}
	_ = worktrees.UmbrellaForTask(projectsRoot, "cross-repo")
}
