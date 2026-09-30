package worktrees

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

const externalHandoffEvidenceVersion = 1

// These seams retain the descriptor-first safety boundary while making the
// kernel failure and post-stat drift outcomes deterministic in unit tests.
var (
	readBoundedRelativeRegularFstat   = unix.Fstat
	readBoundedRelativeRegularReadAll = io.ReadAll
)

type workLogExternalHandoffEvidence = worktreeclaims.ExternalHandoffEvidence

func sameExternalHandoffEvidence(first, second *workLogExternalHandoffEvidence) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}

// ExternalSessionWorkLogPrepareOptions describes the target-side publication
// performed while the launcher is ready but still fenced before Exec.
type ExternalSessionWorkLogPrepareOptions struct {
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	ReceivedAt    time.Time
	Session       session.Record
	AttemptID     string
	AttemptIndex  uint64
	WorktreeDir   string
	PinnedCommit  string
	HandoverBytes []byte

	hooks externalSessionWorkLogHooks
}

type externalSessionWorkLogHooks struct {
	afterClaim      func() error
	afterRunIndex   func() error
	afterProjection func() error
	afterJournal    func() error
	afterOutbox     func() error
}

// ExternalSessionWorkLogPrepareResult identifies stable custody plus the
// attempt-scoped owner evidence appended for this launcher PID.
type ExternalSessionWorkLogPrepareResult struct {
	WorkLogReference string            `json:"work_log_reference"`
	ClaimID          string            `json:"claim_id"`
	ReceivedEvent    LocalWorkLogEvent `json:"received_event"`
	OwnerEvent       LocalWorkLogEvent `json:"owner_event"`
	Replayed         bool              `json:"replayed"`
}

type externalTargetPreparation struct {
	targetReference string
	worktree        string
	handover        []byte
	claim           workLogClaim
	manifest        Manifest
	receivedEvent   LocalWorkLogEvent
	ownerEvent      LocalWorkLogEvent
}

// Publication keeps faultable journal and descriptor operations local to one
// prepared handoff. Its default bindings are the existing durable primitives.
type externalTargetPublicationPorts struct {
	appendEvent func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error)
	closeOutbox func(*os.File) error
}

func defaultExternalTargetPublicationPorts() externalTargetPublicationPorts {
	return externalTargetPublicationPorts{
		appendEvent: appendLocalEventWithoutCustody,
		closeOutbox: (*os.File).Close,
	}
}

// PrepareExternalSessionWorkLog publishes one deterministic external target
// claim before launcher release. Claim identity excludes attempt/PID/time;
// each prepared attempt appends its own idempotent owner evidence under it.
func PrepareExternalSessionWorkLog(ctx context.Context, options ExternalSessionWorkLogPrepareOptions) (ExternalSessionWorkLogPrepareResult, error) {
	return defaultExternalTargetPublicationPorts().prepare(ctx, options)
}

func (p externalTargetPublicationPorts) prepare(ctx context.Context, options ExternalSessionWorkLogPrepareOptions) (ExternalSessionWorkLogPrepareResult, error) {
	var result ExternalSessionWorkLogPrepareResult
	prepared, err := prepareExternalTarget(ctx, options)
	if err != nil {
		return result, err
	}
	claim := prepared.claim
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	locked, err := openLockedWorkLogRun(home, claim.EffortID, claim.RunID, claim.ClaimID, true)
	if err != nil {
		return result, err
	}
	defer locked.close()
	runDir := locked.directory
	result.Replayed, err = publishPreparedTargetClaim(runDir, claim,
		"immutable external target Work Log claim conflicts with admitted handoff",
		"publish immutable external target Work Log claim")
	if err != nil {
		return result, err
	}
	if options.hooks.afterClaim != nil {
		if err := options.hooks.afterClaim(); err != nil {
			return result, err
		}
	}
	if err := ensureWorkLogRunIndex(runDir, claim.EffortID, claim.RunID); err != nil {
		return result, err
	}
	if options.hooks.afterRunIndex != nil {
		if err := options.hooks.afterRunIndex(); err != nil {
			return result, err
		}
	}
	if err := ensureExternalManifest(prepared.worktree, prepared.manifest); err != nil {
		return result, err
	}
	if err := ensureExternalHandoverPrompt(prepared.worktree, claim.RecordedAt, options.Session, options.Request.HandoverDigest, prepared.handover); err != nil {
		return result, err
	}
	prepared.receivedEvent, _, err = p.appendEvent(prepared.worktree, prepared.receivedEvent)
	if err != nil {
		return result, fmt.Errorf("record target Work Log receipt evidence: %w", err)
	}
	prepared.ownerEvent, _, err = p.appendEvent(prepared.worktree, prepared.ownerEvent)
	if err != nil {
		return result, fmt.Errorf("record target Work Log attempt owner: %w", err)
	}
	if options.hooks.afterJournal != nil {
		if err := options.hooks.afterJournal(); err != nil {
			return result, err
		}
	}
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return result, err
	}
	public := preparedTargetPublicEvent(claim)
	err = writeJSONImmutableAt(outbox, claim.RunID+"-"+claim.ClaimID+"-claimed.json", public, true)
	closeErr := p.closeOutbox(outbox)
	if err != nil {
		return result, fmt.Errorf("publish external target Work Log outbox: %w", errors.Join(err, closeErr))
	}
	if closeErr != nil {
		return result, fmt.Errorf("close external target Work Log outbox: %w", closeErr)
	}
	if options.hooks.afterOutbox != nil {
		if err := options.hooks.afterOutbox(); err != nil {
			return result, err
		}
	}
	// The hybrid projection is deliberately the last identity publication;
	// immediately replay the local cache so this final write cannot leave it
	// stale or identity-poor.
	if err := writeWorkLogProjection(prepared.worktree, activeTargetProjection(claim)); err != nil {
		return result, err
	}
	if options.hooks.afterProjection != nil {
		if err := options.hooks.afterProjection(); err != nil {
			return result, err
		}
	}
	if _, err := repairCurrentLocalProjection(prepared.worktree); err != nil {
		return result, err
	}
	result.WorkLogReference, result.ClaimID = prepared.targetReference, claim.ClaimID
	result.ReceivedEvent, result.OwnerEvent = prepared.receivedEvent, prepared.ownerEvent
	return result, nil
}

