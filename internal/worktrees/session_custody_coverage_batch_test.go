package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

// This batch deliberately exercises every custody function that still had an
// uncovered block before running the focused suite. Keep the cheap guards in
// one test: they prove malformed authority is rejected before Git or disk I/O.
func TestSessionCustodyCoverageBatchPublicGuards(t *testing.T) {
	t.Parallel()
	if _, err := PrepareExternalSessionWorkLog(context.Background(), ExternalSessionWorkLogPrepareOptions{}); err == nil {
		t.Fatal("empty external target preparation was accepted")
	}
	if _, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{}); err == nil {
		t.Fatal("empty external completion was accepted")
	}
	if _, err := RecordExternalTargetAttemptFailed(ExternalTargetAttemptFailureOptions{
		Failure: sessionlaunch.FailureEvidence{},
	}); err == nil {
		t.Fatal("empty external attempt failure was accepted")
	}
	if _, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{}); err == nil {
		t.Fatal("source offer without retained authority was accepted")
	}
	if _, err := SealExternalSessionWorkLog(ExternalSourceSealOptions{}); err == nil {
		t.Fatal("source seal without a receipt was accepted")
	}
	if _, err := PrepareParkedSessionWorkLog(context.Background(), ParkedSessionWorkLogPrepareOptions{}); err == nil {
		t.Fatal("empty parked target preparation was accepted")
	}
	if _, err := RecordParkedTargetCompleted(ParkedTargetCompletionOptions{}); err == nil {
		t.Fatal("empty parked completion was accepted")
	}
	if err := validateParkedTargetSession(sessionpark.RemoteRequest{}, session.Record{}); err == nil {
		t.Fatal("empty parked target session was accepted")
	}
}

