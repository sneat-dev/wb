package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type externalTargetFixture struct {
	base     *sessionReceiveFixture
	digest   sessionmove.Digest
	worktree string
	session  session.Record
	options  ExternalSessionWorkLogPrepareOptions
}

type externalSourceFixture struct {
	base     *sessionReceiveFixture
	store    sessionmove.Store
	digest   sessionmove.Digest
	worktree string
	source   session.Record
	claim    workLogClaim
}

func newExternalSourceFixture(t *testing.T) *externalSourceFixture {
	t.Helper()
	base := newSessionReceiveFixture(t)
	resolvedRoot, err := filepath.EvalSymlinks(base.root)
	if err != nil {
		t.Fatal(err)
	}
	sourceWorktree := filepath.Join(resolvedRoot, "source-worktree")
	gitTest(t, base.canonical, "worktree", "add", sourceWorktree, base.request.Branch)
	base.request.SourceNativeHarnessID = "native-source"
	base.request.SourceOfferMessage = "Continue the handoff\n## Embedded user heading\nwithout reverse parsing"
	base.request.SourceOfferNextAction = "Run the remaining checks\n## Another user heading"
	base.request.SourceOfferDigest = sessionmove.DigestSourceOffer(base.request.SourceOfferMessage, base.request.SourceOfferNextAction)
	source := session.Record{
		PID: os.Getpid(), WBSessionID: base.request.PredecessorWBSessionID, Machine: base.request.SourceMachine,
		Runtime: base.request.SourceRuntime, Model: base.request.SourceModel, NativeHarnessID: base.request.SourceNativeHarnessID,
		StartedAt: base.request.CreatedAt.Add(-time.Minute),
	}
	claim := workLogClaim{
		Version: 2, EffortID: "session-move", RunID: "session-move-run", Task: "source session move",
		Repository: "acme/app", Worktree: sourceWorktree, Branch: base.request.Branch, Base: "main",
		BaseSHA: base.request.SourceWorkCommit, Lifecycle: "active", RecordedAt: base.request.CreatedAt.Add(-time.Hour),
		Initiator: source.WBSessionID, AgentID: source.NativeHarnessID, AgentRuntime: source.Runtime, Model: source.Model,
		ModelProvenance: modelProvenanceCallerDeclared, ModelDeclaredBy: source.WBSessionID,
	}
	claim.ClaimID = workLogClaimID(claim.EffortID, CreateResult{
		Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
	})
	base.request.WorkLogReference = "worklog:" + claim.EffortID + "/" + claim.RunID + "/" + claim.ClaimID
	raw, err := sessionmove.EncodeRequest(base.request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	runDir, _, err := openWorkLogRun(base.home, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkLogRunIndex(runDir, claim.EffortID, claim.RunID); err != nil {
		_ = runDir.Close()
		t.Fatal(err)
	}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		_ = runDir.Close()
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", claim, true); err != nil {
		_ = claims.Close()
		_ = runDir.Close()
		t.Fatal(err)
	}
	_ = claims.Close()
	_ = runDir.Close()
	if err := writeWorkLogProjection(sourceWorktree, workLogProjection{
		Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "active",
	}); err != nil {
		t.Fatal(err)
	}
	store := sessionmove.NewStore(filepath.Join(base.home, sessionmove.DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	return &externalSourceFixture{base: base, store: store, digest: digest, worktree: sourceWorktree, source: source, claim: claim}
}

func (fixture *externalSourceFixture) lock(t *testing.T) *sessionmove.ExecutionLock {
	t.Helper()
	lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.base.request.HandoffID, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

func (fixture *externalSourceFixture) receipt(t *testing.T) sessionmove.Receipt {
	t.Helper()
	target, err := sessionmove.ExpectedTargetWorkLogReference(fixture.base.request, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	return sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: fixture.base.request.HandoffID,
		RequestDigest: fixture.digest, SuccessorWBSessionID: fixture.base.request.SuccessorWBSessionID,
		PredecessorWBSessionID: fixture.base.request.PredecessorWBSessionID, TargetMachine: fixture.base.request.TargetMachine,
		TmuxName: "wb-session-" + fixture.base.request.SuccessorWBSessionID, Runtime: fixture.base.request.SourceRuntime,
		Model: fixture.base.request.SourceModel, NativeHarnessID: "native-target",
		AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1, PID: os.Getpid(),
		TargetWorkLogReference: target.String(), PinnedCommit: fixture.base.request.BundleCommit,
		StartedAt: fixture.base.request.CreatedAt.Add(time.Minute),
	}
}

func (fixture *externalSourceFixture) authorizeSeal(t *testing.T, lock *sessionmove.ExecutionLock) sessionmove.Receipt {
	t.Helper()
	if _, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, SourceSession: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.SaveRoute(sessionmove.Route{
		SchemaVersion: sessionmove.RouteSchemaVersion, HandoffID: fixture.base.request.HandoffID, RequestDigest: fixture.digest,
		TargetMachine: fixture.base.request.TargetMachine, Courier: sessionmove.CourierSSH,
		SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"},
	}); err != nil {
		t.Fatal(err)
	}
	receipt := fixture.receipt(t)
	if _, _, err := fixture.store.SaveReceiptUnderLock(lock, fixture.base.request.HandoffID, fixture.digest, receipt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.SaveSuccessorAddressUnderLock(lock, fixture.base.request.HandoffID, fixture.digest, receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func newExternalTargetFixture(t *testing.T) *externalTargetFixture {
	t.Helper()
	base := newSessionReceiveFixture(t)
	raw, err := sessionmove.EncodeRequest(base.request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	worktree := base.targetWorktree()
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, base.canonical, "worktree", "add", "-b", "wb-session/"+base.request.HandoffID, worktree, base.request.BundleCommit)
	startedAt := base.request.CreatedAt.Add(time.Minute)
	record := session.Record{
		PID: os.Getpid(), WBSessionID: base.request.SuccessorWBSessionID, Machine: base.request.TargetMachine,
		Runtime: "codex", Model: "gpt-5", NativeHarnessID: "native-target",
		TmuxName:               "wb-session-" + base.request.SuccessorWBSessionID,
		PredecessorWBSessionID: base.request.PredecessorWBSessionID, HandoffID: base.request.HandoffID,
		StartedAt: startedAt,
	}
	options := ExternalSessionWorkLogPrepareOptions{
		ProjectsRoot: base.projectsRoot, Request: base.request, RequestDigest: digest,
		ReceivedAt: base.request.CreatedAt.Add(30 * time.Second), Session: record,
		AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1,
		WorktreeDir: worktree, PinnedCommit: base.request.BundleCommit, HandoverBytes: base.handover,
	}
	return &externalTargetFixture{base: base, digest: digest, worktree: worktree, session: record, options: options}
}

func (fixture *externalTargetFixture) receipt(t *testing.T, prepared ExternalSessionWorkLogPrepareResult) sessionmove.Receipt {
	t.Helper()
	receipt := sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: fixture.base.request.HandoffID,
		RequestDigest: fixture.digest, SuccessorWBSessionID: fixture.base.request.SuccessorWBSessionID,
		PredecessorWBSessionID: fixture.base.request.PredecessorWBSessionID, TargetMachine: fixture.base.request.TargetMachine,
		TmuxName: fixture.session.TmuxName, Runtime: fixture.session.Runtime, Model: fixture.session.Model,
		NativeHarnessID: fixture.session.NativeHarnessID,
		AttemptID:       fixture.options.AttemptID, AttemptIndex: fixture.options.AttemptIndex, PID: fixture.session.PID,
		TargetWorkLogReference: prepared.WorkLogReference, PinnedCommit: fixture.base.request.BundleCommit,
		StartedAt: fixture.session.StartedAt,
	}
	if _, err := sessionmove.EncodeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestPrepareExternalSessionWorkLogRepairsEveryPublicationBoundary(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	injected := errors.New("injected custody crash")
	hooks := []externalSessionWorkLogHooks{
		{afterClaim: func() error { return injected }},
		{afterRunIndex: func() error { return injected }},
		{afterJournal: func() error { return injected }},
		{afterOutbox: func() error { return injected }},
		{afterProjection: func() error { return injected }},
	}
	for boundary, hooks := range hooks {
		options := fixture.options
		options.hooks = hooks
		if _, err := PrepareExternalSessionWorkLog(context.Background(), options); !errors.Is(err, injected) {
			t.Fatalf("boundary %d error=%v, want injected crash", boundary, err)
		}
	}
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := sessionmove.ExpectedTargetWorkLogReference(fixture.base.request, fixture.digest)
	if prepared.WorkLogReference != want.String() || !prepared.Replayed {
		t.Fatalf("prepared=%#v want reference %s replay", prepared, want.String())
	}
	projection, err := readLocalProjection(fixture.worktree)
	if err != nil || projection.ClaimID != want.ClaimID || projection.Lifecycle != "active" {
		t.Fatalf("repaired local projection=%#v err=%v", projection, err)
	}
}

func TestExternalTargetClaimStableAcrossAttemptsAndCompletionBindsWinner(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	first := fixture.options
	first.Session.PID = 2147483001
	first.Session.StartedAt = fixture.session.StartedAt.Add(-10 * time.Second)
	preparedFirst, err := PrepareExternalSessionWorkLog(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := fixture.options
	second.AttemptID = "000002-" + strings.Repeat("2", 32)
	second.AttemptIndex = 2
	second.Session = fixture.session
	preparedSecond, err := PrepareExternalSessionWorkLog(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if preparedFirst.WorkLogReference != preparedSecond.WorkLogReference || preparedFirst.ClaimID != preparedSecond.ClaimID {
		t.Fatalf("claim drifted across attempts: first=%#v second=%#v", preparedFirst, preparedSecond)
	}
	fixture.options, fixture.session = second, second.Session
	receipt := fixture.receipt(t, preparedSecond)
	if _, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: receipt, WorktreeDir: fixture.worktree,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := readLocalEvents(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if eventByID(events, externalLocalEventID("target-owner", fixture.digest, first.AttemptID)) == nil ||
		eventByID(events, externalLocalEventID("target-owner", fixture.digest, second.AttemptID)) == nil ||
		eventByID(events, externalLocalEventID("target-completed", fixture.digest, "")) == nil {
		t.Fatalf("missing attempt/completion lineage: %#v", events)
	}

	// A later generic owner cannot be substituted for the receipt-bound winner.
	forgedOwner := OwnerRegistration{Agent: fixture.session.Runtime + "/" + fixture.session.WBSessionID,
		Model: fixture.session.Model, Effort: "session-move", PID: os.Getpid(), WBVersion: buildinfo.Version(),
		Command: "forged", At: fixture.session.StartedAt.Add(time.Minute)}
	if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, LocalWorkLogEvent{
		ID: "forged-generic-owner", Type: LocalEventOwner, At: forgedOwner.At, Message: "forged", Owner: &forgedOwner,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: receipt, WorktreeDir: fixture.worktree,
	}); err == nil || !strings.Contains(err.Error(), "latest live") {
		t.Fatalf("forged owner replay error=%v", err)
	}
}

func TestExternalTargetCompletionRejectsMutatedImmutableClaimFields(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	receipt := fixture.receipt(t, prepared)
	reference, _ := sessionmove.ParseWorkLogReference(prepared.WorkLogReference)
	claimPath := filepath.Join(fixture.base.home, "worklogs", reference.EffortID, "runs", reference.RunID, "claims", reference.ClaimID+".json")
	original := mustReadFile(t, claimPath)
	mutations := map[string]func(*workLogClaim){
		"repository": func(claim *workLogClaim) { claim.Repository = "other/repo" },
		"branch":     func(claim *workLogClaim) { claim.Branch = "wb-session/other" },
		"base":       func(claim *workLogClaim) { claim.Base = "other" },
		"base sha":   func(claim *workLogClaim) { claim.BaseSHA = strings.Repeat("f", 40) },
		"initiator":  func(claim *workLogClaim) { claim.Initiator = "wbs-other" },
		"agent":      func(claim *workLogClaim) { claim.AgentID = "wbs-other" },
		"runtime":    func(claim *workLogClaim) { claim.AgentRuntime = "claude-code" },
		"model":      func(claim *workLogClaim) { claim.Model = "other" },
		"provenance": func(claim *workLogClaim) { claim.ModelProvenance = modelProvenanceRuntimeObserved },
		"parent":     func(claim *workLogClaim) { claim.ParentClaimID = strings.Repeat("e", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var claim workLogClaim
			if err := json.Unmarshal(original, &claim); err != nil {
				t.Fatal(err)
			}
			mutate(&claim)
			raw, _ := json.MarshalIndent(claim, "", "  ")
			if err := os.WriteFile(claimPath, append(raw, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{
				ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
				Receipt: receipt, WorktreeDir: fixture.worktree,
			})
			if err == nil {
				t.Fatal("mutated immutable claim authorized completion")
			}
			if err := os.WriteFile(claimPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnsureExternalSourceOfferEvidenceRepairsEveryCheckpointBoundary(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	injected := errors.New("injected source checkpoint crash")
	options := ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, SourceSession: fixture.source,
		hooks: externalSourceOfferHooks{afterOfferedPhase: func() error { return injected }},
	}
	if _, err := EnsureExternalSourceOfferEvidence(options); !errors.Is(err, injected) {
		t.Fatalf("after offered phase error=%v, want injected crash", err)
	}
	state, err := fixture.store.LoadUnderLock(lock, fixture.base.request.HandoffID, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 1 || state.Events[0].Phase != sessionmove.PhaseOffered {
		t.Fatalf("offered phase repair state=%#v", state.Events)
	}
	if events, err := readLocalEvents(fixture.worktree); err != nil || eventByID(events, externalLocalEventID("source-offered", fixture.digest, "")) != nil {
		t.Fatalf("offer crossed injected boundary: events=%#v err=%v", events, err)
	}

	options.hooks = externalSourceOfferHooks{afterOffer: func() error { return injected }}
	if _, err := EnsureExternalSourceOfferEvidence(options); !errors.Is(err, injected) {
		t.Fatalf("after Work Log offer error=%v, want injected crash", err)
	}
	events, err := readLocalEvents(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if eventByID(events, externalLocalEventID("source-offered", fixture.digest, "")) == nil ||
		eventByID(events, externalLocalEventID("source-owner", fixture.digest, "")) != nil {
		t.Fatalf("source boundary evidence=%#v", events)
	}

	options.hooks = externalSourceOfferHooks{}
	repaired, err := EnsureExternalSourceOfferEvidence(options)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Replayed || repaired.OfferEvent.Message != fixture.base.request.SourceOfferMessage ||
		repaired.OfferEvent.NextAction != fixture.base.request.SourceOfferNextAction || repaired.OwnerEvent.Owner == nil {
		t.Fatalf("repaired source evidence=%#v", repaired)
	}
	replayed, err := EnsureExternalSourceOfferEvidence(options)
	if err != nil || !replayed.Replayed || replayed.OfferEvent.ID != repaired.OfferEvent.ID || replayed.OwnerEvent.ID != repaired.OwnerEvent.ID {
		t.Fatalf("source evidence replay=%#v err=%v", replayed, err)
	}
}

func TestEnsureExternalSourceOfferEvidenceRejectsForgedMatchingSubset(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	forged := externalSourceOfferEvent(fixture.base.request, fixture.digest)
	forged.Message = "forged event retaining the old subset"
	if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, forged); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, SourceSession: fixture.source,
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("forged source offer error=%v", err)
	}
	if events, readErr := readLocalEvents(fixture.worktree); readErr != nil ||
		eventByID(events, externalLocalEventID("source-owner", fixture.digest, "")) != nil {
		t.Fatalf("forged offer authorized source owner: events=%#v err=%v", events, readErr)
	}
}

func TestSealExternalSessionWorkLogRequiresDurableSuccessorIndex(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	if _, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, SourceSession: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	receipt := fixture.receipt(t)
	if _, _, err := fixture.store.SaveReceiptUnderLock(lock, fixture.base.request.HandoffID, fixture.digest, receipt); err != nil {
		t.Fatal(err)
	}
	_, err := SealExternalSessionWorkLog(ExternalSourceSealOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: receipt, SourceSession: fixture.source,
	})
	if err == nil || !strings.Contains(err.Error(), "successor address") {
		t.Fatalf("receipt-only source seal error=%v", err)
	}
	projection, projectionErr := readWorkLogProjection(fixture.worktree)
	if projectionErr != nil || projection.Lifecycle != "active" {
		t.Fatalf("source projection after receipt-only refusal=%#v err=%v", projection, projectionErr)
	}
	ref, _ := sessionmove.ParseWorkLogReference(fixture.base.request.WorkLogReference)
	if _, statErr := os.Stat(filepath.Join(fixture.base.home, "worklogs", ref.EffortID, "runs", ref.RunID, "terminals", ref.ClaimID+".json")); !os.IsNotExist(statErr) {
		t.Fatalf("source terminal exists without successor index: %v", statErr)
	}
}

func TestSealExternalSessionWorkLogRejectsDirtySourceAndMismatchedLatestOwner(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *externalSourceFixture)
		want   string
	}{
		{name: "tracked dirty", mutate: func(t *testing.T, fixture *externalSourceFixture) {
			if err := os.WriteFile(filepath.Join(fixture.worktree, "README.md"), []byte("changed after handoff\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: "changed after its handoff bundle"},
		{name: "untracked dirty", mutate: func(t *testing.T, fixture *externalSourceFixture) {
			if err := os.WriteFile(filepath.Join(fixture.worktree, "untracked-after-handoff.txt"), []byte("new work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: "changed after its handoff bundle"},
		{name: "PID reuse shaped agent mismatch", mutate: func(t *testing.T, fixture *externalSourceFixture) {
			owner := OwnerRegistration{Agent: "codex/other-session", Model: fixture.source.Model, Effort: fixture.claim.EffortID,
				PID: fixture.source.PID, WBVersion: buildinfo.Version(), Command: "forged", At: fixture.base.request.CreatedAt.Add(time.Minute)}
			if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, LocalWorkLogEvent{
				ID: "forged-agent-source-owner", Type: LocalEventOwner, At: owner.At, Message: "forged", Owner: &owner,
			}); err != nil {
				t.Fatal(err)
			}
		}, want: "not the current"},
		{name: "model mismatch", mutate: func(t *testing.T, fixture *externalSourceFixture) {
			owner := OwnerRegistration{Agent: fixture.source.Runtime + "/" + fixture.source.WBSessionID, Model: "other-model", Effort: fixture.claim.EffortID,
				PID: fixture.source.PID, WBVersion: buildinfo.Version(), Command: "forged", At: fixture.base.request.CreatedAt.Add(time.Minute)}
			if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, LocalWorkLogEvent{
				ID: "forged-model-source-owner", Type: LocalEventOwner, At: owner.At, Message: "forged", Owner: &owner,
			}); err != nil {
				t.Fatal(err)
			}
		}, want: "not the current"},
		{name: "effort mismatch", mutate: func(t *testing.T, fixture *externalSourceFixture) {
			owner := OwnerRegistration{Agent: fixture.source.Runtime + "/" + fixture.source.WBSessionID, Model: fixture.source.Model, Effort: "other-effort",
				PID: fixture.source.PID, WBVersion: buildinfo.Version(), Command: "forged", At: fixture.base.request.CreatedAt.Add(time.Minute)}
			if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, LocalWorkLogEvent{
				ID: "forged-effort-source-owner", Type: LocalEventOwner, At: owner.At, Message: "forged", Owner: &owner,
			}); err != nil {
				t.Fatal(err)
			}
		}, want: "not the current"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExternalSourceFixture(t)
			lock := fixture.lock(t)
			if _, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
				Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
				Request: fixture.base.request, RequestDigest: fixture.digest, SourceSession: fixture.source,
			}); err != nil {
				t.Fatal(err)
			}
			receipt := fixture.receipt(t)
			if _, _, err := fixture.store.SaveRoute(sessionmove.Route{
				SchemaVersion: sessionmove.RouteSchemaVersion, HandoffID: fixture.base.request.HandoffID, RequestDigest: fixture.digest,
				TargetMachine: fixture.base.request.TargetMachine, Courier: sessionmove.CourierSSH,
				SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"},
			}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := fixture.store.SaveReceiptUnderLock(lock, fixture.base.request.HandoffID, fixture.digest, receipt); err != nil {
				t.Fatal(err)
			}
			if _, _, err := fixture.store.SaveSuccessorAddressUnderLock(lock, fixture.base.request.HandoffID, fixture.digest, receipt); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture)
			_, err := SealExternalSessionWorkLog(ExternalSourceSealOptions{
				Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
				Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: receipt, SourceSession: fixture.source,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("source seal error=%v, want %q", err, test.want)
			}
			projection, projectionErr := readWorkLogProjection(fixture.worktree)
			if projectionErr != nil || projection.Lifecycle != "active" {
				t.Fatalf("source projection after refusal=%#v err=%v", projection, projectionErr)
			}
		})
	}
}

func TestSealExternalSessionWorkLogRepairsEveryPostTerminalBoundary(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	receipt := fixture.authorizeSeal(t, lock)
	injected := errors.New("injected source seal crash")
	base := ExternalSourceSealOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: receipt, SourceSession: fixture.source,
	}
	for boundary, hooks := range []externalSourceSealHooks{
		{afterTerminal: func() error { return injected }},
		{afterProjection: func() error { return injected }},
		{afterCompletion: func() error { return injected }},
	} {
		options := base
		options.hooks = hooks
		if _, err := SealExternalSessionWorkLog(options); !errors.Is(err, injected) {
			t.Fatalf("source terminal boundary %d error=%v, want injected crash", boundary, err)
		}
	}
	sealed, err := SealExternalSessionWorkLog(base)
	if err != nil {
		t.Fatal(err)
	}
	if !sealed.Replayed || sealed.SourceWorkLogReference != fixture.base.request.WorkLogReference ||
		sealed.TargetWorkLogReference != receipt.TargetWorkLogReference || sealed.SealedAt.IsZero() {
		t.Fatalf("repaired source seal=%#v", sealed)
	}
	projection, err := readWorkLogProjection(fixture.worktree)
	if err != nil || projection.Lifecycle != "terminal" {
		t.Fatalf("repaired source projection=%#v err=%v", projection, err)
	}
	events, err := readLocalEvents(fixture.worktree)
	if err != nil || eventByID(events, externalLocalEventID("source-completed", fixture.digest, "")) == nil {
		t.Fatalf("repaired source completion events=%#v err=%v", events, err)
	}
	claimsPath := filepath.Join(fixture.base.home, "worklogs", fixture.claim.EffortID, "runs", fixture.claim.RunID, "claims")
	claims, err := os.ReadDir(claimsPath)
	if err != nil || len(claims) != 1 || claims[0].Name() != fixture.claim.ClaimID+".json" {
		t.Fatalf("source seal manufactured a local successor claim: claims=%#v err=%v", claims, err)
	}
}

func TestSealExternalSessionWorkLogRejectsTerminatedCorruptJournalBeforeMutation(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	receipt := fixture.authorizeSeal(t, lock)
	journalPath := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName)
	file, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("{malformed terminated authority}\n")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = SealExternalSessionWorkLog(ExternalSourceSealOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: receipt, SourceSession: fixture.source,
	})
	if err == nil || !strings.Contains(err.Error(), "parse local work-log event") {
		t.Fatalf("corrupt source journal seal error=%v", err)
	}
	projection, projectionErr := readWorkLogProjection(fixture.worktree)
	if projectionErr != nil || projection.Lifecycle != "active" {
		t.Fatalf("corrupt journal terminalized source projection=%#v err=%v", projection, projectionErr)
	}
	ref, _ := sessionmove.ParseWorkLogReference(fixture.base.request.WorkLogReference)
	if _, statErr := os.Stat(filepath.Join(fixture.base.home, "worklogs", ref.EffortID, "runs", ref.RunID, "terminals", ref.ClaimID+".json")); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt journal created source terminal: %v", statErr)
	}
}
