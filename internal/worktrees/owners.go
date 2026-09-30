package worktrees

import (
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreejournal"
)

const LocalEventOwner = worktreeclaims.LocalEventOwner
const (
	OwnerLive     = worktreeclaims.OwnerLive
	OwnerGone     = worktreeclaims.OwnerGone
	OwnerUnstated = worktreeclaims.OwnerUnstated
)

type OwnerRegistration = worktreejournal.OwnerRegistration

// OwnerView keeps the public event record type and wire shape.
type OwnerView struct {
	OwnerRegistration
	PIDStatus string `json:"pid_status"`
}

var ownerWarnings worktreeclaims.OwnerWarnings

func recordOwner(worktree, effort, agent, model string, pid int) (OwnerRegistration, error) {
	owner, err := ownerPorts().RecordOwner(worktree, effort, agent, model, pid)
	return OwnerRegistration(owner), err
}

func RecordCustody(worktree, effort, command string, identity AgentIdentity) error {
	return ownerPorts().RecordCustody(worktree, effort, command, identity)
}

func TakeOwnerWarnings() []string   { return ownerWarnings.TakeOwnerWarnings() }
func ensureCustody(worktree string) { ownerPorts().EnsureCustody(worktree) }
func ownerViews(worktree string) ([]OwnerView, error) {
	owners, err := ownerPorts().OwnerViews(worktree)
	return fromClaimOwnerViews(owners), err
}
func lifecycleOwnerViews(home, worktree string) ([]OwnerView, error) {
	owners, err := ownerPorts().LifecycleOwnerViews(home, worktree)
	return fromClaimOwnerViews(owners), err
}

func ownerPIDStatus(pid int) string { return ownerPorts().OwnerPIDStatus(pid) }
func worktreeOwnerState(owners []OwnerView) string {
	return worktreeclaims.WorktreeOwnerState(toClaimOwnerViews(owners))
}
func DeclaredOwner(worktree string) (state, agent string, pid int) {
	return ownerPorts().DeclaredOwner(worktree)
}

func fromClaimOwnerViews(owners []worktreeclaims.OwnerView) []OwnerView {
	if owners == nil {
		return nil
	}
	result := make([]OwnerView, len(owners))
	for index, owner := range owners {
		result[index] = OwnerView{OwnerRegistration: OwnerRegistration(owner.OwnerRegistration), PIDStatus: owner.PIDStatus}
	}
	return result
}
func toClaimOwnerViews(owners []OwnerView) []worktreeclaims.OwnerView {
	result := make([]worktreeclaims.OwnerView, len(owners))
	for index, owner := range owners {
		result[index] = worktreeclaims.OwnerView{OwnerRegistration: worktreeclaims.OwnerRegistration(owner.OwnerRegistration), PIDStatus: owner.PIDStatus}
	}
	return result
}
func ownerPorts() worktreeclaims.OwnerPorts {
	return worktreeclaims.OwnerPorts{
		Version: buildinfo.Version, Now: time.Now, MutationInitiator: MutationInitiator, CurrentIdentity: CurrentIdentity, InvokedCommand: InvokedCommand,
		AppendEvent: func(worktree string, event worktreejournal.LocalWorkLogEvent) error {
			_, _, err := appendLocalEvent(worktree, event)
			return err
		},
		ReadEvents:    readLocalEvents,
		ActiveHandoff: readParkedOwnerHandoff,
		ExpectedCompletionID: func(memberID, digest string) string {
			return externalLocalEventID("park-target-completed-"+memberID, sessionmove.Digest(digest), "")
		},
		ReadForInspection: readLocalEventsForInspection,
		ProcessStatus:     processStatus, Warnings: &ownerWarnings,
	}
}

func readParkedOwnerHandoff(home, worktree string) (worktreeclaims.LegacyHandoff, bool) {
	claim, _, _, err := activeWorkLogClaim(home, worktree)
	return legacyHandoffFromClaim(claim, err)
}

func legacyHandoffFromClaim(claim workLogClaim, err error) (worktreeclaims.LegacyHandoff, bool) {
	if err != nil || claim.AcquiredVia != "parked_session_resume" || claim.ExternalHandoff == nil {
		return worktreeclaims.LegacyHandoff{}, false
	}
	evidence := claim.ExternalHandoff
	return worktreeclaims.LegacyHandoff{HandoffID: evidence.HandoffID, MemberID: evidence.MemberID, Repository: claim.Repository,
		PredecessorWBSessionID: evidence.PredecessorWBSessionID, AgentID: claim.AgentID, SourceWorkLogReference: evidence.SourceWorkLogReference,
		TargetWorkLogReference: evidence.TargetWorkLogReference, RequestDigest: evidence.RequestDigest}, true
}