//nolint:paralleltest // the session receive fixture mutates WB home and XDG environment variables.
func TestSessionCustodyCoverageBatchExternalTargetBranches(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	if err := ensureExternalHandoverPrompt(
		fixture.worktree, fixture.base.request.CreatedAt, fixture.session,
		fixture.base.request.HandoverDigest, nil,
	); err == nil {
		t.Fatal("empty first handover prompt was accepted")
	}

	for name, mutate := range map[string]func(*ExternalSessionWorkLogPrepareOptions){
		"relative worktree": func(options *ExternalSessionWorkLogPrepareOptions) { options.WorktreeDir = "." },
		"not git worktree":  func(options *ExternalSessionWorkLogPrepareOptions) { options.WorktreeDir = t.TempDir() },
		"pinned commit":     func(options *ExternalSessionWorkLogPrepareOptions) { options.PinnedCommit = strings.Repeat("f", 40) },
		"checkout head": func(options *ExternalSessionWorkLogPrepareOptions) {
			options.PinnedCommit = strings.Repeat("f", 40)
			options.Request.BundleCommit = options.PinnedCommit
		},
		"target session":    func(options *ExternalSessionWorkLogPrepareOptions) { options.Session.PID = 0 },
		"attempt":           func(options *ExternalSessionWorkLogPrepareOptions) { options.AttemptID = "bad" },
		"repository remote": func(options *ExternalSessionWorkLogPrepareOptions) { options.Request.RepositoryRemote = "not-a-remote" },
		"handover digest":   func(options *ExternalSessionWorkLogPrepareOptions) { options.HandoverBytes = []byte("tampered") },
		"missing handover": func(options *ExternalSessionWorkLogPrepareOptions) {
			options.HandoverBytes = nil
			options.Request.HandoverContent = ""
			options.Request.HandoverPath = "missing-handover.md"
		},
		"received time": func(options *ExternalSessionWorkLogPrepareOptions) {
			options.ReceivedAt = time.Time{}
			options.Request.CreatedAt = time.Time{}
		},
	} {
		//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
		t.Run(name, func(t *testing.T) {
			options := fixture.options
			mutate(&options)
			if _, err := PrepareExternalSessionWorkLog(context.Background(), options); err == nil {
				t.Fatalf("invalid preparation %q was accepted", name)
			}
		})
	}

	fallback := fixture.options
	fallback.ReceivedAt = time.Time{}
	preparedFallback, err := prepareExternalTarget(context.Background(), fallback)
	if err != nil || !preparedFallback.claim.RecordedAt.Equal(fixture.base.request.CreatedAt.UTC()) {
		t.Fatalf("created-at fallback = %#v, err=%v", preparedFallback.claim, err)
	}
	unknownModel := fixture.options
	unknownModel.Request.SourceModel = ""
	unknownModel.Session.Model = ""
	preparedUnknown, err := prepareExternalTarget(context.Background(), unknownModel)
	if err != nil || preparedUnknown.claim.Model != "unknown" || preparedUnknown.claim.ModelProvenance != modelProvenanceUnknown {
		t.Fatalf("unknown-model claim = %#v, err=%v", preparedUnknown.claim, err)
	}

	prepared, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	receipt := fixture.receipt(t, prepared)
	badRequest := fixture.base.request
	badRequest.WorkLogReference = "invalid"
	if err := validateExternalReceipt(badRequest, fixture.digest, receipt); err == nil {
		t.Fatal("receipt for an invalid target reference was accepted")
	}
	claim, target, unlock, err := loadExternalTargetClaim(
		fixture.base.projectsRoot, fixture.base.request, fixture.digest, fixture.worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if target.String() != prepared.WorkLogReference || claim.ClaimID != prepared.ClaimID {
		t.Fatalf("loaded target = %#v/%s, prepared = %#v", claim, target.String(), prepared)
	}
	projection := activeTargetProjection(claim)
	if err := corroborateExternalTargetClaim(
		claim, projection, target, fixture.base.request, fixture.digest, fixture.worktree, claim.Repository,
	); err != nil {
		t.Fatal(err)
	}
	unknownRequest := fixture.base.request
	unknownRequest.SourceModel = ""
	if err := corroborateExternalTargetClaim(
		preparedUnknown.claim, activeTargetProjection(preparedUnknown.claim), target,
		unknownRequest, fixture.digest, fixture.worktree, preparedUnknown.claim.Repository,
	); err != nil {
		t.Fatalf("unknown-model target claim was rejected: %v", err)
	}
	conflictingShape := claim
	conflictingShape.Repository = "other/repository"
	if err := corroborateExternalTargetClaim(
		conflictingShape, projection, target, fixture.base.request, fixture.digest, fixture.worktree, claim.Repository,
	); err == nil {
		t.Fatal("conflicting target claim shape was accepted")
	}
	conflictingProjection := projection
	conflictingProjection.Lifecycle = "completed"
	if err := corroborateExternalTargetClaim(
		claim, conflictingProjection, target, fixture.base.request, fixture.digest, fixture.worktree, claim.Repository,
	); err == nil {
		t.Fatal("conflicting target projection was accepted")
	}
	conflictingEvidence := claim
	conflictingEvidence.ExternalHandoff = &workLogExternalHandoffEvidence{}
	if err := corroborateExternalTargetClaim(
		conflictingEvidence, projection, target, fixture.base.request, fixture.digest, fixture.worktree, claim.Repository,
	); err == nil {
		t.Fatal("conflicting target lineage evidence was accepted")
	}
	if err := validateExternalTargetManifestAndJournal(
		fixture.worktree, fixture.base.request, fixture.digest, claim, receipt.Model,
	); err != nil {
		t.Fatal(err)
	}
	conflictingClaim := claim
	conflictingClaim.Repository = "other/repository"
	if err := validateExternalTargetManifestAndJournal(
		fixture.worktree, fixture.base.request, fixture.digest, conflictingClaim, receipt.Model,
	); err == nil {
		t.Fatal("conflicting target manifest was accepted")
	}
	conflictingRequest := fixture.base.request
	conflictingRequest.HandoverDigest = sessionmove.DigestBytes([]byte("other handover"))
	if err := validateExternalTargetManifestAndJournal(
		fixture.worktree, conflictingRequest, fixture.digest, claim, receipt.Model,
	); err == nil {
		t.Fatal("conflicting target handover was accepted")
	}
	if err := validateExternalAttemptOwner(
		fixture.worktree, fixture.base.request, fixture.digest, claim,
		receipt.AttemptID, receipt.AttemptIndex, receipt.PID, receipt.StartedAt, true,
	); err != nil {
		t.Fatal(err)
	}
	if err := validateExternalAttemptOwner(
		fixture.worktree, fixture.base.request, fixture.digest, claim,
		"000002-"+strings.Repeat("2", 32), 2, receipt.PID, receipt.StartedAt, true,
	); err == nil {
		t.Fatal("missing attempt owner was accepted")
	}
	if err := validateExternalAttemptOwner(
		fixture.worktree, fixture.base.request, fixture.digest, claim,
		receipt.AttemptID, receipt.AttemptIndex, receipt.PID+1, receipt.StartedAt, true,
	); err == nil {
		t.Fatal("conflicting attempt owner was accepted")
	}
	if err := validateExternalAttemptOwner(
		fixture.worktree, fixture.base.request, fixture.digest, claim,
		receipt.AttemptID, receipt.AttemptIndex, receipt.PID, receipt.StartedAt, false,
	); err == nil {
		t.Fatal("active attempt owner was accepted as a failed orphan")
	}
	if err := validateExternalAttemptOwner(
		filepath.Join(t.TempDir(), "missing"), fixture.base.request, fixture.digest, claim,
		receipt.AttemptID, receipt.AttemptIndex, receipt.PID, receipt.StartedAt, true,
	); err == nil {
		t.Fatal("attempt owner with a missing journal was accepted")
	}
	if err := validateExternalHandoverPrompt(
		fixture.worktree, claim.RecordedAt, claim.AgentRuntime, receipt.Model,
		fixture.base.request.HandoverDigest, fixture.base.handover,
	); err != nil {
		t.Fatal(err)
	}
	if err := validateExternalHandoverPrompt(
		fixture.worktree, claim.RecordedAt.Add(time.Second), claim.AgentRuntime, receipt.Model,
		fixture.base.request.HandoverDigest, fixture.base.handover,
	); err == nil {
		t.Fatal("prompt with conflicting metadata was accepted")
	}
	if err := validateExternalHandoverPrompt(
		fixture.worktree, claim.RecordedAt, claim.AgentRuntime, receipt.Model,
		fixture.base.request.HandoverDigest, []byte("different body"),
	); err == nil {
		t.Fatal("prompt with conflicting body was accepted")
	}
	if _, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: receipt, WorktreeDir: fixture.worktree,
	}); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // the session receive fixture mutates WB home and XDG environment variables.
