package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestCreateSessionCheckpointNeverWritesIntoTheSourceRepo is the regression
// test for the ContinuationPrivate cutover: a handoff must never again be
// committed, staged, or otherwise written into the repo under work. It
// captures the complete tracked-file list and full worktree byte state
// before the checkpoint and asserts neither changed, on top of the ordinary
// positive-path assertions that the handover instead travels inline on
// Request.HandoverContent and the source's own HEAD is what gets pinned and
// pushed unchanged.
func TestCreateSessionCheckpointNeverWritesIntoTheSourceRepo(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "session-move-source")
	// The fetch URL is the portable request identity. Publication still uses
	// origin's independently configured, logically equivalent push route.
	gitTest(t, worktree, "remote", "set-url", "--push", "origin", "file://"+fixture.remote)
	sourceHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	trackedFilesBefore := gitTestOutput(t, worktree, "ls-files")
	now := source.StartedAt.Add(time.Second)

	result, err := CreateSessionCheckpoint(context.Background(), SessionCheckpointOptions{
		ProjectsRoot:         fixture.projectsRoot,
		Worktree:             worktree,
		SourceSession:        source,
		TargetMachine:        "hetzner-vm1",
		RequestedHarness:     "claude-code",
		HandoffID:            "handoff-123",
		SuccessorWBSessionID: "wbs-successor",
		Handover: SessionHandover{
			Summary:            "Continue the source checkpoint implementation.",
			ValidationEvidence: "go test ./internal/worktrees",
			RemainingWork:      "Implement the target receiver in the next task.",
			Body:               []byte("Preserve the exact request digest when delivering.\n"),
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("create session checkpoint: %v", err)
	}

	if result.Request.SourceWorkCommit != sourceHead {
		t.Fatalf("source work commit = %q, want %q", result.Request.SourceWorkCommit, sourceHead)
	}
	if result.Request.RepositoryRemote != fixture.remote {
		t.Fatalf("repository remote = %q, want credential-free fetch URL %q", result.Request.RepositoryRemote, fixture.remote)
	}
	// No commit is created on top of the source's own work: the exact commit
	// pinned for the successor is the source commit itself.
	if result.Request.BundleCommit != sourceHead {
		t.Fatalf("bundle commit = %q, want exact source commit %q", result.Request.BundleCommit, sourceHead)
	}
	if result.Request.HandoverPath != "" {
		t.Fatalf("handover path = %q, want empty: the handover must not name a path in the repo under work", result.Request.HandoverPath)
	}
	if result.Request.HandoverContent == "" || result.Request.HandoverContent != string(result.HandoverBytes) {
		t.Fatalf("handover content = %q, want it to carry the rendered document inline", result.Request.HandoverContent)
	}
	if result.Digest != sessionmove.DigestBytes(result.RequestBytes) {
		t.Fatalf("request digest = %q, computed %q", result.Digest, sessionmove.DigestBytes(result.RequestBytes))
	}
	if result.Request.HandoverDigest != sessionmove.DigestBytes(result.HandoverBytes) {
		t.Fatalf("handover digest = %q, computed %q", result.Request.HandoverDigest, sessionmove.DigestBytes(result.HandoverBytes))
	}

	remoteTip := remoteBranchTip(t, worktree, result.Request.Branch)
	if remoteTip != result.Request.BundleCommit {
		t.Fatalf("remote tip = %q, want exact bundle %q", remoteTip, result.Request.BundleCommit)
	}
	// HEAD, the tracked file list, and the full worktree status must be
	// byte-for-byte unchanged: the checkpoint mutates WB's own private
	// handoff store, never the repo under work.
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != sourceHead {
		t.Fatalf("HEAD moved from %s to %s", sourceHead, got)
	}
	if got := gitTestOutput(t, worktree, "ls-files"); got != trackedFilesBefore {
		t.Fatalf("tracked files changed:\nbefore: %q\nafter:  %q", trackedFilesBefore, got)
	}
	if got := gitTestOutput(t, worktree, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
		t.Fatalf("worktree is not clean after checkpoint: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".wb", "handoffs")); !os.IsNotExist(statErr) {
		t.Fatalf("handoff directory exists in the repo under work: %v", statErr)
	}
	for _, want := range []string{
		"handoff-123", "wbs-successor", "wbs-source", "laptop", "hetzner-vm1",
		"codex", "gpt-5", "native-source", sourceHead, "claude-code",
		"Continue the source checkpoint implementation.", "go test ./internal/worktrees",
		"Implement the target receiver in the next task.",
	} {
		if !strings.Contains(result.Request.HandoverContent, want) {
			t.Errorf("handover document does not contain %q:\n%s", want, result.Request.HandoverContent)
		}
	}

	store := sessionmove.NewStore(filepath.Join(fixture.home, sessionmove.DirName))
	state, err := store.Load("handoff-123")
	if err != nil {
		t.Fatalf("load durable handoff: %v", err)
	}
	if len(state.Events) != 1 || state.Events[0].Phase != sessionmove.PhaseOffered || state.Receipt != nil {
		t.Fatalf("handoff state = %#v", state)
	}
	if state.Digest != result.Digest || state.Request.BundleCommit != result.Request.BundleCommit {
		t.Fatalf("durable request = %#v, result = %#v", state, result)
	}

	events, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("Work Log events = %#v, want offer followed by exact owner evidence", events)
	}
	offer, owner := events[len(events)-2], events[len(events)-1]
	if offer.Type != LocalEventHandoff || offer.Result != "offered" || offer.Extra["handoff_id"] != "handoff-123" || offer.Extra["apply"] != false {
		t.Fatalf("source offer Work Log event = %#v", offer)
	}
	if owner.Type != LocalEventOwner || owner.Owner == nil || owner.Message != "predecessor session owns offered external handoff" ||
		owner.Extra["handoff_id"] != "handoff-123" || owner.Owner.PID != source.PID {
		t.Fatalf("source owner Work Log event = %#v", owner)
	}
	home, err := wbhomeRootForTest(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, projection, _, err := activeWorkLogClaim(home, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Lifecycle != "active" {
		t.Fatalf("source claim lifecycle = %q, want active", projection.Lifecycle)
	}
}

func TestCreateSessionCheckpointRejectsOfferTimestampBeforeSourceStartWithoutMutation(t *testing.T) {
	startedAt := time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC)
	_, err := CreateSessionCheckpoint(context.Background(), SessionCheckpointOptions{
		ProjectsRoot: t.TempDir(), Worktree: t.TempDir(), TargetMachine: "hetzner-vm1",
		SourceSession: session.Record{PID: os.Getpid(), WBSessionID: "wbs-source", Machine: "laptop",
			Runtime: "codex", StartedAt: startedAt},
		Handover: SessionHandover{Summary: "continue"}, Now: startedAt.Add(-time.Nanosecond),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot precede") {
		t.Fatalf("checkpoint timestamp error=%v", err)
	}
}

func TestCreateSessionCheckpointReturnsAdmittedIdentityAfterPostAdmissionFailure(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "session-move-post-admission-failure")
	injected := errors.New("injected after durable admission")
	options := SessionCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SourceSession: source,
		TargetMachine: "hetzner-vm1", HandoffID: "handoff-admitted", SuccessorWBSessionID: "wbs-successor",
		Handover: SessionHandover{Summary: "continue from the admitted checkpoint"},
		Now:      source.StartedAt.Add(time.Second),
		afterAdmission: func() error {
			return injected
		},
	}

	result, err := CreateSessionCheckpoint(context.Background(), options)
	if !errors.Is(err, injected) {
		t.Fatalf("checkpoint error = %v, want injected post-admission failure", err)
	}
	if result.Request.HandoffID != options.HandoffID || result.Request.SuccessorWBSessionID != options.SuccessorWBSessionID {
		t.Fatalf("partial result lost resumable identity: %#v", result.Request)
	}
	if result.Digest == "" || result.Digest != sessionmove.DigestBytes(result.RequestBytes) {
		t.Fatalf("partial result digest = %q for request bytes", result.Digest)
	}
	if result.Request.HandoverDigest != sessionmove.DigestBytes(result.HandoverBytes) {
		t.Fatalf("partial handover digest = %q for handover bytes", result.Request.HandoverDigest)
	}
	if result.WorkLogEvent.ID != "" {
		t.Fatalf("partial result claims unrepaired Work Log evidence: %#v", result.WorkLogEvent)
	}

	store := sessionmove.NewStore(filepath.Join(fixture.home, sessionmove.DirName))
	state, err := store.Load(options.HandoffID)
	if err != nil {
		t.Fatalf("load admitted checkpoint: %v", err)
	}
	if state.Request != result.Request || state.Digest != result.Digest || len(state.Events) != 0 {
		t.Fatalf("admitted boundary state = %#v, partial result = %#v", state, result)
	}

	lock, err := store.AcquireExecutionLock(context.Background(), options.HandoffID, result.Digest)
	if err != nil {
		t.Fatalf("acquire admitted checkpoint for resume: %v", err)
	}
	defer func() { _ = lock.Close() }()
	evidence, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: store, ExecutionLock: lock, ProjectsRoot: fixture.projectsRoot,
		Request: result.Request, RequestDigest: result.Digest, SourceSession: source,
	})
	if err != nil {
		t.Fatalf("resume admitted source evidence: %v", err)
	}
	if evidence.OfferEvent.ID == "" || evidence.OwnerEvent.ID == "" {
		t.Fatalf("resumed source evidence = %#v", evidence)
	}
	repaired, err := store.LoadUnderLock(lock, options.HandoffID, result.Digest)
	if err != nil {
		t.Fatalf("load repaired checkpoint: %v", err)
	}
	if len(repaired.Events) != 1 || repaired.Events[0].Phase != sessionmove.PhaseOffered {
		t.Fatalf("repaired aggregate events = %#v", repaired.Events)
	}
}

