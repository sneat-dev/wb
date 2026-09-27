package worktrees

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

//nolint:paralleltest // newGitFixture uses t.Setenv to scope WB and Git configuration for its subprocesses.
func TestLogVerbsKeepPromptBodiesPrivateAndLocalReceiptsExplicit(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "verb-boundaries",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	initialized, err := LogInit(context.Background(), LogInitOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Prompt: []byte("starting instruction"), Model: "unknown"})
	if err != nil || !initialized.Applied {
		t.Fatalf("init = %#v, %v", initialized, err)
	}
	if _, err := LogHandoff(context.Background(), LogHandoffOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Successor: "successor"}); err == nil || !strings.Contains(err.Error(), "--summary") {
		t.Fatalf("empty handoff summary = %v", err)
	}
	if _, err := LogHandoff(context.Background(), LogHandoffOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Summary: "continue work"}); err == nil || !strings.Contains(err.Error(), "--successor") {
		t.Fatalf("empty handoff successor = %v", err)
	}
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "uncertain"}); err == nil || !strings.Contains(err.Error(), "--result") {
		t.Fatalf("invalid finalize result = %v", err)
	}
	if _, err := LogCheckpoint(context.Background(), LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, UsageDisc: "unknown", SkipRemote: true}); err == nil || !strings.Contains(err.Error(), "usage discriminator") {
		t.Fatalf("invalid usage = %v", err)
	}
	steered, err := LogSteer(context.Background(), LogSteerOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Body: []byte("keep this private"), Source: PromptSourceHuman})
	if err != nil || steered.Prompt == "" {
		t.Fatalf("steer = %#v, %v", steered, err)
	}
	shown, _, err := LogShow(context.Background(), fixture.projectsRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range shown.Prompts {
		if prompt.Body != "" {
			t.Fatalf("show leaked body: %#v", prompt)
		}
	}
	checkpoint, err := LogCheckpoint(context.Background(), LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "checkpoint", SkipRemote: true})
	if err != nil || !checkpoint.Applied || checkpoint.RemoteCheckpoint != nil {
		t.Fatalf("local checkpoint = %#v, %v", checkpoint, err)
	}
	if len(checkpoint.Notes) == 0 {
		t.Fatal("checkpoint omitted remote status")
	}
	ahead, behind, err := aheadBehind(context.Background(), worktree, created[0].BaseSHA)
	if err != nil || ahead != 0 || behind != 0 {
		t.Fatalf("ahead/behind = %d/%d, %v", ahead, behind, err)
	}
	if published, err := branchPublished(context.Background(), worktree); err != nil || published {
		t.Fatalf("unpublished branch = %t, %v", published, err)
	}
	refreshed, err := LogRefresh(context.Background(), LogRefreshOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree})
	if err != nil || !refreshed.Applied {
		t.Fatalf("refresh = %#v, %v", refreshed, err)
	}
	if _, err := LogIntegrate(context.Background(), LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Strategy: "invented"}); err == nil || !strings.Contains(err.Error(), "strategy") {
		t.Fatalf("invalid integrate strategy = %v", err)
	}
	if _, err := LogRecover(context.Background(), LogRecoverOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, EstablishClaim: true, Takeover: true}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("conflicting recovery modes = %v", err)
	}
	recovery, err := LogRecover(context.Background(), LogRecoverOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree})
	if err != nil || recovery.Applied || len(recovery.Diagnosis) == 0 {
		t.Fatalf("dry recovery = %#v, %v", recovery, err)
	}
	synced, err := LogSync(context.Background(), LogSyncOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true})
	if err != nil || !synced.Offline || synced.Applied {
		t.Fatalf("offline sync = %#v, %v", synced, err)
	}
	offer, err := LogHandoff(context.Background(), LogHandoffOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Summary: "ready for successor", Successor: "next-session"})
	if err != nil || offer.Applied {
		t.Fatalf("unapplied handoff = %#v, %v", offer, err)
	}
	finalized, err := LogFinalize(context.Background(), LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "failure", Report: []byte("not yet landed")})
	if err != nil || finalized.Applied {
		t.Fatalf("unapplied finalize = %#v, %v", finalized, err)
	}
	archived, err := LogArchive(context.Background(), LogArchiveOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Force: true})
	if err != nil || archived.Applied {
		t.Fatalf("dry archive = %#v, %v", archived, err)
	}
}

func TestSessionReceiveEntryPointsRejectIncompleteAuthorityAndEscapingPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projects := t.TempDir()
	request := sessionmove.Request{}
	if _, err := sessionMoveReceiveSpec(projects, SessionReceiveOptions{Request: request}); err == nil {
		t.Fatal("encoded empty request")
	}
	if _, err := SessionReceiveWorktreePath(projects, request); err == nil {
		t.Fatal("planned empty request")
	}
	if _, err := SessionReceiveMemberPath(projects, SessionReceiveSpec{}); err == nil {
		t.Fatal("planned empty member")
	}
	if _, err := VerifyReceivedSessionBundle(ctx, SessionReceiveOptions{ProjectsRoot: projects, Request: request}); err == nil {
		t.Fatal("verified empty bundle")
	}
	if _, err := VerifyReceivedSessionMember(ctx, SessionMemberReceiveOptions{ProjectsRoot: projects}); err == nil {
		t.Fatal("verified empty member")
	}
	if _, err := ReceiveSessionBundle(ctx, SessionReceiveOptions{ProjectsRoot: projects, Request: request}); err == nil {
		t.Fatal("received empty bundle")
	}
	if _, err := ReceiveSessionMember(ctx, SessionMemberReceiveOptions{ProjectsRoot: projects}); err == nil {
		t.Fatal("received empty member")
	}
	if _, _, err := sessionReceiveCanonicalParent(projects, filepath.Join(projects, "..", "outside")); err == nil {
		t.Fatal("accepted escaping canonical path")
	}
	parent, name, err := sessionReceiveCanonicalParent(projects, filepath.Join(projects, "github.com", "acme", "app"))
	if err != nil || parent != "github.com/acme" || name != "app" {
		t.Fatalf("canonical parent = %q, %q, %v", parent, name, err)
	}
}
