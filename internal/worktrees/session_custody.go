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
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const externalHandoffEvidenceVersion = 1

// workLogExternalHandoffEvidence is immutable, transport-neutral lineage that
// links the source terminal and target active claim without manufacturing a
// source-local successor claim.
type workLogExternalHandoffEvidence struct {
	Version                int    `json:"version"`
	Protocol               string `json:"protocol,omitempty"`
	HandoffID              string `json:"handoff_id"`
	MemberID               string `json:"member_id,omitempty"`
	RequestDigest          string `json:"request_digest"`
	PredecessorWBSessionID string `json:"predecessor_wb_session_id"`
	SuccessorWBSessionID   string `json:"successor_wb_session_id"`
	SourceMachine          string `json:"source_machine"`
	TargetMachine          string `json:"target_machine"`
	SourceWorkLogReference string `json:"source_work_log_reference"`
	TargetWorkLogReference string `json:"target_work_log_reference"`
	SuccessorTmuxName      string `json:"successor_tmux_name"`
}

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

// PrepareExternalSessionWorkLog publishes one deterministic external target
// claim before launcher release. Claim identity excludes attempt/PID/time;
// each prepared attempt appends its own idempotent owner evidence under it.
func PrepareExternalSessionWorkLog(ctx context.Context, options ExternalSessionWorkLogPrepareOptions) (ExternalSessionWorkLogPrepareResult, error) {
	var result ExternalSessionWorkLogPrepareResult
	request := options.Request
	targetReference, err := sessionmove.ExpectedTargetWorkLogReference(request, options.RequestDigest)
	if err != nil {
		return result, fmt.Errorf("derive target Work Log reference: %w", err)
	}
	sourceReference, err := sessionmove.ParseWorkLogReference(request.WorkLogReference)
	if err != nil {
		return result, err
	}
	worktree, err := filepath.Abs(options.WorktreeDir)
	if err != nil || filepath.Clean(worktree) != worktree || worktree != options.WorktreeDir {
		return result, fmt.Errorf("external target Work Log requires one clean absolute worktree path")
	}
	if options.PinnedCommit != request.BundleCommit {
		return result, fmt.Errorf("target Work Log pinned commit does not match admitted bundle commit")
	}
	if err := validateExternalTargetSession(request, options.Session); err != nil {
		return result, err
	}
	if !validExternalAttempt(options.AttemptID, options.AttemptIndex) {
		return result, fmt.Errorf("launcher attempt identity is invalid for target owner evidence")
	}
	branch, err := git(ctx, worktree, "branch", "--show-current")
	if err != nil || branch != "wb-session/"+request.HandoffID {
		return result, fmt.Errorf("target worktree branch %q does not match handoff pin branch", branch)
	}
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil || head != options.PinnedCommit {
		return result, fmt.Errorf("target worktree HEAD %q does not match pinned commit %q", head, options.PinnedCommit)
	}
	remote, err := gitremote.Parse(request.RepositoryRemote)
	if err != nil {
		return result, err
	}
	handover := options.HandoverBytes
	if len(handover) == 0 {
		handover, err = requestHandoverBytes(worktree, request)
		if err != nil {
			return result, fmt.Errorf("read admitted handover document: %w", err)
		}
	}
	if !request.HandoverDigest.Matches(handover) {
		return result, fmt.Errorf("target handover bytes do not match admitted digest")
	}
	receivedAt := options.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = request.CreatedAt.UTC()
	}
	if receivedAt.IsZero() {
		return result, fmt.Errorf("target received time is required")
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
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	runDir, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
	if err != nil {
		return result, err
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, claim.ClaimID)
	if err != nil {
		return result, err
	}
	defer unlock()
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		return result, err
	}
	var existing workLogClaim
	readErr := readJSONAt(claims, claim.ClaimID+".json", &existing)
	result.Replayed = readErr == nil
	if readErr == nil && !reflect.DeepEqual(existing, claim) {
		_ = claims.Close()
		return result, fmt.Errorf("immutable external target Work Log claim conflicts with admitted handoff")
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		_ = claims.Close()
		return result, readErr
	}
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", claim, true); err != nil {
		_ = claims.Close()
		return result, fmt.Errorf("publish immutable external target Work Log claim: %w", err)
	}
	_ = claims.Close()
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
	manifest := Manifest{
		Version: 1, EffortID: claim.EffortID, ParentEffort: ParentEffort(claim.EffortID), EffortKind: EffortKindFor(claim.EffortID),
		Repository: claim.Repository, Worktree: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
		CreatedAt: receivedAt, Initiator: claim.Initiator, AgentID: claim.AgentID, AgentRuntime: claim.AgentRuntime,
		Model: options.Session.Model, RunID: claim.RunID, ClaimID: claim.ClaimID, Provenance: ProvenanceCreated,
	}
	if err := ensureExternalManifest(worktree, manifest); err != nil {
		return result, err
	}
	if err := ensureExternalHandoverPrompt(worktree, receivedAt, options.Session, request.HandoverDigest, handover); err != nil {
		return result, err
	}
	receivedEvent := LocalWorkLogEvent{
		ID: externalLocalEventID("target-received", options.RequestDigest, ""), Type: LocalEventHandoff, At: receivedAt,
		Message: "external session handoff received", Result: "received",
		Extra: externalLocalEventExtra(request, targetReference.String(), "target"),
	}
	receivedEvent, _, err = appendLocalEventWithoutCustody(worktree, receivedEvent)
	if err != nil {
		return result, fmt.Errorf("record target Work Log receipt evidence: %w", err)
	}
	owner := OwnerRegistration{
		Agent: options.Session.Runtime + "/" + options.Session.WBSessionID, Model: options.Session.Model,
		Effort: claim.EffortID, PID: options.Session.PID, WBVersion: buildinfo.Version(), Command: "session receive", At: options.Session.StartedAt.UTC(),
	}
	ownerEvent := LocalWorkLogEvent{
		ID: externalLocalEventID("target-owner", options.RequestDigest, options.AttemptID), Type: LocalEventOwner,
		At: owner.At, Message: "successor launcher attempt prepared", Owner: &owner,
		Extra: map[string]any{"handoff_id": request.HandoffID, "attempt_id": options.AttemptID,
			"attempt_index": options.AttemptIndex, "target_work_log_reference": targetReference.String()},
	}
	ownerEvent, _, err = appendLocalEventWithoutCustody(worktree, ownerEvent)
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
	public := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: claim.RecordedAt,
		EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository,
		Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA, Lifecycle: "active", ExternalHandoff: evidence}
	err = writeJSONImmutableAt(outbox, claim.RunID+"-"+claim.ClaimID+"-claimed.json", public, true)
	_ = outbox.Close()
	if err != nil {
		return result, fmt.Errorf("publish external target Work Log outbox: %w", err)
	}
	if options.hooks.afterOutbox != nil {
		if err := options.hooks.afterOutbox(); err != nil {
			return result, err
		}
	}
	// The hybrid projection is deliberately the last identity publication;
	// immediately replay the local cache so this final write cannot leave it
	// stale or identity-poor.
	hybrid := workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "active"}
	if err := writeWorkLogProjection(worktree, hybrid); err != nil {
		return result, err
	}
	if options.hooks.afterProjection != nil {
		if err := options.hooks.afterProjection(); err != nil {
			return result, err
		}
	}
	if _, err := repairCurrentLocalProjection(worktree); err != nil {
		return result, err
	}
	result.WorkLogReference, result.ClaimID = targetReference.String(), claim.ClaimID
	result.ReceivedEvent, result.OwnerEvent = receivedEvent, ownerEvent
	return result, nil
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
	failure := options.Failure
	expectedReference, referenceErr := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
	if referenceErr != nil || !failure.Authenticates(options.Request.HandoffID, options.RequestDigest, expectedReference.String()) ||
		!validExternalAttempt(failure.AttemptID, failure.AttemptIndex) || failure.PID <= 0 || failure.StartedAt.IsZero() || failure.FailedAt.IsZero() ||
		failure.FailedAt.Before(failure.StartedAt) || strings.TrimSpace(failure.Diagnostic) == "" {
		return LocalWorkLogEvent{}, fmt.Errorf("exact failed launcher attempt evidence is incomplete")
	}
	expectedEventID := externalLocalEventID("target-attempt-failed", options.RequestDigest, failure.AttemptID)
	claim, reference, unlock, err := loadExternalTargetClaim(options.ProjectsRoot, options.Request, options.RequestDigest, options.WorktreeDir)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer unlock()
	if err := validateExternalAttemptOwner(claim.Worktree, options.Request, options.RequestDigest, claim,
		failure.AttemptID, failure.AttemptIndex, failure.PID, failure.StartedAt, false); err != nil {
		return LocalWorkLogEvent{}, err
	}
	diagnosticDigest := sha256.Sum256([]byte(strings.TrimSpace(failure.Diagnostic)))
	event := LocalWorkLogEvent{
		ID: expectedEventID, Type: LocalEventHandoff,
		At: failure.FailedAt.UTC(), Message: "external successor launcher attempt failed after release", Result: "failed",
		Extra: map[string]any{"handoff_id": options.Request.HandoffID, "endpoint": "target",
			"target_work_log_reference": reference.String(), "attempt_id": failure.AttemptID,
			"attempt_index": failure.AttemptIndex, "pid": failure.PID, "started_at": failure.StartedAt.UTC(),
			"diagnostic_sha256": hex.EncodeToString(diagnosticDigest[:])},
	}
	event, _, err = appendLocalEventWithoutCustody(claim.Worktree, event)
	return event, err
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