// prepareExternalTarget validates admitted identity and checkout state before
// the publication transaction acquires the immutable claim lock.
func prepareExternalTarget(ctx context.Context, options ExternalSessionWorkLogPrepareOptions) (externalTargetPreparation, error) {
	var prepared externalTargetPreparation
	request := options.Request
	targetReference, err := sessionmove.ExpectedTargetWorkLogReference(request, options.RequestDigest)
	if err != nil {
		return prepared, fmt.Errorf("derive target Work Log reference: %w", err)
	}
	// ExpectedTargetWorkLogReference already parsed and validated this source.
	sourceReference, _ := sessionmove.ParseWorkLogReference(request.WorkLogReference)
	worktree, err := filepath.Abs(options.WorktreeDir)
	if err != nil || filepath.Clean(worktree) != worktree || worktree != options.WorktreeDir {
		return prepared, fmt.Errorf("external target Work Log requires one clean absolute worktree path")
	}
	if options.PinnedCommit != request.BundleCommit {
		return prepared, fmt.Errorf("target Work Log pinned commit does not match admitted bundle commit")
	}
	if err := validateExternalTargetSession(request, options.Session); err != nil {
		return prepared, err
	}
	if !validExternalAttempt(options.AttemptID, options.AttemptIndex) {
		return prepared, fmt.Errorf("launcher attempt identity is invalid for target owner evidence")
	}
	branch, err := git(ctx, worktree, "branch", "--show-current")
	if err != nil || branch != "wb-session/"+request.HandoffID {
		return prepared, fmt.Errorf("target worktree branch %q does not match handoff pin branch", branch)
	}
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil || head != options.PinnedCommit {
		return prepared, fmt.Errorf("target worktree HEAD %q does not match pinned commit %q", head, options.PinnedCommit)
	}
	remote, err := gitremote.Parse(request.RepositoryRemote)
	if err != nil {
		return prepared, err
	}
	handover := options.HandoverBytes
	if len(handover) == 0 {
		handover, err = requestHandoverBytes(worktree, request)
		if err != nil {
			return prepared, fmt.Errorf("read admitted handover document: %w", err)
		}
	}
	if !request.HandoverDigest.Matches(handover) {
		return prepared, fmt.Errorf("target handover bytes do not match admitted digest")
	}
	receivedAt := options.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = request.CreatedAt.UTC()
	}
	evidence := externalHandoffEvidence(request, options.RequestDigest, targetReference.String())
	model := strings.TrimSpace(options.Session.Model)
	modelProvenance := modelProvenanceCallerDeclared
	if model == "" {
		model = "unknown"
		modelProvenance = modelProvenanceUnknown
	}
	claim := workLogClaim{
		Version: 2, EffortID: sourceReference.EffortID, RunID: sourceReference.RunID, ClaimID: targetReference.ClaimID,
		Task: "external session handoff " + request.HandoffID, Repository: remote.Identity.Repository,
		Worktree: worktree, Branch: branch, Base: request.Branch, BaseSHA: request.SourceWorkCommit,
		Lifecycle: "active", RecordedAt: receivedAt, Initiator: request.PredecessorWBSessionID,
		AgentID: request.SuccessorWBSessionID, AgentRuntime: options.Session.Runtime, Model: model,
		ModelProvenance: modelProvenance, ModelDeclaredBy: request.PredecessorWBSessionID,
		ParentClaimID: sourceReference.ClaimID, AcquiredVia: "external_handoff", ExternalHandoff: evidence,
	}
	manifest := preparedTargetManifest(claim, receivedAt, options.Session.Model)
	receivedEvent := externalTargetReceivedEvent(request, options.RequestDigest, targetReference.String(), receivedAt)
	ownerEvent := externalTargetOwnerEvent(request, options.RequestDigest, claim, targetReference.String(),
		options.AttemptID, options.AttemptIndex, options.Session.PID, options.Session.StartedAt, buildinfo.Version())
	return externalTargetPreparation{
		targetReference: targetReference.String(), worktree: worktree, handover: handover,
		claim: claim, manifest: manifest, receivedEvent: receivedEvent, ownerEvent: ownerEvent,
	}, nil
}

// publishPreparedTargetClaim owns the short-lived claims directory handle.
// The caller retains the claim lock through all later publication stages.
func publishPreparedTargetClaim(runDir *os.File, claim workLogClaim, conflictMessage, writeErrorContext string) (bool, error) {
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		return false, err
	}
	defer func() { _ = claims.Close() }()
	var existing workLogClaim
	readErr := readJSONAt(claims, claim.ClaimID+".json", &existing)
	replayed := readErr == nil
	if replayed && !reflect.DeepEqual(existing, claim) {
		return replayed, errors.New(conflictMessage)
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return replayed, readErr
	}
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", claim, true); err != nil {
		if writeErrorContext != "" {
			return replayed, fmt.Errorf("%s: %w", writeErrorContext, err)
		}
		return replayed, err
	}
	return replayed, nil
}

func preparedTargetManifest(claim workLogClaim, receivedAt time.Time, model string) Manifest {
	return Manifest{
		Version: 1, EffortID: claim.EffortID, ParentEffort: ParentEffort(claim.EffortID), EffortKind: EffortKindFor(claim.EffortID),
		Repository: claim.Repository, Worktree: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
		CreatedAt: receivedAt, Initiator: claim.Initiator, AgentID: claim.AgentID, AgentRuntime: claim.AgentRuntime,
		Model: model, RunID: claim.RunID, ClaimID: claim.ClaimID, Provenance: ProvenanceCreated,
	}
}

func externalTargetReceivedEvent(request sessionmove.Request, digest sessionmove.Digest, targetReference string, at time.Time) LocalWorkLogEvent {
	return LocalWorkLogEvent{
		Version: 1, ID: externalLocalEventID("target-received", digest, ""), Type: LocalEventHandoff, At: at.UTC(),
		Message: "external session handoff received", Result: "received",
		Extra: externalLocalEventExtra(request, targetReference, "target"),
	}
}

func externalTargetOwnerEvent(request sessionmove.Request, digest sessionmove.Digest, claim workLogClaim, targetReference,
	attemptID string, attemptIndex uint64, pid int, startedAt time.Time, wbVersion string,
) LocalWorkLogEvent {
	owner := OwnerRegistration{
		Agent: claim.AgentRuntime + "/" + claim.AgentID, Model: externalReceiptModel(claim),
		Effort: claim.EffortID, PID: pid, WBVersion: wbVersion, Command: "session receive", At: startedAt.UTC(),
	}
	return LocalWorkLogEvent{
		Version: 1, ID: externalLocalEventID("target-owner", digest, attemptID), Type: LocalEventOwner,
		At: owner.At, Message: "successor launcher attempt prepared", Owner: &owner,
		Extra: map[string]any{"handoff_id": request.HandoffID, "attempt_id": attemptID,
			"attempt_index": attemptIndex, "target_work_log_reference": targetReference},
	}
}

// ExternalTargetCompletionOptions records proof of a live successor before a
// receipt may be published in the handoff aggregate.
type ExternalTargetCompletionOptions struct {
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	Receipt       sessionmove.Receipt
	WorktreeDir   string
}

// RecordExternalTargetCompleted appends deterministic completion evidence to
// the active target Work Log. An exact replay repairs its outbox/projection.
func RecordExternalTargetCompleted(options ExternalTargetCompletionOptions) (LocalWorkLogEvent, error) {
	if err := validateExternalReceipt(options.Request, options.RequestDigest, options.Receipt); err != nil {
		return LocalWorkLogEvent{}, err
	}
	expectedEventID := externalLocalEventID("target-completed", options.RequestDigest, "")
	claim, _, unlock, err := loadExternalTargetClaim(options.ProjectsRoot, options.Request, options.RequestDigest, options.WorktreeDir)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer unlock()
	if err := validateExternalAttemptOwner(claim.Worktree, options.Request, options.RequestDigest, claim,
		options.Receipt.AttemptID, options.Receipt.AttemptIndex, options.Receipt.PID, options.Receipt.StartedAt, true); err != nil {
		return LocalWorkLogEvent{}, err
	}
	event := LocalWorkLogEvent{
		ID: expectedEventID, Type: LocalEventHandoff,
		Message: "external successor proved live; target custody completed", Result: "completed",
		Extra: externalLocalEventExtra(options.Request, options.Receipt.TargetWorkLogReference, "target"),
	}
	event.Extra["tmux_name"] = options.Receipt.TmuxName
	event.Extra["runtime"] = options.Receipt.Runtime
	event.Extra["pinned_commit"] = options.Receipt.PinnedCommit
	event.Extra["attempt_id"] = options.Receipt.AttemptID
	event.Extra["attempt_index"] = options.Receipt.AttemptIndex
	event.Extra["pid"] = options.Receipt.PID
	event.Extra["started_at"] = options.Receipt.StartedAt.UTC()
	event, _, err = appendLocalEventWithoutCustody(claim.Worktree, event)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	return event, nil
}

// ExternalTargetAttemptFailureOptions is exact post-release launcher failure
// evidence. It never terminalizes the stable target claim; a later attempt may
// acquire the same claim with a different PID.
type ExternalTargetAttemptFailureOptions struct {
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	WorktreeDir   string
	Failure       sessionlaunch.FailureEvidence
}

