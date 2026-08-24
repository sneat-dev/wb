package worktrees

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

const LocalEventOwner = "owner_attached"

// OwnerRegistration is immutable evidence that a particular agent session
// attached itself to a worktree. It is intentionally append-only: a later
// session never overwrites the creator or a previous owner.
type OwnerRegistration struct {
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// PID is the declared agent session's process, never WB's own. WB is a
	// short-lived CLI: its PID is dead moments after it would be written, and
	// once recycled it would report an abandoned worktree as active. An
	// absent PID therefore reads as unknown, which is the honest answer.
	PID int `json:"pid,omitempty"`
	// WBVersion and Command are always populated, because WB always knows
	// them. They give a worktree provenance even when no agent identity was
	// declared.
	WBVersion string    `json:"wb_version,omitempty"`
	Command   string    `json:"command,omitempty"`
	At        time.Time `json:"at"`
}

// sameCustody reports whether two registrations describe the same session
// doing the same kind of work. It deliberately ignores At and Effort: a
// session writing ten times should leave one custody record, not ten, but a
// changed model or a WB upgrade is worth a new entry.
func (o OwnerRegistration) sameCustody(other OwnerRegistration) bool {
	return o.Agent == other.Agent &&
		o.PID == other.PID &&
		o.Model == other.Model &&
		o.WBVersion == other.WBVersion
}

// OwnerView is the live presentation of one registration. PIDStatus is
// evaluated when WB reads the worktree; it is never persisted as metadata.
type OwnerView struct {
	OwnerRegistration
	PIDStatus string `json:"pid_status"`
}

func recordOwner(worktree, effort, agent, model string, pid int) (OwnerRegistration, error) {
	owner := OwnerRegistration{
		Agent: agent, Model: model, Effort: effort, PID: pid,
		WBVersion: buildinfo.Version(), At: time.Now().UTC(),
	}
	_, _, err := appendLocalEvent(worktree, LocalWorkLogEvent{
		Type: LocalEventOwner, Message: "agent session attached", Owner: &owner,
	})
	return owner, err
}

// lastOwner returns the most recently appended owner registration, if any.
func lastOwner(worktree string) (OwnerRegistration, bool, error) {
	owners, err := ownerViews(worktree)
	if err != nil || len(owners) == 0 {
		return OwnerRegistration{}, false, err
	}
	return owners[len(owners)-1].OwnerRegistration, true, nil
}

// RecordCustody records who is driving a worktree, appending only when custody
// actually changed. A session doing repeated writes therefore leaves one record
// rather than a command trace, while a new session, model, or WB version starts
// a new link in the chain.
//
// An entry is written even for an undeclared identity: the WB version and
// command are real provenance, and the absent PID keeps liveness honestly
// unknown rather than asserting a dead or recycled process is alive. An empty
// effort inherits the previous owner's, so an automatic write does not erase
// what the creator recorded.
func RecordCustody(worktree, effort, command string, identity AgentIdentity) error {
	if !identity.Declared() {
		noteUndeclared(worktree)
	}

	previous, found, err := lastOwner(worktree)
	if err != nil {
		return err
	}

	candidate := OwnerRegistration{
		Agent: identity.Agent(), Model: strings.TrimSpace(identity.Model),
		Effort: effort, PID: identity.PID,
		WBVersion: buildinfo.Version(), Command: command,
	}
	if found && previous.sameCustody(candidate) {
		return nil
	}
	if candidate.Effort == "" && found {
		candidate.Effort = previous.Effort
	}
	candidate.At = time.Now().UTC()

	_, _, err = appendLocalEvent(worktree, LocalWorkLogEvent{
		Type: LocalEventOwner, Message: custodyMessage(identity), Owner: &candidate,
	})
	return err
}

func custodyMessage(identity AgentIdentity) string {
	if identity.Declared() {
		return "agent session attached"
	}
	return "wb wrote here; no agent identity declared"
}