func TestSessionCustodyCoverageBatchExternalSourceBranches(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	forged := externalSourceOfferEvent(fixture.base.request, fixture.digest)
	forged.Message = "forged offer"
	if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, forged); err != nil {
		t.Fatal(err)
	}
	if err := validateExternalSourceOffer(fixture.worktree, fixture.base.request, fixture.digest); err == nil {
		t.Fatal("forged source offer was accepted")
	}

	noOwner := newExternalSourceFixture(t)
	if err := validateLiveExternalSourceOwner(
		noOwner.worktree, noOwner.source, noOwner.base.request, noOwner.digest, noOwner.claim,
	); err == nil || !strings.Contains(err.Error(), "no live source owner") {
		t.Fatalf("missing owner error = %v", err)
	}

	deadOwner := newExternalSourceFixture(t)
	deadOwner.source.PID = 2147483001
	owner := OwnerRegistration{
		Agent: deadOwner.source.Runtime + "/" + deadOwner.source.WBSessionID,
		Model: deadOwner.source.Model, Effort: deadOwner.claim.EffortID, PID: deadOwner.source.PID,
		WBVersion: buildinfo.Version(), Command: "session move offer", At: deadOwner.base.request.CreatedAt.UTC(),
	}
	if _, _, err := appendLocalEventWithoutCustody(deadOwner.worktree, LocalWorkLogEvent{
		ID: externalLocalEventID("source-owner", deadOwner.digest, ""), Type: LocalEventOwner,
		At: deadOwner.base.request.CreatedAt.UTC(), Message: "predecessor session owns offered external handoff",
		Owner: &owner, Extra: externalSourceOwnerExtra(deadOwner.base.request, deadOwner.digest),
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateLiveExternalSourceOwner(
		deadOwner.worktree, deadOwner.source, deadOwner.base.request, deadOwner.digest, deadOwner.claim,
	); err == nil || !strings.Contains(err.Error(), "live source session") {
		t.Fatalf("dead owner error = %v", err)
	}
}

func TestSessionCustodyCoverageBatchFilesystemFailures(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	if err := validateExternalSourceOffer(missing, sessionmove.Request{}, ""); err == nil {
		t.Fatal("missing source worktree was accepted")
	}
	if err := validateLiveExternalSourceOwner(missing, session.Record{}, sessionmove.Request{}, "", workLogClaim{}); err == nil {
		t.Fatal("missing source owner journal was accepted")
	}
	if err := validateExternalTargetManifestAndJournal(missing, sessionmove.Request{}, "", workLogClaim{}, ""); err == nil {
		t.Fatal("missing target manifest was accepted")
	}
	if err := validateExternalAttemptOwner(missing, sessionmove.Request{}, "", workLogClaim{}, "bad", 0, 0, time.Time{}, false); err == nil {
		t.Fatal("incomplete target attempt owner was accepted")
	}
	if err := validateExternalHandoverPrompt(missing, time.Time{}, "", "", "", nil); err == nil {
		t.Fatal("missing handover prompt was accepted")
	}
	if _, _, unlock, err := loadExternalTargetClaim("", sessionmove.Request{}, "", missing); err == nil || unlock != nil {
		t.Fatalf("invalid target claim load = unlock %v, err %v", unlock != nil, err)
	}

	blocking := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocking, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureExternalManifest(blocking, Manifest{}); err == nil {
		t.Fatal("manifest was written through a non-directory worktree")
	}
	if err := ensureExternalHandoverPrompt(blocking, time.Now(), session.Record{}, "", []byte("body")); err == nil {
		t.Fatal("prompt was written through a non-directory worktree")
	}

	root := t.TempDir()
	original := filepath.Join(root, "original")
	alias := filepath.Join(root, "alias")
	if err := os.WriteFile(original, []byte("linked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRelativeRegular(root, "alias", 64); err == nil {
		t.Fatal("multiply-linked handover was accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "body"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRelativeRegular(root, "linked-dir/body", 64); err == nil {
		t.Fatal("handover below a symlinked directory was accepted")
	}
}

func TestSessionCustodyCoverageBatchPreparedClaimAndTerminal(t *testing.T) {
	t.Parallel()
	t.Run("claim read and write errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "claims"), 0o700); err != nil {
			t.Fatal(err)
		}
		claim := workLogClaim{ClaimID: strings.Repeat("a", 64)}
		if err := os.WriteFile(filepath.Join(root, "claims", claim.ClaimID+".json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		runDir, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = runDir.Close() }()
		if _, err := publishPreparedTargetClaim(runDir, claim, "conflict", "publish claim"); err == nil {
			t.Fatal("corrupt prepared claim was accepted")
		}
	})

	claim := workLogClaim{Version: 2, ClaimID: strings.Repeat("a", 64), Lifecycle: "active"}
	target := sessionmove.WorkLogReference{EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("b", 64)}
	request := sessionmove.Request{BundleCommit: strings.Repeat("c", 40), SuccessorWBSessionID: "wbs-successor"}
	evidence := &workLogExternalHandoffEvidence{Version: externalHandoffEvidenceVersion}

	t.Run("missing terminal", func(t *testing.T) {
		t.Parallel()
		runDir, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = runDir.Close() }()
		found, sealedAt, err := validateExistingExternalTerminal(runDir, claim, request, target, evidence)
		if err != nil || found || !sealedAt.IsZero() {
			t.Fatalf("missing terminal = %t/%s/%v", found, sealedAt, err)
		}
	})

	t.Run("corrupt terminal", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "terminals"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "terminals", claim.ClaimID+".json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		runDir, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = runDir.Close() }()
		if _, _, err := validateExistingExternalTerminal(runDir, claim, request, target, evidence); err == nil {
			t.Fatal("corrupt terminal was accepted")
		}
	})

	t.Run("conflicting and matching terminal", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		runDir, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = runDir.Close() }()
		terminals, err := openPrivateChild(runDir, "terminals", true)
		if err != nil {
			t.Fatal(err)
		}
		terminalClaim := claim
		terminalClaim.Lifecycle = "terminal"
		sealedAt := time.Unix(123, 0).UTC()
		terminal := workLogTerminalRecord{
			workLogClaim: terminalClaim, FinalCommit: request.BundleCommit,
			Disposition: "external_handoff", SealedAt: sealedAt,
			SuccessorClaimID: target.ClaimID, SuccessorAgentID: request.SuccessorWBSessionID,
			ExternalHandoff: evidence,
		}
		if err := writeJSONImmutableAt(terminals, claim.ClaimID+".json", terminal, true); err != nil {
			_ = terminals.Close()
			t.Fatal(err)
		}
		_ = terminals.Close()
		found, gotAt, err := validateExistingExternalTerminal(runDir, claim, request, target, evidence)
		if err != nil || !found || !gotAt.Equal(sealedAt) {
			t.Fatalf("matching terminal = %t/%s/%v", found, gotAt, err)
		}
		if _, _, err := validateExistingExternalTerminal(runDir, claim, request, target,
			&workLogExternalHandoffEvidence{Version: externalHandoffEvidenceVersion, HandoffID: "other"}); err == nil {
			t.Fatal("conflicting terminal was accepted")
		}
	})
}

