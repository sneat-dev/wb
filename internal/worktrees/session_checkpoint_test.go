package worktrees

import (
	"bytes"
	"context"
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

func TestCreateSessionCheckpointPublishesOnlyTheTrackedHandoverAndRecordsAnOffer(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "session-move-source")
	// The fetch URL is the portable request identity. Publication still uses
	// origin's independently configured, logically equivalent push route.
	gitTest(t, worktree, "remote", "set-url", "--push", "origin", "file://"+fixture.remote)
	sourceHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	now := time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC)

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
	if result.Request.BundleCommit == sourceHead || result.Request.BundleCommit == "" {
		t.Fatalf("bundle commit = %q, source = %q", result.Request.BundleCommit, sourceHead)
	}
	if result.Request.HandoverPath != ".wb/handoffs/handoff-123.md" {
		t.Fatalf("handover path = %q", result.Request.HandoverPath)
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
	changed := strings.Split(gitTestOutput(t, worktree, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"), "\n")
	if len(changed) != 1 || changed[0] != result.Request.HandoverPath {
		t.Fatalf("bundle commit paths = %#v", changed)
	}
	if got := gitTestOutput(t, worktree, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("worktree is dirty after checkpoint: %q", got)
	}
	document, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(result.Request.HandoverPath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"handoff-123", "wbs-successor", "wbs-source", "laptop", "hetzner-vm1",
		"codex", "gpt-5", "native-source", sourceHead, "claude-code",
		"Continue the source checkpoint implementation.", "go test ./internal/worktrees",
		"Implement the target receiver in the next task.",
	} {
		if !strings.Contains(string(document), want) {
			t.Errorf("handover document does not contain %q:\n%s", want, document)
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
	last := events[len(events)-1]
	if last.Type != LocalEventHandoff || last.Result != "offered" || last.Extra["handoff_id"] != "handoff-123" || last.Extra["apply"] != false {
		t.Fatalf("last Work Log event = %#v", last)
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
				Now:      time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC),
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

func TestVerifySessionBundleCommitRejectsCommittedBlobMismatchWhenWorktreeMatches(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "session-move-blob-mismatch")
	options := SessionCheckpointOptions{
		ProjectsRoot:  fixture.projectsRoot,
		Worktree:      worktree,
		SourceSession: source,
		TargetMachine: "hetzner-vm1",
		Handover:      SessionHandover{Body: []byte("expected handover\n")},
		Now:           time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC),
	}
	preflight, err := preflightSessionCheckpoint(context.Background(), options, "handoff-blob-mismatch", "wbs-successor")
	if err != nil {
		t.Fatal(err)
	}
	defer preflight.close()

	wantedPath := ".wb/handoffs/handoff-blob-mismatch.md"
	wantedBytes := []byte("expected handover\n")
	committedBytes := []byte("different committed handover\n")
	absolutePath := filepath.Join(worktree, filepath.FromSlash(wantedPath))
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolutePath, committedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "--force", "--", wantedPath)
	gitTest(t, worktree, "commit", "-m", "commit mismatched handover", "--", wantedPath)
	if err := os.WriteFile(absolutePath, wantedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	// A hook can hide its restored worktree edit from status. Bundle
	// verification must therefore authenticate the committed blob itself,
	// rather than trusting either status or the ambient worktree bytes.
	gitTest(t, worktree, "update-index", "--assume-unchanged", "--", wantedPath)
	if got := gitTestOutput(t, worktree, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
		t.Fatalf("test setup must appear clean, got status %q", got)
	}
	if got, err := os.ReadFile(absolutePath); err != nil || !bytes.Equal(got, wantedBytes) {
		t.Fatalf("test setup worktree bytes = %q, err = %v", got, err)
	}

	_, err = verifySessionBundleCommit(context.Background(), preflight, wantedPath, wantedBytes)
	if err == nil || !strings.Contains(err.Error(), "committed handover bytes changed") {
		t.Fatalf("error = %v, want committed blob mismatch refusal", err)
	}
}

func TestCreateSessionCheckpointRefusesSanitizedPostCommitRemoteRedirectBeforePush(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "session-move-remote-redirect")
	hooksPath := gitTestOutput(t, worktree, "rev-parse", "--git-path", "hooks")
	if !filepath.IsAbs(hooksPath) {
		hooksPath = filepath.Join(worktree, hooksPath)
	}
	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := []byte("#!/bin/sh\ngit config remote.origin.pushurl 'https://user:top-secret@example.invalid/acme/app.git'\n")
	if err := os.WriteFile(filepath.Join(hooksPath, "post-commit"), hook, 0o755); err != nil {
		t.Fatal(err)
	}
	branch := gitTestOutput(t, worktree, "branch", "--show-current")

	_, err := CreateSessionCheckpoint(context.Background(), SessionCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SourceSession: source,
		TargetMachine: "hetzner-vm1", HandoffID: "handoff-redirect", SuccessorWBSessionID: "wbs-successor",
		Handover: SessionHandover{Body: []byte("continue safely\n")},
		Now:      time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "origin remote changed after handover commit") {
		t.Fatalf("error = %v, want post-commit remote redirect refusal", err)
	}
	if strings.Contains(err.Error(), "top-secret") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("remote redirect refusal exposed credential-bearing URL: %v", err)
	}
	if got := remoteBranchTipIfPresent(t, worktree, branch); got != "" {
		t.Fatalf("remote branch advanced despite pre-push redirect refusal: %s", got)
	}
}

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
		Now:      time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC),
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

func remoteBranchTipIfPresent(t *testing.T, worktree, branch string) string {
	t.Helper()
	return remoteBranchTipFrom(t, worktree, "origin", branch)
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