// pendingWarnings collects worktrees this invocation wrote to without a
// declared owner. The write path is deep in the library and must not print;
// the command layer drains this once and reports it on stderr.
var (
	warningsMu      sync.Mutex
	pendingWarnings []string
)

func noteUndeclared(worktree string) {
	warningsMu.Lock()
	defer warningsMu.Unlock()
	for _, existing := range pendingWarnings {
		if existing == worktree {
			return
		}
	}
	pendingWarnings = append(pendingWarnings, worktree)
}

// TakeOwnerWarnings returns the worktrees written to without a declared owner
// and clears the list, so a caller reports each one once.
func TakeOwnerWarnings() []string {
	warningsMu.Lock()
	defer warningsMu.Unlock()
	taken := pendingWarnings
	pendingWarnings = nil
	return taken
}

// ensureCustody keeps the owner chain current on every worktree write. It runs
// before the caller's own event is appended, so the record of who was driving
// precedes the work they did.
//
// Its error is deliberately dropped: provenance must never be the reason a real
// operation fails, and a missing entry is itself visible as a gap in the chain.
func ensureCustody(worktree string) {
	_ = RecordCustody(worktree, "", InvokedCommand(), CurrentIdentity())
}

func ownerViews(worktree string) ([]OwnerView, error) {
	events, err := readLocalEvents(worktree)
	if err != nil {
		return nil, err
	}
	owners := make([]OwnerView, 0)
	for _, event := range events {
		if event.Owner == nil {
			continue
		}
		owner := *event.Owner
		owners = append(owners, OwnerView{OwnerRegistration: owner, PIDStatus: ownerPIDStatus(owner.PID)})
	}
	sort.SliceStable(owners, func(i, j int) bool { return owners[i].At.Before(owners[j].At) })
	return owners, nil
}

func ownerPIDStatus(pid int) string {
	if pid <= 0 {
		return "unknown"
	}
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return "active"
	}
	if errors.Is(err, syscall.ESRCH) {
		return "orphaned"
	}
	return "unknown"
}

func worktreeOwnerState(owners []OwnerView) string {
	if len(owners) == 0 {
		return "orphaned"
	}
	for _, owner := range owners {
		if owner.PIDStatus == "active" {
			return "active"
		}
	}
	for _, owner := range owners {
		if owner.PIDStatus == "unknown" {
			return "unknown"
		}
	}
	return "orphaned"
}

// Declared-owner states used when triaging a worktree. They are deliberately
// distinct from worktreeOwnerState, which treats "no records at all" as
// orphaned. For triage that conflation is the whole problem: never having said
// who you are is not the same as having said so and then exiting.
const (
	OwnerLive     = "live"     // a declared session is running
	OwnerGone     = "gone"     // every declared session has exited
	OwnerUnstated = "unstated" // nobody declared a session
)

// DeclaredOwner reports whether a live session is declared for a worktree, and
// names the most recent declaration.
//
// Only records carrying a PID count. An entry written by a WB command with no
// declaration is provenance, not a claim of ownership, so it must not be read
// as a dead session.
func DeclaredOwner(worktree string) (state, agent string, pid int) {
	views, err := ownerViews(worktree)
	if err != nil || len(views) == 0 {
		return OwnerUnstated, "", 0
	}
	state = OwnerUnstated
	for _, view := range views {
		if view.PID <= 0 {
			continue
		}
		// Any live declared session makes the worktree live, whichever link in
		// the chain it sits at: a running process is running whether or not a
		// later session declared itself and then exited. Among dead sessions
		// the most recent is reported, since that is the one that left the
		// work behind.
		switch view.PIDStatus {
		case "active":
			state, agent, pid = OwnerLive, view.Agent, view.PID
		case "orphaned":
			if state != OwnerLive {
				state, agent, pid = OwnerGone, view.Agent, view.PID
			}
		default:
			if state == OwnerUnstated {
				agent, pid = view.Agent, view.PID
			}
		}
	}
	return state, agent, pid
}