func TestSessionCustodyCoverageBatchModelsAndParkedBranches(t *testing.T) {
	t.Parallel()
	if got := externalReceiptModel(workLogClaim{Model: "unknown", ModelProvenance: modelProvenanceUnknown}); got != "" {
		t.Fatalf("unknown receipt model = %q", got)
	}
	if got := externalReceiptModel(workLogClaim{Model: "gpt-5"}); got != "gpt-5" {
		t.Fatalf("declared receipt model = %q", got)
	}

	digest := sessionmove.DigestBytes([]byte("parked custody batch"))
	sourceClaimID := strings.Repeat("b", 64)
	member := sessionpark.RemoteMember{
		MemberID: "m-001-abcdef01", Repository: "acme/app", RepositoryRemote: "https://github.com/acme/app.git",
		Branch: "main", Commit: strings.Repeat("c", 40),
		SourceWorkLogReference: "worklog:session-park/source-run/" + sourceClaimID,
	}
	request := sessionpark.RemoteRequest{
		SchemaVersion: sessionpark.RequestSchemaVersion, ResumeID: "resume-custody-batch", ParkedSessionID: "park-custody-batch",
		SuccessorWBSessionID: "wbs-park-successor", PredecessorWBSessionID: "wbs-park-source",
		SourceMachine: "source", TargetMachine: "target", SourceRuntime: "codex", SourceModel: "gpt-5",
		Continuation: "continue", Members: []sessionpark.RemoteMember{member}, CreatedAt: time.Unix(100, 0).UTC(),
	}
	targetValue, err := sessionpark.TargetWorkLogReference(request, digest, member)
	if err != nil {
		t.Fatal(err)
	}
	target, err := sessionmove.ParseWorkLogReference(targetValue)
	if err != nil {
		t.Fatal(err)
	}
	claim := workLogClaim{
		EffortID: target.EffortID, RunID: target.RunID, ClaimID: target.ClaimID,
		ParentClaimID: sourceClaimID, AgentID: request.SuccessorWBSessionID, Repository: member.Repository,
		ExternalHandoff: &workLogExternalHandoffEvidence{
			Version: externalHandoffEvidenceVersion, Protocol: "parked_session_resume",
			HandoffID: request.ResumeID, MemberID: member.MemberID, RequestDigest: string(digest),
			PredecessorWBSessionID: request.PredecessorWBSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID,
			SourceWorkLogReference: member.SourceWorkLogReference, TargetWorkLogReference: targetValue,
			SuccessorTmuxName: "wb-session-" + request.SuccessorWBSessionID,
		},
	}
	wantClaimID, err := expectedParkedSessionClaimID(claim)
	if err != nil || wantClaimID != target.ClaimID {
		t.Fatalf("parked claim id = %q/%v, want %q", wantClaimID, err, target.ClaimID)
	}
	invalid := claim
	invalidEvidence := *claim.ExternalHandoff
	invalid.ExternalHandoff = &invalidEvidence
	invalid.ExternalHandoff.Protocol = "other"
	if _, err := expectedParkedSessionClaimID(invalid); err == nil {
		t.Fatal("parked claim with a conflicting protocol was accepted")
	}
	invalid = claim
	invalidEvidence = *claim.ExternalHandoff
	invalid.ExternalHandoff = &invalidEvidence
	invalid.ExternalHandoff.MemberID = ""
	if _, err := expectedParkedSessionClaimID(invalid); err == nil {
		t.Fatal("parked claim without a member was accepted")
	}
	invalid = claim
	invalid.ParentClaimID = strings.Repeat("d", 64)
	if _, err := expectedParkedSessionClaimID(invalid); err == nil {
		t.Fatal("parked claim with conflicting source lineage was accepted")
	}
	invalid = claim
	invalid.ClaimID = strings.Repeat("e", 64)
	if _, err := expectedParkedSessionClaimID(invalid); err == nil {
		t.Fatal("parked claim with conflicting target lineage was accepted")
	}

	record := session.Record{
		PID: os.Getpid(), WBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		Machine: request.TargetMachine, Runtime: request.SourceRuntime, Model: request.SourceModel,
		TmuxName: "wb-session-" + request.SuccessorWBSessionID, HandoffID: request.ResumeID,
		StartedAt: time.Unix(200, 0).UTC(),
	}
	if err := validateParkedTargetSession(request, record); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ParkedSessionWorkLogPrepareOptions){
		"relative worktree": func(options *ParkedSessionWorkLogPrepareOptions) { options.WorktreeDir = "." },
		"pinned commit": func(options *ParkedSessionWorkLogPrepareOptions) {
			options.WorktreeDir = t.TempDir()
			options.PinnedCommit = strings.Repeat("f", 40)
		},
		"target session": func(options *ParkedSessionWorkLogPrepareOptions) {
			options.WorktreeDir = t.TempDir()
			options.Session.PID = 0
		},
		"attempt": func(options *ParkedSessionWorkLogPrepareOptions) {
			options.WorktreeDir = t.TempDir()
			options.AttemptID = "bad"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := ParkedSessionWorkLogPrepareOptions{
				Request: request, RequestDigest: digest, Member: member, ReceivedAt: request.CreatedAt,
				Session: record, AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1,
				WorktreeDir: t.TempDir(), PinnedCommit: member.Commit,
			}
			mutate(&options)
			if _, err := PrepareParkedSessionWorkLog(context.Background(), options); err == nil {
				t.Fatalf("invalid parked preparation %q was accepted", name)
			}
		})
	}
}

