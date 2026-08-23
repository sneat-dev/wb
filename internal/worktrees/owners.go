package worktrees

import (
	"errors"
	"os"
	"sort"
	"syscall"
	"time"
)

const LocalEventOwner = "owner_attached"

// OwnerRegistration is immutable evidence that a particular agent session
// attached itself to a worktree. It is intentionally append-only: a later
// session never overwrites the creator or a previous owner.
type OwnerRegistration struct {
	Agent  string    `json:"agent,omitempty"`
	Model  string    `json:"model,omitempty"`
	Effort string    `json:"effort,omitempty"`
	PID    int       `json:"pid,omitempty"`
	At     time.Time `json:"at"`
}

// OwnerView is the live presentation of one registration. PIDStatus is
// evaluated when WB reads the worktree; it is never persisted as metadata.
type OwnerView struct {
	OwnerRegistration
	PIDStatus string `json:"pid_status"`
}

func recordOwner(worktree, effort, agent, model string, pid int) (OwnerRegistration, error) {
	owner := OwnerRegistration{Agent: agent, Model: model, Effort: effort, PID: pid, At: time.Now().UTC()}
	_, _, err := appendLocalEvent(worktree, LocalWorkLogEvent{
		Type: LocalEventOwner, Message: "agent session attached", Owner: &owner,
	})
	return owner, err
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

// currentProcessID is isolated for tests and documents that a PID records the
// WB caller which attached the owner record, not a Git process it later starts.
func currentProcessID() int { return os.Getpid() }
