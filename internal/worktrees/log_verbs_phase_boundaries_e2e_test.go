//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// logVerbPhaseCreated supplies a real claimed checkout for native verb boundaries.
func logVerbPhaseCreated(t *testing.T, operation string) (*gitFixture, string, LocalWorkLogProjection, string) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: operation,
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create claimed fixture: %+v, %v", created, err)
	}
	worktree := created[0].WorktreeDir
	projection, err := readLocalProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	return fixture, worktree, projection, claimPath
}

func logVerbPhaseLastNewEvent(t *testing.T, before, after []LocalWorkLogEvent, expectedType string) LocalWorkLogEvent {
	t.Helper()
	if len(after) <= len(before) {
		t.Fatalf("journal did not append %s: before=%d after=%d", expectedType, len(before), len(after))
	}
	if !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatalf("journal rewrote preexisting event values: before=%+v after=%+v", before, after[:len(before)])
	}
	count := 0
	for _, event := range after[len(before):] {
		if event.Type == expectedType {
			count++
		} else if event.Type != LocalEventOwner {
			t.Fatalf("unexpected event appended before %s: %+v", expectedType, event)
		}
	}
	if count != 1 || after[len(after)-1].Type != expectedType {
		t.Fatalf("journal appended %d %s events, final event=%+v", count, expectedType, after[len(after)-1])
	}
	return after[len(after)-1]
}

func logVerbPhaseEntryNames(t *testing.T, path string) string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return strings.Join(names, "\n")
}