func TestSessionCustodyRefactorBatchParkedPreparation(t *testing.T) {
	t.Parallel()
	digest := sessionmove.DigestBytes([]byte("parked preparation refactor"))
	member := sessionpark.RemoteMember{
		MemberID: "m-001-abcdef01", Repository: "acme/app", RepositoryRemote: "https://github.com/acme/app.git",
		Branch: "main", Commit: strings.Repeat("c", 40),
		SourceWorkLogReference: "worklog:session-park/source-run/" + strings.Repeat("b", 64),
	}
	request := sessionpark.RemoteRequest{
		SchemaVersion: sessionpark.RequestSchemaVersion, ResumeID: "resume-refactor", ParkedSessionID: "park-refactor",
		SuccessorWBSessionID: "wbs-park-successor", PredecessorWBSessionID: "wbs-park-source",
		SourceMachine: "source", TargetMachine: "target", SourceRuntime: "codex", SourceModel: "gpt-5",
		Continuation: "continue", Members: []sessionpark.RemoteMember{member}, CreatedAt: time.Unix(100, 0).UTC(),
	}
	record := session.Record{
		PID: os.Getpid(), WBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		Machine: request.TargetMachine, Runtime: request.SourceRuntime, Model: request.SourceModel,
		TmuxName: "wb-session-" + request.SuccessorWBSessionID, HandoffID: request.ResumeID,
		StartedAt: time.Unix(200, 0).UTC(),
	}
	newOptions := func(t *testing.T) ParkedSessionWorkLogPrepareOptions {
		t.Helper()
		return ParkedSessionWorkLogPrepareOptions{
			Request: request, RequestDigest: digest, Member: member, Session: record,
			AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1,
			WorktreeDir: t.TempDir(), PinnedCommit: member.Commit,
		}
	}
	withCheckout := func(t *testing.T, options ParkedSessionWorkLogPrepareOptions, branch, head string, failCall int) context.Context {
		t.Helper()
		fake := runnertest.New(t)
		fake.Expect(func(call runnertest.Call) bool {
			return ordinaryGitCall(call, options.WorktreeDir, "branch")
		}, runner.Result{CombinedOutput: branch + "\n"}, nil)
		if failCall != 1 && branch == sessionpark.MemberPin(request.ResumeID, member.MemberID) {
			fake.Expect(func(call runnertest.Call) bool {
				return ordinaryGitCall(call, options.WorktreeDir, "rev-parse")
			}, runner.Result{CombinedOutput: head + "\n"}, nil)
		}
		if failCall != 0 {
			fake.FailCall(failCall, errors.New("injected git failure"))
		}
		return withGitRunner(context.Background(), fake)
	}

	t.Run("deterministic preparation", func(t *testing.T) {
		t.Parallel()
		options := newOptions(t)
		wantBranch := sessionpark.MemberPin(request.ResumeID, member.MemberID)
		prepared, err := prepareParkedTarget(withCheckout(t, options, wantBranch, member.Commit, 0), options)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.worktree != options.WorktreeDir || prepared.claim.Branch != wantBranch ||
			prepared.claim.BaseSHA != member.Commit || prepared.claim.Model != request.SourceModel ||
			prepared.claim.RecordedAt != request.CreatedAt.UTC() || prepared.manifest.ClaimID != prepared.claim.ClaimID {
			t.Fatalf("prepared target = %#v", prepared)
		}
		if prepared.receivedEvent.Type != LocalEventHandoff || prepared.ownerEvent.Type != LocalEventOwner ||
			prepared.ownerEvent.Owner == nil || prepared.ownerEvent.Owner.PID != record.PID {
			t.Fatalf("prepared events = %#v / %#v", prepared.receivedEvent, prepared.ownerEvent)
		}
		public := preparedTargetPublicEvent(prepared.claim)
		if public.ClaimID != prepared.claim.ClaimID || public.ExternalHandoff != prepared.claim.ExternalHandoff {
			t.Fatalf("public event = %#v", public)
		}
		projection := activeTargetProjection(prepared.claim)
		if projection.ClaimID != prepared.claim.ClaimID || projection.Lifecycle != "active" {
			t.Fatalf("projection = %#v", projection)
		}
	})

	for name, mutate := range map[string]func(*ParkedSessionWorkLogPrepareOptions){
		"source reference": func(options *ParkedSessionWorkLogPrepareOptions) {
			options.Member.SourceWorkLogReference = "invalid"
			options.Request.Members = []sessionpark.RemoteMember{options.Member}
		},
		"relative worktree": func(options *ParkedSessionWorkLogPrepareOptions) { options.WorktreeDir = "." },
		"pinned commit":     func(options *ParkedSessionWorkLogPrepareOptions) { options.PinnedCommit = strings.Repeat("f", 40) },
		"target session":    func(options *ParkedSessionWorkLogPrepareOptions) { options.Session.PID = 0 },
		"attempt":           func(options *ParkedSessionWorkLogPrepareOptions) { options.AttemptID = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := newOptions(t)
			mutate(&options)
			if _, err := prepareParkedTarget(context.Background(), options); err == nil {
				t.Fatalf("invalid parked preparation %q was accepted", name)
			}
		})
	}

	t.Run("branch query", func(t *testing.T) {
		t.Parallel()
		options := newOptions(t)
		if _, err := prepareParkedTarget(withCheckout(t, options, "wrong", member.Commit, 0), options); err == nil {
			t.Fatal("wrong parked branch was accepted")
		}
	})
	t.Run("branch error", func(t *testing.T) {
		t.Parallel()
		options := newOptions(t)
		wantBranch := sessionpark.MemberPin(request.ResumeID, member.MemberID)
		if _, err := prepareParkedTarget(withCheckout(t, options, wantBranch, member.Commit, 1), options); err == nil {
			t.Fatal("failed parked branch query was accepted")
		}
	})
	t.Run("head", func(t *testing.T) {
		t.Parallel()
		options := newOptions(t)
		wantBranch := sessionpark.MemberPin(request.ResumeID, member.MemberID)
		if _, err := prepareParkedTarget(withCheckout(t, options, wantBranch, "wrong", 0), options); err == nil {
			t.Fatal("wrong parked head was accepted")
		}
	})
	t.Run("head error", func(t *testing.T) {
		t.Parallel()
		options := newOptions(t)
		wantBranch := sessionpark.MemberPin(request.ResumeID, member.MemberID)
		if _, err := prepareParkedTarget(withCheckout(t, options, wantBranch, member.Commit, 2), options); err == nil {
			t.Fatal("failed parked head query was accepted")
		}
	})
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		options := newOptions(t)
		options.Member.RepositoryRemote = "https://github.com/other/app.git"
		wantBranch := sessionpark.MemberPin(request.ResumeID, member.MemberID)
		if _, err := prepareParkedTarget(withCheckout(t, options, wantBranch, member.Commit, 0), options); err == nil {
			t.Fatal("conflicting parked repository was accepted")
		}
	})
	t.Run("unknown model", func(t *testing.T) {
		t.Parallel()
		options := newOptions(t)
		options.Request.SourceModel = ""
		options.Session.Model = ""
		wantBranch := sessionpark.MemberPin(request.ResumeID, member.MemberID)
		prepared, err := prepareParkedTarget(withCheckout(t, options, wantBranch, member.Commit, 0), options)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.claim.Model != "unknown" || prepared.claim.ModelProvenance != modelProvenanceUnknown {
			t.Fatalf("unknown model claim = %#v", prepared.claim)
		}
	})
}

