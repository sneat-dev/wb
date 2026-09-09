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

// TestLogFinalizeReportRecordsTerminalEvidenceAndListFilters proves the
// wb worktree log finalize --report journey at the library level: the report
// body lands under WB_HOME (never inside the worktree/source Git), the sealed
// terminal/outbox carry terminal_result/terminal_message/report_path,
// ListWithDiagnostics surfaces those on the still-live worktree, the
// Finalized filter selects on that evidence, and LoadWorkLogView's redaction
// split holds: only IncludePromptBodies=true returns the report body.
func TestLogFinalizeReportRecordsTerminalEvidenceAndListFilters(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "finalize with a report\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "log-finalize-report",
		WorkLog: WorkLogOptions{
			RunID: "log-finalize-report-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir

	if _, err := LogInit(context.Background(), LogInitOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
	}); err != nil {
		t.Fatal(err)
	}

	reportBody := []byte("# Report\n\nEverything shipped.\n")
	finalize, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Result: "success", Message: "shipped it", Apply: true, Report: reportBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finalize.Applied {
		t.Fatalf("finalize = %#v", finalize)
	}

	results, err := List(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "log-finalize-report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("list results = %#v", results)
	}
	result := results[0]
	if result.TerminalResult != "success" || result.TerminalMessage != "shipped it" || result.ReportPath == "" || result.FinalizedAt.IsZero() {
		t.Fatalf("list did not surface finalize evidence: %#v", result)
	}
	if strings.Contains(result.ReportPath, worktree) {
		t.Fatalf("report path %q leaked into the worktree; it must live under WB_HOME only", result.ReportPath)
	}
	onDisk, err := os.ReadFile(result.ReportPath)
	if err != nil || string(onDisk) != string(reportBody) {
		t.Fatalf("report on disk = %q, err=%v, want %q", onDisk, err, reportBody)
	}

	// --finalized / --not-finalized (Finalized filter) select on exactly this.
	trueFilter := true
	finalizedOnly, err := List(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "log-finalize-report", Finalized: &trueFilter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(finalizedOnly) != 1 {
		t.Fatalf("Finalized=true results = %#v", finalizedOnly)
	}
	falseFilter := false
	notFinalized, err := List(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "log-finalize-report", Finalized: &falseFilter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(notFinalized) != 0 {
		t.Fatalf("Finalized=false results = %#v, want none", notFinalized)
	}

	// The bare dump (agent bootstrap) carries the exact report body.
	dump, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, IncludePromptBodies: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dump.Terminal == nil || dump.Terminal.TerminalResult != "success" || dump.Terminal.Disposition != "landed" {
		t.Fatalf("bare dump terminal = %#v", dump.Terminal)
	}
	if dump.FinalizeReportBody != string(reportBody) {
		t.Fatalf("bare dump report body = %q, want %q", dump.FinalizeReportBody, reportBody)
	}

	// The redacted show never carries the body.
	show, _, err := LogShow(context.Background(), fixture.projectsRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if show.Terminal == nil || show.Terminal.ReportPath == "" {
		t.Fatalf("redacted show missing terminal metadata: %#v", show.Terminal)
	}
	if show.FinalizeReportBody != "" {
		t.Fatalf("redacted show leaked the report body: %q", show.FinalizeReportBody)
	}
}

// TestLogFinalizeReportRejectsOversizedBody proves the 1 MiB cap is enforced
// by the library itself (defense in depth behind the CLI's own check) and
// that a rejected report never mutates the claim.
func TestLogFinalizeReportRejectsOversizedBody(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "finalize with an oversized report\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "log-finalize-oversized",
		WorkLog: WorkLogOptions{
			RunID: "log-finalize-oversized-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, err := LogInit(context.Background(), LogInitOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
	}); err != nil {
		t.Fatal(err)
	}

	oversized := make([]byte, MaxFinalizeReportBytes+1)
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Result: "success", Apply: true, Report: oversized,
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized report err = %v, want an exceeds-cap error", err)
	}

	// The claim is untouched: a normal finalize still succeeds afterward.
	finalize, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Result: "success", Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finalize.Applied {
		t.Fatalf("finalize after rejected oversized report = %#v", finalize)
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
