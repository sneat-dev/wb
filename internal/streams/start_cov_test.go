package streams

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefusalErrorAndTheRefusedHelper(t *testing.T) {
	t.Parallel()
	if got := (&Refusal{Message: "plain refusal"}).Error(); got != "plain refusal" {
		t.Fatalf("refusal without sanctions = %q", got)
	}
	got := (&Refusal{Message: "guarded", Sanctioned: []string{"wb one", "wb two"}}).Error()
	if got != "guarded; run: wb one or wb two" {
		t.Fatalf("refusal with sanctions = %q", got)
	}
	if refusal, ok := Refused(errors.New("an ordinary failure")); ok || refusal != nil {
		t.Fatalf("Refused(ordinary error) = %#v, %t; want false", refusal, ok)
	}
	sentinel := &Refusal{Code: "x"}
	refusal, ok := Refused(sentinel)
	if !ok || refusal != sentinel {
		t.Fatalf("Refused(refusal) = %#v, %t", refusal, ok)
	}
}

func TestStartRefusesUsageErrors(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	ctx := context.Background()

	if _, err := engine.Start(ctx, StartOptions{Name: "has space", Repositories: []string{"acme/app"}}, nil); err == nil {
		t.Fatal("Start accepted an invalid stream name")
	}
	if _, err := engine.Start(ctx, StartOptions{Name: "no-members"}, nil); err == nil {
		t.Fatal("Start accepted a stream with no repositories")
	}
	if _, err := engine.Start(ctx, StartOptions{Name: "bad-repo", Repositories: []string{"nope"}}, nil); err == nil {
		t.Fatal("Start accepted a repository that is not owner/repository")
	}
	if _, err := engine.Start(ctx, StartOptions{Name: "dupe", Repositories: []string{"acme/app", "ACME/APP"}}, nil); err == nil {
		t.Fatal("Start accepted the same repository twice")
	}
}