func TestSessionCustodyRefactorBatchParkedCompletionValues(t *testing.T) {
	t.Parallel()
	digest := sessionmove.DigestBytes([]byte("parked completion refactor"))
	startedAt := time.Unix(300, 0).UTC()
	options := ParkedTargetCompletionOptions{
		Request:       sessionpark.RemoteRequest{ResumeID: "resume-refactor", SuccessorWBSessionID: "wbs-successor"},
		RequestDigest: digest,
		Member:        sessionpark.RemoteMember{MemberID: "m-001-abcdef01", Commit: strings.Repeat("c", 40)},
		WorktreeDir:   "/tmp/parked-refactor",
		Successor: sessionlaunch.Result{
			AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1,
			PID: os.Getpid(), StartedAt: startedAt,
		},
	}
	targetValue := "worklog:session-park/run/" + strings.Repeat("a", 64)
	target, err := sessionmove.ParseWorkLogReference(targetValue)
	if err != nil {
		t.Fatal(err)
	}
	claim := workLogClaim{
		EffortID: target.EffortID, RunID: target.RunID, ClaimID: target.ClaimID,
		Worktree: options.WorktreeDir, Branch: sessionpark.MemberPin(options.Request.ResumeID, options.Member.MemberID),
		BaseSHA: options.Member.Commit, Lifecycle: "active", AgentID: options.Request.SuccessorWBSessionID,
		ExternalHandoff: &workLogExternalHandoffEvidence{TargetWorkLogReference: targetValue},
	}
	if err := corroborateParkedTargetClaim(claim, options, target, targetValue); err != nil {
		t.Fatal(err)
	}
	invalid := claim
	invalid.Lifecycle = "terminal"
	if err := corroborateParkedTargetClaim(invalid, options, target, targetValue); err == nil {
		t.Fatal("terminal parked claim was accepted")
	}
	wantOwnerID := externalLocalEventID("park-target-owner-"+options.Member.MemberID, digest, options.Successor.AttemptID)
	owner := OwnerRegistration{PID: options.Successor.PID, At: startedAt}
	events := []LocalWorkLogEvent{{ID: wantOwnerID, Type: LocalEventOwner, Owner: &owner}}
	if err := validateParkedTargetOwner(events, options); err != nil {
		t.Fatal(err)
	}
	events = append(events, LocalWorkLogEvent{ID: "later-owner", Type: LocalEventOwner, Owner: &OwnerRegistration{PID: os.Getpid(), At: startedAt.Add(time.Second)}})
	if err := validateParkedTargetOwner(events, options); err == nil {
		t.Fatal("superseded parked owner was accepted")
	}
	if err := validateParkedTargetOwner(nil, options); err == nil {
		t.Fatal("missing parked owner was accepted")
	}
	event := parkedTargetCompletionEvent(options, targetValue)
	if event.Type != LocalEventHandoff || event.Result != "completed" || event.At != startedAt ||
		event.Extra["target_work_log_reference"] != targetValue || event.Extra["pid"] != options.Successor.PID {
		t.Fatalf("completion event = %#v", event)
	}
}

