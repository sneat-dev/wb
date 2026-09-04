package streams

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AC: stream-groups-worktrees-under-one-name — one worktree per repository
// through the existing creation path, each on stream/<name> with a draft pull
// request open to main, and the whole set recorded in WB-owned state outside
// every repository.
func TestStartGroupsWorktreesUnderOneNameWithDraftPullRequests(t *testing.T) {
	engine, _, hub, worktrees := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/app", "acme/site"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	result, err := engine.Start(context.Background(), StartOptions{
		Name:         "checkout-rewrite",
		Repositories: []string{"acme/library", "acme/app", "acme/site"},
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(result.Stream.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(result.Stream.Members))
	}
	library, ok := result.Stream.Library()
	if !ok || library.Repository != "acme/library" {
		t.Fatalf("library = %#v, want acme/library", library)
	}
	for _, member := range result.Stream.Members {
		if member.Branch != "stream/checkout-rewrite" {
			t.Errorf("%s branch = %q, want stream/checkout-rewrite", member.Repository, member.Branch)
		}
		if member.PullRequest == 0 {
			t.Errorf("%s has no draft pull request: %s", member.Repository, member.PullRequestError)
		}
		if member.Base != "main" {
			t.Errorf("%s base = %q, want main", member.Repository, member.Base)
		}
		if member.Lease.Holder() != "octocat/workstation" || member.Lease.Session != "wbs-1" {
			t.Errorf("%s lease = %#v, want the live session on this machine", member.Repository, member.Lease)
		}
	}
	if len(worktrees.created) != 3 {
		t.Fatalf("the existing worktree creation path published %d checkouts, want 3", len(worktrees.created))
	}
	for _, pullRequest := range hub.created {
		if !pullRequest.Draft {
			t.Errorf("pull request %d is not a draft; only landing marks a stream pull request ready", pullRequest.Number)
		}
		if pullRequest.Base != "main" {
			t.Errorf("pull request %d targets %q, want main", pullRequest.Number, pullRequest.Base)
		}
	}

	// The state is WB-owned and outside every repository: no member worktree
	// gained a file, and the record survives a fresh reader.
	for _, member := range result.Stream.Members {
		entries, err := os.ReadDir(member.Worktree)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("%s worktree gained %d file(s); stream membership must live outside the repository", member.Repository, len(entries))
		}
	}
	reread := OpenAt(engine.Store.Root)
	restored, err := reread.Load("checkout-rewrite")
	if err != nil {
		t.Fatalf("reload after a session restart: %v", err)
	}
	if len(restored.Members) != 3 {
		t.Fatalf("restored members = %d, want 3", len(restored.Members))
	}
}

func TestStartDoesNotReserveAStreamWhenWorktreePlanningFails(t *testing.T) {
	engine, _, _, worktrees := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	worktrees.planErr = errors.New("worktrees config root: must be an absolute path")

	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "invalid-placement", Repositories: []string{"acme/library"},
	}, nil); err == nil || !strings.Contains(err.Error(), "plan worktree") {
		t.Fatalf("start error = %v, want planning refusal", err)
	}
	if _, err := engine.Store.Load("invalid-placement"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid placement reserved a stream: %v", err)
	}
	if len(worktrees.created) != 0 {
		t.Fatalf("invalid placement created %d worktrees", len(worktrees.created))
	}
}

// AC: a-second-stream-and-a-stale-lease-are-both-refused (the start half): a
// repository that already carries an open stream refuses, names the holding
// stream, and names the sanctioned commands.
func TestStartRefusesARepositoryThatAlreadyCarriesAnOpenStream(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/app"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "first", Repositories: []string{"acme/library", "acme/app"},
	}, nil); err != nil {
		t.Fatalf("first start: %v", err)
	}
	_, err := engine.Start(context.Background(), StartOptions{
		Name: "second", Repositories: []string{"acme/app"},
	}, nil)
	refusal, refused := Refused(err)
	if !refused {
		t.Fatalf("second start error = %v, want a refusal", err)
	}
	if refusal.Code != RefusalRepositoryInStream {
		t.Errorf("refusal code = %q, want %q", refusal.Code, RefusalRepositoryInStream)
	}
	if !strings.Contains(refusal.Message, `"first"`) {
		t.Errorf("refusal does not name the holding stream: %s", refusal.Message)
	}
	joined := strings.Join(refusal.Sanctioned, " ")
	if !strings.Contains(joined, "wb stream join first acme/app") {
		t.Errorf("refusal does not name `wb stream join`: %v", refusal.Sanctioned)
	}
	if !strings.Contains(joined, "wb stream end first") {
		t.Errorf("refusal does not name the wait-it-out command: %v", refusal.Sanctioned)
	}
}