//nolint:paralleltest // newGitFixture sets process-wide Git and WB environment.
func TestE2ELogVerbClaimLockRefusalPrecedesEveryMutation(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, projection, claimPath := logVerbPhaseCreated(t, "log-fence-phase")
	lockPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "locks", projection.ClaimID+".lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeEvents := logVerbFileBytes(t, logVerbEventsPath(worktree))
	beforeProjection := logVerbFileBytes(t, logVerbProjectionPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	beforeHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	cases := []struct {
		name string
		run  func() error
	}{
		{"init", func() error {
			_, err := LogInit(ctx, LogInitOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Prompt: []byte("do not publish")})
			return err
		}},
		{"checkpoint", func() error {
			_, err := LogCheckpoint(ctx, LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SkipRemote: true})
			return err
		}},
		{"handoff", func() error {
			_, err := LogHandoff(ctx, LogHandoffOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Summary: "offer", Successor: "next", Apply: true})
			return err
		}},
		{"finalize", func() error {
			_, err := LogFinalize(ctx, LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "failure", Apply: true})
			return err
		}},
	}
	for _, tc := range cases {
		if err := tc.run(); err == nil || !strings.Contains(err.Error(), "claim-lock") {
			t.Fatalf("%s did not stop at held claim-lock boundary: %v", tc.name, err)
		}
		logVerbAssertFileBytes(t, tc.name, logVerbEventsPath(worktree), beforeEvents)
		logVerbAssertFileBytes(t, tc.name, logVerbProjectionPath(worktree), beforeProjection)
		logVerbAssertFileBytes(t, tc.name, claimPath, beforeClaim)
		if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != beforeHead {
			t.Fatalf("%s moved HEAD from %s to %s", tc.name, beforeHead, got)
		}
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git and WB environment.
func TestE2ELogSteerRejectsPromptSourceBeforeJournalEvent(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-steer-phase")
	beforeEvents := logVerbFileBytes(t, logVerbEventsPath(worktree))
	beforeProjection := logVerbFileBytes(t, logVerbProjectionPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	result, err := LogSteer(ctx, LogSteerOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Source: "not-a-prompt-source", Body: []byte("never publish"),
	})
	if err == nil || !strings.Contains(err.Error(), "prompt source must be one of") || result.Applied || result.Event != nil {
		t.Fatalf("steer source refusal = %+v, %v", result, err)
	}
	logVerbAssertFileBytes(t, "invalid steer source", logVerbEventsPath(worktree), beforeEvents)
	logVerbAssertFileBytes(t, "invalid steer source", logVerbProjectionPath(worktree), beforeProjection)
	logVerbAssertFileBytes(t, "invalid steer source", claimPath, beforeClaim)
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("invalid steer published prompts: %+v, %v", prompts, err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git and WB environment.
func TestE2ELogHandoffOfferSurvivesInvalidSuccessorTransfer(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, projection, claimPath := logVerbPhaseCreated(t, "log-handoff-phase")
	beforeEvents, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	beforeClaim := logVerbFileBytes(t, claimPath)
	beforeHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	outboxPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "outbox")
	beforeOutbox := logVerbPhaseEntryNames(t, outboxPath)
	beforeClaims := logVerbPhaseEntryNames(t, filepath.Dir(claimPath))
	beforeHybrid, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	result, err := LogHandoff(ctx, LogHandoffOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Summary: "handoff evidence", Successor: "bad\nsuccessor", Apply: true,
		Model: "unknown", HandoffID: "handoff-phase", TargetMachine: "target-host",
		RequestDigest: "phase-digest",
	})
	if err == nil || !strings.Contains(err.Error(), "one successor agent/session ID is required") || result.Applied {
		t.Fatalf("post-offer successor refusal = %+v, %v", result, err)
	}
	afterEvents, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	offer := logVerbPhaseLastNewEvent(t, beforeEvents, afterEvents, LocalEventHandoff)
	if offer.Type != LocalEventHandoff || offer.Result != "offered" || offer.Message != "handoff evidence" {
		t.Fatalf("last event is not the immutable offer: %+v", offer)
	}
	if offer.Extra["handoff_id"] != "handoff-phase" || offer.Extra["target_machine"] != "target-host" || offer.Extra["request_digest"] != "phase-digest" {
		t.Fatalf("offer lost exact handoff metadata: %+v", offer.Extra)
	}
	logVerbAssertFileBytes(t, "failed transfer", claimPath, beforeClaim)
	if afterOutbox := logVerbPhaseEntryNames(t, outboxPath); afterOutbox != beforeOutbox {
		t.Fatalf("failed transfer changed private outbox: before=%q after=%q", beforeOutbox, afterOutbox)
	}
	if afterClaims := logVerbPhaseEntryNames(t, filepath.Dir(claimPath)); afterClaims != beforeClaims {
		t.Fatalf("failed transfer published successor claim: before=%q after=%q", beforeClaims, afterClaims)
	}
	if afterHybrid, err := readWorkLogProjection(worktree); err != nil || afterHybrid != beforeHybrid {
		t.Fatalf("failed transfer changed Hybrid projection: before=%+v after=%+v err=%v", beforeHybrid, afterHybrid, err)
	}
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	if _, err := os.Stat(terminalPath); !os.IsNotExist(err) {
		t.Fatalf("failed transfer sealed original claim: %v", err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("failed transfer moved HEAD from %s to %s", beforeHead, got)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git and WB environment.
func TestE2ELogFinalizeReportWriteFailureLeavesClaimUnsealed(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, projection, claimPath := logVerbPhaseCreated(t, "log-finalize-phase")
	reportsPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "reports")
	if err := os.WriteFile(reportsPath, []byte("occupied report directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeClaim := logVerbFileBytes(t, claimPath)
	beforeEvents, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	result, err := LogFinalize(ctx, LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Result: "failure", Message: "failed outcome", Apply: true,
		Report: []byte("private completion report"),
	})
	if err == nil || !strings.Contains(err.Error(), "write finalize report") || result.Applied {
		t.Fatalf("report write refusal = %+v, %v", result, err)
	}
	afterEvents, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	finalize := logVerbPhaseLastNewEvent(t, beforeEvents, afterEvents, LocalEventFinalize)
	if finalize.Result != "failure" || finalize.Message != "failed outcome" {
		t.Fatalf("finalize event missing before report refusal: %+v", finalize)
	}
	logVerbAssertFileBytes(t, "failed report", claimPath, beforeClaim)
	logVerbAssertFileBytes(t, "failed report", reportsPath, []byte("occupied report directory"))
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	if _, err := os.Stat(terminalPath); !os.IsNotExist(err) {
		t.Fatalf("failed report sealed claim: %v", err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git and WB environment.
func TestE2ELogSyncRefusesRedirectedOutboxBeforeEvent(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-sync-phase")
	outboxPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogOutboxName)
	outsidePath := filepath.Join(t.TempDir(), "outside-outbox")
	outside := []byte("outside bytes stay private\n")
	if err := os.WriteFile(outsidePath, outside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(outboxPath, outboxPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, outboxPath); err != nil {
		t.Fatal(err)
	}
	beforeEvents := logVerbFileBytes(t, logVerbEventsPath(worktree))
	beforeProjection := logVerbFileBytes(t, logVerbProjectionPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	result, err := LogSync(ctx, LogSyncOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true})
	if !errors.Is(err, syscall.ELOOP) || result.Applied || result.Event != nil {
		t.Fatalf("redirected outbox accepted: %+v, %v", result, err)
	}
	logVerbAssertFileBytes(t, "redirected outbox", logVerbEventsPath(worktree), beforeEvents)
	logVerbAssertFileBytes(t, "redirected outbox", logVerbProjectionPath(worktree), beforeProjection)
	logVerbAssertFileBytes(t, "redirected outbox", claimPath, beforeClaim)
	logVerbAssertFileBytes(t, "redirected outbox", outsidePath, outside)
}

//nolint:paralleltest // newGitFixture sets process-wide Git and WB environment.
func TestE2ELogShowSeparatesMissingLocalProjectionFromUnreadableView(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-show-phase")
	projectionPath := logVerbProjectionPath(worktree)
	beforeProjection := logVerbFileBytes(t, projectionPath)
	beforeEvents := logVerbFileBytes(t, logVerbEventsPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	if err := os.Remove(projectionPath); err != nil {
		t.Fatal(err)
	}
	view, projection, err := LogShow(ctx, fixture.projectsRoot, worktree)
	if err != nil || view.Claim == nil || view.Claim.ClaimID == "" || projection != (LocalWorkLogProjection{}) {
		t.Fatalf("show with absent derived local projection = view %+v, projection %+v, err %v", view, projection, err)
	}
	if err := os.WriteFile(projectionPath, beforeProjection, 0o600); err != nil {
		t.Fatal(err)
	}
	promptsPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.Rename(promptsPath, promptsPath+".saved"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside-prompts")
	if err := os.Mkdir(outsidePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, promptsPath); err != nil {
		t.Fatal(err)
	}
	view, projection, err = LogShow(ctx, fixture.projectsRoot, worktree)
	if !errors.Is(err, syscall.ENOTDIR) || !strings.Contains(err.Error(), "open work-log journal component prompts") || view.Claim != nil || projection != (LocalWorkLogProjection{}) {
		t.Fatalf("show accepted redirected prompts: view %+v, projection %+v, err %v", view, projection, err)
	}
	if entries, err := os.ReadDir(outsidePath); err != nil || len(entries) != 0 {
		t.Fatalf("show mutated outside prompts: %+v, %v", entries, err)
	}
	logVerbAssertFileBytes(t, "read-only show", logVerbEventsPath(worktree), beforeEvents)
	logVerbAssertFileBytes(t, "read-only show", projectionPath, beforeProjection)
	logVerbAssertFileBytes(t, "read-only show", claimPath, beforeClaim)
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogInitReconstructsLegacyBeforeFirstPrompt(t *testing.T) {
	ctx := context.Background()
	worktree := newJournalWorktree(t)
	gitTest(t, worktree, "commit", "--allow-empty", "-m", "seed")
	gitTest(t, worktree, "checkout", "-b", "legacy-effort")
	result, err := LogInit(ctx, LogInitOptions{
		ProjectsRoot: t.TempDir(), Worktree: worktree, Prompt: []byte("first real instruction"),
	})
	if err != nil || !result.Applied || result.Event == nil || result.Event.Type != LocalEventInit || result.Prompt == "" {
		t.Fatalf("legacy init = %+v, %v", result, err)
	}
	manifest, err := ReadManifest(worktree)
	if err != nil || manifest.Provenance != ProvenanceReconstructed || manifest.Branch != "legacy-effort" || manifest.EffortID == "" {
		t.Fatalf("reconstructed manifest = %+v, %v", manifest, err)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "reconstructed manifest for effort "+manifest.EffortID {
		t.Fatalf("legacy reconstruction note = %+v", result.Notes)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 1 || prompts[0].Source != PromptSourceHuman {
		t.Fatalf("first real prompt = %+v, %v", prompts, err)
	}
	last, found, err := ownerPorts().LastOwner(worktree)
	if err != nil || !found || last.Effort != manifest.EffortID {
		t.Fatalf("owner after durable init = %+v, %t, %v", last, found, err)
	}
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogInitRefusesUnreadablePromptSequenceBeforeEvent(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-init-prompt-refusal")
	promptsPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.Rename(promptsPath, promptsPath+".saved"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-prompts")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, promptsPath); err != nil {
		t.Fatal(err)
	}
	beforeEvents := logVerbFileBytes(t, logVerbEventsPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	result, err := LogInit(ctx, LogInitOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Prompt: []byte("must not be published"),
	})
	if !errors.Is(err, syscall.ENOTDIR) || !strings.Contains(err.Error(), "open work-log journal component prompts") || result.Applied {
		t.Fatalf("redirected prompt-sequence refusal = %+v, %v", result, err)
	}
	logVerbAssertFileBytes(t, "unreadable prompt sequence", logVerbEventsPath(worktree), beforeEvents)
	logVerbAssertFileBytes(t, "unreadable prompt sequence", claimPath, beforeClaim)
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("init changed outside prompt directory: %+v, %v", entries, err)
	}
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogVerbEventRedirectRefusesEveryAppendPhase(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-event-phase")
	eventsPath := logVerbEventsPath(worktree)
	beforeEvents := logVerbFileBytes(t, eventsPath)
	beforeProjection := logVerbFileBytes(t, logVerbProjectionPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	if err := os.Rename(eventsPath, eventsPath+".saved"); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside-events")
	outside := []byte("outside journal bytes\n")
	if err := os.WriteFile(outsidePath, outside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, eventsPath); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{"init", func() error {
			_, err := LogInit(ctx, LogInitOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree})
			return err
		}},
		{"steer", func() error {
			_, err := LogSteer(ctx, LogSteerOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Body: []byte("prompt saved before refused event")})
			return err
		}},
		{"checkpoint", func() error {
			_, err := LogCheckpoint(ctx, LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SkipRemote: true})
			return err
		}},
		{"handoff", func() error {
			_, err := LogHandoff(ctx, LogHandoffOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Summary: "refused offer", Successor: "next", Apply: true})
			return err
		}},
		{"finalize", func() error {
			_, err := LogFinalize(ctx, LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "failure", Apply: true})
			return err
		}},
		{"sync", func() error {
			_, err := LogSync(ctx, LogSyncOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true})
			return err
		}},
	}
	for _, tc := range cases {
		if err := tc.run(); !errors.Is(err, syscall.ELOOP) {
			t.Fatalf("%s did not refuse redirected journal append: %v", tc.name, err)
		}
		logVerbAssertFileBytes(t, tc.name, eventsPath+".saved", beforeEvents)
		logVerbAssertFileBytes(t, tc.name, logVerbProjectionPath(worktree), beforeProjection)
		logVerbAssertFileBytes(t, tc.name, claimPath, beforeClaim)
		logVerbAssertFileBytes(t, tc.name, outsidePath, outside)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 1 || prompts[0].Source != PromptSourceAgent {
		t.Fatalf("steer prompt was not durably published before event refusal: %+v, %v", prompts, err)
	}
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogFinalizeRefusesBlockedTerminalAfterLocalEvent(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, projection, claimPath := logVerbPhaseCreated(t, "log-finalize-seal-phase")
	terminalDirectory := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals")
	if err := os.WriteFile(terminalDirectory, []byte("occupied terminal directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeClaim := logVerbFileBytes(t, claimPath)
	beforeEvents, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	result, err := LogFinalize(ctx, LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "failure", Apply: true,
	})
	if err == nil || result.Applied || !strings.Contains(err.Error(), "terminals") {
		t.Fatalf("blocked terminal seal = %+v, %v", result, err)
	}
	afterEvents, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	finalize := logVerbPhaseLastNewEvent(t, beforeEvents, afterEvents, LocalEventFinalize)
	if finalize.Result != "failure" {
		t.Fatalf("wrong durable result before seal refusal: %+v", finalize)
	}
	logVerbAssertFileBytes(t, "blocked terminal", claimPath, beforeClaim)
	logVerbAssertFileBytes(t, "blocked terminal", terminalDirectory, []byte("occupied terminal directory"))
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogInitAndSteerRefuseDetachedManifestReconstruction(t *testing.T) {
	ctx := context.Background()
	worktree := newJournalWorktree(t)
	gitTest(t, worktree, "commit", "--allow-empty", "-m", "seed")
	gitTest(t, worktree, "checkout", "--detach")
	beforeHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if _, err := LogInit(ctx, LogInitOptions{ProjectsRoot: t.TempDir(), Worktree: worktree, Prompt: []byte("no publish")}); err == nil || !strings.Contains(err.Error(), "cannot reconstruct a manifest for a detached HEAD") {
		t.Fatalf("init accepted detached reconstruction: %v", err)
	}
	if _, err := LogSteer(ctx, LogSteerOptions{ProjectsRoot: t.TempDir(), Worktree: worktree, Body: []byte("no publish")}); err == nil || !strings.Contains(err.Error(), "cannot reconstruct a manifest for a detached HEAD") {
		t.Fatalf("steer accepted detached reconstruction: %v", err)
	}
	if _, err := ReadManifest(worktree); !errors.Is(err, errManifestNotFound) {
		t.Fatalf("detached refusals published a manifest: %v", err)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("detached refusals published a prompt: %+v, %v", prompts, err)
	}
	events, err := readLocalEvents(worktree)
	if err != nil || len(events) != 0 {
		t.Fatalf("detached refusals published events: %+v, %v", events, err)
	}
	if afterHead := gitTestOutput(t, worktree, "rev-parse", "HEAD"); afterHead != beforeHead {
		t.Fatalf("detached refusals moved HEAD from %s to %s", beforeHead, afterHead)
	}
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogInitRefusesOccupiedJournalBeforePrompt(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-occupied-journal-phase")
	worklogPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
	if err := os.Rename(worklogPath, worklogPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worklogPath, []byte("occupied worklog directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeEvents := logVerbFileBytes(t, filepath.Join(worklogPath+".saved", localWorkLogEventsName))
	beforeClaim := logVerbFileBytes(t, claimPath)
	result, err := LogInit(ctx, LogInitOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Prompt: []byte("not published")})
	if err == nil || result.Applied || !strings.Contains(err.Error(), "worklog") {
		t.Fatalf("occupied local journal accepted: %+v, %v", result, err)
	}
	logVerbAssertFileBytes(t, "occupied local journal", filepath.Join(worklogPath+".saved", localWorkLogEventsName), beforeEvents)
	logVerbAssertFileBytes(t, "occupied local journal", claimPath, beforeClaim)
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("occupied journal published a prompt: %+v, %v", prompts, err)
	}
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogInitRefusesInvalidPromptSourceBeforeEvent(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-init-source-phase")
	beforeEvents := logVerbFileBytes(t, logVerbEventsPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	result, err := LogInit(ctx, LogInitOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Prompt: []byte("must not publish"), Source: "invalid-source",
	})
	if err == nil || !strings.Contains(err.Error(), "prompt source must be one of") || result.Applied {
		t.Fatalf("invalid init prompt source = %+v, %v", result, err)
	}
	logVerbAssertFileBytes(t, "invalid init source", logVerbEventsPath(worktree), beforeEvents)
	logVerbAssertFileBytes(t, "invalid init source", claimPath, beforeClaim)
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("invalid init source published prompt: %+v, %v", prompts, err)
	}
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2ELogFinalizeDefaultsToSuccessWithoutSealingOnDryRun(t *testing.T) {
	ctx := context.Background()
	fixture, worktree, projection, claimPath := logVerbPhaseCreated(t, "log-finalize-default-phase")
	beforeClaim := logVerbFileBytes(t, claimPath)
	result, err := LogFinalize(ctx, LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree})
	if err != nil || result.Applied || result.Event == nil || result.Event.Result != "success" || result.Event.Type != LocalEventFinalize {
		t.Fatalf("default result dry-run = %+v, %v", result, err)
	}
	logVerbAssertFileBytes(t, "default dry-run", claimPath, beforeClaim)
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	if _, err := os.Stat(terminalPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run sealed terminal: %v", err)
	}
}

//nolint:paralleltest // The Git and WB fixtures set process-wide environment.
func TestE2EOptionalClaimFenceRejectsInvalidHomeBeforeObservation(t *testing.T) {
	_, worktree, _, claimPath := logVerbPhaseCreated(t, "log-invalid-home-phase")
	beforeEvents := logVerbFileBytes(t, logVerbEventsPath(worktree))
	beforeClaim := logVerbFileBytes(t, claimPath)
	fence, err := withOptionalClaimFence("\x00", worktree, false)
	if err == nil || fence.unlock != nil || fence.home != "" {
		t.Fatalf("invalid home passed optional claim fence: %+v, %v", fence, err)
	}
	logVerbAssertFileBytes(t, "invalid home", logVerbEventsPath(worktree), beforeEvents)
	logVerbAssertFileBytes(t, "invalid home", claimPath, beforeClaim)
}
