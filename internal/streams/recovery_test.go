package streams

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// MF-1. State is written BEFORE the first side effect, so a start that dies
// after the first push leaves a record `wb stream end` can retire.
//
// Requirements: dependency-streams#req:every-stream-verb-has-a-terminal-recovery.
func TestAStartInterruptedAfterTheFirstPushIsRecoverableByEnd(t *testing.T) {
	t.Parallel()
	engine, git, hub, worktrees := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/app"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	// The second member's pull request cannot be opened, standing in for any
	// failure in the publication window.
	failing := filepath.Join(worktrees.root, "worktrees", "interrupted", "acme", "app")
	hub.createErr[failing] = errors.New("gh: the remote went away")

	result, err := engine.Start(context.Background(), StartOptions{
		Name: "interrupted", Repositories: []string{"acme/library", "acme/app"},
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Everything published so far is in the record, including the member
	// whose pull request never opened.
	stored, err := OpenAt(engine.Store.Root).Load("interrupted")
	if err != nil {
		t.Fatalf("the interrupted start left no recoverable record: %v", err)
	}
	if len(stored.Members) != 2 {
		t.Fatalf("members = %d, want both recorded", len(stored.Members))
	}
	app, _ := stored.Member("acme/app")
	if app.Worktree == "" || app.Branch != "stream/interrupted" {
		t.Fatalf("the unpublished member carries no coordinates to recover from: %#v", app)
	}
	if app.PullRequestError == "" {
		t.Error("the failure was not recorded against the member")
	}
	if !strings.Contains(strings.Join(git.pushedBranches(), " "), "stream/interrupted") {
		t.Error("no branch was pushed, so the test is not exercising the publication window")
	}
	_ = result

	// end reaches every published effect from the record alone.
	ended, err := engine.End(context.Background(), EndOptions{Name: "interrupted", Apply: true})
	if err != nil {
		t.Fatalf("end could not retire the interrupted stream: %v", err)
	}
	if len(ended.Members) != 2 {
		t.Fatalf("end retired %d member(s), want both", len(ended.Members))
	}
	for _, member := range ended.Members {
		if !member.WorktreeRemoved {
			t.Errorf("%s worktree was not removed: %s", member.Repository, member.Detail)
		}
	}
	after, err := engine.Store.Load("interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if after.Open() {
		t.Error("the stream is still open after end")
	}
}

// MF-1, the reservation half: the record exists in the `creating` phase from
// before the first worktree is published, and a concurrent start on the same
// name refuses without publishing anything of its own.
func TestASecondStartOnTheSameNameRefusesBeforePublishingAnything(t *testing.T) {
	t.Parallel()
	engine, _, _, worktrees := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "reserved", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	published := len(worktrees.created)
	_, err := engine.Start(context.Background(), StartOptions{
		Name: "reserved", Repositories: []string{"acme/library"},
	}, nil)
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalStreamExists {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalStreamExists)
	}
	if len(worktrees.created) != published {
		t.Errorf("the refused start published %d extra checkout(s)", len(worktrees.created)-published)
	}
}

// MF-2. The absorption guard fails CLOSED: a comparison that could not run
// refuses, and nothing is closed or removed.
//
// Requirements: dependency-streams#req:stream-end-proves-absorption-and-removes-its-own-scaffolding.
func TestEndRefusesWhenTheAbsorptionCheckCouldNotRun(t *testing.T) {
	t.Parallel()
	engine, git, hub, worktrees, stream := startedStream(t, "unknown-absorption", "acme/library")
	member := stream.Members[0]
	hub.targeting[member.Worktree+" stream/unknown-absorption"] = []PullRequest{
		{Number: 7, URL: "https://example.test/pull/7", Head: "agent/a", Base: "stream/unknown-absorption"},
	}
	git.notInErr[member.Worktree+" stream/unknown-absorption origin/main"] = errors.New("fatal: bad revision 'origin/main'")

	_, err := engine.End(context.Background(), EndOptions{Name: "unknown-absorption", Apply: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUnabsorbedWork {
		t.Fatalf("error = %v, want a %s refusal — an unknown must not pass", err, RefusalUnabsorbedWork)
	}
	if !strings.Contains(refusal.Message, "could not run") {
		t.Errorf("refusal does not say the check could not answer: %s", refusal.Message)
	}
	if len(hub.closed) != 0 {
		t.Errorf("agent pull requests were closed on the strength of a check that never answered: %v", hub.closed)
	}
	if len(worktrees.removed) != 0 {
		t.Errorf("worktrees were removed after an unknown absorption check: %v", worktrees.removed)
	}
	sanctioned := strings.Join(refusal.Sanctioned, " | ")
	if !strings.Contains(sanctioned, "--force-unabsorbed") || !strings.Contains(sanctioned, "--reason") {
		t.Errorf("refusal does not name the sanctioned escape: %v", refusal.Sanctioned)
	}
}

// --force-unabsorbed is an audited step-over, not a silent bypass: it requires
// a reason and records both the reason and what it stepped over.
func TestForceUnabsorbedRequiresAReasonAndRecordsIt(t *testing.T) {
	t.Parallel()
	engine, git, _, _, stream := startedStream(t, "forced", "acme/library")
	member := stream.Members[0]
	git.notIn[member.Worktree+" stream/forced origin/main"] = []Commit{
		{SHA: "35c480ed6e1e718a910d8aa617c4da94dd47557a", Subject: "feat: not landed", PatchID: "aa11"},
	}

	_, err := engine.End(context.Background(), EndOptions{Name: "forced", Apply: true, ForceUnabsorbed: true})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalUsage {
		t.Fatalf("error = %v, want --force-unabsorbed without --reason to be refused", err)
	}

	result, err := engine.End(context.Background(), EndOptions{
		Name: "forced", Apply: true, ForceUnabsorbed: true, Reason: "landed by hand in #412",
	})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if result.ForcedReason != "landed by hand in #412" || len(result.Forced) != 1 {
		t.Fatalf("result = %#v, want the step-over recorded", result)
	}
	events, err := ReadEvents(engine.Store.EventLog("forced").Path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Phase == "absorption" && strings.Contains(event.Detail, "landed by hand in #412") {
			found = true
		}
	}
	if !found {
		t.Error("the forced step-over was not recorded in the event log")
	}
}

// MF-3. Origin is re-read before any read of origin/<base> or of the tags.
//
// Requirements: dependency-streams#req:stream-verbs-re-read-state-before-mutating.
func TestEndAndStatusReReadOriginBeforeTrustingALocalRef(t *testing.T) {
	t.Parallel()
	engine, git, _, _, stream := startedStream(t, "fresh", "acme/library")
	member := stream.Members[0]
	// The library must publish something, or status returns before it ever
	// reads the tags and the assertion below would pass vacuously.
	writeFiles(t, member.Worktree, map[string]string{
		"backend/go.mod": "module github.com/acme/library/backend\n\ngo 1.27\n",
	})
	git.tags[member.Worktree] = []string{"backend/v0.4.0"}
	git.calls = nil
	if _, err := engine.Status(context.Background(), "fresh"); err != nil {
		t.Fatal(err)
	}
	if !git.fetchedBefore("commits " + member.Worktree) {
		t.Errorf("status read origin/<base> without re-fetching first: %v", git.calls)
	}
	if !git.fetchedBefore("tags " + member.Worktree) {
		t.Errorf("status read the tags without re-fetching first: %v", git.calls)
	}

	git.calls = nil
	if _, err := engine.End(context.Background(), EndOptions{Name: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if !git.fetchedBefore("commits " + member.Worktree) {
		t.Errorf("end judged absorption without re-fetching first: %v", git.calls)
	}
}

// MF-4. A credential inside a child-process error never reaches the state file
// or the report.
//
// Requirements: dependency-streams#req:redaction-runs-before-any-bytes-leave-the-process.
func TestACredentialInAGitErrorNeverReachesTheStateFile(t *testing.T) {
	t.Parallel()
	engine, git, _, worktrees := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	secret := "ghp_0123456789abcdefghijklmnopqrstuvwx"
	failing := filepath.Join(worktrees.root, "worktrees", "leak", "acme", "library")
	git.pushErr[failing] = errors.New(
		"push stream/leak: remote: fatal: could not read from https://x-access-token:" + secret + "@github.com/acme/library.git")

	result, err := engine.Start(context.Background(), StartOptions{
		Name: "leak", Repositories: []string{"acme/library"},
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	member, _ := result.Stream.Member("acme/library")
	if strings.Contains(member.PullRequestError, secret) {
		t.Fatalf("the member record carries the credential: %s", member.PullRequestError)
	}
	if !strings.Contains(member.PullRequestError, "[redacted]") {
		t.Fatalf("the error was not redacted at all: %s", member.PullRequestError)
	}
	contents, err := os.ReadFile(filepath.Join(engine.Store.Dir("leak"), "stream.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), secret) {
		t.Fatal("stream.json contains the credential")
	}
	events, err := os.ReadFile(engine.Store.EventLog("leak").Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), secret) {
		t.Fatal("the event log contains the credential")
	}
}

// MF-6. An ended stream's name is reusable: start archives the old record —
// keeping it, because the event log is evidence — and proceeds.
func TestStartReusesTheNameOfAnEndedStreamByArchivingIt(t *testing.T) {
	t.Parallel()
	engine, _, _, _, _ := startedStream(t, "recycled", "acme/library")
	if _, err := engine.End(context.Background(), EndOptions{Name: "recycled", Apply: true}); err != nil {
		t.Fatalf("end: %v", err)
	}
	result, err := engine.Start(context.Background(), StartOptions{
		Name: "recycled", Repositories: []string{"acme/library"},
	}, nil)
	if err != nil {
		t.Fatalf("start over an ended stream: %v", err)
	}
	if result.Stream.Lifecycle() != PhaseOpen {
		t.Fatalf("phase = %q, want open", result.Stream.Lifecycle())
	}
	all, _, err := engine.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	archived := false
	for _, stream := range all {
		if stream.ArchivedFrom == "recycled" {
			archived = true
			if stream.Open() {
				t.Error("the archived record is not marked ended")
			}
		}
	}
	if !archived {
		t.Fatal("the previous record was discarded rather than archived")
	}
}

// MF-6, the delete half.
func TestDeleteRefusesAnOpenStreamAndRemovesAnEndedOne(t *testing.T) {
	t.Parallel()
	engine, _, _, _, _ := startedStream(t, "removable", "acme/library")
	if err := engine.Store.Delete("removable"); err == nil {
		t.Fatal("deleting an open stream succeeded; its worktrees would be stranded")
	}
	if _, err := engine.End(context.Background(), EndOptions{Name: "removable", Apply: true}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.Delete("removable"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := engine.Store.Load("removable"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the stream survived delete: %v", err)
	}
}

// SHOULD-FIX: one unreadable stream must not refuse every start on the machine.
func TestAnUnreadableStreamIsReportedAndDoesNotBlockOthers(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	broken := engine.Store.Dir("broken")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "stream.json"), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "healthy", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatalf("one truncated record refused an unrelated start: %v", err)
	}
	_, unreadable, err := engine.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 1 || unreadable[0].Name != "broken" {
		t.Fatalf("unreadable = %#v, want the truncated record reported", unreadable)
	}
}

// SHOULD-FIX: join retries a member whose draft pull request never opened,
// which is the recovery publishMember documents.
func TestJoinRetriesAMemberWhoseDraftPullRequestNeverOpened(t *testing.T) {
	t.Parallel()
	engine, _, hub, worktrees := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	failing := filepath.Join(worktrees.root, "worktrees", "retry", "acme", "library")
	hub.createErr[failing] = errors.New("gh: transient")
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "retry", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	delete(hub.createErr, failing)

	result, err := engine.Join(context.Background(), JoinOptions{Name: "retry", Repository: "acme/library"})
	if err != nil {
		t.Fatalf("join retry: %v", err)
	}
	member, _ := result.Stream.Member("acme/library")
	if member.PullRequest == 0 {
		t.Fatalf("join did not retry the missing pull request: %#v", member)
	}
	if member.PullRequestError != "" {
		t.Errorf("the stale failure was not cleared: %s", member.PullRequestError)
	}
}

// The remote PR can be created successfully just before the process dies and
// before its identity reaches stream.json. Recovery must adopt that exact open
// PR rather than asking GitHub to create a duplicate and getting stuck again.
func TestJoinAdoptsAnOpenMemberPullRequestMissingFromStreamState(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "adopt-pr", "acme/library")
	member := stream.Members[0]
	createdBefore := len(hub.created)
	existing := PullRequest{
		Number: 919, URL: "https://example.test/pull/919",
		Title: "stream(adopt-pr): acme/library", Head: member.Branch,
		Base: member.Base, Draft: true, State: "OPEN",
	}
	hub.byBranch[member.Worktree+" "+member.Branch] = existing
	hub.byNumber[existing.Number] = existing
	if _, err := engine.setMember("adopt-pr", member.Repository, func(stored *Member) {
		stored.PullRequest = 0
		stored.PullRequestURL = ""
		stored.PullRequestError = "process interrupted before the PR receipt was stored"
	}); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Join(context.Background(), JoinOptions{Name: "adopt-pr", Repository: member.Repository})
	if err != nil {
		t.Fatalf("join recovery: %v", err)
	}
	if len(hub.created) != createdBefore {
		t.Fatalf("created PR count = %d, want existing remote PR adopted without another create", len(hub.created))
	}
	recovered, _ := result.Stream.Member(member.Repository)
	if recovered.PullRequest != existing.Number || recovered.PullRequestURL != existing.URL || recovered.PullRequestError != "" {
		t.Fatalf("recovered member = %#v, want adopted PR %#v", recovered, existing)
	}
	wantTitle := "feat(stream): adopt-pr in acme/library"
	if hub.updatedTitles[existing.Number] != wantTitle {
		t.Fatalf("updated title = %q, want %q", hub.updatedTitles[existing.Number], wantTitle)
	}
}

func TestJoinPersistsAnAdoptedPullRequestWhenLegacyTitleRepairFails(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "adopt-title-failure", "acme/library")
	member := stream.Members[0]
	existing := PullRequest{
		Number: 920, URL: "https://example.test/pull/920",
		Title: "stream(adopt-title-failure): acme/library", Head: member.Branch,
		Base: member.Base, Draft: true, State: "OPEN",
	}
	hub.byBranch[member.Worktree+" "+member.Branch] = existing
	hub.byNumber[existing.Number] = existing
	hub.updateTitleErr[existing.Number] = errors.New("GitHub rejected the title update")
	if _, err := engine.setMember("adopt-title-failure", member.Repository, func(stored *Member) {
		stored.PullRequest = 0
		stored.PullRequestURL = ""
		stored.PullRequestError = "process interrupted before the PR receipt was stored"
	}); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Join(context.Background(), JoinOptions{Name: "adopt-title-failure", Repository: member.Repository})
	if err != nil {
		t.Fatalf("join recovery: %v", err)
	}
	recovered, _ := result.Stream.Member(member.Repository)
	if recovered.PullRequest != existing.Number || recovered.PullRequestURL != existing.URL {
		t.Fatalf("recovered member = %#v, want adopted PR %#v persisted despite title failure", recovered, existing)
	}
	if len(result.Reported) != 1 || result.Reported[0].Check != "stream-pull-request-title" || strings.Contains(result.Reported[0].Detail, "run no CI") {
		t.Fatalf("reported = %#v, want the title finding without a false missing-PR claim", result.Reported)
	}
}

func TestJoinRepairsARecordedLegacyMemberPullRequestTitleIdempotently(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "repair-title", "acme/library")
	member := stream.Members[0]
	legacy := "stream(repair-title): acme/library"
	pullRequest := hub.byNumber[member.PullRequest]
	pullRequest.Title = legacy
	hub.byNumber[member.PullRequest] = pullRequest
	hub.byBranch[member.Worktree+" "+member.Branch] = pullRequest

	for attempt := 0; attempt < 2; attempt++ {
		result, err := engine.Join(context.Background(), JoinOptions{Name: "repair-title", Repository: member.Repository})
		if err != nil {
			t.Fatalf("join attempt %d: %v", attempt+1, err)
		}
		if len(result.Reported) != 0 {
			t.Fatalf("join attempt %d reported findings: %#v", attempt+1, result.Reported)
		}
	}
	if len(hub.titleUpdateCalls) != 1 {
		t.Fatalf("title update calls = %#v, want one idempotent repair", hub.titleUpdateCalls)
	}
	wantTitle := "feat(stream): repair-title in acme/library"
	if hub.updatedTitles[member.PullRequest] != wantTitle {
		t.Fatalf("updated title = %q, want %q", hub.updatedTitles[member.PullRequest], wantTitle)
	}
}

func TestJoinPreservesAUserAuthoredMemberPullRequestTitle(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "custom-title", "acme/library")
	member := stream.Members[0]
	pullRequest := hub.byNumber[member.PullRequest]
	pullRequest.Title = "stream(custom-title): acme/library — operator context"
	hub.byNumber[member.PullRequest] = pullRequest
	hub.byBranch[member.Worktree+" "+member.Branch] = pullRequest

	if _, err := engine.Join(context.Background(), JoinOptions{Name: "custom-title", Repository: member.Repository}); err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(hub.updatedTitles) != 0 {
		t.Fatalf("user-authored title was overwritten: %#v", hub.updatedTitles)
	}
}

func TestJoinRechecksALegacyTitleBeforeEditing(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "concurrent-title", "acme/library")
	member := stream.Members[0]
	pullRequest := hub.byNumber[member.PullRequest]
	pullRequest.Title = "stream(concurrent-title): acme/library"
	hub.byNumber[member.PullRequest] = pullRequest
	hub.byBranch[member.Worktree+" "+member.Branch] = pullRequest
	if _, err := engine.setMember("concurrent-title", member.Repository, func(stored *Member) {
		stored.PullRequest = 0
		stored.PullRequestURL = ""
	}); err != nil {
		t.Fatal(err)
	}
	hub.beforePullRequest = func(number int) {
		hub.beforePullRequest = nil
		current := hub.byNumber[number]
		current.Title = "fix(api): operator edited during recovery"
		hub.byNumber[number] = current
	}

	result, err := engine.Join(context.Background(), JoinOptions{Name: "concurrent-title", Repository: member.Repository})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(hub.titleUpdateCalls) != 0 {
		t.Fatalf("concurrent operator title was overwritten: %#v", hub.titleUpdateCalls)
	}
	recovered, _ := result.Stream.Member(member.Repository)
	if recovered.PullRequest != pullRequest.Number {
		t.Fatalf("recovered member = %#v, want the discovered PR persisted", recovered)
	}
}

func TestJoinReportsAndRedactsALegacyTitleUpdateFailure(t *testing.T) {
	t.Parallel()
	engine, _, hub, _, stream := startedStream(t, "title-failure", "acme/library")
	member := stream.Members[0]
	pullRequest := hub.byNumber[member.PullRequest]
	pullRequest.Title = "stream(title-failure): acme/library"
	hub.byNumber[member.PullRequest] = pullRequest
	hub.byBranch[member.Worktree+" "+member.Branch] = pullRequest
	const secret = "github_pat_this_secret_must_never_escape"
	hub.updateTitleErr[member.PullRequest] = errors.New("github refused token=" + secret)

	result, err := engine.Join(context.Background(), JoinOptions{Name: "title-failure", Repository: member.Repository})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(result.Reported) != 1 || result.Reported[0].Check != "stream-pull-request-title" {
		t.Fatalf("reported = %#v, want one title-repair finding", result.Reported)
	}
	detail := result.Reported[0].Detail
	if strings.Contains(detail, secret) || !strings.Contains(detail, "[redacted]") {
		t.Fatalf("finding was not redacted: %q", detail)
	}
	recovered, _ := result.Stream.Member(member.Repository)
	if strings.Contains(recovered.PullRequestError, secret) || !strings.Contains(recovered.PullRequestError, "[redacted]") {
		t.Fatalf("member error was not redacted: %q", recovered.PullRequestError)
	}
	jsonResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonResult), secret) || !strings.Contains(string(jsonResult), `"status":"fail"`) {
		t.Fatalf("JSON result does not carry the redacted finding contract: %s", jsonResult)
	}
	events, err := ReadEvents(engine.Store.EventLog("title-failure").Path)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Phase != "pull-request-title" || last.Outcome != "findings" || strings.Contains(last.Detail, secret) {
		t.Fatalf("event = %#v, want a redacted title finding", last)
	}
}