func TestStartRefusesAStreamNameThatAlreadyExists(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "same", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Start(context.Background(), StartOptions{
		Name: "same", Repositories: []string{"acme/library"},
	}, nil)
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalStreamExists {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalStreamExists)
	}
}

// REQ: stream-start-proves-the-fleet-is-ready — a member whose hooks are
// broken refuses the start before any worktree is created, and the refusal
// names the command that satisfies it.
func TestStartRefusesBeforeCreatingAnythingWhenHooksAreUnhealthy(t *testing.T) {
	engine, _, _, worktrees := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	engine.HooksCheck = func(path string) ([]string, error) {
		return []string{"pre-push shim is missing"}, nil
	}
	_, err := engine.Start(context.Background(), StartOptions{
		Name: "unready", Repositories: []string{"acme/library"},
	}, nil)
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalPreflight {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalPreflight)
	}
	if !strings.Contains(refusal.Error(), "wb hooks repair") {
		t.Errorf("refusal does not name the sanctioned command: %s", refusal.Error())
	}
	if len(worktrees.created) != 0 {
		t.Errorf("start created %d worktree(s) before its fences passed", len(worktrees.created))
	}
	if _, err := engine.Store.Load("unready"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused start left stream state behind: %v", err)
	}
}

// REQ: push-hook-defers-to-ci-on-stream-branches — start reports, per member,
// a stream-PR workflow that will not cancel a superseded run.
func TestStartReportsAMemberWhoseStreamWorkflowDoesNotCancelInProgress(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": "name: CI\non:\n  pull_request:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo build\n",
	})
	result, err := engine.Start(context.Background(), StartOptions{
		Name: "reported", Repositories: []string{"acme/library"},
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finding, ok := findingFor(result.Reported, "acme/library", CheckStreamConcurrency)
	if !ok {
		t.Fatalf("reported findings = %#v, want a %s finding", result.Reported, CheckStreamConcurrency)
	}
	if !strings.Contains(finding.Detail, "concurrency group") {
		t.Errorf("finding does not say what is missing: %s", finding.Detail)
	}
	if len(result.Stream.Members) != 1 {
		t.Errorf("a reported finding must not refuse the start; members = %d", len(result.Stream.Members))
	}
}

// REQ: stream-start-proves-the-fleet-is-ready — a red default branch is
// reported per member, never silently passed.
func TestStartReportsARedDefaultBranch(t *testing.T) {
	engine, _, hub, _ := newTestEngine(t)
	path := writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	hub.mainStatus[path] = "failure"
	result, err := engine.Start(context.Background(), StartOptions{
		Name: "red-base", Repositories: []string{"acme/library"},
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finding, ok := findingFor(result.Reported, "acme/library", CheckRedMain)
	if !ok || finding.Status != PreflightFail {
		t.Fatalf("reported = %#v, want a failing %s finding", result.Reported, CheckRedMain)
	}
}

// REQ: stream-start-proves-the-fleet-is-ready — two members publishing the
// same npm package name is an ambiguous provider identity, and it refuses.
func TestStartRefusesTwoMembersDeclaringTheSamePackageName(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/fork"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
			"libs/core/package.json":   `{"name":"@acme/core","version":"1.0.0"}`,
		})
	}
	_, err := engine.Start(context.Background(), StartOptions{
		Name: "ambiguous", Repositories: []string{"acme/library", "acme/fork"},
	}, nil)
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalPreflight {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalPreflight)
	}
	if !strings.Contains(refusal.Message, "@acme/core") {
		t.Errorf("refusal does not name the ambiguous package: %s", refusal.Message)
	}
}

