package worktrees

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type ParkedSessionWorkLogPrepareOptions struct {
	ProjectsRoot  string
	Request       sessionpark.RemoteRequest
	RequestDigest sessionmove.Digest
	Member        sessionpark.RemoteMember
	ReceivedAt    time.Time
	Session       session.Record
	AttemptID     string
	AttemptIndex  uint64
	WorktreeDir   string
	PinnedCommit  string
}

type ParkedSessionWorkLogPrepareResult struct {
	WorkLogReference string
	ClaimID          string
	ReceivedEvent    LocalWorkLogEvent
	OwnerEvent       LocalWorkLogEvent
	Replayed         bool
}

// PrepareParkedSessionWorkLog installs the deterministic target claim before
// publishing any owner record. The same prepared successor is attached to
// every member only after that member's immutable claim, manifest, and
// received evidence corroborate the exact pinned checkout.
func PrepareParkedSessionWorkLog(ctx context.Context, options ParkedSessionWorkLogPrepareOptions) (ParkedSessionWorkLogPrepareResult, error) {
	var result ParkedSessionWorkLogPrepareResult
	request, member := options.Request, options.Member
	targetValue, err := sessionpark.TargetWorkLogReference(request, options.RequestDigest, member)
	if err != nil {
		return result, err
	}
	targetReference, _ := sessionmove.ParseWorkLogReference(targetValue)
	sourceReference, err := sessionmove.ParseWorkLogReference(member.SourceWorkLogReference)
	if err != nil {
		return result, err
	}
	worktree, err := filepath.Abs(options.WorktreeDir)
	if err != nil || filepath.Clean(worktree) != worktree || worktree != options.WorktreeDir {
		return result, fmt.Errorf("parked target Work Log requires one clean absolute worktree path")
	}
	if options.PinnedCommit != member.Commit {
		return result, fmt.Errorf("parked target Work Log pin does not match admitted member commit")
	}
	if err := validateParkedTargetSession(request, options.Session); err != nil {
		return result, err
	}
	if !validExternalAttempt(options.AttemptID, options.AttemptIndex) {
		return result, fmt.Errorf("launcher attempt identity is invalid for parked target owner evidence")
	}
	branch, err := git(ctx, worktree, "branch", "--show-current")
	wantBranch := sessionpark.MemberPin(request.ResumeID, member.MemberID)
	if err != nil || branch != wantBranch {
		return result, fmt.Errorf("target worktree branch %q does not match parked member pin %q", branch, wantBranch)
	}
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil || head != member.Commit {
		return result, fmt.Errorf("target worktree HEAD %q does not match parked member commit %q", head, member.Commit)
	}
	remote, err := gitremote.Parse(member.RepositoryRemote)
	if err != nil || remote.Identity.Repository != member.Repository {
		return result, fmt.Errorf("parked target repository identity does not match admitted member")
	}
	receivedAt := options.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = request.CreatedAt.UTC()
	}
	runtime, model := sessionpark.RequestedRuntimeModel(request)
	claimModel, provenance := model, modelProvenanceCallerDeclared
	if claimModel == "" {
		claimModel, provenance = "unknown", modelProvenanceUnknown
	}
	evidence := &workLogExternalHandoffEvidence{
		Version: externalHandoffEvidenceVersion, Protocol: "parked_session_resume", HandoffID: request.ResumeID,
		MemberID: member.MemberID, RequestDigest: string(options.RequestDigest),
		PredecessorWBSessionID: request.PredecessorWBSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		SourceMachine: request.SourceMachine, TargetMachine: request.TargetMachine,
		SourceWorkLogReference: member.SourceWorkLogReference, TargetWorkLogReference: targetValue,
		SuccessorTmuxName: "wb-session-" + request.SuccessorWBSessionID,
	}
	claim := workLogClaim{
		Version: 2, EffortID: sourceReference.EffortID, RunID: sourceReference.RunID, ClaimID: targetReference.ClaimID,
		Task: "parked session resume " + request.ResumeID, Repository: member.Repository,
		Worktree: worktree, Branch: wantBranch, Base: member.Branch, BaseSHA: member.Commit,
		Lifecycle: "active", RecordedAt: receivedAt, Initiator: request.PredecessorWBSessionID,
		AgentID: request.SuccessorWBSessionID, AgentRuntime: runtime, Model: claimModel,
		ModelProvenance: provenance, ModelDeclaredBy: request.PredecessorWBSessionID,
		ParentClaimID: sourceReference.ClaimID, AcquiredVia: "parked_session_resume", ExternalHandoff: evidence,
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	runDir, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
	if err != nil {
		return result, err
	}
	defer runDir.Close()
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
		return result, fmt.Errorf("immutable parked target Work Log claim conflicts with admitted bundle")
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		_ = claims.Close()
		return result, readErr
	}
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", claim, true); err != nil {
		_ = claims.Close()
		return result, err
	}
	_ = claims.Close()
	if err := ensureWorkLogRunIndex(runDir, claim.EffortID, claim.RunID); err != nil {
		return result, err
	}
	manifest := Manifest{
		Version: 1, EffortID: claim.EffortID, ParentEffort: ParentEffort(claim.EffortID), EffortKind: EffortKindFor(claim.EffortID),
		Repository: claim.Repository, Worktree: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
		CreatedAt: receivedAt, Initiator: claim.Initiator, AgentID: claim.AgentID, AgentRuntime: claim.AgentRuntime,
		Model: model, RunID: claim.RunID, ClaimID: claim.ClaimID, Provenance: ProvenanceCreated,
	}
	if err := ensureExternalManifest(worktree, manifest); err != nil {
		return result, err
	}
	extra := parkedLocalEventExtra(request, member, targetValue)
	receivedEvent := LocalWorkLogEvent{
		ID:   externalLocalEventID("park-target-received-"+member.MemberID, options.RequestDigest, ""),
		Type: LocalEventHandoff, At: receivedAt, Message: "parked session member received", Result: "received", Extra: extra,
	}
	receivedEvent, _, err = appendLocalEventWithoutCustody(worktree, receivedEvent)
	if err != nil {
		return result, err
	}
	owner := OwnerRegistration{Agent: options.Session.Runtime + "/" + options.Session.WBSessionID, Model: options.Session.Model,
		Effort: claim.EffortID, PID: options.Session.PID, WBVersion: buildinfo.Version(), Command: "session receive-park", At: options.Session.StartedAt.UTC()}
	ownerExtra := parkedLocalEventExtra(request, member, targetValue)
	ownerExtra["attempt_id"], ownerExtra["attempt_index"] = options.AttemptID, options.AttemptIndex
	ownerEvent := LocalWorkLogEvent{
		ID:   externalLocalEventID("park-target-owner-"+member.MemberID, options.RequestDigest, options.AttemptID),
		Type: LocalEventOwner, At: owner.At, Message: "parked successor launcher attempt prepared", Owner: &owner, Extra: ownerExtra,
	}
	ownerEvent, _, err = appendLocalEventWithoutCustody(worktree, ownerEvent)
	if err != nil {
		return result, err
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
		return result, err
	}
	projection := workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "active"}
	if err := writeWorkLogProjection(worktree, projection); err != nil {
		return result, err
	}
	if _, err := repairCurrentLocalProjection(worktree); err != nil {
		return result, err
	}
	result.WorkLogReference, result.ClaimID = targetValue, claim.ClaimID
	result.ReceivedEvent, result.OwnerEvent = receivedEvent, ownerEvent
	return result, nil
}