func TestJoinClearsAnObsoleteTitleFailureWhenTheRecordedPullRequestNoLongerQualifies(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*fakeHub, Member)
	}{
		{name: "missing", mutate: func(hub *fakeHub, member Member) {
			delete(hub.byNumber, member.PullRequest)
		}},
		{name: "closed", mutate: func(hub *fakeHub, member Member) {
			pullRequest := hub.byNumber[member.PullRequest]
			pullRequest.State = "CLOSED"
			hub.byNumber[member.PullRequest] = pullRequest
		}},
		{name: "different head", mutate: func(hub *fakeHub, member Member) {
			pullRequest := hub.byNumber[member.PullRequest]
			pullRequest.Head = "operator/other-branch"
			hub.byNumber[member.PullRequest] = pullRequest
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			engine, _, hub, _, stream := startedStream(t, "obsolete-title-error", "acme/library")
			member := stream.Members[0]
			if _, err := engine.setMember("obsolete-title-error", member.Repository, func(stored *Member) {
				stored.PullRequestError = "legacy title update failed"
			}); err != nil {
				t.Fatal(err)
			}
			test.mutate(hub, member)

			result, err := engine.Join(context.Background(), JoinOptions{Name: "obsolete-title-error", Repository: member.Repository})
			if err != nil {
				t.Fatalf("join: %v", err)
			}
			recovered, _ := result.Stream.Member(member.Repository)
			if recovered.PullRequestError != "" || len(result.Reported) != 0 || len(hub.titleUpdateCalls) != 0 {
				t.Fatalf("recovered = %#v, reported = %#v, updates = %#v; want obsolete title failure retired", recovered, result.Reported, hub.titleUpdateCalls)
			}
		})
	}
}