func RecordExternalTargetAttemptFailed(options ExternalTargetAttemptFailureOptions) (LocalWorkLogEvent, error) {
	return recordExternalTargetAttemptFailed(options, func(failure sessionlaunch.FailureEvidence, handoffID string, digest sessionmove.Digest, targetReference string) bool {
		return failure.Authenticates(handoffID, digest, targetReference)
	})
}

func recordExternalTargetAttemptFailed(options ExternalTargetAttemptFailureOptions,
	authenticates func(sessionlaunch.FailureEvidence, string, sessionmove.Digest, string) bool,
) (LocalWorkLogEvent, error) {
	failure := options.Failure
	expectedReference, referenceErr := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
	if referenceErr != nil || !authenticates(failure, options.Request.HandoffID, options.RequestDigest, expectedReference.String()) ||
		!validExternalAttempt(failure.AttemptID, failure.AttemptIndex) || failure.PID <= 0 || failure.StartedAt.IsZero() || failure.FailedAt.IsZero() ||
		failure.FailedAt.Before(failure.StartedAt) || strings.TrimSpace(failure.Diagnostic) == "" {
		return LocalWorkLogEvent{}, fmt.Errorf("exact failed launcher attempt evidence is incomplete")
	}
	claim, reference, unlock, err := loadExternalTargetClaim(options.ProjectsRoot, options.Request, options.RequestDigest, options.WorktreeDir)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer unlock()
	if err := validateExternalAttemptOwner(claim.Worktree, options.Request, options.RequestDigest, claim,
		failure.AttemptID, failure.AttemptIndex, failure.PID, failure.StartedAt, false); err != nil {
		return LocalWorkLogEvent{}, err
	}
	return appendExternalTargetAttemptFailure(
		claim.Worktree, options.Request, options.RequestDigest, reference.String(), externalAttemptFailureRecord{
			AttemptID: failure.AttemptID, AttemptIndex: failure.AttemptIndex, PID: failure.PID,
			StartedAt: failure.StartedAt, FailedAt: failure.FailedAt, Diagnostic: failure.Diagnostic,
		},
	)
}

// externalAttemptFailureRecord only exists after RecordExternalTargetAttemptFailed
// authenticates immutable launcher evidence. Keeping the durable append separate
// lets its journal behavior be tested without forging cross-package evidence.
type externalAttemptFailureRecord struct {
	AttemptID    string
	AttemptIndex uint64
	PID          int
	StartedAt    time.Time
	FailedAt     time.Time
	Diagnostic   string
}

func appendExternalTargetAttemptFailure(worktree string, request sessionmove.Request, digest sessionmove.Digest,
	targetReference string, failure externalAttemptFailureRecord,
) (LocalWorkLogEvent, error) {
	event := externalTargetAttemptFailureEvent(request, digest, targetReference, failure)
	event, _, err := appendLocalEventWithoutCustody(worktree, event)
	return event, err
}

func externalTargetAttemptFailureEvent(request sessionmove.Request, digest sessionmove.Digest, targetReference string, failure externalAttemptFailureRecord) LocalWorkLogEvent {
	diagnosticDigest := sha256.Sum256([]byte(strings.TrimSpace(failure.Diagnostic)))
	return LocalWorkLogEvent{
		ID: externalLocalEventID("target-attempt-failed", digest, failure.AttemptID), Type: LocalEventHandoff,
		At: failure.FailedAt.UTC(), Message: "external successor launcher attempt failed after release", Result: "failed",
		Extra: map[string]any{"handoff_id": request.HandoffID, "endpoint": "target",
			"target_work_log_reference": targetReference, "attempt_id": failure.AttemptID,
			"attempt_index": failure.AttemptIndex, "pid": failure.PID, "started_at": failure.StartedAt.UTC(),
			"diagnostic_sha256": hex.EncodeToString(diagnosticDigest[:])},
	}
}

// ExternalSourceSealOptions describes receipt-authorized predecessor sealing.
// The caller must first persist the receipt under its exact aggregate lock.
type ExternalSourceSealOptions struct {
	Store         sessionmove.Store
	ExecutionLock *sessionmove.ExecutionLock
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	Receipt       sessionmove.Receipt
	SourceSession session.Record

	hooks externalSourceSealHooks
}

// ExternalSourceOfferOptions supplies the exact admitted source aggregate and
// the still-live predecessor that owns it. The retained execution lock makes
// repair descriptor-relative to the same request authority later used for the
// receipt and completed phase.
type ExternalSourceOfferOptions struct {
	Store         sessionmove.Store
	ExecutionLock *sessionmove.ExecutionLock
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	SourceSession session.Record

	hooks externalSourceOfferHooks
}

type externalSourceOfferHooks struct {
	afterOfferedPhase func() error
	afterOffer        func() error
}

// ExternalSourceOfferResult reports the exact request-bound source evidence.
// Replayed is true only when both Work Log records already existed.
type ExternalSourceOfferResult struct {
	OfferEvent LocalWorkLogEvent `json:"offer_event"`
	OwnerEvent LocalWorkLogEvent `json:"owner_event"`
	Replayed   bool              `json:"replayed"`
}

func validateExternalOfferState(state sessionmove.State, request sessionmove.Request, digest sessionmove.Digest) (bool, error) {
	if state.Request != request || state.Digest != digest {
		return false, fmt.Errorf("source offer repair does not match exact admitted request")
	}
	offeredFound := false
	for _, event := range state.Events {
		if event.Phase != sessionmove.PhaseOffered {
			continue
		}
		if !event.At.Equal(request.CreatedAt.UTC()) || event.Diagnostic != "" {
			return false, fmt.Errorf("durable offered phase conflicts with admitted source checkpoint")
		}
		offeredFound = true
	}
	return offeredFound, nil
}

// EnsureExternalSourceOfferEvidence repairs the two source checkpoint crash
// gaps under one exact admitted aggregate authority:
//
//	PhaseOffered -> deterministic offer-only Work Log event -> source owner.
//
// It never derives event content by parsing free-form Markdown. The request
// carries the exact normalized fields and their digest, so headings in a user
// handover cannot make an otherwise valid move unsealable.
func EnsureExternalSourceOfferEvidence(options ExternalSourceOfferOptions) (ExternalSourceOfferResult, error) {
	return defaultExternalSourceOfferPorts().ensure(options)
}

// The admitted Store.LoadUnderLock and execution descriptor remain direct in
// ensure. Only fallible follow-on effects use these invocation-local ports.
type externalSourceOfferPorts struct {
	appendOffered func(ExternalSourceOfferOptions) error
	gitStatus     func(string) (string, error)
	appendEvent   func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error)
}

func defaultExternalSourceOfferPorts() externalSourceOfferPorts {
	return externalSourceOfferPorts{
		appendOffered: func(options ExternalSourceOfferOptions) error {
			_, err := options.Store.AppendEventUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest,
				sessionmove.HandoffEvent{Phase: sessionmove.PhaseOffered, At: options.Request.CreatedAt.UTC()})
			return err
		},
		gitStatus: func(worktree string) (string, error) {
			return git(context.Background(), worktree, "status", "--porcelain=v1", "--untracked-files=all")
		},
		appendEvent: appendLocalEventWithoutCustody,
	}
}