func TestCreateSessionCheckpointRefusesBeforeAnyMutation(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, fixture *gitFixture, worktree string, options *SessionCheckpointOptions)
		want    string
	}{
		{
			name: "empty handover",
			prepare: func(_ *testing.T, _ *gitFixture, _ string, options *SessionCheckpointOptions) {
				options.Handover = SessionHandover{}
			},
			want: "handover must not be empty",
		},
		{
			name: "dirty worktree",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				if err := os.WriteFile(filepath.Join(worktree, "dirty.txt"), []byte("not committed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "worktree is dirty",
		},
		{
			name: "detached head",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "checkout", "--detach")
			},
			want: "named branch",
		},
		{
			name: "live session does not own work log",
			prepare: func(_ *testing.T, _ *gitFixture, _ string, options *SessionCheckpointOptions) {
				options.SourceSession.PID = 999999
			},
			want: "does not own",
		},
		{
			name: "unmanaged work log",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				if err := os.RemoveAll(filepath.Join(worktree, workLogProjectionDirectory)); err != nil {
					t.Fatal(err)
				}
			},
			want: "active managed Work Log",
		},
		{
			name: "missing remote",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "remote", "remove", "origin")
			},
			want: "no usable origin",
		},
		{
			name: "credential bearing fetch remote",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "remote", "set-url", "origin", "https://user:secret@example.invalid/acme/app.git")
			},
			want: "origin fetch remote is unsafe",
		},
		{
			name: "credential bearing push remote",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "remote", "set-url", "--push", "origin", "https://user:secret@example.invalid/acme/app.git")
			},
			want: "origin push remote is unsafe",
		},
		{
			name: "query bearing fetch remote",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "remote", "set-url", "origin", "https://example.invalid/acme/app.git?token=secret")
			},
			want: "origin fetch remote is unsafe",
		},
		{
			name: "fragment bearing push remote",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "remote", "set-url", "--push", "origin", "https://example.invalid/acme/app.git#fragment")
			},
			want: "origin push remote is unsafe",
		},
		{
			name: "fetch and push identify different repositories",
			prepare: func(t *testing.T, _ *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "remote", "set-url", "--push", "origin", "https://evil.example/acme/app.git")
			},
			want: "origin fetch and push remotes identify different repositories",
		},
		{
			name: "multiple push routes",
			prepare: func(t *testing.T, fixture *gitFixture, worktree string, _ *SessionCheckpointOptions) {
				gitTest(t, worktree, "remote", "set-url", "--add", "--push", "origin", fixture.remote)
				gitTest(t, worktree, "remote", "set-url", "--add", "--push", "origin", "file://"+fixture.remote)
			},
			want: "no usable origin push remote",
		},
		{
			name:    "remote rejects non force publication",
			prepare: makeRemoteBranchAhead,
			want:    "cannot be pushed without force",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "refuse-"+strings.ReplaceAll(test.name, " ", "-"))
			options := SessionCheckpointOptions{
				ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SourceSession: source,
				TargetMachine: "hetzner-vm1", HandoffID: "handoff-refused", SuccessorWBSessionID: "wbs-successor",
				Handover: SessionHandover{Summary: "summary", ValidationEvidence: "tests pass", RemainingWork: "continue", Body: []byte("details\n")},
				Now:      source.StartedAt.Add(time.Second),
			}
			test.prepare(t, fixture, worktree, &options)
			headBefore := gitTestOutput(t, worktree, "rev-parse", "HEAD")
			statusBefore := gitTestOutput(t, worktree, "status", "--porcelain=v1")
			eventsBefore, err := readLocalEvents(worktree)
			if err != nil {
				t.Fatal(err)
			}

			_, err = CreateSessionCheckpoint(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("refusal exposed credential-bearing remote: %v", err)
			}
			if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != headBefore {
				t.Fatalf("HEAD changed from %s to %s", headBefore, got)
			}
			if got := gitTestOutput(t, worktree, "status", "--porcelain=v1"); got != statusBefore {
				t.Fatalf("status changed from %q to %q", statusBefore, got)
			}
			if _, statErr := os.Stat(filepath.Join(worktree, ".wb", "handoffs")); !os.IsNotExist(statErr) {
				t.Fatalf("handoff directory exists after refusal: %v", statErr)
			}
			eventsAfter, eventErr := readLocalEvents(worktree)
			if eventErr != nil {
				t.Fatal(eventErr)
			}
			if len(eventsAfter) != len(eventsBefore) {
				t.Fatalf("Work Log changed from %d to %d events", len(eventsBefore), len(eventsAfter))
			}
			if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, "handoff-refused")); !os.IsNotExist(statErr) {
				t.Fatalf("durable handoff exists after refusal: %v", statErr)
			}
		})
	}
}

