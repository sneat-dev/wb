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
	git.notIn[member.Worktree+" stream/unabsorbed origin/main"] = []string{"feat: not landed anywhere"}
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