// REQ: stream-membership-is-proposed-from-the-transitive-graph — a transitive
// consumer left out of the stream is named, never silently dropped.
func TestStartNamesTransitiveConsumersLeftOut(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/app"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	result, err := engine.Start(context.Background(), StartOptions{
		Name: "partial", Repositories: []string{"acme/library", "acme/app"},
	}, []string{"acme/library", "acme/app", "acme/reports"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := sortedStrings(result.TransitiveOmissions); len(got) != 1 || got[0] != "acme/reports" {
		t.Fatalf("transitive omissions = %v, want [acme/reports]", got)
	}
}

// REQ: stream-branch-with-draft-pr — a member whose pull request could not be
// opened is recorded with its reason instead of stranding the whole start.
func TestStartRecordsAMemberWhosePullRequestCouldNotBeOpened(t *testing.T) {
	engine, _, hub, worktrees := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/app"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	failing := filepath.Join(worktrees.root, "worktrees", "partial-pr", "acme", "app")
	hub.createErr[failing] = errors.New("gh: no default remote")
	result, err := engine.Start(context.Background(), StartOptions{
		Name: "partial-pr", Repositories: []string{"acme/library", "acme/app"},
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	member, ok := result.Stream.Member("acme/app")
	if !ok {
		t.Fatal("acme/app was not recorded")
	}
	if member.PullRequest != 0 || !strings.Contains(member.PullRequestError, "no default remote") {
		t.Errorf("member = %#v, want the pull-request failure recorded", member)
	}
	if library, ok := result.Stream.Member("acme/library"); !ok || library.PullRequest == 0 {
		t.Errorf("one member's pull-request failure stranded another: %#v", library)
	}
}

func TestJoinAddsAMemberToAnExistingStream(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/app"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "joinable", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Join(context.Background(), JoinOptions{Name: "joinable", Repository: "acme/app"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	member, ok := result.Stream.Member("acme/app")
	if !ok {
		t.Fatal("join did not record the member")
	}
	if member.Role != RoleConsumer {
		t.Errorf("role = %q, want consumer", member.Role)
	}
	if member.Branch != "stream/joinable" || member.PullRequest == 0 {
		t.Errorf("join must create the branch and draft pull request exactly as start does: %#v", member)
	}
}

func TestJoinIsIdempotentForAMemberAlreadyInTheStream(t *testing.T) {
	engine, _, _, worktrees := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "twice", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	before := len(worktrees.created)
	if _, err := engine.Join(context.Background(), JoinOptions{Name: "twice", Repository: "acme/library"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(worktrees.created) != before {
		t.Errorf("re-joining an existing member created another worktree")
	}
}

func TestJoinRefusesASecondLibrary(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/other"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "one-library", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Join(context.Background(), JoinOptions{
		Name: "one-library", Repository: "acme/other", Role: RoleLibrary,
	})
	refusal, refused := Refused(err)
	// The code says what happened: the stream ALREADY HAS a library. It used
	// to reuse RefusalNoLibrary, which stated the opposite condition, and
	// refusal codes are contract that skills branch on.
	if !refused || refusal.Code != RefusalLibraryExists {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalLibraryExists)
	}
}

func TestStartAssignsTheLibraryRoleFromTheExplicitFlag(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	for _, repository := range []string{"acme/app", "acme/library"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	result, err := engine.Start(context.Background(), StartOptions{
		Name: "explicit-library", Repositories: []string{"acme/app", "acme/library"},
		Library: "acme/library",
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	library, ok := result.Stream.Library()
	if !ok || library.Repository != "acme/library" {
		t.Fatalf("library = %#v, want acme/library", library)
	}
}

func TestStartRejectsALibraryThatIsNotAMember(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/app", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	_, err := engine.Start(context.Background(), StartOptions{
		Name: "bad-library", Repositories: []string{"acme/app"}, Library: "acme/elsewhere",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "not one of the stream repositories") {
		t.Fatalf("error = %v, want a rejection naming the mistake", err)
	}
}
