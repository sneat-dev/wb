package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogVerbsSteerCheckpointRefreshFinalize(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "build the mutating log verbs\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "log-verbs",
		WorkLog: WorkLogOptions{
			RunID: "log-verbs-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir

	initResult, err := LogInit(context.Background(), LogInitOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !initResult.Applied || initResult.Event == nil || initResult.Event.Type != LocalEventInit {
		t.Fatalf("init = %#v", initResult)
	}

	steer, err := LogSteer(context.Background(), LogSteerOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Body: []byte("continue with checkpoint"), Source: PromptSourceAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if steer.Prompt == "" || !strings.HasPrefix(steer.Prompt, "0001-") {
		t.Fatalf("steer prompt = %q", steer.Prompt)
	}

	checkpoint, err := LogCheckpoint(context.Background(), LogCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Message: "first durable progress", NextAction: "refresh target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Event == nil || checkpoint.Event.Git == nil || checkpoint.Event.Git.Head == "" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}

	refresh, err := LogRefresh(context.Background(), LogRefreshOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refresh.Event == nil || refresh.Event.Target == nil || refresh.Event.Target.SHA == "" {
		t.Fatalf("refresh = %#v", refresh)
	}

	show, projection, err := LogShow(context.Background(), fixture.projectsRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if show.OriginalPrompt != nil && show.OriginalPrompt.Body != "" {
		t.Fatalf("show leaked prompt body: %#v", show.OriginalPrompt)
	}
	if projection.LastSeq < 0 {
		t.Fatalf("projection = %#v", projection)
	}

	finalize, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Result: "success", Message: "done", Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalize.Event == nil || finalize.Event.Type != LocalEventFinalize || !finalize.Applied {
		t.Fatalf("finalize = %#v", finalize)
	}

	events, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("events = %#v", events)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".wb", "local", "worklog", "events.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestLogRecoverDryRunAndApply(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "recover journey\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "log-recover",
		WorkLog: WorkLogOptions{
			RunID: "log-recover-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, err := LogCheckpoint(context.Background(), LogCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "before recover",
	}); err != nil {
		t.Fatal(err)
	}

	dry, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Applied || len(dry.Diagnosis) == 0 {
		t.Fatalf("dry recover = %#v", dry)
	}

	applied, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true, Takeover: true, Actor: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Event == nil || applied.Event.Type != LocalEventRecover {
		t.Fatalf("apply recover = %#v", applied)
	}
}

func TestLogSyncStaysOffline(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "sync offline\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "log-sync",
		WorkLog: WorkLogOptions{
			RunID: "log-sync-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, err := LogCheckpoint(context.Background(), LogCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "enqueue outbox",
	}); err != nil {
		t.Fatal(err)
	}
	sync, err := LogSync(context.Background(), LogSyncOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sync.Offline || sync.Outbox < 1 {
		t.Fatalf("sync = %#v", sync)
	}
}
