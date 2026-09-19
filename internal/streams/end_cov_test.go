package streams

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndReportsAMissingStream(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.End(context.Background(), EndOptions{Name: "absent"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("End of a missing stream = %v, want ErrNotFound", err)
	}
}

// A member that was reserved but never published has no checkout to absorb and
// no worktree to remove; its lease is still released, which is what makes an
// interrupted `stream start` retirable.
func TestEndRetiresAMemberReservedButNeverPublished(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Store.Create(Stream{
		Name: "reserved", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "acme/app", Role: RoleLibrary, Worktree: "",
			Branch: "stream/reserved", Base: "main",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.End(context.Background(), EndOptions{Name: "reserved", Apply: true})
	if err != nil {
		t.Fatalf("End: %v", err)
	}
	if len(result.Members) != 1 || !result.Members[0].LeaseReleased {
		t.Fatalf("members = %#v, want the reserved member's lease released", result.Members)
	}
	if result.Members[0].DraftAction != "none" {
		t.Fatalf("draft action = %q, want none", result.Members[0].DraftAction)
	}
	ended, err := engine.Store.Load("reserved")
	if err != nil {
		t.Fatal(err)
	}
	if ended.Open() || ended.EndedAt == nil {
		t.Fatalf("stream after end = %#v, want it ended", ended)
	}
}

// The absorption guard fails closed: a fetch that did not happen means the
// check could not answer, and an unknown must refuse rather than pass.
func TestEndFailsClosedWhenOriginCannotBeReRead(t *testing.T) {
	t.Parallel()
	engine, git, _, worktrees, stream := startedStream(t, "stale-origin", "acme/library")
	member := stream.Members[0]
	git.fetchErr[member.Worktree] = errors.New("origin unreachable")

	_, err := engine.End(context.Background(), EndOptions{Name: "stale-origin", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUnabsorbedWork {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalUnabsorbedWork)
	}
	if !strings.Contains(refusal.Message, "could not re-read origin") {
		t.Fatalf("refusal = %q, want it to name the failed fetch", refusal.Message)
	}
	if len(worktrees.removed) != 0 {
		t.Fatalf("a refused end removed %v", worktrees.removed)
	}
}

func TestEndFailsClosedOnUnreadableLeaseHeads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("remote head", func(t *testing.T) {
		t.Parallel()
		engine, git, _, _, _ := startedStream(t, "remote-unreadable", "acme/library")
		engine.Git = stCovGitRemoteHeadErr{fakeGit: git, err: errors.New("remote head unreadable")}
		_, err := engine.End(ctx, EndOptions{Name: "remote-unreadable", Apply: true})
		refusal, refused := Refused(err)
		if !refused || !strings.Contains(refusal.Message, "for deletion lease") {
			t.Fatalf("error = %v, want the unreadable remote head reported", err)
		}
	})

	t.Run("local head", func(t *testing.T) {
		t.Parallel()
		engine, git, _, _, stream := startedStream(t, "local-unreadable", "acme/library")
		member := stream.Members[0]
		git.remoteHeads[member.Worktree+" "+member.Branch] = "remote-sha"
		engine.Git = stCovGitLocalHeadErr{fakeGit: git, err: errors.New("local head unreadable")}
		_, err := engine.End(ctx, EndOptions{Name: "local-unreadable", Apply: true})
		refusal, refused := Refused(err)
		if !refused || !strings.Contains(refusal.Message, "read member HEAD") {
			t.Fatalf("error = %v, want the unreadable member HEAD reported", err)
		}
	})
}

func TestEndReportsAFinalStateWriteFailure(t *testing.T) {
	t.Parallel()
	engine, _, _, _, stream := startedStream(t, "write-fails", "acme/library")
	stCovBlockStreamLock(t, engine.Store, stream.Name)
	_, err := engine.End(context.Background(), EndOptions{Name: stream.Name, Apply: true})
	if err == nil {
		t.Fatal("End reported success although the ended state could not be written")
	}
}

// stCovSquashMember is a member whose recorded stream pull request is a merged
// receipt with immutable identities, which is the only state that authorizes
// the squash-absorption proof.
func stCovSquashMember(t *testing.T, engine *Engine, hub *fakeHub) (Member, string) {
	t.Helper()
	worktree := t.TempDir()
	canonical := t.TempDir()
	member := Member{
		Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: canonical,
		Branch: "stream/squash", Base: "main", PullRequest: 7,
	}
	hub.byNumber[7] = PullRequest{
		Number: 7, State: "MERGED", Head: member.Branch, Base: member.Base,
		HeadSHA: "head-sha", MergeSHA: "merge-sha",
	}
	return member, worktree
}

func TestProvedSquashAbsorbedMemberReportsEveryUnreadableInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("pull request read", func(t *testing.T) {
		t.Parallel()
		engine, _, hub, _ := newTestEngine(t)
		member, _ := stCovSquashMember(t, engine, hub)
		engine.GitHub = stCovHubViewErr{fakeHub: hub, err: errors.New("view failed")}
		if _, err := engine.provedSquashAbsorbedMember(ctx, member); err == nil || !strings.Contains(err.Error(), "read recorded stream pull request #7") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("dirty inspection", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, _ := stCovSquashMember(t, engine, hub)
		engine.Git = stCovGitDirtyErr{fakeGit: git, err: errors.New("status failed")}
		if _, err := engine.provedSquashAbsorbedMember(ctx, member); err == nil || !strings.Contains(err.Error(), "inspect member worktree dirtiness") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("local head", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, worktree := stCovSquashMember(t, engine, hub)
		git.currentBranch[worktree] = member.Branch
		engine.Git = stCovGitLocalHeadErr{fakeGit: git, err: errors.New("head unreadable")}
		if _, err := engine.provedSquashAbsorbedMember(ctx, member); err == nil || !strings.Contains(err.Error(), "read member worktree HEAD") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("local stream ref read", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, worktree := stCovSquashMember(t, engine, hub)
		git.currentBranch[worktree] = member.Branch
		engine.Git = stCovGitLocalBranchHeadErr{fakeGit: git, err: errors.New("ref unreadable")}
		if _, err := engine.provedSquashAbsorbedMember(ctx, member); err == nil || !strings.Contains(err.Error(), "read local stream ref") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("local stream ref absent", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, worktree := stCovSquashMember(t, engine, hub)
		git.currentBranch[worktree] = member.Branch
		engine.Git = stCovGitLocalBranchHeadErr{fakeGit: git, found: false}
		if _, err := engine.provedSquashAbsorbedMember(ctx, member); err == nil || !strings.Contains(err.Error(), "is absent from an existing member worktree") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("ancestry", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, worktree := stCovSquashMember(t, engine, hub)
		git.currentBranch[worktree] = member.Branch
		git.localHeads[worktree] = "local-head"
		git.localBranchHeads[worktree+" "+member.Branch] = "local-head"
		engine.Git = stCovGitIsAncestorErr{fakeGit: git, err: errors.New("merge-base failed")}
		if _, err := engine.provedSquashAbsorbedMember(ctx, member); err == nil || !strings.Contains(err.Error(), "compare member HEAD") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("remote head after the receipt", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, worktree := stCovSquashMember(t, engine, hub)
		git.currentBranch[worktree] = member.Branch
		git.localHeads[worktree] = "local-head"
		git.localBranchHeads[worktree+" "+member.Branch] = "local-head"
		git.ancestors[worktree+" local-head head-sha"] = true
		engine.Git = stCovGitRemoteHeadErr{fakeGit: git, err: errors.New("remote unreadable")}
		if _, err := engine.provedSquashAbsorbedMember(ctx, member); err == nil || !strings.Contains(err.Error(), "after merged receipt") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("proven receipt", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, worktree := stCovSquashMember(t, engine, hub)
		git.currentBranch[worktree] = member.Branch
		git.localHeads[worktree] = "local-head"
		git.localBranchHeads[worktree+" "+member.Branch] = "local-head"
		git.ancestors[worktree+" local-head head-sha"] = true
		receipt, err := engine.provedSquashAbsorbedMember(ctx, member)
		if err != nil || receipt == nil {
			t.Fatalf("receipt = %#v, %v; want the proven absorption", receipt, err)
		}
		if receipt.SourceSHA != "local-head" || receipt.CandidateSHA != "head-sha" || receipt.LandingSHA != "merge-sha" || receipt.Target != "main" {
			t.Fatalf("receipt = %#v", receipt)
		}
	})
}

func TestProvedAlreadyRetiredMemberReportsEveryUnreadableInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("worktree inspection fails", func(t *testing.T) {
		t.Parallel()
		engine, _, _, _ := newTestEngine(t)
		blocker := filepath.Join(t.TempDir(), "regular-file")
		if err := os.WriteFile(blocker, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		member := Member{Worktree: filepath.Join(blocker, "wt"), Canonical: "/canon/app", Branch: "stream/x", PullRequest: 7}
		if _, _, err := engine.provedAlreadyRetiredMember(ctx, member); err == nil || !strings.Contains(err.Error(), "inspect member worktree") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("absent without a receipt", func(t *testing.T) {
		t.Parallel()
		engine, _, _, _ := newTestEngine(t)
		member := Member{Worktree: filepath.Join(t.TempDir(), "gone"), Canonical: "/canon/app", Branch: "stream/x"}
		if _, _, err := engine.provedAlreadyRetiredMember(ctx, member); err == nil || !strings.Contains(err.Error(), "absent without an exact recorded stream pull request") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("pull request read", func(t *testing.T) {
		t.Parallel()
		engine, _, hub, _ := newTestEngine(t)
		engine.GitHub = stCovHubViewErr{fakeHub: hub, err: errors.New("view failed")}
		member := Member{Worktree: filepath.Join(t.TempDir(), "gone"), Canonical: "/canon/app", Branch: "stream/x", Base: "main", PullRequest: 7}
		if _, _, err := engine.provedAlreadyRetiredMember(ctx, member); err == nil || !strings.Contains(err.Error(), "read recorded stream pull request #7") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("fetch", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, _ := stCovSquashMember(t, engine, hub)
		member.Worktree = filepath.Join(t.TempDir(), "gone")
		git.fetchErr[member.Canonical] = errors.New("origin unreachable")
		if _, _, err := engine.provedAlreadyRetiredMember(ctx, member); err == nil || !strings.Contains(err.Error(), "re-read origin before verifying retired member") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("remote head", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, _ := stCovSquashMember(t, engine, hub)
		member.Worktree = filepath.Join(t.TempDir(), "gone")
		engine.Git = stCovGitRemoteHeadErr{fakeGit: git, err: errors.New("remote unreadable")}
		if _, _, err := engine.provedAlreadyRetiredMember(ctx, member); err == nil || !strings.Contains(err.Error(), "after merged receipt") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("local stream ref", func(t *testing.T) {
		t.Parallel()
		engine, git, hub, _ := newTestEngine(t)
		member, _ := stCovSquashMember(t, engine, hub)
		member.Worktree = filepath.Join(t.TempDir(), "gone")
		engine.Git = stCovGitLocalBranchHeadErr{fakeGit: git, err: errors.New("ref unreadable")}
		if _, _, err := engine.provedAlreadyRetiredMember(ctx, member); err == nil || !strings.Contains(err.Error(), "after merged receipt") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestEndReportsAnUnreadableAgentPullRequestList(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "pr-list-error", "acme/library")
	member := stream.Members[0]
	hub.targetingErr[member.Worktree+" "+member.Branch] = errors.New("gh list failed")

	result, err := engine.End(context.Background(), EndOptions{Name: "pr-list-error"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AgentPullRequests) != 1 || result.AgentPullRequests[0].Action != "unknown" {
		t.Fatalf("agent pull requests = %#v, want the unreadable list reported as unknown", result.AgentPullRequests)
	}
	if !strings.Contains(result.AgentPullRequests[0].Detail, "could not enumerate") {
		t.Fatalf("detail = %q", result.AgentPullRequests[0].Detail)
	}
}

func TestEndPlansRetargetWithoutApplying(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "plan-retarget", "acme/library")
	member := stream.Members[0]
	hub.targeting[member.Worktree+" "+member.Branch] = []PullRequest{
		{Number: 21, URL: "https://example.test/pull/21", Head: "agent/a", Base: member.Branch},
	}
	result, err := engine.End(context.Background(), EndOptions{Name: "plan-retarget", Retarget: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AgentPullRequests) != 1 || result.AgentPullRequests[0].Action != "would-retarget" {
		t.Fatalf("agent pull requests = %#v, want a dry-run retarget", result.AgentPullRequests)
	}
	if result.AgentPullRequests[0].Detail != "onto main" {
		t.Fatalf("detail = %q", result.AgentPullRequests[0].Detail)
	}
	if len(hub.retargeted) != 0 {
		t.Fatalf("a dry run retargeted %v", hub.retargeted)
	}
}

func TestEndReportsFailedRetargetAndClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("retarget failure", func(t *testing.T) {
		t.Parallel()
		engine, _, hub, _, stream := startedStream(t, "retarget-fails", "acme/library")
		member := stream.Members[0]
		hub.targeting[member.Worktree+" "+member.Branch] = []PullRequest{
			{Number: 31, URL: "https://example.test/pull/31", Head: "agent/a", Base: member.Branch},
		}
		engine.GitHub = stCovHubRetargetErr{fakeHub: hub, err: errors.New("retarget refused")}
		result, err := engine.End(ctx, EndOptions{Name: "retarget-fails", Apply: true, Retarget: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.AgentPullRequests[0].Action != "failed" || !strings.Contains(result.AgentPullRequests[0].Detail, "retarget refused") {
			t.Fatalf("outcome = %#v", result.AgentPullRequests[0])
		}
	})

	t.Run("close failure", func(t *testing.T) {
		t.Parallel()
		engine, _, hub, _, stream := startedStream(t, "close-fails", "acme/library")
		member := stream.Members[0]
		hub.targeting[member.Worktree+" "+member.Branch] = []PullRequest{
			{Number: 32, URL: "https://example.test/pull/32", Head: "agent/a", Base: member.Branch},
		}
		hub.closeErr[32] = errors.New("close refused")
		result, err := engine.End(ctx, EndOptions{Name: "close-fails", Apply: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.AgentPullRequests[0].Action != "failed" || !strings.Contains(result.AgentPullRequests[0].Detail, "close refused") {
			t.Fatalf("outcome = %#v", result.AgentPullRequests[0])
		}
	})
}

func TestEndReportsFailedDraftPullRequestHandling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("close failure", func(t *testing.T) {
		t.Parallel()
		engine, _, hub, _ := newTestEngine(t)
		worktree := t.TempDir()
		if _, err := engine.Store.Create(Stream{
			Name: "draft-close", Phase: PhaseOpen,
			Members: []Member{{
				Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
				Branch: "stream/draft-close", Base: "main", PullRequest: 9,
			}},
		}); err != nil {
			t.Fatal(err)
		}
		hub.byNumber[9] = PullRequest{Number: 9, State: "OPEN", Head: "stream/draft-close", Base: "main"}
		hub.closeErr[9] = errors.New("close refused")

		result, err := engine.End(ctx, EndOptions{Name: "draft-close", Apply: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.Members[0].DraftAction != "failed" || !strings.Contains(result.Members[0].Detail, "close refused") {
			t.Fatalf("member = %#v", result.Members[0])
		}
	})

	t.Run("view failure", func(t *testing.T) {
		t.Parallel()
		engine, _, hub, _ := newTestEngine(t)
		worktree := t.TempDir()
		if _, err := engine.Store.Create(Stream{
			Name: "draft-view", Phase: PhaseOpen,
			Members: []Member{{
				Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
				Branch: "stream/draft-view", Base: "main", PullRequest: 9,
			}},
		}); err != nil {
			t.Fatal(err)
		}
		hub.byNumber[9] = PullRequest{Number: 9, State: "OPEN", Head: "stream/draft-view", Base: "main"}
		engine.GitHub = stCovHubViewErr{fakeHub: hub, err: errors.New("view failed")}

		result, err := engine.End(ctx, EndOptions{Name: "draft-view", Apply: true, ForceUnabsorbed: true, Reason: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Members[0].DraftAction != "failed" || !strings.Contains(result.Members[0].Detail, "view failed") {
			t.Fatalf("member = %#v", result.Members[0])
		}
	})
}

// A proof failure during retirement leaves the lease held and is reported
// rather than silently removing the member.
func TestEndReportsAProofFailureDuringRetirement(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Store.Create(Stream{
		Name: "proof-fails", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "acme/app", Role: RoleLibrary,
			Worktree: filepath.Join(t.TempDir(), "gone"),
			Branch:   "stream/proof-fails", Base: "main",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.End(context.Background(), EndOptions{
		Name: "proof-fails", Apply: true, ForceUnabsorbed: true, Reason: "retiring a lost checkout",
	})
	if err == nil {
		t.Fatal("End reported success although a member could not be proofed for retirement")
	}
	if result.Members[0].LeaseReleased {
		t.Fatalf("member = %#v, want the lease held when retirement was not proven", result.Members[0])
	}
	if !strings.Contains(result.Members[0].Detail, "absent without an exact recorded stream pull request") {
		t.Fatalf("detail = %q", result.Members[0].Detail)
	}
}

func TestEndReportsAFailedRetiredBranchDeletion(t *testing.T) {
	t.Parallel()
	engine, git, hub, _ := newTestEngine(t)
	canonical := t.TempDir()
	member := Member{
		Repository: "acme/app", Role: RoleLibrary,
		Worktree: filepath.Join(t.TempDir(), "gone"), Canonical: canonical,
		Branch: "stream/retired", Base: "main", PullRequest: 7,
	}
	if _, err := engine.Store.Create(Stream{Name: "retired", Phase: PhaseOpen, Members: []Member{member}}); err != nil {
		t.Fatal(err)
	}
	hub.byNumber[7] = PullRequest{
		Number: 7, State: "MERGED", Head: member.Branch, Base: member.Base,
		HeadSHA: "head-sha", MergeSHA: "merge-sha",
	}
	git.remoteHeads[canonical+" "+member.Branch] = "head-sha"
	git.deleteErr[canonical+" "+member.Branch] = errors.New("delete refused")

	result, err := engine.End(context.Background(), EndOptions{Name: "retired", Apply: true})
	if err == nil {
		t.Fatal("End reported success although the remote stream branch could not be deleted")
	}
	if !strings.Contains(result.Members[0].Detail, "delete refused") {
		t.Fatalf("detail = %q", result.Members[0].Detail)
	}
	if result.Members[0].RemoteBranchDeleted {
		t.Fatal("a failed deletion was reported as deleted")
	}
}

func TestEndReportsAFailedWorktreeRemoval(t *testing.T) {
	t.Parallel()
	engine, _, _, worktrees, stream := startedStream(t, "remove-fails", "acme/library")
	member := stream.Members[0]
	worktrees.removeErr[member.Repository] = errors.New("cleanup refused")

	result, err := engine.End(context.Background(), EndOptions{Name: "remove-fails", Apply: true})
	if err == nil {
		t.Fatal("End reported success although the worktree could not be removed")
	}
	if !strings.Contains(result.Members[0].Detail, "cleanup refused") {
		t.Fatalf("detail = %q", result.Members[0].Detail)
	}
}