func (p externalSourceOfferPorts) ensure(options ExternalSourceOfferOptions) (ExternalSourceOfferResult, error) {
	var result ExternalSourceOfferResult
	if options.ExecutionLock == nil {
		return result, fmt.Errorf("external source offer repair requires retained admitted request authority")
	}
	state, err := options.Store.LoadUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest)
	if err != nil {
		return result, fmt.Errorf("load exact source offer aggregate: %w", err)
	}
	offeredFound, err := validateExternalOfferState(state, options.Request, options.RequestDigest)
	if err != nil {
		return result, err
	}
	if err := validateExternalSourceSession(options.SourceSession, options.Request); err != nil {
		return result, err
	}
	if options.Request.CreatedAt.Before(options.SourceSession.StartedAt.UTC()) {
		return result, fmt.Errorf("admitted source offer predates the predecessor session")
	}

	if !offeredFound {
		if err := p.appendOffered(options); err != nil {
			return result, fmt.Errorf("repair durable offered phase: %w", err)
		}
	}
	if options.hooks.afterOfferedPhase != nil {
		if err := options.hooks.afterOfferedPhase(); err != nil {
			return result, err
		}
	}

	// LoadUnderLock decoded and validated the exact admitted request, and
	// validateExternalOfferState matched it to options.Request. The parse is
	// deterministic, so a second error check cannot be reached here.
	sourceReference, _ := sessionmove.ParseWorkLogReference(options.Request.WorkLogReference)
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	locked, err := openLockedWorkLogRun(home, sourceReference.EffortID, sourceReference.RunID, sourceReference.ClaimID, false)
	if err != nil {
		return result, err
	}
	defer locked.close()
	runDir := locked.directory
	claim, err := readWorkLogClaimAt(runDir, sourceReference.ClaimID)
	if err != nil {
		return result, err
	}
	projection, err := readWorkLogProjection(claim.Worktree)
	if err != nil {
		return result, err
	}
	if claim.EffortID != sourceReference.EffortID || claim.RunID != sourceReference.RunID || claim.ClaimID != sourceReference.ClaimID ||
		projection.EffortID != sourceReference.EffortID || projection.RunID != sourceReference.RunID || projection.ClaimID != sourceReference.ClaimID ||
		(projection.Lifecycle != "active" && projection.Lifecycle != "terminal") {
		return result, fmt.Errorf("source Work Log identity conflicts with admitted offer")
	}
	if projection.Lifecycle == "active" {
		if err := corroborateClaim(claim.Worktree, options.Request.BundleCommit, projection, claim); err != nil {
			return result, fmt.Errorf("corroborate active source Work Log before offer repair: %w", err)
		}
		status, statusErr := p.gitStatus(claim.Worktree)
		if statusErr != nil {
			return result, fmt.Errorf("inspect source worktree before offer repair: %w", statusErr)
		}
		if status != "" {
			return result, fmt.Errorf("source worktree changed after its admitted handoff checkpoint")
		}
		if ownerPIDStatus(options.SourceSession.PID) != "active" {
			return result, fmt.Errorf("admitted predecessor session is not live")
		}
	}
	handover, err := requestHandoverBytes(claim.Worktree, options.Request)
	if err != nil {
		return result, fmt.Errorf("read admitted source handover document: %w", err)
	}
	if !options.Request.HandoverDigest.Matches(handover) {
		return result, fmt.Errorf("source handover document does not match admitted immutable bytes")
	}

	existing, err := readLocalEvents(claim.Worktree)
	if err != nil {
		return result, fmt.Errorf("read source Work Log offer repair authority: %w", err)
	}
	offer, offerFound, err := findExternalSourceOffer(existing, options.Request, options.RequestDigest)
	if err != nil {
		return result, err
	}
	if !offerFound {
		offer, _, err = p.appendEvent(claim.Worktree, externalSourceOfferEvent(options.Request, options.RequestDigest))
		if err != nil {
			return result, fmt.Errorf("repair deterministic source Work Log offer: %w", err)
		}
	}
	result.OfferEvent = offer
	if options.hooks.afterOffer != nil {
		if err := options.hooks.afterOffer(); err != nil {
			return result, err
		}
	}

	existing, err = readLocalEvents(claim.Worktree)
	if err != nil {
		return result, err
	}
	owner, ownerFound, err := findExternalSourceOwner(existing, options.Request, options.RequestDigest, options.SourceSession, claim, false)
	if err != nil {
		return result, err
	}
	if !ownerFound {
		owner, _, err = p.appendEvent(claim.Worktree, externalSourceOwnerEvent(options, claim))
		if err != nil {
			return result, fmt.Errorf("repair exact source session owner for handoff: %w", err)
		}
	}
	result.OwnerEvent = owner
	result.Replayed = offerFound && ownerFound
	return result, nil
}

type externalSourceSealHooks struct {
	afterTerminal   func() error
	afterProjection func() error
	afterCompletion func() error
}

type ExternalSourceSealResult struct {
	SourceWorkLogReference string            `json:"source_work_log_reference"`
	TargetWorkLogReference string            `json:"target_work_log_reference"`
	SealedAt               time.Time         `json:"sealed_at"`
	CompletionEvent        LocalWorkLogEvent `json:"completion_event"`
	Replayed               bool              `json:"replayed"`
}

func validateExternalSealState(state sessionmove.State, request sessionmove.Request, digest sessionmove.Digest, receipt sessionmove.Receipt) error {
	if state.Request != request || state.Digest != digest || state.Receipt == nil || *state.Receipt != receipt {
		return fmt.Errorf("durable source receipt does not exactly authorize requested custody seal")
	}
	return nil
}

func externalSourceOwnerEvent(options ExternalSourceOfferOptions, claim workLogClaim) LocalWorkLogEvent {
	owner := OwnerRegistration{
		Agent: options.SourceSession.Runtime + "/" + options.SourceSession.WBSessionID,
		Model: options.SourceSession.Model, Effort: claim.EffortID, PID: options.SourceSession.PID,
		WBVersion: buildinfo.Version(), Command: "session move offer", At: options.Request.CreatedAt.UTC(),
	}
	return LocalWorkLogEvent{
		ID: externalLocalEventID("source-owner", options.RequestDigest, ""), Type: LocalEventOwner,
		At: options.Request.CreatedAt.UTC(), Message: "predecessor session owns offered external handoff", Owner: &owner,
		Extra: externalSourceOwnerExtra(options.Request, options.RequestDigest),
	}
}

func terminalTargetProjection(claim workLogClaim) workLogProjection {
	return workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "terminal"}
}

func externalSourceCompletionEvent(request sessionmove.Request, digest sessionmove.Digest, targetReference string) LocalWorkLogEvent {
	return LocalWorkLogEvent{
		ID: externalLocalEventID("source-completed", digest, ""), Type: LocalEventHandoff,
		Message: "external successor receipt accepted; predecessor custody sealed", Result: "completed",
		Extra: externalLocalEventExtra(request, targetReference, "source"),
	}
}

// externalSourceSealPorts keeps live Work Log and Git I/O local to one seal.
// Durable receipt reads and the retained execution lock stay direct below.
type externalSourceSealPorts struct {
	openRun          func(string, string, string, string, bool) (*lockedWorkLogRun, error)
	readClaim        func(*os.File, string) (workLogClaim, error)
	readProjection   func(string) (workLogProjection, error)
	validateOffer    func(string, sessionmove.Request, sessionmove.Digest) error
	validateTerminal func(*os.File, workLogClaim, sessionmove.Request, sessionmove.WorkLogReference, *workLogExternalHandoffEvidence) (bool, time.Time, error)
	validateOwner    func(string, session.Record, sessionmove.Request, sessionmove.Digest, workLogClaim) error
	gitStatus        func(string) (string, error)
	corroborate      func(string, string, workLogProjection, workLogClaim) error
	sealTerminal     func(string, *os.File, worktreeclaims.TerminalSealRequest) (time.Time, error)
	writeProjection  func(string, workLogProjection) error
	appendEvent      func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error)
}