func expectedParkedSessionClaimID(claim workLogClaim) (string, error) {
	evidence := claim.ExternalHandoff
	if evidence == nil || evidence.Version != externalHandoffEvidenceVersion || evidence.Protocol != "parked_session_resume" ||
		evidence.HandoffID == "" || evidence.MemberID == "" || evidence.PredecessorWBSessionID == "" ||
		evidence.SuccessorWBSessionID != claim.AgentID || evidence.SourceWorkLogReference == "" ||
		evidence.TargetWorkLogReference == "" || evidence.SuccessorTmuxName != "wb-session-"+claim.AgentID {
		return "", fmt.Errorf("private parked successor claim metadata is invalid")
	}
	source, err := sessionmove.ParseWorkLogReference(evidence.SourceWorkLogReference)
	if err != nil || source.EffortID != claim.EffortID || source.RunID != claim.RunID || source.ClaimID != claim.ParentClaimID {
		return "", fmt.Errorf("private parked source Work Log lineage is invalid")
	}
	target, err := sessionmove.ParseWorkLogReference(evidence.TargetWorkLogReference)
	if err != nil || target.EffortID != claim.EffortID || target.RunID != claim.RunID || target.ClaimID != claim.ClaimID {
		return "", fmt.Errorf("private parked target Work Log lineage is invalid")
	}
	return sessionpark.TargetWorkLogClaimID(sessionmove.Digest(evidence.RequestDigest), claim.AgentID,
		evidence.MemberID, claim.Repository, source.ClaimID)
}

type ParkedTargetCompletionOptions struct {
	ProjectsRoot  string
	Request       sessionpark.RemoteRequest
	RequestDigest sessionmove.Digest
	Member        sessionpark.RemoteMember
	WorktreeDir   string
	Successor     sessionlaunch.Result
}