// Exercise the complete recovery path with real Git: the member checkout is
// strictly behind its remote stream branch, join fast-forwards it, creates the
// missing PR in that member context, and persists both remote head and PR.
func TestJoinRecoversARemoteAheadMemberAndPersistsPublication(t *testing.T) {
	t.Parallel()
	local, other := newPublishedStreamFixture(t)
	runStreamGit(t, other, "checkout", "stream/recovery")
	commitStreamFile(t, other, "remote.txt", "remote\n", "feat: remote advance")
	runStreamGit(t, other, "push", "origin", "stream/recovery")
	remoteHead := strings.TrimSpace(runStreamGit(t, other, "rev-parse", "HEAD"))

	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{
		Name: "recovery", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "datatug/datatug-core", Role: RoleLibrary,
			Worktree: local, Branch: "stream/recovery", Base: "main",
			PullRequestError: "historical non-fast-forward",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	hub := newFakeHub()
	engine := &Engine{Store: store, Git: ExecGit{Timeout: time.Minute}, GitHub: hub}

	result, err := engine.Join(context.Background(), JoinOptions{Name: "recovery", Repository: "datatug/datatug-core"})
	if err != nil {
		t.Fatalf("join recovery: %v", err)
	}
	member, _ := result.Stream.Member("datatug/datatug-core")
	if member.Lease.RecordedHead != remoteHead || member.PullRequest == 0 || member.PullRequestURL == "" || member.PullRequestError != "" {
		t.Fatalf("recovered member = %#v, want remote head %s and a persisted PR", member, remoteHead)
	}
	if localHead := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD")); localHead != remoteHead {
		t.Fatalf("local head = %s, want fast-forwarded remote %s", localHead, remoteHead)
	}
	if len(hub.created) != 1 || hub.created[0].Head != "stream/recovery" || hub.created[0].Base != "main" {
		t.Fatalf("created PRs = %#v, want one member-context stream PR", hub.created)
	}
}