func defaultExternalSourceSealPorts() externalSourceSealPorts {
	return externalSourceSealPorts{
		openRun:   openLockedWorkLogRun,
		readClaim: readWorkLogClaimAt, readProjection: readWorkLogProjection,
		validateOffer:    validateExternalSourceOffer,
		validateTerminal: validateExistingExternalTerminal, validateOwner: validateLiveExternalSourceOwner,
		gitStatus: func(worktree string) (string, error) {
			return git(context.Background(), worktree, "status", "--porcelain=v1", "--untracked-files=all")
		},
		corroborate: corroborateClaim, sealTerminal: sealWorkLogTerminal,
		writeProjection: writeWorkLogProjection, appendEvent: appendLocalEventWithoutCustody,
	}
}

// SealExternalSessionWorkLog directly terminalizes the predecessor as an
// external_handoff. It does not create a source-local successor claim.
func SealExternalSessionWorkLog(options ExternalSourceSealOptions) (ExternalSourceSealResult, error) {
	return defaultExternalSourceSealPorts().sealExternalSessionWorkLog(options)
}

func (p externalSourceSealPorts) sealExternalSessionWorkLog(options ExternalSourceSealOptions) (ExternalSourceSealResult, error) {
	var result ExternalSourceSealResult
	if err := validateExternalReceipt(options.Request, options.RequestDigest, options.Receipt); err != nil {
		return result, err
	}
	if options.ExecutionLock == nil {
		return result, fmt.Errorf("external source seal requires retained durable receipt authority")
	}
	state, err := options.Store.LoadUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest)
	if err != nil {
		return result, fmt.Errorf("load durable source receipt authority: %w", err)
	}
	if err := validateExternalSealState(state, options.Request, options.RequestDigest, options.Receipt); err != nil {
		return result, err
	}
	if _, err := options.Store.LoadSuccessorAddressUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest); err != nil {
		return result, fmt.Errorf("load durable completed-successor address before custody seal: %w", err)
	}
	request := options.Request
	if err := validateExternalSourceSession(options.SourceSession, request); err != nil {
		return result, err
	}
	// validateExternalReceipt already validated this exact request, including
	// its Work Log reference, through ExpectedTargetWorkLogReference.
	sourceReference, _ := sessionmove.ParseWorkLogReference(request.WorkLogReference)
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	locked, err := p.openRun(home, sourceReference.EffortID, sourceReference.RunID, sourceReference.ClaimID, false)
	if err != nil {
		return result, err
	}
	defer locked.close()
	runDir := locked.directory
	claim, err := p.readClaim(runDir, sourceReference.ClaimID)
	if err != nil {
		return result, err
	}
	projection, err := p.readProjection(claim.Worktree)
	if err != nil {
		return result, err
	}
	if projection.EffortID != sourceReference.EffortID || projection.RunID != sourceReference.RunID || projection.ClaimID != sourceReference.ClaimID ||
		(projection.Lifecycle != "active" && projection.Lifecycle != "terminal") {
		return result, fmt.Errorf("source Work Log projection conflicts with receipt lineage")
	}
	if err := p.validateOffer(claim.Worktree, request, options.RequestDigest); err != nil {
		return result, err
	}
	targetReference, _ := sessionmove.ExpectedTargetWorkLogReference(request, options.RequestDigest)
	evidence := externalHandoffEvidence(request, options.RequestDigest, targetReference.String())
	terminalExists, terminalSealedAt, err := p.validateTerminal(runDir, claim, request, targetReference, evidence)
	if err != nil {
		return result, err
	}
	if projection.Lifecycle == "terminal" && !terminalExists {
		return result, fmt.Errorf("terminal source Work Log projection has no exact immutable external terminal authority")
	}
	result.Replayed = terminalExists
	if !terminalExists {
		if err := p.validateOwner(claim.Worktree, options.SourceSession, request, options.RequestDigest, claim); err != nil {
			return result, err
		}
		status, statusErr := p.gitStatus(claim.Worktree)
		if statusErr != nil {
			return result, fmt.Errorf("inspect predecessor worktree before external custody seal: %w", statusErr)
		}
		if status != "" {
			return result, fmt.Errorf("predecessor worktree changed after its handoff bundle; commit and create a new handoff before sealing custody")
		}
	}
	if err := p.corroborate(claim.Worktree, request.BundleCommit, projection, claim); err != nil {
		return result, fmt.Errorf("corroborate source Work Log before external seal: %w", err)
	}
	sealedAt, err := p.sealTerminal(home, runDir, worktreeclaims.TerminalSealRequest{
		Claim: claim, FinalCommit: request.BundleCommit, Disposition: "external_handoff",
		SuccessorClaimID: targetReference.ClaimID, SuccessorAgentID: request.SuccessorWBSessionID,
		Evidence: worktreeclaims.TerminalEvidence{ExternalHandoff: evidence},
	})
	if err != nil {
		return result, err
	}
	if terminalExists && !sealedAt.Equal(terminalSealedAt) {
		return result, fmt.Errorf("replayed external source terminal changed its immutable sealed time")
	}
	if options.hooks.afterTerminal != nil {
		if err := options.hooks.afterTerminal(); err != nil {
			return result, err
		}
	}
	terminalProjection := terminalTargetProjection(claim)
	if err := p.writeProjection(claim.Worktree, terminalProjection); err != nil {
		return result, err
	}
	if options.hooks.afterProjection != nil {
		if err := options.hooks.afterProjection(); err != nil {
			return result, err
		}
	}
	completion := externalSourceCompletionEvent(request, options.RequestDigest, targetReference.String())
	completion, _, err = p.appendEvent(claim.Worktree, completion)
	if err != nil {
		return result, err
	}
	if options.hooks.afterCompletion != nil {
		if err := options.hooks.afterCompletion(); err != nil {
			return result, err
		}
	}
	// appendLocalEvent projects terminal lifecycle from the hybrid pointer and
	// repairs an interrupted local outbox before returning.
	result.SourceWorkLogReference = request.WorkLogReference
	result.TargetWorkLogReference = targetReference.String()
	result.SealedAt, result.CompletionEvent = sealedAt, completion
	return result, nil
}

func validateExternalSourceOffer(worktree string, request sessionmove.Request, digest sessionmove.Digest) error {
	events, err := readLocalEvents(worktree)
	if err != nil {
		return fmt.Errorf("read source Work Log handoff offer: %w", err)
	}
	handover, err := requestHandoverBytes(worktree, request)
	if err != nil || !request.HandoverDigest.Matches(handover) {
		return fmt.Errorf("source handover document does not match admitted immutable bytes")
	}
	_, found, err := findExternalSourceOffer(events, request, digest)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	return fmt.Errorf("source Work Log lacks deterministic request-bound offer evidence for handoff %s", request.HandoffID)
}

func externalSourceOfferEvent(request sessionmove.Request, digest sessionmove.Digest) LocalWorkLogEvent {
	emptyStatus := sha256.Sum256(nil)
	return LocalWorkLogEvent{
		ID: externalLocalEventID("source-offered", digest, ""), Type: LocalEventHandoff, At: request.CreatedAt.UTC(),
		Message: request.SourceOfferMessage, NextAction: request.SourceOfferNextAction,
		Git: &LocalGitEvidence{Branch: request.Branch, Head: request.BundleCommit, Dirty: false,
			StatusSHA: hex.EncodeToString(emptyStatus[:])},
		Result: "offered",
		Extra: map[string]any{
			"successor": request.SuccessorWBSessionID, "apply": false, "handoff_id": request.HandoffID,
			"target_machine": request.TargetMachine, "bundle_commit": request.BundleCommit, "request_digest": string(digest),
			"source_work_log_reference": request.WorkLogReference, "predecessor_wb_session_id": request.PredecessorWBSessionID,
			"source_machine": request.SourceMachine,
		},
	}
}