// Two tests formerly lived here:
//
//   - TestVerifySessionBundleCommitRejectsCommittedBlobMismatchWhenWorktreeMatches
//     tested verifySessionBundleCommit, which authenticated the blob of a
//     commit WB generated inside the source worktree. That function is
//     deleted along with the commit it verified: the ContinuationPrivate
//     cutover means CreateSessionCheckpoint no longer creates any commit in
//     the repo under work, so there is no generated commit left for a hook
//     to tamper with.
//   - TestCreateSessionCheckpointRefusesSanitizedPostCommitRemoteRedirectBeforePush
//     used a real post-commit Git hook to prove a hook rewriting
//     remote.origin.pushurl right after WB's generated commit was caught
//     before publication. That trigger point is also gone for the same
//     reason: nothing commits in the source worktree anymore, so a
//     post-commit hook never fires during a checkpoint.
//
// The surviving config-changed-around-a-mutating-git-operation threat is the
// exact same one TestCreateSessionCheckpointUsesAuthenticatedExactPushURLDespiteOriginConfigTOCTOU
// below already covers, deterministically, via the afterPushRemoteAuthentication
// test seam, so that coverage is not reproduced with a different Git hook.

func TestCreateSessionCheckpointUsesAuthenticatedExactPushURLDespiteOriginConfigTOCTOU(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "session-move-push-url-toctou")
	wrongRemote := filepath.Join(filepath.Dir(fixture.projectsRoot), "wrong-remotes", "acme", "app.git")
	if err := os.MkdirAll(filepath.Dir(wrongRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(wrongRemote), "init", "--bare", "--initial-branch=main", wrongRemote)
	options := SessionCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SourceSession: source,
		TargetMachine: "hetzner-vm1", HandoffID: "handoff-push-toctou", SuccessorWBSessionID: "wbs-successor",
		Handover: SessionHandover{Body: []byte("continue safely\n")},
		Now:      source.StartedAt.Add(time.Second),
	}
	options.afterPushRemoteAuthentication = func() {
		gitTest(t, worktree, "remote", "set-url", "--push", "origin", wrongRemote)
	}
	branch := gitTestOutput(t, worktree, "branch", "--show-current")

	_, err := CreateSessionCheckpoint(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "origin remote changed while publishing") {
		t.Fatalf("error = %v, want post-push origin reauthentication refusal", err)
	}
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if got := remoteBranchTipFrom(t, worktree, fixture.remote, branch); got != head {
		t.Fatalf("authenticated push remote tip = %q, want %q", got, head)
	}
	if got := remoteBranchTipFrom(t, worktree, wrongRemote, branch); got != "" {
		t.Fatalf("mutable origin redirected push to wrong remote at %s", got)
	}
}