func TestStartReportsAnUnreadableExistingRecord(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if err := os.MkdirAll(engine.Store.Dir("broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(engine.Store.statePath("broken"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Start(context.Background(), StartOptions{Name: "broken", Repositories: []string{"acme/app"}}, nil)
	if err == nil {
		t.Fatal("Start reported success for a stream whose existing record could not be read")
	}
	if _, refused := Refused(err); refused {
		t.Fatalf("error = %v, want the unreadable record reported as a failure", err)
	}
}

func TestStartReportsAnUnresolvableDefaultBranch(t *testing.T) {
	t.Parallel()
	engine, git, _, _ := newTestEngine(t)
	engine.Git = stCovGitDefaultBranchErr{fakeGit: git, err: errors.New("no origin")}
	_, err := engine.Start(context.Background(), StartOptions{Name: "no-base", Repositories: []string{"acme/app"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "acme/app") {
		t.Fatalf("error = %v, want the unresolvable base named per repository", err)
	}
}

func TestStartReportsAWorktreeCreationFailure(t *testing.T) {
	t.Parallel()
	engine, _, _, worktrees := newTestEngine(t)
	worktrees.createErr = errors.New("worktree creation refused")

	_, err := engine.Start(context.Background(), StartOptions{Name: "create-fails", Repositories: []string{"acme/app"}}, nil)
	if err == nil {
		t.Fatal("Start reported success although no worktree was created")
	}
	if !strings.Contains(err.Error(), "recorded as creating") || !strings.Contains(err.Error(), "wb stream end create-fails --apply") {
		t.Fatalf("error = %v, want it to name the recorded state and its recovery", err)
	}
}

func TestStartReportsACheckoutRecordFailure(t *testing.T) {
	t.Parallel()
	engine, _, _, worktrees := newTestEngine(t)
	engine.Worktrees = &stCovLockingWorktrees{
		fakeWorktrees: worktrees,
		lockDir:       filepath.Join(engine.Store.Dir("record-fails"), "stream.lock"),
	}
	_, err := engine.Start(context.Background(), StartOptions{Name: "record-fails", Repositories: []string{"acme/app"}}, nil)
	if err == nil {
		t.Fatal("Start reported success although the published checkout could not be recorded")
	}
}

// The advisory fences run before the store lock, so a concurrent start that
// wins the race is caught by the locked decision rather than by the advisory
// one. These subtests drive that window from the hooks checker, which runs
// between the two.
func TestStartReDecidesUnderTheStoreLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("name claimed during preflight", func(t *testing.T) {
		t.Parallel()
		engine, _, _, _ := newTestEngine(t)
		engine.HooksCheck = func(string) ([]string, error) {
			_, err := engine.Store.Create(Stream{Name: "race", Phase: PhaseOpen})
			return nil, err
		}
		_, err := engine.Start(ctx, StartOptions{Name: "race", Repositories: []string{"acme/app"}}, nil)
		refusal, refused := Refused(err)
		if !refused || refusal.Code != RefusalStreamExists {
			t.Fatalf("error = %v, want a %s refusal", err, RefusalStreamExists)
		}
	})

	t.Run("record became unreadable", func(t *testing.T) {
		t.Parallel()
		engine, _, _, _ := newTestEngine(t)
		engine.HooksCheck = func(string) ([]string, error) {
			if err := os.MkdirAll(engine.Store.Dir("race-unreadable"), 0o700); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(engine.Store.statePath("race-unreadable"), []byte("{not json"), 0o600)
		}
		_, err := engine.Start(ctx, StartOptions{Name: "race-unreadable", Repositories: []string{"acme/app"}}, nil)
		if err == nil {
			t.Fatal("Start reported success for a record that became unreadable during preflight")
		}
		if _, refused := Refused(err); refused {
			t.Fatalf("error = %v, want the unreadable record reported as a failure", err)
		}
	})

	t.Run("repository claimed during preflight", func(t *testing.T) {
		t.Parallel()
		engine, _, _, _ := newTestEngine(t)
		engine.HooksCheck = func(string) ([]string, error) {
			_, err := engine.Store.Create(Stream{
				Name: "holder", Phase: PhaseOpen,
				Members: []Member{{Repository: "acme/app", Role: RoleLibrary}},
			})
			return nil, err
		}
		_, err := engine.Start(ctx, StartOptions{Name: "race-held", Repositories: []string{"acme/app"}}, nil)
		refusal, refused := Refused(err)
		if !refused || refusal.Code != RefusalRepositoryInStream {
			t.Fatalf("error = %v, want a %s refusal", err, RefusalRepositoryInStream)
		}
		if !strings.Contains(refusal.Message, "holder") {
			t.Fatalf("refusal = %q, want it to name the holder", refusal.Message)
		}
	})

	t.Run("reservation cannot be written", func(t *testing.T) {
		t.Parallel()
		engine, _, _, _ := newTestEngine(t)
		engine.HooksCheck = func(string) ([]string, error) {
			if err := os.MkdirAll(engine.Store.Root, 0o700); err != nil {
				return nil, err
			}
			return nil, os.Symlink(filepath.Join(engine.Store.Root, "missing"), engine.Store.Dir("race-dangling"))
		}
		_, err := engine.Start(ctx, StartOptions{Name: "race-dangling", Repositories: []string{"acme/app"}}, nil)
		if err == nil {
			t.Fatal("Start reported success although the reservation could not be written")
		}
	})
}

func TestMemberBasePrefersExplicitThenInputs(t *testing.T) {
	t.Parallel()
	if got := memberBase("release", nil, "acme/app"); got != "release" {
		t.Fatalf("memberBase with an explicit base = %q", got)
	}
	if got := memberBase("", nil, "acme/app"); got != "" {
		t.Fatalf("memberBase with no matching input = %q, want empty", got)
	}
	inputs := []PreflightInput{{Repository: "acme/app", DefaultBranch: "main"}}
	if got := memberBase("", inputs, "ACME/APP"); got != "main" {
		t.Fatalf("memberBase from inputs = %q, want the resolved default branch", got)
	}
}

func TestRecordCheckoutFillsInThePublishedCoordinates(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Store.Create(Stream{
		Name: "fill", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "acme/app", Role: RoleLibrary, Worktree: "/planned", Branch: "stream/fill",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := engine.recordCheckout("fill", CreatedWorktree{
		Repository: "acme/app", Worktree: "/published", Canonical: "/canonical", Branch: "stream/fill", Base: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	member := updated.Members[0]
	if member.Worktree != "/published" || member.Canonical != "/canonical" || member.Base != "main" {
		t.Fatalf("member = %#v, want the published coordinates and the base filled in", member)
	}
}

func TestJoinRefusesUsageAndMissingStreams(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	ctx := context.Background()

	if _, err := engine.Join(ctx, JoinOptions{Name: "has space", Repository: "acme/app"}); err == nil {
		t.Fatal("Join accepted an invalid stream name")
	}
	if _, err := engine.Join(ctx, JoinOptions{Name: "ok", Repository: "nope"}); err == nil {
		t.Fatal("Join accepted a repository that is not owner/repository")
	}
	if _, err := engine.Join(ctx, JoinOptions{Name: "absent", Repository: "acme/app"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Join of a missing stream = %v, want ErrNotFound", err)
	}
}

func TestJoinRefusesAnEndedStream(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	ended := engine.Store.Now()
	if _, err := engine.Store.Create(Stream{Name: "over", Phase: PhaseEnded, EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Join(context.Background(), JoinOptions{Name: "over", Repository: "acme/app"})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalStreamEnded {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalStreamEnded)
	}
	if !strings.Contains(strings.Join(refusal.Sanctioned, " "), "wb stream start over acme/app") {
		t.Fatalf("refusal = %v, want the sanctioned restart", refusal.Sanctioned)
	}
}

func TestJoinReturnsEarlyForAReservedMember(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Store.Create(Stream{
		Name: "reserved-join", Phase: PhaseOpen,
		Members: []Member{{Repository: "acme/app", Role: RoleLibrary, Branch: "stream/reserved-join"}},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Join(context.Background(), JoinOptions{Name: "reserved-join", Repository: "acme/app"})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if result.Stream.Name != "reserved-join" {
		t.Fatalf("result = %#v, want the unchanged stream", result.Stream)
	}
}

func stCovJoinStream(t *testing.T, engine *Engine, name string, member Member) {
	t.Helper()
	if _, err := engine.Store.Create(Stream{Name: name, Phase: PhaseOpen, Members: []Member{member}}); err != nil {
		t.Fatal(err)
	}
}

func TestJoinReportsAPublishFailureForAnExistingMember(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	worktree := t.TempDir()
	stCovJoinStream(t, engine, "join-publish", Member{
		Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
		Branch: "stream/join-publish", Base: "main",
	})
	stCovBlockStreamLock(t, engine.Store, "join-publish")

	_, err := engine.Join(context.Background(), JoinOptions{Name: "join-publish", Repository: "acme/app"})
	if err == nil {
		t.Fatal("Join reported success although the published member could not be recorded")
	}
}

func TestJoinReportsARecordedPullRequestTitleFailure(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	worktree := t.TempDir()
	stCovJoinStream(t, engine, "join-title", Member{
		Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
		Branch: "stream/join-title", Base: "main", PullRequest: 9,
	})
	engine.GitHub = stCovHubViewErr{fakeHub: hub, err: errors.New("view failed")}

	result, err := engine.Join(context.Background(), JoinOptions{Name: "join-title", Repository: "acme/app"})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if result.Stream.Members[0].PullRequestError == "" {
		t.Fatal("the unreadable recorded pull request was not persisted as a member error")
	}
}

func TestJoinReportsAnUnresolvableDefaultBranch(t *testing.T) {
	t.Parallel()
	engine, git, _, _ := newTestEngine(t)
	engine.Git = stCovGitDefaultBranchErr{fakeGit: git, err: errors.New("no origin")}
	stCovJoinStream(t, engine, "join-base", Member{Repository: "acme/library", Role: RoleLibrary, Worktree: t.TempDir()})

	_, err := engine.Join(context.Background(), JoinOptions{Name: "join-base", Repository: "acme/app"})
	if err == nil || !strings.Contains(err.Error(), "acme/app") {
		t.Fatalf("error = %v, want the unresolvable base named", err)
	}
}

func TestJoinRefusesWhenTheJoiningRepositoryIsNotReady(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	stCovJoinStream(t, engine, "join-preflight", Member{Repository: "acme/library", Role: RoleLibrary, Worktree: t.TempDir()})
	engine.HooksCheck = func(string) ([]string, error) { return []string{"hook missing"}, nil }

	_, err := engine.Join(context.Background(), JoinOptions{Name: "join-preflight", Repository: "acme/app"})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalPreflight {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalPreflight)
	}
}

func TestJoinRefusesARepositoryClaimedByAnotherStream(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	stCovJoinStream(t, engine, "join-claim", Member{Repository: "acme/library", Role: RoleLibrary, Worktree: t.TempDir()})
	if _, err := engine.Store.Create(Stream{
		Name: "holder", Phase: PhaseOpen,
		Members: []Member{{Repository: "acme/app", Role: RoleLibrary}},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := engine.Join(context.Background(), JoinOptions{Name: "join-claim", Repository: "acme/app"})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalRepositoryInStream {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalRepositoryInStream)
	}
	if !strings.Contains(refusal.Message, "holder") {
		t.Fatalf("refusal = %q, want the holder named", refusal.Message)
	}
}

func TestJoinReportsAPlanOrCreationFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("plan", func(t *testing.T) {
		t.Parallel()
		engine, _, _, worktrees := newTestEngine(t)
		stCovJoinStream(t, engine, "join-plan", Member{Repository: "acme/library", Role: RoleLibrary, Worktree: t.TempDir()})
		worktrees.planErr = errors.New("cannot plan")
		_, err := engine.Join(ctx, JoinOptions{Name: "join-plan", Repository: "acme/app"})
		if err == nil || !strings.Contains(err.Error(), "cannot plan") {
			t.Fatalf("error = %v, want the plan failure", err)
		}
	})

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		engine, _, _, worktrees := newTestEngine(t)
		stCovJoinStream(t, engine, "join-create", Member{Repository: "acme/library", Role: RoleLibrary, Worktree: t.TempDir()})
		worktrees.createErr = errors.New("cannot create")
		_, err := engine.Join(ctx, JoinOptions{Name: "join-create", Repository: "acme/app"})
		if err == nil || !strings.Contains(err.Error(), "cannot create") {
			t.Fatalf("error = %v, want the creation failure", err)
		}
	})

	t.Run("checkout record", func(t *testing.T) {
		t.Parallel()
		engine, _, _, worktrees := newTestEngine(t)
		stCovJoinStream(t, engine, "join-record", Member{Repository: "acme/library", Role: RoleLibrary, Worktree: t.TempDir()})
		engine.Worktrees = &stCovLockingWorktrees{
			fakeWorktrees: worktrees,
			lockDir:       filepath.Join(engine.Store.Dir("join-record"), "stream.lock"),
		}
		_, err := engine.Join(ctx, JoinOptions{Name: "join-record", Repository: "acme/app"})
		if err == nil {
			t.Fatal("Join reported success although the published checkout could not be recorded")
		}
	})
}

// A member that a concurrent join added during preflight is not duplicated:
// the locked update sees it and leaves it alone.
func TestJoinDoesNotDuplicateAMemberAddedDuringPreflight(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Store.Create(Stream{Name: "join-race", Phase: PhaseOpen}); err != nil {
		t.Fatal(err)
	}
	engine.HooksCheck = func(string) ([]string, error) {
		_, err := engine.Store.Update("join-race", func(current *Stream) error {
			current.Members = append(current.Members, Member{
				Repository: "acme/app", Role: RoleConsumer, Branch: "stream/join-race", Base: "main",
			})
			return nil
		})
		return nil, err
	}
	result, err := engine.Join(context.Background(), JoinOptions{Name: "join-race", Repository: "acme/app"})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	count := 0
	for _, member := range result.Stream.Members {
		if strings.EqualFold(member.Repository, "acme/app") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("members = %#v, want the concurrently added member once", result.Stream.Members)
	}
}

func TestPublishMemberReportsABaseMismatch(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	worktree := t.TempDir()
	stCovJoinStream(t, engine, "publish-base", Member{
		Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
		Branch: "stream/publish-base", Base: "main",
	})
	hub.byBranch[worktree+" stream/publish-base"] = PullRequest{
		Number: 41, URL: "https://example.test/pull/41", Head: "stream/publish-base", Base: "release", State: "OPEN",
	}

	result, err := engine.Join(context.Background(), JoinOptions{Name: "publish-base", Repository: "acme/app"})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	// The mismatched open pull request must not be recorded as this stream's.
	if result.Stream.Members[0].PullRequest != 0 {
		t.Fatalf("member = %#v, want the mismatched pull request refused", result.Stream.Members[0])
	}
	if !strings.Contains(result.Stream.Members[0].PullRequestError, "not stream base main") {
		t.Fatalf("member error = %q, want the base mismatch recorded", result.Stream.Members[0].PullRequestError)
	}
}

func TestPublishMemberReportsALegacyTitleRepairFailure(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	worktree := t.TempDir()
	const name = "publish-title"
	stCovJoinStream(t, engine, name, Member{
		Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
		Branch: "stream/" + name, Base: "main",
	})
	hub.byBranch[worktree+" stream/"+name] = PullRequest{
		Number: 42, URL: "https://example.test/pull/42", Head: "stream/" + name, Base: "main",
		Title: legacyStreamPullRequestTitle(name, "acme/app"), State: "OPEN",
	}
	engine.GitHub = stCovHubViewErr{fakeHub: hub, err: errors.New("view failed")}

	result, err := engine.Join(context.Background(), JoinOptions{Name: name, Repository: "acme/app"})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if !strings.Contains(result.Stream.Members[0].PullRequestError, "re-read legacy stream pull request") {
		t.Fatalf("member error = %q, want the failed title repair recorded", result.Stream.Members[0].PullRequestError)
	}
}

func TestRefuseSecondStreamReportsAnUnreadableStore(t *testing.T) {
	t.Parallel()
	blocked := OpenAt(filepath.Join(t.TempDir(), "store-file"))
	if _, err := os.Create(blocked.Root); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Store: blocked}
	if _, err := engine.refuseSecondStream("acme/app", "joining"); err == nil {
		t.Fatal("refuseSecondStream reported a decision from a store it could not read")
	}
}

func TestJoinReportsAFailedTitleReconciliation(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	worktree := t.TempDir()
	stCovJoinStream(t, engine, "join-reconcile", Member{
		Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
		Branch: "stream/join-reconcile", Base: "main", PullRequest: 9,
	})
	hub.byNumber[9] = PullRequest{Number: 9, State: "OPEN", Head: "stream/join-reconcile", Base: "main"}
	stCovBlockStreamLock(t, engine.Store, "join-reconcile")

	if _, err := engine.Join(context.Background(), JoinOptions{Name: "join-reconcile", Repository: "acme/app"}); err == nil {
		t.Fatal("Join reported success although the reconciliation could not be recorded")
	}
}

func TestJoinReportsAFailedReceiptWriteForADiscoveredPullRequest(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	worktree := t.TempDir()
	stCovJoinStream(t, engine, "join-receipt", Member{
		Repository: "acme/app", Role: RoleLibrary, Worktree: worktree, Canonical: worktree,
		Branch: "stream/join-receipt", Base: "main",
	})
	hub.byBranch[worktree+" stream/join-receipt"] = PullRequest{
		Number: 51, URL: "https://example.test/pull/51", Head: "stream/join-receipt", Base: "main", State: "OPEN",
	}
	engine.GitHub = &stCovLockingBranchHub{
		fakeHub: hub,
		lockDir: filepath.Join(engine.Store.Dir("join-receipt"), "stream.lock"),
	}

	if _, err := engine.Join(context.Background(), JoinOptions{Name: "join-receipt", Repository: "acme/app"}); err == nil {
		t.Fatal("Join reported success although the discovered receipt could not be written")
	}
}

// A push that lands but whose result cannot be recorded leaves the member
// unpublished, and start reports the failure rather than claiming success.
func TestStartReportsAPublishFailure(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	engine.GitHub = &stCovLockingCreateHub{
		fakeHub: hub,
		lockDir: filepath.Join(engine.Store.Dir("publish-fails"), "stream.lock"),
	}
	_, err := engine.Start(context.Background(), StartOptions{Name: "publish-fails", Repositories: []string{"acme/app"}}, nil)
	if err == nil {
		t.Fatal("Start reported success although the published member could not be recorded")
	}
}

func TestJoinReportsAPublishFailureForANewMember(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	stCovJoinStream(t, engine, "join-publish-fail", Member{Repository: "acme/library", Role: RoleLibrary, Worktree: t.TempDir()})
	engine.GitHub = &stCovLockingCreateHub{
		fakeHub: hub,
		lockDir: filepath.Join(engine.Store.Dir("join-publish-fail"), "stream.lock"),
	}
	_, err := engine.Join(context.Background(), JoinOptions{Name: "join-publish-fail", Repository: "acme/app"})
	if err == nil {
		t.Fatal("Join reported success although the published member could not be recorded")
	}
}