// EnsureExternalSourceOfferEvidence repairs the two source checkpoint crash
// gaps under one exact admitted aggregate authority:
//
//	PhaseOffered -> deterministic offer-only Work Log event -> source owner.
//
// It never derives event content by parsing free-form Markdown. The request
// carries the exact normalized fields and their digest, so headings in a user
// handover cannot make an otherwise valid move unsealable.
func EnsureExternalSourceOfferEvidence(options ExternalSourceOfferOptions) (ExternalSourceOfferResult, error) {
	var result ExternalSourceOfferResult
	if options.ExecutionLock == nil {
		return result, fmt.Errorf("external source offer repair requires retained admitted request authority")
	}
	state, err := options.Store.LoadUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest)
	if err != nil {
		return result, fmt.Errorf("load exact source offer aggregate: %w", err)
	}
	if state.Request != options.Request || state.Digest != options.RequestDigest {
		return result, fmt.Errorf("source offer repair does not match exact admitted request")
	}
	if err := validateExternalSourceSession(options.SourceSession, options.Request); err != nil {
		return result, err
	}
	if options.Request.CreatedAt.Before(options.SourceSession.StartedAt.UTC()) {
		return result, fmt.Errorf("admitted source offer predates the predecessor session")
	}

	offeredFound := false
	for _, event := range state.Events {
		if event.Phase != sessionmove.PhaseOffered {
			continue
		}
		if !event.At.Equal(options.Request.CreatedAt.UTC()) || event.Diagnostic != "" {
			return result, fmt.Errorf("durable offered phase conflicts with admitted source checkpoint")
		}
		offeredFound = true
	}
	if !offeredFound {
		if _, err := options.Store.AppendEventUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest,
			sessionmove.HandoffEvent{Phase: sessionmove.PhaseOffered, At: options.Request.CreatedAt.UTC()}); err != nil {
			return result, fmt.Errorf("repair durable offered phase: %w", err)
		}
	}
	if options.hooks.afterOfferedPhase != nil {
		if err := options.hooks.afterOfferedPhase(); err != nil {
			return result, err
		}
	}

	sourceReference, err := sessionmove.ParseWorkLogReference(options.Request.WorkLogReference)
	if err != nil {
		return result, err
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	runDir, _, err := openWorkLogRun(home, sourceReference.EffortID, sourceReference.RunID, false)
	if err != nil {
		return result, err
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, sourceReference.ClaimID)
	if err != nil {
		return result, err
	}
	defer unlock()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return result, err
	}
	var claim workLogClaim
	err = readJSONAt(claims, sourceReference.ClaimID+".json", &claim)
	_ = claims.Close()
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
		status, statusErr := git(context.Background(), claim.Worktree, "status", "--porcelain=v1", "--untracked-files=all")
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
	if err != nil || !options.Request.HandoverDigest.Matches(handover) {
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
		offer, _, err = appendLocalEventWithoutCustody(claim.Worktree, externalSourceOfferEvent(options.Request, options.RequestDigest))
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
		ownerRegistration := OwnerRegistration{
			Agent: options.SourceSession.Runtime + "/" + options.SourceSession.WBSessionID,
			Model: options.SourceSession.Model, Effort: claim.EffortID, PID: options.SourceSession.PID,
			WBVersion: buildinfo.Version(), Command: "session move offer", At: options.Request.CreatedAt.UTC(),
		}
		owner, _, err = appendLocalEventWithoutCustody(claim.Worktree, LocalWorkLogEvent{
			ID: externalLocalEventID("source-owner", options.RequestDigest, ""), Type: LocalEventOwner,
			At: options.Request.CreatedAt.UTC(), Message: "predecessor session owns offered external handoff", Owner: &ownerRegistration,
			Extra: externalSourceOwnerExtra(options.Request, options.RequestDigest),
		})
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

// SealExternalSessionWorkLog directly terminalizes the predecessor as an
// external_handoff. It deliberately does not call LogHandoff Apply,
// transferWorkLogClaim, or create a source-local successor claim.
func SealExternalSessionWorkLog(options ExternalSourceSealOptions) (ExternalSourceSealResult, error) {
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
	if state.Request != options.Request || state.Digest != options.RequestDigest || state.Receipt == nil || *state.Receipt != options.Receipt {
		return result, fmt.Errorf("durable source receipt does not exactly authorize requested custody seal")
	}
	if _, err := options.Store.LoadSuccessorAddressUnderLock(options.ExecutionLock, options.Request.HandoffID, options.RequestDigest); err != nil {
		return result, fmt.Errorf("load durable completed-successor address before custody seal: %w", err)
	}
	request := options.Request
	if err := validateExternalSourceSession(options.SourceSession, request); err != nil {
		return result, err
	}
	sourceReference, err := sessionmove.ParseWorkLogReference(request.WorkLogReference)
	if err != nil {
		return result, err
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	runDir, _, err := openWorkLogRun(home, sourceReference.EffortID, sourceReference.RunID, false)
	if err != nil {
		return result, err
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, sourceReference.ClaimID)
	if err != nil {
		return result, err
	}
	defer unlock()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return result, err
	}
	var claim workLogClaim
	err = readJSONAt(claims, sourceReference.ClaimID+".json", &claim)
	_ = claims.Close()
	if err != nil {
		return result, err
	}
	projection, err := readWorkLogProjection(claim.Worktree)
	if err != nil {
		return result, err
	}
	if projection.EffortID != sourceReference.EffortID || projection.RunID != sourceReference.RunID || projection.ClaimID != sourceReference.ClaimID ||
		(projection.Lifecycle != "active" && projection.Lifecycle != "terminal") {
		return result, fmt.Errorf("source Work Log projection conflicts with receipt lineage")
	}
	if err := validateExternalSourceOffer(claim.Worktree, request, options.RequestDigest); err != nil {
		return result, err
	}
	targetReference, _ := sessionmove.ExpectedTargetWorkLogReference(request, options.RequestDigest)
	evidence := externalHandoffEvidence(request, options.RequestDigest, targetReference.String())
	terminalExists, terminalSealedAt, err := validateExistingExternalTerminal(runDir, claim, request, targetReference, evidence)
	if err != nil {
		return result, err
	}
	if projection.Lifecycle == "terminal" && !terminalExists {
		return result, fmt.Errorf("terminal source Work Log projection has no exact immutable external terminal authority")
	}
	result.Replayed = terminalExists
	if !terminalExists {
		if err := validateLiveExternalSourceOwner(claim.Worktree, options.SourceSession, request, options.RequestDigest, claim); err != nil {
			return result, err
		}
		status, statusErr := git(context.Background(), claim.Worktree, "status", "--porcelain=v1", "--untracked-files=all")
		if statusErr != nil {
			return result, fmt.Errorf("inspect predecessor worktree before external custody seal: %w", statusErr)
		}
		if status != "" {
			return result, fmt.Errorf("predecessor worktree changed after its handoff bundle; commit and create a new handoff before sealing custody")
		}
	}
	if err := corroborateClaim(claim.Worktree, request.BundleCommit, projection, claim); err != nil {
		return result, fmt.Errorf("corroborate source Work Log before external seal: %w", err)
	}
	sealedAt, err := writeWorkLogTerminal(home, runDir, claim, request.BundleCommit, "external_handoff",
		targetReference.ClaimID, request.SuccessorWBSessionID, evidence)
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
	terminalProjection := workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "terminal"}
	if err := writeWorkLogProjection(claim.Worktree, terminalProjection); err != nil {
		return result, err
	}
	if options.hooks.afterProjection != nil {
		if err := options.hooks.afterProjection(); err != nil {
			return result, err
		}
	}
	completion := LocalWorkLogEvent{
		ID: externalLocalEventID("source-completed", options.RequestDigest, ""), Type: LocalEventHandoff,
		Message: "external successor receipt accepted; predecessor custody sealed", Result: "completed",
		Extra: externalLocalEventExtra(request, targetReference.String(), "source"),
	}
	completion, _, err = appendLocalEventWithoutCustody(claim.Worktree, completion)
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
	terminals, err := openPrivateChild(runDir, "terminals", false)
	if errors.Is(err, os.ErrNotExist) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	defer func() { _ = terminals.Close() }()
	var terminal workLogTerminalRecord
	if err := readJSONAt(terminals, claim.ClaimID+".json", &terminal); errors.Is(err, os.ErrNotExist) {
		return false, time.Time{}, nil
	} else if err != nil {
		return false, time.Time{}, err
	}
	wantClaim := claim
	wantClaim.Lifecycle = "terminal"
	if !reflect.DeepEqual(terminal.workLogClaim, wantClaim) || terminal.FinalCommit != request.BundleCommit ||
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
	wantManifest := Manifest{
		Version: 1, EffortID: claim.EffortID, ParentEffort: ParentEffort(claim.EffortID), EffortKind: EffortKindFor(claim.EffortID),
		Repository: claim.Repository, Worktree: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
		CreatedAt: claim.RecordedAt, Initiator: claim.Initiator, AgentID: claim.AgentID, AgentRuntime: claim.AgentRuntime,
		Model: receiptModel, RunID: claim.RunID, ClaimID: claim.ClaimID, Provenance: ProvenanceCreated,
	}
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
	receivedID := externalLocalEventID("target-received", digest, "")
	receivedFound := false
	for _, event := range events {
		if event.ID != receivedID {
			continue
		}
		wantExtra := externalLocalEventExtra(request, targetReference.String(), "target")
		if event.Type != LocalEventHandoff || !event.At.Equal(claim.RecordedAt) || event.Message != "external session handoff received" ||
			event.Result != "received" || !reflect.DeepEqual(event.Extra, wantExtra) {
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
	wantExtra := map[string]any{"handoff_id": request.HandoffID, "attempt_id": attemptID,
		"attempt_index": attemptIndex, "target_work_log_reference": claim.ExternalHandoff.TargetWorkLogReference}
	want := LocalWorkLogEvent{
		Version: 1, Seq: found.Seq, ID: wantID, Type: LocalEventOwner, At: startedAt.UTC(),
		Message: "successor launcher attempt prepared",
		Owner: &OwnerRegistration{Agent: claim.AgentRuntime + "/" + claim.AgentID, Model: externalReceiptModel(claim),
			Effort: claim.EffortID, PID: pid, WBVersion: owner.WBVersion, Command: "session receive", At: startedAt.UTC()},
		Extra: wantExtra,
	}
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
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 1 {
		return fmt.Errorf("external target Work Log must have exactly one handover prompt")
	}
	wantDigest := strings.TrimPrefix(string(digest), sessionmove.DigestAlgorithmSHA256+":")
	header := prompts[0]
	if header.Seq != 0 || !header.At.Equal(at) || header.SHA256 != wantDigest || header.Source != PromptSourceAgent ||
		header.Runtime != runtime || header.Model != model {
		return fmt.Errorf("external target Work Log prompt metadata conflicts with admitted handover")
	}
	directory, err := openJournalSubdirectory(worktree, promptsDirectory, false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	var name string
	for _, candidate := range names {
		if promptFileName.MatchString(candidate) {
			if name != "" {
				return fmt.Errorf("external target Work Log has multiple prompt files")
			}
			name = candidate
		}
	}
	content, err := readBytesAt(directory, name)
	if err != nil {
		return err
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
	runDir, _, err := openWorkLogRun(home, target.EffortID, target.RunID, false)
	if err != nil {
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	unlockClaim, err := lockClaim(runDir, target.ClaimID)
	if err != nil {
		_ = runDir.Close()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	unlock := func() { unlockClaim(); _ = runDir.Close() }
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	var claim workLogClaim
	err = readJSONAt(claims, target.ClaimID+".json", &claim)
	_ = claims.Close()
	if err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	remote, err := gitremote.Parse(request.RepositoryRemote)
	if err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
	source, _ := sessionmove.ParseWorkLogReference(request.WorkLogReference)
	runtime, receiptModel := externalTargetRuntimeModel(request)
	claimModel, provenance := receiptModel, modelProvenanceCallerDeclared
	if claimModel == "" {
		claimModel, provenance = "unknown", modelProvenanceUnknown
	}
	if claim.Version != 2 || claim.ClaimID != target.ClaimID || claim.EffortID != target.EffortID || claim.RunID != target.RunID ||
		claim.Task != "external session handoff "+request.HandoffID || claim.Repository != remote.Identity.Repository ||
		claim.Worktree != worktree || claim.Branch != "wb-session/"+request.HandoffID || claim.Base != request.Branch ||
		claim.BaseSHA != request.SourceWorkCommit || claim.Lifecycle != "active" || claim.RecordedAt.IsZero() ||
		claim.Initiator != request.PredecessorWBSessionID || claim.AgentID != request.SuccessorWBSessionID ||
		claim.AgentRuntime != runtime || claim.Model != claimModel || claim.ModelProvenance != provenance ||
		claim.ModelDeclaredBy != request.PredecessorWBSessionID || claim.CLI != "" || claim.Provider != "" ||
		claim.PromptArchive != "" || claim.PromptDigest != "" || claim.ParentClaimID != source.ClaimID ||
		claim.AcquiredVia != "external_handoff" {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, fmt.Errorf("external target Work Log claim conflicts with receipt")
	}
	projection, err := readWorkLogProjection(worktree)
	if err != nil || projection != (workLogProjection{Version: 1, EffortID: target.EffortID, RunID: target.RunID, ClaimID: target.ClaimID, Lifecycle: "active"}) {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, fmt.Errorf("external target Work Log projection conflicts with receipt lineage")
	}
	wantEvidence := externalHandoffEvidence(request, digest, target.String())
	if !sameExternalHandoffEvidence(claim.ExternalHandoff, wantEvidence) {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, fmt.Errorf("external target Work Log claim carries conflicting lineage evidence")
	}
	if _, err := expectedExternalClaimID(claim); err != nil {
		unlock()
		return workLogClaim{}, sessionmove.WorkLogReference{}, nil, err
	}
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
	evidence := claim.ExternalHandoff
	if evidence == nil || evidence.Version != externalHandoffEvidenceVersion || evidence.HandoffID == "" ||
		evidence.PredecessorWBSessionID == "" || evidence.SuccessorWBSessionID != claim.AgentID ||
		evidence.SourceWorkLogReference == "" || evidence.TargetWorkLogReference == "" ||
		evidence.SuccessorTmuxName != "wb-session-"+claim.AgentID {
		return "", fmt.Errorf("private external successor claim metadata is invalid")
	}
	source, err := sessionmove.ParseWorkLogReference(evidence.SourceWorkLogReference)
	if err != nil || source.EffortID != claim.EffortID || source.RunID != claim.RunID || source.ClaimID != claim.ParentClaimID {
		return "", fmt.Errorf("private external source Work Log lineage is invalid")
	}
	target, err := sessionmove.ParseWorkLogReference(evidence.TargetWorkLogReference)
	if err != nil || target.EffortID != claim.EffortID || target.RunID != claim.RunID || target.ClaimID != claim.ClaimID {
		return "", fmt.Errorf("private external target Work Log lineage is invalid")
	}
	return sessionmove.ExternalHandoffClaimID(sessionmove.Digest(evidence.RequestDigest), claim.AgentID)
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
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size < 0 || stat.Size > limit {
		return nil, fmt.Errorf("relative handover is not one bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != stat.Size {
		return nil, fmt.Errorf("relative handover changed while being read")
	}
	return bytes.Clone(raw), nil
}