// A cross-repository end can be interrupted after member cleanup and before
// the stream record closes. Retrying must accept a receipt-proven remote ref
// that is authoritatively gone even while the canonical clone still has its
// stale origin/stream/* tracking ref. A present different SHA remains guarded
// by the deletion lease in the lower-level Git-port regression.
func TestEndResumesAfterMultiMemberWorktreesAndRemoteRefsWereAlreadyRetired(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		absentMembers map[string]bool
	}{
		{name: "second member ref absent", absentMembers: map[string]bool{"datatug/datatug-cli": true}},
		{name: "both member refs absent", absentMembers: map[string]bool{"datatug/datatug-core": true, "datatug/datatug-cli": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const streamName = "incidentius-task1-store"
			const branch = "stream/" + streamName
			store := OpenAt(filepath.Join(t.TempDir(), "streams"))
			hub := newFakeHub()
			worktrees := &fakeWorktrees{root: t.TempDir(), removeErr: map[string]error{}}
			members := make([]Member, 0, 2)

			for index, repository := range []string{"datatug/datatug-core", "datatug/datatug-cli"} {
				canonical, remotePeer := newPublishedStreamFixture(t)
				runStreamGit(t, canonical, "branch", "-m", "stream/recovery", branch)
				runStreamGit(t, canonical, "push", "origin", branch+":"+branch)
				head := strings.TrimSpace(runStreamGit(t, canonical, "rev-parse", "HEAD"))
				runStreamGit(t, canonical, "checkout", "main")
				runStreamGit(t, canonical, "branch", "-D", branch)

				member := Member{
					Repository:  repository,
					Role:        RoleConsumer,
					Worktree:    filepath.Join(filepath.Dir(canonical), "retired-"+filepath.Base(canonical)),
					Canonical:   canonical,
					Branch:      branch,
					Base:        "main",
					PullRequest: 700 + index,
					Lease:       Lease{Login: "octocat", Machine: "workstation", Session: "wbs-interrupted", RecordedHead: head, HeldSince: time.Now().UTC()},
				}
				if index == 0 {
					member.Role = RoleLibrary
				}
				hub.byNumber[member.PullRequest] = exactSquashReceipt(member, head)
				if test.absentMembers[repository] {
					runStreamGit(t, remotePeer, "push", "origin", "--delete", branch)
					if stale := strings.TrimSpace(runStreamGit(t, canonical, "rev-parse", "refs/remotes/origin/"+branch)); stale != head {
						t.Fatalf("%s stale tracking head = %s, want %s", repository, stale, head)
					}
				}
				members = append(members, member)
			}
			if _, err := store.Create(Stream{Name: streamName, Phase: PhaseOpen, Members: members}); err != nil {
				t.Fatal(err)
			}
			engine := &Engine{Store: store, Git: ExecGit{Timeout: time.Minute}, GitHub: hub, Worktrees: worktrees}

			result, err := engine.End(context.Background(), EndOptions{Name: streamName, Apply: true})
			if err != nil {
				t.Fatalf("resume interrupted end: %v", err)
			}
			if len(result.Members) != len(members) {
				t.Fatalf("retired members = %d, want %d", len(result.Members), len(members))
			}
			for _, member := range result.Members {
				if !member.RemoteBranchDeleted || !member.WorktreeRemoved || !member.LeaseReleased {
					t.Errorf("member result = %#v, want fully retired idempotent state", member)
				}
			}
			ended, err := store.Load(streamName)
			if err != nil || ended.Open() {
				t.Fatalf("stream = %#v, err=%v; want ended", ended, err)
			}
			for _, member := range ended.Members {
				if member.Lease != (Lease{}) {
					t.Errorf("%s lease = %#v, want released", member.Repository, member.Lease)
				}
				if remote := strings.TrimSpace(runStreamGit(t, member.Canonical, "ls-remote", "--heads", "origin", "refs/heads/"+branch)); remote != "" {
					t.Errorf("%s remote stream ref survived: %s", member.Repository, remote)
				}
			}
		})
	}
}