func newSessionCheckpointFixture(t *testing.T, operation string) (*gitFixture, string, session.Record) {
	t.Helper()
	neutralizeGitSigning(t)
	fixture := newGitFixture(t)
	now := time.Now().UTC().Add(-time.Second)
	source := session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex",
		Model: "gpt-5", NativeHarnessID: "native-source", StartedAt: now,
	}
	t.Setenv(EnvAgentPID, fmt.Sprint(source.PID))
	t.Setenv(EnvAgentRuntime, source.Runtime)
	t.Setenv(EnvAgentModel, source.Model)
	t.Setenv(EnvAgentID, source.NativeHarnessID)
	prompt := writeWorkLogPromptFile(t, "implement a source session checkpoint\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		WorkLog: WorkLogOptions{
			RunID: operation + "-run", Model: source.Model, AgentRuntime: source.Runtime,
			AgentID: source.NativeHarnessID, OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, created[0].WorktreeDir, source
}

func neutralizeGitSigning(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgSign")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
}

func remoteBranchTip(t *testing.T, worktree, branch string) string {
	t.Helper()
	fields := strings.Fields(gitTestOutput(t, worktree, "ls-remote", "--heads", "origin", "refs/heads/"+branch))
	if len(fields) != 2 {
		t.Fatalf("unexpected remote branch response: %#v", fields)
	}
	return fields[0]
}

