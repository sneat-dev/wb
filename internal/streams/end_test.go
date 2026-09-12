package streams

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func startedStream(t *testing.T, name string, repositories ...string) (*Engine, *fakeGit, *fakeHub, *fakeWorktrees, Stream) {
	t.Helper()
	engine, git, hub, worktrees := newTestEngine(t)
	for _, repository := range repositories {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	result, err := engine.Start(context.Background(), StartOptions{Name: name, Repositories: repositories}, nil)
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return engine, git, hub, worktrees, result.Stream
}

// REQ: stream-end-restores-published-state — end refuses while any link is
// live, and the refusal names the exact undo command per link.
func TestEndRefusesWhileALinkIsLive(t *testing.T) {
	engine, _, _, worktrees, stream := startedStream(t, "linked", "acme/library", "acme/app")
	library, _ := stream.Member("acme/library")
	if _, err := engine.Store.Update("linked", func(current *Stream) error {
		for index := range current.Members {
			if current.Members[index].Repository != "acme/app" {
				continue
			}
			current.Members[index].Links = []Link{{
				Library:           library.Worktree,
				LibraryRepository: "acme/library",
				Mechanism:         MechanismGoWork,
				Identity:          "github.com/acme/library/backend",
				PreviousVersion:   "v0.5.0",
				CreatedAt:         time.Now().UTC(),
			}}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := engine.End(context.Background(), EndOptions{Name: "linked", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalLiveLink {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalLiveLink)
	}
	if !strings.Contains(strings.Join(refusal.Sanctioned, " "), "wb deps propagate local") {
		t.Errorf("refusal does not name the undo command: %v", refusal.Sanctioned)
	}
	if len(worktrees.removed) != 0 {
		t.Errorf("a refused end removed %d worktree(s)", len(worktrees.removed))
	}
}

// REQ: stream-end-proves-absorption-and-removes-its-own-scaffolding — a member
// whose branch carries work the base has not absorbed refuses, named at the
// content level rather than by listing paths.
func TestEndRefusesUnabsorbedWork(t *testing.T) {
	engine, git, _, worktrees, stream := startedStream(t, "unabsorbed", "acme/library")
	member := stream.Members[0]
	git.notIn[member.Worktree+" stream/unabsorbed origin/main"] = []Commit{
		{SHA: "35c480ed6e1e718a910d8aa617c4da94dd47557a", Subject: "feat: not landed anywhere", PatchID: "aa11"},
	}
	_, err := engine.End(context.Background(), EndOptions{Name: "unabsorbed", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUnabsorbedWork {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalUnabsorbedWork)
	}
	if !strings.Contains(refusal.Message, "feat: not landed anywhere") {
		t.Errorf("refusal does not name the unabsorbed work: %s", refusal.Message)
	}
	if len(worktrees.removed) != 0 {
		t.Errorf("a refused end removed %d worktree(s)", len(worktrees.removed))
	}
}

// REQ: stream-end-proves-absorption-and-removes-its-own-scaffolding — every
// still-open pull request targeting the stream branch is closed before the
// branch could be deleted, so GitHub never silently retargets one at main.
func TestEndClosesStillOpenAgentPullRequestsBeforeRemovingTheBranch(t *testing.T) {
	engine, _, hub, worktrees, stream := startedStream(t, "agents", "acme/library")
	member := stream.Members[0]
	hub.targeting[member.Worktree+" stream/agents"] = []PullRequest{
		{Number: 11, URL: "https://example.test/pull/11", Head: "agent/a", Base: "stream/agents"},
		{Number: 12, URL: "https://example.test/pull/12", Head: "agent/b", Base: "stream/agents"},
	}
	result, err := engine.End(context.Background(), EndOptions{Name: "agents", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(result.AgentPullRequests) != 2 {
		t.Fatalf("agent pull requests = %#v, want both reported", result.AgentPullRequests)
	}
	for _, outcome := range result.AgentPullRequests {
		if outcome.Action != "closed" {
			t.Errorf("pull request %d action = %q, want closed", outcome.Number, outcome.Action)
		}
	}
	closed := map[int]bool{}
	for _, number := range hub.closed {
		closed[number] = true
	}
	if !closed[11] || !closed[12] {
		t.Errorf("closed pull requests = %v, want both agent pull requests", hub.closed)
	}
	if len(worktrees.removed) != 1 {
		t.Errorf("removed worktrees = %v, want the one member's checkout", worktrees.removed)
	}
	if _, err := os.Stat(member.Worktree); !os.IsNotExist(err) {
		t.Errorf("member worktree still exists after end: %v", err)
	}
	ended, err := engine.Store.Load("agents")
	if err != nil {
		t.Fatal(err)
	}
	if ended.Open() {
		t.Error("stream is still open after end")
	}
	if ended.Members[0].Lease.Holder() != "" {
		t.Errorf("lease was not released: %#v", ended.Members[0].Lease)
	}
}

func TestEndCanRetargetAgentPullRequestsInstead(t *testing.T) {
	engine, _, hub, _, stream := startedStream(t, "retarget", "acme/library")
	member := stream.Members[0]
	hub.targeting[member.Worktree+" stream/retarget"] = []PullRequest{
		{Number: 21, URL: "https://example.test/pull/21", Head: "agent/a", Base: "stream/retarget"},
	}
	result, err := engine.End(context.Background(), EndOptions{Name: "retarget", Apply: true, Retarget: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if hub.retargeted[21] != "main" {
		t.Errorf("retargeted = %v, want pull request 21 onto main", hub.retargeted)
	}
	if result.AgentPullRequests[0].Action != "retargeted" {
		t.Errorf("action = %q, want retargeted", result.AgentPullRequests[0].Action)
	}
}

// A successful wb pr land can retire a member before its enclosing stream is
// ended. The missing checkout is recoverable only from that member's exact
// merged PR receipt, never from absence alone.
func TestEndRetiresMemberAlreadyRemovedByMergedStreamPullRequest(t *testing.T) {
	engine, git, hub, worktrees, stream := startedStream(t, "already-landed", "acme/library")
	member := stream.Members[0]
	if err := os.RemoveAll(member.Worktree); err != nil {
		t.Fatal(err)
	}
	git.fetchErr[member.Worktree] = os.ErrNotExist // proves the old path would refuse.
	hub.byNumber[member.PullRequest] = PullRequest{
		Number: member.PullRequest, State: "MERGED", Head: member.Branch, Base: member.Base,
		HeadSHA: "0123456789012345678901234567890123456789", MergeSHA: "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
	}
	result, err := engine.End(context.Background(), EndOptions{Name: "already-landed", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if !result.Members[0].WorktreeRemoved || !result.Members[0].LeaseReleased {
		t.Fatalf("member result = %#v, want proved prior retirement", result.Members[0])
	}
	if len(worktrees.removed) != 0 {
		t.Fatalf("removed worktrees = %v, want no second removal", worktrees.removed)
	}
	ended, err := engine.Store.Load("already-landed")
	if err != nil || ended.Open() {
		t.Fatalf("stream = %#v, err=%v; want ended", ended, err)
	}
}

func TestEndRefusesRemovedMemberWithoutExactMergedStreamPullRequest(t *testing.T) {
	engine, _, hub, worktrees, stream := startedStream(t, "missing-without-receipt", "acme/library")
	member := stream.Members[0]
	if err := os.RemoveAll(member.Worktree); err != nil {
		t.Fatal(err)
	}
	hub.byNumber[member.PullRequest] = PullRequest{Number: member.PullRequest, State: "MERGED", Head: member.Branch, Base: member.Base}
	_, err := engine.End(context.Background(), EndOptions{Name: "missing-without-receipt", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUnabsorbedWork {
		t.Fatalf("error = %v, want unabsorbed-work refusal", err)
	}
	if len(worktrees.removed) != 0 {
		t.Fatalf("removed worktrees = %v, want none", worktrees.removed)
	}
}

func TestEndRefusesRemovedMemberWhoseRemoteBranchAdvancedAfterMerge(t *testing.T) {
	engine, git, hub, worktrees, stream := startedStream(t, "missing-advanced", "acme/library")
	member := stream.Members[0]
	if err := os.RemoveAll(member.Worktree); err != nil {
		t.Fatal(err)
	}
	mergedHead := "0123456789012345678901234567890123456789"
	hub.byNumber[member.PullRequest] = PullRequest{Number: member.PullRequest, State: "MERGED", Head: member.Branch, Base: member.Base, HeadSHA: mergedHead, MergeSHA: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	git.remoteHeads[member.Canonical+" "+member.Branch] = "fedcbafedcbafedcbafedcbafedcbafedcbafedc"
	_, err := engine.End(context.Background(), EndOptions{Name: "missing-advanced", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUnabsorbedWork || !strings.Contains(refusal.Message, "advanced") {
		t.Fatalf("error = %v, want advanced-branch refusal", err)
	}
	if len(worktrees.removed) != 0 {
		t.Fatalf("removed worktrees = %v, want none", worktrees.removed)
	}
}

func TestEndRefusesRemovedMemberWhoseLocalStreamBranchAdvancedAfterMerge(t *testing.T) {
	engine, git, hub, worktrees, stream := startedStream(t, "missing-local-advanced", "acme/library")
	member := stream.Members[0]
	if err := os.RemoveAll(member.Worktree); err != nil {
		t.Fatal(err)
	}
	mergedHead := "0123456789012345678901234567890123456789"
	hub.byNumber[member.PullRequest] = PullRequest{Number: member.PullRequest, State: "MERGED", Head: member.Branch, Base: member.Base, HeadSHA: mergedHead, MergeSHA: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	git.localBranchHeads[member.Canonical+" "+member.Branch] = "fedcbafedcbafedcbafedcbafedcbafedcbafedc"
	_, err := engine.End(context.Background(), EndOptions{Name: "missing-local-advanced", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUnabsorbedWork || !strings.Contains(refusal.Message, "local") {
		t.Fatalf("error = %v, want local-advanced refusal", err)
	}
	if len(worktrees.removed) != 0 {
		t.Fatalf("removed worktrees = %v, want none", worktrees.removed)
	}
}

// A missing recorded path does not establish that wb pr land removed the
// checkout: git worktree move can leave the branch checked out elsewhere with
// dirty or untracked work. The recovery receipt is valid only after pr land
// has removed the local stream branch entirely.
func TestEndRefusesRemovedMemberWhoseLocalStreamBranchStillExistsAtMergedHead(t *testing.T) {
	engine, git, hub, worktrees, stream := startedStream(t, "missing-local-present", "acme/library")
	member := stream.Members[0]
	if err := os.RemoveAll(member.Worktree); err != nil {
		t.Fatal(err)
	}
	mergedHead := "0123456789012345678901234567890123456789"
	hub.byNumber[member.PullRequest] = PullRequest{Number: member.PullRequest, State: "MERGED", Head: member.Branch, Base: member.Base, HeadSHA: mergedHead, MergeSHA: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	// Models a relocated checkout still on this exact branch and commit. Its
	// filesystem state is outside the recorded path and therefore must remain
	// protected rather than inferred clean from the matching SHA.
	git.localBranchHeads[member.Canonical+" "+member.Branch] = mergedHead
	_, err := engine.End(context.Background(), EndOptions{Name: "missing-local-present", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUnabsorbedWork || !strings.Contains(refusal.Message, "local") {
		t.Fatalf("error = %v, want local-branch-present refusal", err)
	}
	if len(worktrees.removed) != 0 {
		t.Fatalf("removed worktrees = %v, want none", worktrees.removed)
	}
}

func TestEndRetiresCleanExistingMemberWhoseExactStreamPRWasSquashMerged(t *testing.T) {
	engine, git, hub, _, stream := startedStream(t, "squash-merged", "acme/library")
	member := stream.Members[0]
	localHead := "41cd41cd41cd41cd41cd41cd41cd41cd41cd41cd"
	mergedPRHead := "8def8def8def8def8def8def8def8def8def8def"
	git.localHeads[member.Worktree] = localHead
	git.ancestors[member.Worktree+" "+localHead+" "+mergedPRHead] = true
	delete(git.remoteHeads, member.Worktree+" "+member.Branch) // squash PR land deleted it
	// This is the real recovery shape: a squash merge puts a new commit on
	// main, so git cherry still sees the four original stream commits even
	// though GitHub has the immutable merged-PR receipt.
	git.notIn[member.Worktree+" "+member.Branch+" origin/"+member.Base] = []Commit{
		{SHA: "1", Subject: "stream commit 1"}, {SHA: "2", Subject: "stream commit 2"},
		{SHA: "3", Subject: "stream commit 3"}, {SHA: "4", Subject: "stream commit 4"},
	}
	hub.byNumber[member.PullRequest] = exactSquashReceipt(member, mergedPRHead)

	result, err := engine.End(context.Background(), EndOptions{Name: "squash-merged", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(result.Members) != 1 || !result.Members[0].WorktreeRemoved || !result.Members[0].LeaseReleased {
		t.Fatalf("member result = %#v, want a retired clean squash-merged member", result.Members)
	}
}

func TestEndRefusesUnsafeExistingSquashMergedMemberRecovery(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(member Member, git *fakeGit, hub *fakeHub, localHead, mergedPRHead string)
		want      string
	}{
		{
			name: "dirty", want: "dirty",
			configure: func(member Member, git *fakeGit, _ *fakeHub, _, _ string) {
				git.dirty[member.Worktree] = []string{"untracked.txt"}
			},
		},
		{
			name: "divergent local head", want: "not an ancestor",
			configure: func(member Member, git *fakeGit, _ *fakeHub, localHead, mergedPRHead string) {
				git.ancestors[member.Worktree+" "+localHead+" "+mergedPRHead] = false
			},
		},
		{
			name: "missing immutable identity", want: "immutable",
			configure: func(member Member, _ *fakeGit, hub *fakeHub, _, _ string) {
				pullRequest := hub.byNumber[member.PullRequest]
				pullRequest.MergeSHA = ""
				hub.byNumber[member.PullRequest] = pullRequest
			},
		},
		{
			name: "remote stream branch advanced", want: "origin/",
			configure: func(member Member, git *fakeGit, _ *fakeHub, _, _ string) {
				git.remoteHeads[member.Worktree+" "+member.Branch] = "advancedadvancedadvancedadvancedadvanced"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, git, hub, worktrees, stream := startedStream(t, "unsafe-squash-"+strings.ReplaceAll(test.name, " ", "-"), "acme/library")
			member := stream.Members[0]
			localHead := "41cd41cd41cd41cd41cd41cd41cd41cd41cd41cd"
			mergedPRHead := "8def8def8def8def8def8def8def8def8def8def"
			git.localHeads[member.Worktree] = localHead
			git.ancestors[member.Worktree+" "+localHead+" "+mergedPRHead] = true
			// Empty on purpose: each safety check must refuse rather than fall
			// back to ordinary patch comparison and erase this checkout.
			git.notIn[member.Worktree+" "+member.Branch+" origin/"+member.Base] = nil
			hub.byNumber[member.PullRequest] = exactSquashReceipt(member, mergedPRHead)
			test.configure(member, git, hub, localHead, mergedPRHead)

			_, err := engine.End(context.Background(), EndOptions{Name: "unsafe-squash-" + strings.ReplaceAll(test.name, " ", "-"), Apply: true})
			refusal, refused := Refused(err)
			if !refused || refusal.Code != RefusalUnabsorbedWork || !strings.Contains(refusal.Message, test.want) {
				t.Fatalf("error = %v, want refusal containing %q", err, test.want)
			}
			if len(worktrees.removed) != 0 {
				t.Fatalf("removed worktrees = %v, want none", worktrees.removed)
			}
		})
	}
}

func exactSquashReceipt(member Member, head string) PullRequest {
	return PullRequest{
		Number: member.PullRequest, State: "MERGED", Head: member.Branch, Base: member.Base,
		HeadSHA: head, MergeSHA: "cafebabecafebabecafebabecafebabecafebabe",
	}
}

func TestEndUsesCanonicalCheckoutForAlreadyRemovedMemberGitHubCalls(t *testing.T) {
	engine, _, hub, _, stream := startedStream(t, "missing-canonical-cwd", "acme/library")
	member := stream.Members[0]
	if err := os.RemoveAll(member.Worktree); err != nil {
		t.Fatal(err)
	}
	hub.requireExistingDir = true
	hub.byNumber[member.PullRequest] = PullRequest{Number: member.PullRequest, State: "MERGED", Head: member.Branch, Base: member.Base, HeadSHA: "0123456789012345678901234567890123456789", MergeSHA: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	hub.targeting[member.Canonical+" "+member.Branch] = []PullRequest{{Number: 99, Head: "agent/a", Base: member.Branch}}
	result, err := engine.End(context.Background(), EndOptions{Name: "missing-canonical-cwd", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(result.AgentPullRequests) != 1 || result.AgentPullRequests[0].Action != "closed" {
		t.Fatalf("agent pull requests = %#v, want canonical-cwd close", result.AgentPullRequests)
	}
}

// Without --apply the verb reports exactly what it would do and changes
// nothing, so an operator sees which pull requests would be closed first.
func TestEndWithoutApplyChangesNothing(t *testing.T) {
	engine, _, hub, worktrees, stream := startedStream(t, "dry", "acme/library")
	member := stream.Members[0]
	hub.targeting[member.Worktree+" stream/dry"] = []PullRequest{
		{Number: 31, URL: "https://example.test/pull/31", Head: "agent/a", Base: "stream/dry"},
	}
	result, err := engine.End(context.Background(), EndOptions{Name: "dry"})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if result.Applied {
		t.Error("result claims it applied")
	}
	if result.AgentPullRequests[0].Action != "would-close" {
		t.Errorf("action = %q, want would-close", result.AgentPullRequests[0].Action)
	}
	if len(hub.closed) != 0 || len(worktrees.removed) != 0 {
		t.Errorf("a dry end mutated: closed=%v removed=%v", hub.closed, worktrees.removed)
	}
	current, err := engine.Store.Load("dry")
	if err != nil {
		t.Fatal(err)
	}
	if !current.Open() {
		t.Error("a dry end closed the stream")
	}
}

// REQ: stream-end-restores-published-state — ending publishes, bumps and
// merges nothing: the member's own draft pull request is closed, never merged.
func TestEndClosesTheDraftPullRequestAndMergesNothing(t *testing.T) {
	engine, _, hub, _, stream := startedStream(t, "no-merge", "acme/library")
	member := stream.Members[0]
	result, err := engine.End(context.Background(), EndOptions{Name: "no-merge", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if result.Members[0].DraftAction != "closed" {
		t.Errorf("draft action = %q, want closed", result.Members[0].DraftAction)
	}
	found := false
	for _, number := range hub.closed {
		if number == member.PullRequest {
			found = true
		}
	}
	if !found {
		t.Errorf("closed = %v, want the member's own draft pull request %d", hub.closed, member.PullRequest)
	}
}