func findExternalSourceOffer(events []LocalWorkLogEvent, request sessionmove.Request, digest sessionmove.Digest) (LocalWorkLogEvent, bool, error) {
	want := externalSourceOfferEvent(request, digest)
	for _, event := range events {
		if event.ID != want.ID {
			continue
		}
		want.Version, want.Seq = 1, event.Seq
		if !sameLocalEvent(event, want) {
			return LocalWorkLogEvent{}, false, fmt.Errorf("deterministic source Work Log offer conflicts with admitted request evidence")
		}
		return event, true, nil
	}
	return LocalWorkLogEvent{}, false, nil
}

func externalSourceOwnerExtra(request sessionmove.Request, digest sessionmove.Digest) map[string]any {
	return map[string]any{"handoff_id": request.HandoffID, "request_digest": string(digest),
		"source_work_log_reference": request.WorkLogReference}
}

func findExternalSourceOwner(events []LocalWorkLogEvent, request sessionmove.Request, digest sessionmove.Digest,
	source session.Record, claim workLogClaim, requireLatest bool,
) (LocalWorkLogEvent, bool, error) {
	wantID := externalLocalEventID("source-owner", digest, "")
	var found LocalWorkLogEvent
	foundIndex, latestOwnerIndex := -1, -1
	for index, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil {
			latestOwnerIndex = index
		}
		if event.ID == wantID {
			if foundIndex >= 0 {
				return LocalWorkLogEvent{}, false, fmt.Errorf("deterministic source owner occurs more than once")
			}
			found, foundIndex = event, index
		}
	}
	if foundIndex < 0 {
		return LocalWorkLogEvent{}, false, nil
	}
	if requireLatest && latestOwnerIndex != foundIndex {
		return LocalWorkLogEvent{}, false, fmt.Errorf("deterministic source owner is not the current Work Log owner")
	}
	owner := found.Owner
	if found.Type != LocalEventOwner || owner == nil || found.Message != "predecessor session owns offered external handoff" ||
		!found.At.Equal(request.CreatedAt.UTC()) || !reflect.DeepEqual(found.Extra, externalSourceOwnerExtra(request, digest)) ||
		owner.Agent != source.Runtime+"/"+source.WBSessionID || owner.Model != source.Model || owner.Effort != claim.EffortID ||
		owner.PID != source.PID || strings.TrimSpace(owner.WBVersion) == "" || owner.Command != "session move offer" ||
		!owner.At.Equal(request.CreatedAt.UTC()) || owner.At.Before(source.StartedAt.UTC()) {
		return LocalWorkLogEvent{}, false, fmt.Errorf("deterministic source owner conflicts with admitted predecessor identity")
	}
	return found, true, nil
}

func validateExistingExternalTerminal(runDir *os.File, claim workLogClaim, request sessionmove.Request, target sessionmove.WorkLogReference, evidence *workLogExternalHandoffEvidence) (bool, time.Time, error) {
	terminal, err := readWorkLogTerminalAt(runDir, claim.ClaimID)
	if errors.Is(err, os.ErrNotExist) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	wantClaim := claim
	wantClaim.Lifecycle = "terminal"
	if !reflect.DeepEqual(terminal.Claim, wantClaim) || terminal.FinalCommit != request.BundleCommit ||
		terminal.Disposition != "external_handoff" || terminal.SuccessorClaimID != target.ClaimID ||
		terminal.SuccessorAgentID != request.SuccessorWBSessionID || terminal.SealedAt.IsZero() ||
		!sameExternalHandoffEvidence(terminal.ExternalHandoff, evidence) {
		return false, time.Time{}, fmt.Errorf("immutable external source terminal conflicts with admitted receipt lineage")
	}
	return true, terminal.SealedAt, nil
}

func externalHandoffEvidence(request sessionmove.Request, digest sessionmove.Digest, targetReference string) *workLogExternalHandoffEvidence {
	return &workLogExternalHandoffEvidence{
		Version: externalHandoffEvidenceVersion, HandoffID: request.HandoffID, RequestDigest: string(digest),
		PredecessorWBSessionID: request.PredecessorWBSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		SourceMachine: request.SourceMachine, TargetMachine: request.TargetMachine,
		SourceWorkLogReference: request.WorkLogReference, TargetWorkLogReference: targetReference,
		SuccessorTmuxName: "wb-session-" + request.SuccessorWBSessionID,
	}
}

func externalLocalEventExtra(request sessionmove.Request, targetReference, endpoint string) map[string]any {
	return map[string]any{
		"handoff_id": request.HandoffID, "endpoint": endpoint,
		"predecessor_wb_session_id": request.PredecessorWBSessionID,
		"successor_wb_session_id":   request.SuccessorWBSessionID,
		"source_work_log_reference": request.WorkLogReference,
		"target_work_log_reference": targetReference,
	}
}

func externalLocalEventID(kind string, digest sessionmove.Digest, attemptID string) string {
	hash := sha256.New()
	for _, part := range []string{"wb.session.local-worklog-event.v1", kind, string(digest), attemptID} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validateExternalTargetSession(request sessionmove.Request, record session.Record) error {
	runtime, model := externalTargetRuntimeModel(request)
	if record.PID <= 0 || record.StartedAt.IsZero() || record.WBSessionID != request.SuccessorWBSessionID ||
		record.PredecessorWBSessionID != request.PredecessorWBSessionID || record.HandoffID != request.HandoffID ||
		record.Machine != request.TargetMachine || record.TmuxName != "wb-session-"+request.SuccessorWBSessionID ||
		record.Runtime != runtime || strings.TrimSpace(record.Model) != model {
		return fmt.Errorf("prepared successor session does not match deterministic admitted target identity")
	}
	return nil
}

func validateExternalReceipt(request sessionmove.Request, digest sessionmove.Digest, receipt sessionmove.Receipt) error {
	if _, err := sessionmove.EncodeReceipt(receipt); err != nil {
		return err
	}
	target, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		return err
	}
	runtime, model := externalTargetRuntimeModel(request)
	if receipt.HandoffID != request.HandoffID || receipt.RequestDigest != digest ||
		receipt.SuccessorWBSessionID != request.SuccessorWBSessionID || receipt.PredecessorWBSessionID != request.PredecessorWBSessionID ||
		receipt.TargetMachine != request.TargetMachine || receipt.TmuxName != "wb-session-"+request.SuccessorWBSessionID ||
		receipt.Runtime != runtime || strings.TrimSpace(receipt.Model) != model || receipt.PinnedCommit != request.BundleCommit ||
		receipt.TargetWorkLogReference != target.String() {
		return fmt.Errorf("successor receipt conflicts with deterministic external custody lineage")
	}
	return nil
}

func externalTargetRuntimeModel(request sessionmove.Request) (string, string) {
	runtime := strings.TrimSpace(request.RequestedHarness)
	if runtime == "" {
		runtime = strings.TrimSpace(request.SourceRuntime)
	}
	model := ""
	if runtime == strings.TrimSpace(request.SourceRuntime) {
		model = strings.TrimSpace(request.SourceModel)
	}
	return runtime, model
}

func sessionNativeHarnessID(record session.Record) string {
	if value := strings.TrimSpace(record.NativeHarnessID); value != "" {
		return value
	}
	return strings.TrimSpace(record.AgentID)
}

func validateExternalSourceSession(source session.Record, request sessionmove.Request) error {
	if source.PID <= 0 || source.StartedAt.IsZero() ||
		source.WBSessionID != request.PredecessorWBSessionID || source.Machine != request.SourceMachine ||
		source.Runtime != request.SourceRuntime || source.Model != request.SourceModel ||
		sessionNativeHarnessID(source) != request.SourceNativeHarnessID {
		return fmt.Errorf("source session does not match admitted predecessor identity")
	}
	return nil
}