func remoteBranchTipFrom(t *testing.T, worktree, remote, branch string) string {
	t.Helper()
	fields := strings.Fields(gitTestOutput(t, worktree, "ls-remote", "--heads", "--", remote, "refs/heads/"+branch))
	if len(fields) == 0 {
		return ""
	}
	if len(fields) != 2 {
		t.Fatalf("unexpected remote branch response: %#v", fields)
	}
	return fields[0]
}

func makeRemoteBranchAhead(t *testing.T, fixture *gitFixture, worktree string, _ *SessionCheckpointOptions) {
	t.Helper()
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	gitTest(t, worktree, "push", "-u", "origin", "HEAD:refs/heads/"+branch)
	writer := filepath.Join(filepath.Dir(fixture.projectsRoot), "session-move-remote-writer")
	command := exec.Command("git", "clone", "--branch", branch, fixture.remote, writer)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone remote writer: %v\n%s", err, output)
	}
	neutralizeGitSigning(t)
	configureGitUser(t, writer)
	if err := os.WriteFile(filepath.Join(writer, "remote-change.txt"), []byte("remote advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, writer, "add", "remote-change.txt")
	gitTest(t, writer, "commit", "-m", "advance remote")
	gitTest(t, writer, "push", "origin", "HEAD:refs/heads/"+branch)
}

func wbhomeRootForTest(projectsRoot string) (string, error) {
	return wbhome.Root(projectsRoot)
}
