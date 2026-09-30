package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type workLogCoverageBatchFixture struct {
	projectsRoot string
	home         string
	worktree     string
	task         string
	result       CreateResult
	options      WorkLogOptions
	outcome      WorkLogPublicationOutcome
}

func newWorkLogCoverageBatchFixture(t *testing.T, task string) workLogCoverageBatchFixture {
	t.Helper()
	gitFixture := newGitFixture(t)
	projectsRoot := gitFixture.projectsRoot
	home := gitFixture.home
	worktree := gitFixture.canonical
	gitEvidence := observeLocalGit(context.Background(), worktree)
	result := CreateResult{
		Repository: "acme/app", WorktreeDir: worktree, Branch: gitEvidence.Branch,
		Base: "main", BaseSHA: gitEvidence.Head,
	}
	options, err := (WorkLogOptions{
		EffortID: task, RunID: "run", Model: "unknown",
	}).WithOriginalPromptFromStdin([]byte("cover the work log lifecycle\n"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := recordWorkLogWithHooks(home, task, result, options, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}
	return workLogCoverageBatchFixture{
		projectsRoot: projectsRoot, home: home, worktree: worktree,
		task: task, result: result, options: options, outcome: outcome,
	}
}

//nolint:paralleltest // the shared Git fixture mutates WB home and XDG environment variables.
func TestWorkLogCoverageBatchPublicationAndPromptReuse(t *testing.T) {
	fixture := newWorkLogCoverageBatchFixture(t, "worklog-batch")

	if _, err := recordWorkLogWithHooks(fixture.home, "bad-options", fixture.result,
		WorkLogOptions{Model: "bad model"}, workLogPublicationHooks{}); err == nil {
		t.Fatal("recordWorkLogWithHooks accepted an invalid model")
	}
	same, err := EnsureWorkLogClaim(fixture.home, fixture.task, fixture.result, fixture.options)
	if err != nil || same.ClaimID != fixture.outcome.ClaimID {
		t.Fatalf("idempotent claim = %#v, %v", same, err)
	}
	mismatched := fixture.result
	mismatched.Repository = "acme/other"
	if _, err := EnsureWorkLogClaim(fixture.home, fixture.task, mismatched, fixture.options); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched existing claim error = %v", err)
	}

	extended, err := workLogOptionsForClaimExtension(fixture.home, fixture.options, fixture.outcome.claim)
	if err != nil || extended.EffortID != fixture.outcome.claim.EffortID || len(extended.snapshot.Contents) == 0 {
		t.Fatalf("extended options = %#v, %v", extended, err)
	}
	if err := corroborateExistingRunPrompt(fixture.home, fixture.outcome.EffortID, fixture.outcome.RunID, fixture.options); err != nil {
		t.Fatal(err)
	}
	runDir, runPath, err := openWorkLogRun(fixture.home, fixture.outcome.EffortID, fixture.outcome.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runDir.Close() }()
	archive, digest, err := ensureOriginalPromptArchive(runDir, fixture.options, fixture.outcome.claim.RecordedAt)
	if err != nil || archive == "" || digest != fixture.outcome.claim.PromptDigest {
		t.Fatalf("prompt archive = %q/%q, %v", archive, digest, err)
	}
	if err := migrateLegacySingletonClaim(runDir, runPath, fixture.home, fixture.outcome.EffortID, fixture.outcome.RunID); err != nil {
		t.Fatal(err)
	}
	identity, corrections, err := projectExecutionIdentity(runDir, fixture.outcome.claim)
	if err != nil || identity.Model != "unknown" || len(corrections) != 0 {
		t.Fatalf("projected identity = %#v/%#v, %v", identity, corrections, err)
	}

	renameOptions := fixture.options
	renameOptions.EffortID = "worklog-batch-renamed"
	renameOptions.RunID = "rename-run"
	if err := reservePreApplyRenameWorkLog(fixture.home, fixture.task, "worklog-batch-renamed", renameOptions); err != nil {
		t.Fatal(err)
	}
	reservations, err := findPreApplyRenameReservations(fixture.home, "worklog-batch-renamed")
	if err != nil || len(reservations) != 1 || reservations[0].OldTask != fixture.task {
		t.Fatalf("rename reservations = %#v, %v", reservations, err)
	}

	projectionDir, err := openWorkLogProjectionDirectory(fixture.worktree, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = projectionDir.Close()
	if err := removeWorkLogProjection(fixture.worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := openWorkLogProjectionDirectory(fixture.worktree, false); !os.IsNotExist(err) {
		t.Fatalf("removed projection reopen error = %v", err)
	}
}

//nolint:paralleltest // the shared Git fixture mutates WB home and XDG environment variables.
func TestWorkLogCoverageBatchCorrectionsAndTerminalBoundaries(t *testing.T) {
	fixture := newWorkLogCoverageBatchFixture(t, "worklog-terminal-batch")
	model := "gpt-6-sol"
	corrected, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{
		ProjectsRoot: fixture.projectsRoot,
		EffortID:     fixture.outcome.EffortID,
		RunID:        fixture.outcome.RunID,
		ClaimID:      fixture.outcome.ClaimID,
		EventID:      "coverage-correction",
		Actor:        "coverage-reviewer",
		Reason:       "exercise immutable correction projection",
		Model:        &model,
	})
	if err != nil || corrected.Identity.Model != model {
		t.Fatalf("correction = %#v, %v", corrected, err)
	}

	runDir, _, err := openWorkLogRun(fixture.home, fixture.outcome.EffortID, fixture.outcome.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runDir.Close() }()
	identity, corrections, err := projectExecutionIdentity(runDir, fixture.outcome.claim)
	if err != nil || identity.Model != model || len(corrections) != 1 {
		t.Fatalf("corrected identity = %#v/%#v, %v", identity, corrections, err)
	}
	finalCommit := strings.Repeat("b", 40)
	sealedAt, err := writeWorkLogTerminalWithEvidence(
		fixture.home, runDir, fixture.outcome.claim, finalCommit, "landed", "", "",
		nil, nil, nil, nil, nil,
	)
	if err != nil || sealedAt.IsZero() {
		t.Fatalf("terminal = %s, %v", sealedAt, err)
	}

	missingProjection := t.TempDir()
	if err := acceptExistingCleanupTerminal(fixture.home, missingProjection, finalCommit); err != nil {
		t.Fatalf("missing projection should be a legacy no-op: %v", err)
	}
	if err := acceptAdvancedCleanupTerminal(fixture.home, fixture.worktree, finalCommit, workLogProjection{}); err == nil {
		t.Fatal("advanced cleanup accepted an empty projection")
	}
	if err := transferWorkLogClaim(fixture.home, fixture.worktree, finalCommit, "handoff", "", ClaimExecutionIdentity{Model: "unknown"}); err == nil {
		t.Fatal("claim transfer accepted an empty successor")
	}
	if err := recoverFailedRecycleClaim(fixture.home, fixture.worktree, finalCommit, workLogProjection{}); err == nil {
		t.Fatal("failed recycle recovery accepted an empty prior projection")
	}

	expectation := TerminalWorkLogExpectation{
		Task: fixture.task, Repository: fixture.result.Repository, Worktree: fixture.worktree,
		Branch: fixture.result.Branch, Base: fixture.result.Base, FinalCommit: finalCommit,
	}
	if base, err := validateRemovedTerminalWorkLog(fixture.home, expectation); err == nil || base != "" {
		t.Fatalf("landed terminal accepted as removed evidence: base=%q err=%v", base, err)
	}
	matches := 0
	if _, err := validateRemovedTerminalWorkLogRun(fixture.home, expectation, "missing-run", &matches); err == nil {
		t.Fatal("missing terminal run was accepted")
	}

	entry := ListResult{OpenPullRequest: &PullRequest{URL: "https://example.test/pull/1"}}
	if ok, err := legacyRepositoryRelocationForCleanup(context.Background(), fixture.home, fixture.projectsRoot, entry, false, nil); err == nil || ok {
		t.Fatalf("open-PR legacy relocation = %t, %v", ok, err)
	}
}

func TestWorkLogCoverageBatchFilesystemRefusals(t *testing.T) {
	t.Parallel()
	blocking := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocking, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := findPreApplyRenameReservations(blocking, "task"); err == nil {
		t.Fatal("reservation scan accepted a file as WB home")
	}
	if _, err := openWorkLogProjectionDirectory(blocking, false); err == nil {
		t.Fatal("projection open accepted a file as worktree")
	}
	if err := removeWorkLogProjection(blocking); err == nil {
		t.Fatal("projection removal accepted a file as worktree")
	}
	if _, err := validateRemovedTerminalWorkLog(blocking, TerminalWorkLogExpectation{Task: "task"}); err == nil {
		t.Fatal("terminal validation accepted a file as WB home")
	}
}

func TestActiveClaimPublication(t *testing.T) {
	t.Parallel()
	recordedAt := time.Date(2026, time.September, 28, 12, 30, 0, 0, time.UTC)
	parent := workLogClaim{
		ClaimID: "claim", EffortID: "effort", RunID: "run", RecordedAt: recordedAt,
		Repository: "acme/app", Branch: "feature", Base: "main", BaseSHA: strings.Repeat("a", 40),
		AgentRuntime: "parent-runtime", Model: "parent-model", CLI: "parent-cli", Provider: "parent-provider",
	}
	claim := newSuccessorWorkLogClaim(
		parent, "successor", recordedAt.Add(time.Minute), "agent-2", "handoff",
		ClaimExecutionIdentity{Model: " gpt-6-sol ", CLI: " codex ", Provider: " openai-codex "},
	)
	if claim.Version != 2 || claim.ClaimID != "successor" || claim.Lifecycle != "active" ||
		claim.RecordedAt != recordedAt.Add(time.Minute) || claim.Initiator != "agent-2" || claim.AgentID != "agent-2" ||
		claim.AgentRuntime != "" || claim.Model != "gpt-6-sol" || claim.ModelProvenance != modelProvenanceCallerDeclared ||
		claim.ModelDeclaredBy != "agent-2" || claim.CLI != "codex" || claim.Provider != "openai-codex" ||
		claim.ParentClaimID != "claim" || claim.AcquiredVia != "handoff" {
		t.Fatalf("successor claim = %#v", claim)
	}
	unknown := newSuccessorWorkLogClaim(parent, "recovery", recordedAt, "recovery-agent", "recycle_failed", ClaimExecutionIdentity{Model: "unknown"})
	if unknown.ModelProvenance != modelProvenanceUnknown {
		t.Fatalf("unknown successor provenance = %q", unknown.ModelProvenance)
	}
	if parent.AgentRuntime != "parent-runtime" || parent.Model != "parent-model" {
		t.Fatalf("parent claim mutated = %#v", parent)
	}
	projection := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: "prior", Lifecycle: "terminal"}

	claimName, outboxName, event, nextProjection := activeClaimPublication(claim, "handoff", projection)
	if claimName != "successor.json" || outboxName != "run-successor-claimed.json" {
		t.Fatalf("publication names = %q, %q", claimName, outboxName)
	}
	wantEvent := workLogPublicEvent{
		Version: 1, Type: "worktree.claimed", At: recordedAt.Add(time.Minute),
		EffortID: "effort", RunID: "run", ClaimID: "successor", Repository: "acme/app",
		Branch: "feature", Base: "main", BaseSHA: strings.Repeat("a", 40),
		Lifecycle: "active", Disposition: "handoff",
	}
	if event != wantEvent {
		t.Fatalf("publication event = %#v, want %#v", event, wantEvent)
	}
	if nextProjection.ClaimID != "successor" || nextProjection.Lifecycle != "active" {
		t.Fatalf("next projection = %#v", nextProjection)
	}
	if projection.ClaimID != "prior" || projection.Lifecycle != "terminal" {
		t.Fatalf("input projection mutated = %#v", projection)
	}
}