func validateLiveExternalSourceOwner(worktree string, source session.Record, request sessionmove.Request, digest sessionmove.Digest, claim workLogClaim) error {
	events, err := readLocalEvents(worktree)
	if err != nil {
		return fmt.Errorf("read predecessor Work Log owners: %w", err)
	}
	_, found, err := findExternalSourceOwner(events, request, digest, source, claim, true)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("predecessor Work Log has no live source owner evidence")
	}
	if ownerPIDStatus(source.PID) != "active" {
		return fmt.Errorf("current predecessor Work Log owner does not match the live source session")
	}
	return nil
}

func validateExternalTargetManifestAndJournal(worktree string, request sessionmove.Request, digest sessionmove.Digest, claim workLogClaim, receiptModel string) error {
	manifest, err := ReadManifest(worktree)
	if err != nil {
		return fmt.Errorf("read external target Work Log manifest: %w", err)
	}
	wantManifest := preparedTargetManifest(claim, claim.RecordedAt, receiptModel)
	if !reflect.DeepEqual(manifest, wantManifest) {
		return fmt.Errorf("immutable external target Work Log manifest conflicts with admitted request")
	}
	handover, err := readBoundedRelativeRegular(worktree, request.HandoverPath, 1<<20)
	if err != nil || !request.HandoverDigest.Matches(handover) {
		return fmt.Errorf("external target handover document no longer matches admitted bytes")
	}
	if err := validateExternalHandoverPrompt(worktree, claim.RecordedAt, claim.AgentRuntime, receiptModel, request.HandoverDigest, handover); err != nil {
		return err
	}
	events, err := readLocalEvents(worktree)
	if err != nil {
		return fmt.Errorf("read external target Work Log journal: %w", err)
	}
	targetReference, _ := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	wantReceived := externalTargetReceivedEvent(request, digest, targetReference.String(), claim.RecordedAt)
	receivedFound := false
	for _, event := range events {
		if event.ID != wantReceived.ID {
			continue
		}
		wantReceived.Seq = event.Seq
		if !sameLocalEvent(event, wantReceived) {
			return fmt.Errorf("external target received event conflicts with admitted request")
		}
		receivedFound = true
	}
	if !receivedFound {
		return fmt.Errorf("external target Work Log lacks deterministic received evidence")
	}
	return nil
}

func validateExternalAttemptOwner(worktree string, request sessionmove.Request, digest sessionmove.Digest, claim workLogClaim, attemptID string, attemptIndex uint64, pid int, startedAt time.Time, requireLive bool) error {
	if !validExternalAttempt(attemptID, attemptIndex) || pid <= 0 || startedAt.IsZero() {
		return fmt.Errorf("external target attempt owner identity is incomplete")
	}
	events, err := readLocalEvents(worktree)
	if err != nil {
		return err
	}
	wantID := externalLocalEventID("target-owner", digest, attemptID)
	var found *LocalWorkLogEvent
	var latestOwnerID string
	for index := range events {
		if events[index].Type == LocalEventOwner && events[index].Owner != nil {
			latestOwnerID = events[index].ID
		}
		if events[index].ID == wantID {
			found = &events[index]
		}
	}
	if found == nil || found.Owner == nil {
		return fmt.Errorf("external target Work Log lacks deterministic owner for attempt %s", attemptID)
	}
	owner := found.Owner
	want := externalTargetOwnerEvent(request, digest, claim, claim.ExternalHandoff.TargetWorkLogReference,
		attemptID, attemptIndex, pid, startedAt, owner.WBVersion)
	want.Seq = found.Seq
	if owner.WBVersion == "" || !sameLocalEvent(*found, want) {
		return fmt.Errorf("external target attempt owner conflicts with immutable launch evidence")
	}
	status := ownerPIDStatus(pid)
	if requireLive {
		if status != "active" || latestOwnerID != wantID {
			return fmt.Errorf("winning external target attempt is not the latest live Work Log owner")
		}
	} else if status != "orphaned" {
		return fmt.Errorf("failed external target attempt PID is not proven gone")
	}
	return nil
}

func externalReceiptModel(claim workLogClaim) string {
	if claim.Model == "unknown" && claim.ModelProvenance == modelProvenanceUnknown {
		return ""
	}
	return claim.Model
}