func TestSessionCustodyRefactorBatchExternalSourceValues(t *testing.T) {
	t.Parallel()
	at := time.Unix(400, 0).UTC()
	digest := sessionmove.DigestBytes([]byte("external source values"))
	request := sessionmove.Request{HandoffID: "handoff-values", CreatedAt: at}
	state := sessionmove.State{Request: request, Digest: digest}
	found, err := validateExternalOfferState(state, request, digest)
	if err != nil || found {
		t.Fatalf("empty offered state = %t/%v", found, err)
	}
	state.Events = []sessionmove.HandoffEvent{
		{Phase: sessionmove.PhaseReceived, At: at.Add(-time.Second)},
		{Phase: sessionmove.PhaseOffered, At: at},
	}
	found, err = validateExternalOfferState(state, request, digest)
	if err != nil || !found {
		t.Fatalf("exact offered state = %t/%v", found, err)
	}
	conflicting := state
	conflicting.Digest = sessionmove.DigestBytes([]byte("other"))
	if _, err := validateExternalOfferState(conflicting, request, digest); err == nil {
		t.Fatal("conflicting admitted offer state was accepted")
	}
	conflicting = state
	conflicting.Events = []sessionmove.HandoffEvent{{Phase: sessionmove.PhaseOffered, At: at.Add(time.Second)}}
	if _, err := validateExternalOfferState(conflicting, request, digest); err == nil {
		t.Fatal("conflicting offered checkpoint was accepted")
	}
	conflicting.Events[0].At = at
	conflicting.Events[0].Diagnostic = "failed"
	if _, err := validateExternalOfferState(conflicting, request, digest); err == nil {
		t.Fatal("diagnostic-bearing offered checkpoint was accepted")
	}

	receipt := sessionmove.Receipt{HandoffID: request.HandoffID}
	state.Receipt = &receipt
	if err := validateExternalSealState(state, request, digest, receipt); err != nil {
		t.Fatal(err)
	}
	missingReceipt := state
	missingReceipt.Receipt = nil
	if err := validateExternalSealState(missingReceipt, request, digest, receipt); err == nil {
		t.Fatal("seal without an exact durable receipt was accepted")
	}

	claim := workLogClaim{EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64)}
	projection := terminalTargetProjection(claim)
	if projection.EffortID != claim.EffortID || projection.RunID != claim.RunID ||
		projection.ClaimID != claim.ClaimID || projection.Lifecycle != "terminal" {
		t.Fatalf("terminal projection = %#v", projection)
	}
	offerOptions := ExternalSourceOfferOptions{
		Request: request, RequestDigest: digest,
		SourceSession: session.Record{Runtime: "codex", WBSessionID: "wbs-source", Model: "gpt-5", PID: os.Getpid()},
	}
	ownerEvent := externalSourceOwnerEvent(offerOptions, claim)
	if ownerEvent.Type != LocalEventOwner || ownerEvent.Owner == nil || ownerEvent.Owner.Effort != claim.EffortID ||
		ownerEvent.Owner.PID != os.Getpid() || ownerEvent.At != at {
		t.Fatalf("source owner event = %#v", ownerEvent)
	}
	completion := externalSourceCompletionEvent(request, digest, "worklog:effort/run/claim")
	if completion.Type != LocalEventHandoff || completion.Result != "completed" ||
		completion.Extra["target_work_log_reference"] != "worklog:effort/run/claim" || completion.Extra["endpoint"] != "source" {
		t.Fatalf("source completion event = %#v", completion)
	}
}

func TestSessionCustodyRefactorBatchExternalAttemptFailureEvent(t *testing.T) {
	t.Parallel()
	digest := sessionmove.DigestBytes([]byte("external attempt failure"))
	startedAt := time.Unix(500, 0).UTC()
	failure := sessionlaunch.FailureEvidence{
		AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1, PID: 123,
		StartedAt: startedAt, FailedAt: startedAt.Add(time.Second), Diagnostic: "  launcher failed  ",
	}
	event := externalTargetAttemptFailureEvent(
		sessionmove.Request{HandoffID: "handoff-failure"}, digest, "worklog:effort/run/claim", failure,
	)
	wantDigest := sha256.Sum256([]byte("launcher failed"))
	if event.Type != LocalEventHandoff || event.Result != "failed" || !event.At.Equal(failure.FailedAt.UTC()) ||
		event.Extra["handoff_id"] != "handoff-failure" || event.Extra["attempt_id"] != failure.AttemptID ||
		event.Extra["target_work_log_reference"] != "worklog:effort/run/claim" ||
		event.Extra["diagnostic_sha256"] != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("attempt failure event = %#v", event)
	}
}
