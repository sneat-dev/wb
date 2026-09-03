package streams

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MF-1. State is written BEFORE the first side effect, so a start that dies
// after the first push leaves a record `wb stream end` can retire.
//
// Requirements: dependency-streams#req:every-stream-verb-has-a-terminal-recovery.
func TestAStartInterruptedAfterTheFirstPushIsRecoverableByEnd(t *testing.T) {
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

// SHOULD-FIX: end removes the remote stream branch, after the agent pull
// requests targeting it are settled.
func TestEndDeletesTheRemoteStreamBranchAfterSettlingItsPullRequests(t *testing.T) {
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