func validateExternalHandoverPrompt(worktree string, at time.Time, runtime, model string, digest sessionmove.Digest, body []byte) error {
	directory, err := openJournalSubdirectory(worktree, promptsDirectory, false)
	if err != nil {
		return fmt.Errorf("external target Work Log must have exactly one handover prompt")
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	var header PromptHeader
	var content []byte
	found := false
	for _, candidate := range names {
		match := promptFileName.FindStringSubmatch(candidate)
		if match != nil {
			if found {
				return fmt.Errorf("external target Work Log has multiple prompt files")
			}
			ordinal, _ := strconv.Atoi(match[1]) // promptFileName accepts exactly four ASCII digits.
			content, err = readBytesAt(directory, candidate)
			if err != nil {
				return err
			}
			header, err = parsePromptHeader(content)
			if err != nil {
				return fmt.Errorf("prompt %s: %w", candidate, err)
			}
			if header.Seq != ordinal || header.Seq != 0 {
				return fmt.Errorf("external target Work Log must have exactly one handover prompt")
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("external target Work Log must have exactly one handover prompt")
	}
	return corroborateExternalHandoverPrompt(header, content, at, runtime, model, digest, body)
}

func corroborateExternalHandoverPrompt(header PromptHeader, content []byte, at time.Time, runtime, model string,
	digest sessionmove.Digest, body []byte,
) error {
	wantDigest := strings.TrimPrefix(string(digest), sessionmove.DigestAlgorithmSHA256+":")
	if header.Seq != 0 || !header.At.Equal(at) || header.SHA256 != wantDigest || header.Source != PromptSourceAgent ||
		header.Runtime != runtime || header.Model != model {
		return fmt.Errorf("external target Work Log prompt metadata conflicts with admitted handover")
	}
	separator := []byte("\n---\n\n")
	frontmatterEnd := bytes.Index(content, separator)
	if frontmatterEnd < 0 {
		return fmt.Errorf("external target Work Log prompt is malformed")
	}
	storedBody := content[frontmatterEnd+len(separator):]
	wantBody := append([]byte(nil), body...)
	if len(wantBody) == 0 || wantBody[len(wantBody)-1] != '\n' {
		wantBody = append(wantBody, '\n')
	}
	if !bytes.Equal(storedBody, wantBody) {
		return fmt.Errorf("external target Work Log prompt body conflicts with admitted handover")
	}
	return nil
}

func loadExternalTargetClaim(projectsRoot string, request sessionmove.Request, digest sessionmove.Digest, worktree string) (workLogClaim, sessionmove.WorkLogReference, func(), error) {
	target, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	locked, err := openLockedWorkLogRun(home, target.EffortID, target.RunID, target.ClaimID, false)
	if err != nil {
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	runDir := locked.directory
	unlock := locked.close
	claim, err := readWorkLogClaimAt(runDir, target.ClaimID)
	if err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	remote, err := gitremote.Parse(request.RepositoryRemote)
	if err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, fmt.Errorf("external target Work Log projection conflicts with receipt lineage")
	}
	if err := corroborateExternalTargetClaim(claim, projection, target, request, digest, worktree, remote.Identity.Repository); err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	_, receiptModel := externalTargetRuntimeModel(request)
	if err := validateExternalTargetManifestAndJournal(worktree, request, digest, claim, receiptModel); err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	if err := corroborateClaim(worktree, request.BundleCommit, projection, claim); err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, fmt.Errorf("corroborate external target Work Log live pin: %w", err)
	}
	return claim, target, unlock, nil
}

func corroborateExternalTargetClaim(claim workLogClaim, projection workLogProjection, target sessionmove.WorkLogReference,
	request sessionmove.Request, digest sessionmove.Digest, worktree, repository string,
) error {
	source, _ := sessionmove.ParseWorkLogReference(request.WorkLogReference)
	runtime, claimModel := externalTargetRuntimeModel(request)
	provenance := modelProvenanceCallerDeclared
	if claimModel == "" {
		claimModel, provenance = "unknown", modelProvenanceUnknown
	}
	if claim.Version != 2 || claim.ClaimID != target.ClaimID || claim.EffortID != target.EffortID || claim.RunID != target.RunID ||
		claim.Task != "external session handoff "+request.HandoffID || claim.Repository != repository ||
		claim.Worktree != worktree || claim.Branch != "wb-session/"+request.HandoffID || claim.Base != request.Branch ||
		claim.BaseSHA != request.SourceWorkCommit || claim.Lifecycle != "active" || claim.RecordedAt.IsZero() ||
		claim.Initiator != request.PredecessorWBSessionID || claim.AgentID != request.SuccessorWBSessionID ||
		claim.AgentRuntime != runtime || claim.Model != claimModel || claim.ModelProvenance != provenance ||
		claim.ModelDeclaredBy != request.PredecessorWBSessionID || claim.CLI != "" || claim.Provider != "" ||
		claim.PromptArchive != "" || claim.PromptDigest != "" || claim.ParentClaimID != source.ClaimID ||
		claim.AcquiredVia != "external_handoff" {
		return fmt.Errorf("external target Work Log claim conflicts with receipt")
	}
	if projection != activeTargetProjection(claim) {
		return fmt.Errorf("external target Work Log projection conflicts with receipt lineage")
	}
	wantEvidence := externalHandoffEvidence(request, digest, target.String())
	if !sameExternalHandoffEvidence(claim.ExternalHandoff, wantEvidence) {
		return fmt.Errorf("external target Work Log claim carries conflicting lineage evidence")
	}
	return nil
}

func validExternalAttempt(attemptID string, index uint64) bool {
	prefix := fmt.Sprintf("%06d-", index)
	if index == 0 || index > 999999 || !strings.HasPrefix(attemptID, prefix) || len(attemptID) != len(prefix)+32 {
		return false
	}
	entropy := strings.TrimPrefix(attemptID, prefix)
	decoded, err := hex.DecodeString(entropy)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == entropy
}

func ensureExternalManifest(worktree string, manifest Manifest) error {
	if existing, err := ReadManifest(worktree); err == nil {
		if !reflect.DeepEqual(existing, manifest) {
			return fmt.Errorf("immutable target Work Log manifest conflicts with external claim")
		}
		return nil
	} else if !errors.Is(err, errManifestNotFound) {
		return err
	}
	if err := WriteManifest(worktree, manifest); err != nil {
		if existing, readErr := ReadManifest(worktree); readErr == nil && reflect.DeepEqual(existing, manifest) {
			return nil
		}
		return err
	}
	return nil
}

func ensureExternalHandoverPrompt(worktree string, at time.Time, record session.Record, digest sessionmove.Digest, body []byte) error {
	prompts, err := ListPrompts(worktree)
	if err != nil {
		return err
	}
	wantDigest := strings.TrimPrefix(string(digest), sessionmove.DigestAlgorithmSHA256+":")
	if len(prompts) != 0 {
		if len(prompts) != 1 || prompts[0].Seq != 0 || prompts[0].SHA256 != wantDigest ||
			!prompts[0].At.Equal(at) || prompts[0].Source != PromptSourceAgent ||
			prompts[0].Runtime != record.Runtime || prompts[0].Model != record.Model {
			return fmt.Errorf("immutable target Work Log prompt conflicts with admitted handover")
		}
		return validateExternalHandoverPrompt(worktree, at, record.Runtime, record.Model, digest, body)
	}
	_, err = AppendPrompt(worktree, PromptHeader{At: at, Source: PromptSourceAgent, Runtime: record.Runtime,
		Model: record.Model, Slug: "session-handover"}, body)
	if err != nil {
		return err
	}
	return validateExternalHandoverPrompt(worktree, at, record.Runtime, record.Model, digest, body)
}

func expectedExternalClaimID(claim workLogClaim) (string, error) {
	evidence, err := validateExternalSuccessorClaimLineage(claim, externalSuccessorClaimPolicy{label: "external"})
	if err != nil {
		return "", err
	}
	return sessionmove.ExternalHandoffClaimID(sessionmove.Digest(evidence.RequestDigest), claim.AgentID)
}

type externalSuccessorClaimPolicy struct {
	label         string
	protocol      string
	requireMember bool
}

// validateExternalSuccessorClaimLineage owns the common immutable lineage
// contract shared by direct external handoffs and parked-session members.
// The caller retains only the protocol-specific deterministic claim formula.
func validateExternalSuccessorClaimLineage(claim workLogClaim, policy externalSuccessorClaimPolicy) (*workLogExternalHandoffEvidence, error) {
	evidence := claim.ExternalHandoff
	if evidence == nil || evidence.Version != externalHandoffEvidenceVersion || evidence.HandoffID == "" ||
		evidence.PredecessorWBSessionID == "" || evidence.SuccessorWBSessionID != claim.AgentID ||
		evidence.SourceWorkLogReference == "" || evidence.TargetWorkLogReference == "" ||
		evidence.SuccessorTmuxName != "wb-session-"+claim.AgentID ||
		(policy.protocol != "" && evidence.Protocol != policy.protocol) || (policy.requireMember && evidence.MemberID == "") {
		return nil, fmt.Errorf("private %s successor claim metadata is invalid", policy.label)
	}
	source, err := sessionmove.ParseWorkLogReference(evidence.SourceWorkLogReference)
	if err != nil || source.EffortID != claim.EffortID || source.RunID != claim.RunID || source.ClaimID != claim.ParentClaimID {
		return nil, fmt.Errorf("private %s source Work Log lineage is invalid", policy.label)
	}
	target, err := sessionmove.ParseWorkLogReference(evidence.TargetWorkLogReference)
	if err != nil || target.EffortID != claim.EffortID || target.RunID != claim.RunID || target.ClaimID != claim.ClaimID {
		return nil, fmt.Errorf("private %s target Work Log lineage is invalid", policy.label)
	}
	return evidence, nil
}

// requestHandoverBytes returns the exact bytes source or target must
// reverify against request.HandoverDigest before custody advances. A request
// with inline handover content (every checkpoint created after the
// ContinuationPrivate cutover) never wrote anything into the worktree, so its
// content is read from the immutable admitted request itself. A pre-cutover
// request has no inline content and is read from its legacy HandoverPath
// inside the worktree, exactly as before the cutover.
func requestHandoverBytes(worktree string, request sessionmove.Request) ([]byte, error) {
	if request.HandoverContent != "" {
		return []byte(request.HandoverContent), nil
	}
	return readBoundedRelativeRegular(worktree, request.HandoverPath, 1<<20)
}

func readBoundedRelativeRegular(rootPath, relative string, limit int64) ([]byte, error) {
	root, err := openAbsoluteDirectoryNoFollow(rootPath, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return nil, fmt.Errorf("unsafe relative file path %q", relative)
	}
	segments := strings.Split(clean, string(filepath.Separator))
	current := root
	for _, segment := range segments[:len(segments)-1] {
		fd, openErr := unix.Openat(int(current.Fd()), segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if current != root {
			_ = current.Close()
		}
		if openErr != nil {
			return nil, openErr
		}
		current = os.NewFile(uintptr(fd), segment)
	}
	if current != root {
		defer func() { _ = current.Close() }()
	}
	fd, err := unix.Openat(int(current.Fd()), segments[len(segments)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), segments[len(segments)-1])
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := readBoundedRelativeRegularFstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size < 0 || stat.Size > limit {
		return nil, fmt.Errorf("relative handover is not one bounded regular file")
	}
	raw, err := readBoundedRelativeRegularReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != stat.Size {
		return nil, fmt.Errorf("relative handover changed while being read")
	}
	return bytes.Clone(raw), nil
}
