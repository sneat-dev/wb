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

func TestLogRefreshResolvesEarlierFetchFailureWithoutHidingIntegrateConflict(t *testing.T) {
	now := time.Now().UTC()
	failure := LocalWorkLogEvent{Version: 1, Seq: 0, ID: "failed-fetch", Type: LocalEventRefreshNeed, At: now, Conflict: "fetch_failed", Target: &LocalTargetEvidence{Ref: "origin/main"}}
	success := LocalWorkLogEvent{Version: 1, Seq: 1, ID: "fetched-target", Type: LocalEventRefresh, At: now.Add(time.Second), Target: &LocalTargetEvidence{Ref: "origin/main", SHA: strings.Repeat("a", 40)}}
	projection, err := rebuildLocalProjection([]LocalWorkLogEvent{failure, success})
	if err != nil || projection.Conflict != "" || projection.LastTarget == nil || projection.LastTarget.SHA != success.Target.SHA {
		t.Fatalf("successful refresh left stale fetch failure: projection=%#v err=%v", projection, err)
	}
	projection, err = rebuildLocalProjection([]LocalWorkLogEvent{success, failure})
	if err != nil || projection.Conflict != "fetch_failed" {
		t.Fatalf("latest fetch failure disappeared: projection=%#v err=%v", projection, err)
	}
	integration := LocalWorkLogEvent{Version: 1, Seq: 1, ID: "merge-conflict", Type: LocalEventIntegrate, At: now.Add(time.Second), Conflict: "integrate_conflict", Target: success.Target}
	success.Seq = 2
	projection, err = rebuildLocalProjection([]LocalWorkLogEvent{failure, integration, success})
	if err != nil || projection.Conflict != "integrate_conflict" {
		t.Fatalf("successful refresh hid merge conflict: projection=%#v err=%v", projection, err)
	}
}