// SHOULD-FIX: end removes the remote stream branch, after the agent pull
// requests targeting it are settled.
func TestEndDeletesTheRemoteStreamBranchAfterSettlingItsPullRequests(t *testing.T) {
	t.Parallel()
	engine, git, hub, _, stream := startedStream(t, "scaffolding", "acme/library")
	member := stream.Members[0]
	hub.targeting[member.Worktree+" stream/scaffolding"] = []PullRequest{
		{Number: 9, URL: "https://example.test/pull/9", Head: "agent/a", Base: "stream/scaffolding"},
	}
	result, err := engine.End(context.Background(), EndOptions{Name: "scaffolding", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if !result.Members[0].RemoteBranchDeleted {
		t.Fatalf("the remote stream branch survived: %#v", result.Members[0])
	}
	if len(git.deleted) != 1 || !strings.HasSuffix(git.deleted[0], "stream/scaffolding") {
		t.Fatalf("deleted = %v, want the stream branch", git.deleted)
	}
	if len(hub.closed) == 0 {
		t.Error("the agent pull request was not settled before the branch was deleted")
	}
}

// SHOULD-FIX: the tag read is scoped to the library's own module, so a
// repository carrying tags for several modules cannot mis-report gap 3.
func TestStatusReadsOnlyTheLibraryModulesTags(t *testing.T) {
	t.Parallel()
	engine, git, _, _, stream := startedStream(t, "tagscope", "acme/library")
	library := stream.Members[0]
	writeFiles(t, library.Worktree, map[string]string{
		"backend/go.mod": "module github.com/acme/library/backend\n\ngo 1.27\n",
	})
	git.tagPatterns = nil
	if _, err := engine.Status(context.Background(), "tagscope"); err != nil {
		t.Fatal(err)
	}
	if len(git.tagPatterns) == 0 || git.tagPatterns[0] != "backend/v*" {
		t.Fatalf("tag patterns = %v, want the library's own module glob", git.tagPatterns)
	}
}

// A preflight fetch that fails is reported, never discarded: the readiness
// checks then ran against a possibly stale clone, and the operator has to know
// that before trusting a green start.
func TestAFailedPreflightFetchIsReportedNotDiscarded(t *testing.T) {
	t.Parallel()
	engine, git, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	git.fetchErr[canonicalPath(engine.ProjectsRoot, "acme/library")] = errors.New("could not resolve host github.com")

	result, err := engine.Start(context.Background(), StartOptions{
		Name: "stale-clone", Repositories: []string{"acme/library"},
	}, nil)
	if err != nil {
		t.Fatalf("an unreachable remote must not refuse the start: %v", err)
	}
	finding, ok := findingFor(result.Reported, "acme/library", "origin-refresh")
	if !ok {
		t.Fatalf("reported = %#v, want the failed fetch reported", result.Reported)
	}
	if finding.Status != PreflightUnknown || !strings.Contains(finding.Detail, "possibly stale") {
		t.Errorf("finding = %#v, want an unknown saying the checks may be stale", finding)
	}
}

// A member left without a draft pull request is a FINDING, so the verb exits 1
// rather than reporting success while that member's pushes run no CI.
func TestAMemberWithoutADraftPullRequestIsAReportedFinding(t *testing.T) {
	t.Parallel()
	engine, _, hub, worktrees := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/app"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	failing := filepath.Join(worktrees.root, "worktrees", "no-pr", "acme", "app")
	hub.createErr[failing] = errors.New("gh: no default remote")

	result, err := engine.Start(context.Background(), StartOptions{
		Name: "no-pr", Repositories: []string{"acme/library", "acme/app"},
	}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finding, ok := findingFor(result.Reported, "acme/app", "draft-pull-request")
	if !ok {
		t.Fatalf("reported = %#v, want the missing pull request reported", result.Reported)
	}
	if finding.Status != PreflightFail {
		t.Errorf("status = %q, want a failing finding so the verb exits 1", finding.Status)
	}
	if !strings.Contains(finding.Detail, "run no CI") {
		t.Errorf("finding does not say what is lost: %s", finding.Detail)
	}
	if !strings.Contains(finding.Detail, "wb stream join no-pr acme/app") {
		t.Errorf("finding does not name the retry: %s", finding.Detail)
	}
	// The healthy member is not reported.
	if _, reported := findingFor(result.Reported, "acme/library", "draft-pull-request"); reported {
		t.Error("a member with a draft pull request was reported as missing one")
	}
	status, err := engine.Status(context.Background(), "no-pr")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var recovery string
	for _, member := range status.Members {
		if member.Repository == "acme/app" {
			recovery = member.PullRequestRecovery
		}
	}
	if recovery != "wb stream join no-pr acme/app" {
		t.Fatalf("status recovery = %q, want the exact WB retry verb", recovery)
	}
	if _, err := engine.setMember("no-pr", "acme/app", func(member *Member) {
		member.PullRequestError = "stream branch diverged: both sides carry unique work; an owner must choose"
	}); err != nil {
		t.Fatal(err)
	}
	status, err = engine.Status(context.Background(), "no-pr")
	if err != nil {
		t.Fatalf("diverged status: %v", err)
	}
	for _, member := range status.Members {
		if member.Repository == "acme/app" && (member.PullRequestRecovery != "" || !strings.Contains(member.PullRequestBlocked, "owner")) {
			t.Fatalf("diverged member status = %#v, want an explicit owner-decision block and no retry loop", member)
		}
	}
}

// The one-open-stream guard reports records it could not read, so a "no stream
// holds this repository" answer never looks more certain than it is.
func TestStartReportsStreamRecordsItCouldNotRead(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	if err := os.MkdirAll(engine.Store.Dir("broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine.Store.Dir("broken"), "stream.json"), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Start(context.Background(), StartOptions{
		Name: "reports-unreadable", Repositories: []string{"acme/library"},
	}, nil)
	if err != nil {
		t.Fatalf("one truncated record must not refuse an unrelated start: %v", err)
	}
	found := false
	for _, finding := range result.Reported {
		if finding.Check == "stream-state-readable" && strings.Contains(finding.Detail, "broken") {
			found = true
		}
	}
	if !found {
		t.Fatalf("reported = %#v, want the unreadable record surfaced", result.Reported)
	}
}