func RecordParkedTargetCompleted(options ParkedTargetCompletionOptions) (LocalWorkLogEvent, error) {
	targetValue, err := sessionpark.TargetWorkLogReference(options.Request, options.RequestDigest, options.Member)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	target, _ := sessionmove.ParseWorkLogReference(targetValue)
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	runDir, _, err := openWorkLogRun(home, target.EffortID, target.RunID, false)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer runDir.Close()
	unlock, err := lockClaim(runDir, target.ClaimID)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	defer unlock()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	var claim workLogClaim
	err = readJSONAt(claims, target.ClaimID+".json", &claim)
	_ = claims.Close()
	if err != nil || claim.Worktree != options.WorktreeDir || claim.Branch != sessionpark.MemberPin(options.Request.ResumeID, options.Member.MemberID) ||
		claim.BaseSHA != options.Member.Commit || claim.Lifecycle != "active" || claim.AgentID != options.Request.SuccessorWBSessionID ||
		claim.ExternalHandoff == nil || claim.ExternalHandoff.TargetWorkLogReference != targetValue {
		return LocalWorkLogEvent{}, fmt.Errorf("parked target Work Log claim does not corroborate receipt member")
	}
	projection, err := readWorkLogProjection(options.WorktreeDir)
	if err != nil || projection != (workLogProjection{Version: 1, EffortID: target.EffortID, RunID: target.RunID, ClaimID: target.ClaimID, Lifecycle: "active"}) {
		return LocalWorkLogEvent{}, fmt.Errorf("parked target Work Log projection conflicts with receipt member")
	}
	if err := corroborateClaim(options.WorktreeDir, options.Member.Commit, projection, claim); err != nil {
		return LocalWorkLogEvent{}, err
	}
	events, err := readLocalEvents(options.WorktreeDir)
	if err != nil {
		return LocalWorkLogEvent{}, err
	}
	wantOwnerID := externalLocalEventID("park-target-owner-"+options.Member.MemberID, options.RequestDigest, options.Successor.AttemptID)
	ownerFound := false
	latestOwnerID := ""
	for _, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil {
			latestOwnerID = event.ID
		}
		if event.ID == wantOwnerID && event.Owner != nil && event.Owner.PID == options.Successor.PID &&
			event.Owner.At.Equal(options.Successor.StartedAt.UTC()) {
			ownerFound = true
		}
	}
	if !ownerFound || latestOwnerID != wantOwnerID || ownerPIDStatus(options.Successor.PID) != "active" {
		return LocalWorkLogEvent{}, fmt.Errorf("parked target member does not have the winning live successor as latest owner")
	}
	extra := parkedLocalEventExtra(options.Request, options.Member, targetValue)
	extra["attempt_id"], extra["attempt_index"], extra["pid"] = options.Successor.AttemptID, options.Successor.AttemptIndex, options.Successor.PID
	event := LocalWorkLogEvent{
		ID:   externalLocalEventID("park-target-completed-"+options.Member.MemberID, options.RequestDigest, ""),
		Type: LocalEventHandoff, At: options.Successor.StartedAt.UTC(), Message: "parked successor proved live; target member custody completed",
		Result: "completed", Extra: extra,
	}
	event, _, err = appendLocalEventWithoutCustody(options.WorktreeDir, event)
	return event, err
}

func validateParkedTargetSession(request sessionpark.RemoteRequest, record session.Record) error {
	runtime, model := sessionpark.RequestedRuntimeModel(request)
	if record.PID <= 0 || record.StartedAt.IsZero() || record.WBSessionID != request.SuccessorWBSessionID ||
		record.PredecessorWBSessionID != request.PredecessorWBSessionID || record.HandoffID != request.ResumeID ||
		record.Machine != request.TargetMachine || record.TmuxName != "wb-session-"+request.SuccessorWBSessionID ||
		record.Runtime != runtime || strings.TrimSpace(record.Model) != model {
		return fmt.Errorf("prepared successor session does not match admitted parked target identity")
	}
	return nil
}

func parkedLocalEventExtra(request sessionpark.RemoteRequest, member sessionpark.RemoteMember, targetReference string) map[string]any {
	return map[string]any{
		"resume_id": request.ResumeID, "parked_session_id": request.ParkedSessionID, "member_id": member.MemberID,
		"repository": member.Repository, "predecessor_wb_session_id": request.PredecessorWBSessionID,
		"successor_wb_session_id": request.SuccessorWBSessionID, "source_work_log_reference": member.SourceWorkLogReference,
		"target_work_log_reference": targetReference,
	}
}