func TestLogIntegrateAfterRecoveredFetchFailure(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "log-fetch-recovered", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, _, err := appendLocalEvent(worktree, LocalWorkLogEvent{Type: LocalEventRefreshNeed, Conflict: "fetch_failed", Target: &LocalTargetEvidence{Ref: "origin/main"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := LogCheckpoint(context.Background(), LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "clean checkpoint"}); err != nil {
		t.Fatal(err)
	}
	refreshed, err := LogRefresh(context.Background(), LogRefreshOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main"})
	if err != nil || refreshed.Projection == nil || refreshed.Projection.Conflict != "" || refreshed.Projection.LastTarget == nil || refreshed.Projection.LastTarget.SHA == "" {
		t.Fatalf("recovered refresh = %#v, err=%v", refreshed, err)
	}
	integrated, err := LogIntegrate(context.Background(), LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main", Strategy: "merge"})
	if err != nil || !integrated.Applied || integrated.Projection == nil || integrated.Projection.Conflict != "" {
		t.Fatalf("integrate after recovered fetch = %#v, err=%v", integrated, err)
	}
}

func TestLogHandoffRecordsOfferBeforeTransferringClaim(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "handoff-offer",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	before, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := LogHandoff(context.Background(), LogHandoffOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Summary: " hand over the current feature ", NextAction: "review the patch",
		Successor: "next-session", HandoffID: "handoff-1", TargetMachine: "machine-b",
		BundleCommit: created[0].BaseSHA, SourceWorkLogReference: "source-log",
		PredecessorWBSessionID: "prior-session", SourceMachine: "machine-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if offer.Applied || offer.Event == nil || offer.Event.Type != LocalEventHandoff ||
		offer.Event.Message != "hand over the current feature" || offer.Event.NextAction != "review the patch" ||
		offer.Event.Git == nil || offer.Event.Git.Head == "" || offer.Event.Extra["apply"] != false ||
		offer.Event.Extra["handoff_id"] != "handoff-1" || offer.Event.Extra["target_machine"] != "machine-b" ||
		offer.Event.Extra["bundle_commit"] != created[0].BaseSHA ||
		offer.Event.Extra["source_work_log_reference"] != "source-log" ||
		offer.Event.Extra["predecessor_wb_session_id"] != "prior-session" ||
		offer.Event.Extra["source_machine"] != "machine-a" {
		t.Fatalf("handoff offer lost journal evidence: %#v", offer)
	}
	if current, err := readWorkLogProjection(worktree); err != nil || current != before {
		t.Fatalf("unapplied handoff changed claim: before=%#v after=%#v err=%v", before, current, err)
	}
	applied, err := LogHandoff(context.Background(), LogHandoffOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Summary: "successor accepted",
		Successor: "next-session", Model: "unknown", Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Event == nil || applied.Event.Extra["apply"] != true {
		t.Fatalf("applied handoff = %#v", applied)
	}
	after, err := readWorkLogProjection(worktree)
	if err != nil || after.ClaimID == before.ClaimID || after.Lifecycle != "active" {
		t.Fatalf("handoff successor claim = %#v, err=%v; prior=%#v", after, err, before)
	}
}

func TestObserveUsageRequiresProvenanceAndTotalsProvidedTokens(t *testing.T) {
	input, output := int64(13), int64(7)
	cost := 0.25
	if usage, err := observeUsage("", nil, nil, nil, "", ""); err != nil || usage != nil {
		t.Fatalf("absent usage = (%#v, %v), want no observation", usage, err)
	}
	if _, err := observeUsage("", &input, nil, nil, "", ""); err == nil || !strings.Contains(err.Error(), "--usage-discriminator") {
		t.Fatalf("tokens without provenance = %v", err)
	}
	if _, err := observeUsage("unknown", nil, nil, nil, "", ""); err == nil || !strings.Contains(err.Error(), "usage discriminator") {
		t.Fatalf("unknown provenance = %v", err)
	}
	for _, tc := range []struct {
		name  string
		input *int64
		out   *int64
		want  int64
	}{
		{"input only", &input, nil, input},
		{"output only", nil, &output, output},
		{"both", &input, &output, input + output},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, err := observeUsage(" provider_reported ", tc.input, tc.out, &cost, " USD ", " receipt-1 ")
			if err != nil || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != tc.want ||
				usage.Discriminator != "provider_reported" || usage.Currency != "USD" || usage.ProviderRef != "receipt-1" ||
				usage.EstimatedCost == nil || *usage.EstimatedCost != cost {
				t.Fatalf("usage observation = (%#v, %v), want %d token total with provenance", usage, err, tc.want)
			}
		})
	}
	usage, err := observeUsage("unavailable", nil, nil, nil, "", "")
	if err != nil || usage == nil || usage.TotalTokens != nil {
		t.Fatalf("unavailable token observation = (%#v, %v), want no invented total", usage, err)
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

func TestLogIntegrateAcceptsCheckpointedManualConflictResolution(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "log-integrate-resolved-conflict",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "README.md")
	gitTest(t, worktree, "commit", "-m", "source change")
	if _, err := LogCheckpoint(context.Background(), LogCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "source ready",
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(fixture.canonical, "README.md"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "README.md")
	gitTest(t, fixture.canonical, "commit", "-m", "target change")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	conflictedTarget := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")

	conflicted, err := LogIntegrate(context.Background(), LogIntegrateOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main", Strategy: "merge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if conflicted.Applied || conflicted.Projection == nil || conflicted.Projection.Conflict != "integrate_conflict" {
		t.Fatalf("conflicted integrate = %#v", conflicted)
	}
	if _, err := LogIntegrate(context.Background(), LogIntegrateOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main", Strategy: "merge",
	}); err == nil || !strings.Contains(err.Error(), "does not contain attempted target") {
		t.Fatalf("unresolved checkpoint retry error = %v", err)
	}

	if _, err := gitTestRun(worktree, "merge", "--no-edit", conflictedTarget); err == nil {
		t.Fatal("manual merge unexpectedly avoided the recorded conflict")
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("source and target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "README.md")
	gitTest(t, worktree, "commit", "-m", "resolve target conflict")
	if _, err := LogCheckpoint(context.Background(), LogCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "conflict resolved",
	}); err != nil {
		t.Fatal(err)
	}

	fixture.pushRemoteCommit(t, "next target change")
	integrated, err := LogIntegrate(context.Background(), LogIntegrateOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main", Strategy: "merge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !integrated.Applied || integrated.Projection == nil || integrated.Projection.Conflict != "resolved" {
		t.Fatalf("integrated after manual resolution = %#v", integrated)
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
