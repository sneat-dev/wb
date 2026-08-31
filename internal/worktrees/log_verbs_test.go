package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestLogRecoverEstablishesClaimForBlankCampaignManifest(t *testing.T) {
	fixture := newGitFixture(t)
	operation := "deps-bump-npm-legacy"
	worktree := filepath.Join(fixture.home, "worktrees", operation, "acme", "app")
	branch := "wb/" + operation
	gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, worktree, "origin/main")
	baseOutput, err := gitTestRun(fixture.canonical, "rev-parse", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	baseSHA := strings.TrimSpace(baseOutput)
	if err := WriteManifest(worktree, Manifest{
		Version: 1, EffortID: operation + ".acme-app", ParentEffort: operation,
		EffortKind: EffortKindTask, Repository: "acme/app", Worktree: worktree,
		Branch: branch, Base: "main", BaseSHA: baseSHA, CreatedAt: time.Now().UTC(),
		DependencyCampaign: true,
		RunID:              "", ClaimID: "", Provenance: ProvenanceCreated,
	}); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrompt(worktree, PromptHeader{Source: PromptSourceAgent, Slug: "campaign"}, []byte("dependency campaign")); err != nil {
		t.Fatal(err)
	}
	if _, err := LogInit(context.Background(), LogInitOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(filepath.Join(worktree, ".wb", "local", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := os.ReadFile(filepath.Join(worktree, ".wb", "local", "worklog", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	dry, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, EstablishClaim: true,
	})
	if err != nil || dry.Applied || len(dry.Diagnosis) == 0 {
		t.Fatalf("dry claim recovery = %#v err=%v", dry, err)
	}
	if _, _, _, err := activeWorkLogClaim(fixture.home, worktree); err == nil {
		t.Fatal("dry claim recovery published an authoritative claim")
	}

	applied, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, EstablishClaim: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Event == nil || applied.Event.Type != LocalEventRecover || applied.Projection == nil || applied.Projection.ClaimID == "" {
		t.Fatalf("applied claim recovery = %#v", applied)
	}
	manifestAfter, err := os.ReadFile(filepath.Join(worktree, ".wb", "local", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestAfter) != string(manifestBefore) {
		t.Fatal("claim recovery rewrote the immutable manifest")
	}
	eventsAfter, err := os.ReadFile(filepath.Join(worktree, ".wb", "local", "worklog", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(eventsAfter), string(eventsBefore)) || len(eventsAfter) <= len(eventsBefore) {
		t.Fatal("claim recovery did not preserve and append to the local journal")
	}
	claim, projection, _, err := activeWorkLogClaim(fixture.home, worktree)
	if err != nil {
		t.Fatalf("recovered claim is not authoritative: %v", err)
	}
	if claim.ClaimID != applied.Projection.ClaimID || projection.ClaimID != claim.ClaimID {
		t.Fatalf("claim/projection = %s/%s, result = %s", claim.ClaimID, projection.ClaimID, applied.Projection.ClaimID)
	}
	retried, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, EstablishClaim: true, Apply: true,
	})
	if err != nil || !retried.Applied || retried.Projection == nil || retried.Projection.ClaimID != claim.ClaimID {
		t.Fatalf("idempotent claim recovery = %#v err=%v", retried, err)
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
