package worktreeclaims

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

const LocalEventOwner = "owner_attached"
const (
	OwnerLive     = "live"
	OwnerGone     = "gone"
	OwnerUnstated = "unstated"
)

type OwnerRegistration struct {
	Agent     string    `json:"agent,omitempty"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	Initiator string    `json:"initiator,omitempty"`
	PID       int       `json:"pid,omitempty"`
	WBVersion string    `json:"wb_version,omitempty"`
	Command   string    `json:"command,omitempty"`
	At        time.Time `json:"at"`
}
type OwnerView struct {
	OwnerRegistration
	PIDStatus string `json:"pid_status"`
}
type OwnerWarnings struct {
	mu      sync.Mutex
	pending []string
}

func (w *OwnerWarnings) NoteUndeclared(worktree string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, existing := range w.pending {
		if existing == worktree {
			return
		}
	}
	w.pending = append(w.pending, worktree)
}
func (w *OwnerWarnings) TakeOwnerWarnings() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	taken := w.pending
	w.pending = nil
	return taken
}

type LegacyHandoff struct {
	HandoffID, MemberID, Repository, PredecessorWBSessionID, AgentID, SourceWorkLogReference, TargetWorkLogReference, RequestDigest string
}
type OwnerPorts struct {
	Version              func() string
	Now                  func() time.Time
	MutationInitiator    func() string
	CurrentIdentity      func() AgentIdentity
	InvokedCommand       func() string
	AppendEvent          func(string, worktreejournal.LocalWorkLogEvent) error
	ReadEvents           func(string) ([]worktreejournal.LocalWorkLogEvent, error)
	ActiveHandoff        func(string, string) (LegacyHandoff, bool)
	ExpectedCompletionID func(string, string) string
	ReadForInspection    func(string, func(worktreejournal.LocalWorkLogEvent) bool) ([]worktreejournal.LocalWorkLogEvent, bool, error)
	ProcessStatus        func(int) error
	Warnings             *OwnerWarnings
}

func toJournalOwner(owner OwnerRegistration) worktreejournal.OwnerRegistration {
	return worktreejournal.OwnerRegistration(owner)
}
func fromJournalOwner(owner worktreejournal.OwnerRegistration) OwnerRegistration {
	return OwnerRegistration(owner)
}
func SameCustody(o, other OwnerRegistration) bool {
	return o.Agent == other.Agent && o.PID == other.PID && o.Model == other.Model && o.Initiator == other.Initiator && o.WBVersion == other.WBVersion
}
func (p OwnerPorts) RecordOwner(worktree, effort, agent, model string, pid int) (OwnerRegistration, error) {
	owner := OwnerRegistration{Agent: agent, Model: model, Effort: effort, PID: pid, Initiator: p.MutationInitiator(), WBVersion: p.Version(), At: p.Now().UTC()}
	record := toJournalOwner(owner)
	err := p.AppendEvent(worktree, worktreejournal.LocalWorkLogEvent{Type: LocalEventOwner, Message: "agent session attached", Owner: &record})
	return owner, err
}
func (p OwnerPorts) LastOwner(worktree string) (OwnerRegistration, bool, error) {
	owners, err := p.OwnerViews(worktree)
	if err != nil || len(owners) == 0 {
		return OwnerRegistration{}, false, err
	}
	return owners[len(owners)-1].OwnerRegistration, true, nil
}
func (p OwnerPorts) RecordCustody(worktree, effort, command string, identity AgentIdentity) error {
	if !identity.Declared() {
		p.Warnings.NoteUndeclared(worktree)
	}
	previous, found, err := p.LastOwner(worktree)
	if err != nil {
		return err
	}
	candidate := OwnerRegistration{Agent: identity.Agent(), Model: strings.TrimSpace(identity.Model), Effort: effort, PID: identity.PID, Initiator: p.MutationInitiator(), WBVersion: p.Version(), Command: command}
	if found && SameCustody(previous, candidate) {
		return nil
	}
	if candidate.Effort == "" && found {
		candidate.Effort = previous.Effort
	}
	candidate.At = p.Now().UTC()
	record := toJournalOwner(candidate)
	return p.AppendEvent(worktree, worktreejournal.LocalWorkLogEvent{Type: LocalEventOwner, Message: CustodyMessage(identity), Owner: &record})
}
func CustodyMessage(identity AgentIdentity) string {
	if identity.Declared() {
		return "agent session attached"
	}
	return "wb wrote here; no agent identity declared"
}
func (p OwnerPorts) EnsureCustody(worktree string) {
	_ = p.RecordCustody(worktree, "", p.InvokedCommand(), p.CurrentIdentity())
}
func (p OwnerPorts) OwnerViews(worktree string) ([]OwnerView, error) {
	events, err := p.ReadEvents(worktree)
	if err != nil {
		return nil, err
	}
	return p.ownerViewsFromEvents(events), nil
}
func (p OwnerPorts) ownerViewsFromEvents(events []worktreejournal.LocalWorkLogEvent) []OwnerView {
	owners := make([]OwnerView, 0)
	for _, event := range events {
		if event.Owner == nil {
			continue
		}
		owner := fromJournalOwner(*event.Owner)
		owners = append(owners, OwnerView{OwnerRegistration: owner, PIDStatus: p.OwnerPIDStatus(owner.PID)})
	}
	sort.SliceStable(owners, func(i, j int) bool { return owners[i].At.Before(owners[j].At) })
	return owners
}
func (p OwnerPorts) LifecycleOwnerViews(home, worktree string) ([]OwnerView, error) {
	owners, err := p.OwnerViews(worktree)
	if err == nil {
		return owners, nil
	}
	evidence, ok := p.ActiveHandoff(home, worktree)
	if !ok {
		return nil, err
	}
	expectedID := p.ExpectedCompletionID(evidence.MemberID, evidence.RequestDigest)
	events, compatible, inspectionErr := p.ReadForInspection(worktree, func(event worktreejournal.LocalWorkLogEvent) bool {
		if event.ID != expectedID {
			return false
		}
		extra := event.Extra
		return ExtraString(extra, "resume_id") == evidence.HandoffID && ExtraString(extra, "member_id") == evidence.MemberID &&
			ExtraString(extra, "repository") == evidence.Repository && ExtraString(extra, "predecessor_wb_session_id") == evidence.PredecessorWBSessionID &&
			ExtraString(extra, "successor_wb_session_id") == evidence.AgentID && ExtraString(extra, "source_work_log_reference") == evidence.SourceWorkLogReference &&
			ExtraString(extra, "target_work_log_reference") == evidence.TargetWorkLogReference
	})
	if inspectionErr != nil || !compatible {
		return nil, err
	}
	return p.ownerViewsFromEvents(events), nil
}
func ExtraString(extra map[string]any, key string) string {
	value, _ := extra[key].(string)
	return strings.TrimSpace(value)
}
func (p OwnerPorts) OwnerPIDStatus(pid int) string {
	if pid <= 0 {
		return "unknown"
	}
	err := p.ProcessStatus(pid)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return "active"
	}
	if errors.Is(err, syscall.ESRCH) {
		return "orphaned"
	}
	return "unknown"
}
func WorktreeOwnerState(owners []OwnerView) string {
	state := "unknown"
	for _, owner := range owners {
		if owner.PID <= 0 {
			continue
		}
		if owner.PIDStatus == "active" {
			return "active"
		}
		if owner.PIDStatus == "orphaned" {
			state = "orphaned"
		}
	}
	return state
}
func (p OwnerPorts) DeclaredOwner(worktree string) (state, agent string, pid int) {
	state, view := p.DeclaredOwnerView(worktree)
	return state, view.Agent, view.PID
}

// DeclaredOwnerView is DeclaredOwner with the whole registration the verdict
// rests on (its zero value when the state is OwnerUnstated for want of one).
func (p OwnerPorts) DeclaredOwnerView(worktree string) (state string, chosen OwnerView) {
	views, err := p.OwnerViews(worktree)
	if err != nil || len(views) == 0 {
		return OwnerUnstated, OwnerView{}
	}
	state = OwnerUnstated
	for _, view := range views {
		if view.PID <= 0 {
			continue
		}
		switch view.PIDStatus {
		case "active":
			state, chosen = OwnerLive, view
		case "orphaned":
			if state != OwnerLive {
				state, chosen = OwnerGone, view
			}
		default:
			if state == OwnerUnstated {
				chosen = view
			}
		}
	}
	return state, chosen
}
