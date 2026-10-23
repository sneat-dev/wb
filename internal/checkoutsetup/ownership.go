package checkoutsetup

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"path/filepath"
)

type OwnershipRequest struct {
	Path                string
	Admitted, Overrides worktrees.AgentIdentity
}
type OwnershipResult struct {
	Path     string
	Identity worktrees.AgentIdentity
}
type OwnershipDependencies struct {
	Abs             func(string) (string, error)
	IdentityFromEnv func() worktrees.AgentIdentity
	RecordCustody   func(string, string, string, worktrees.AgentIdentity) error
}
type Ownership struct{ deps OwnershipDependencies }

func NewOwnership(deps OwnershipDependencies) *Ownership { return &Ownership{deps: deps} }
func DefaultOwnershipDependencies() OwnershipDependencies {
	return OwnershipDependencies{filepath.Abs, worktrees.IdentityFromEnv, worktrees.RecordCustody}
}
func (s *Ownership) Record(req OwnershipRequest) (OwnershipResult, error) {

	// The local work log refuses a relative path, so resolve before
	// recording; "." is the documented default and must work.
	path, err := s.deps.Abs(req.Path)
	if err != nil {
		return OwnershipResult{}, err
	}
	// Environment first, flags on top: a session exports once, and a
	// single command can still correct one field without restating
	// the rest.
	declared := s.deps.IdentityFromEnv()
	if req.Admitted.Registered {
		// Agent admission is authoritative: ambient flags/environment may
		// describe a different actor, but cannot spoof the live session
		// that is recorded in the custody chain.
		declared = req.Admitted
	} else {
		if req.Overrides.Runtime != "" {
			declared.Runtime = req.Overrides.Runtime
		}
		if req.Overrides.AgentID != "" {
			declared.AgentID = req.Overrides.AgentID
		}
		if req.Overrides.Model != "" {
			declared.Model = req.Overrides.Model
		}
		if req.Overrides.PID > 0 {
			declared.PID = req.Overrides.PID
		}
	}
	if !declared.Declared() {
		return OwnershipResult{}, fmt.Errorf("nothing to declare: pass --pid/--runtime/--model or set %s/%s/%s",
			worktrees.EnvAgentPID, worktrees.EnvAgentRuntime, worktrees.EnvAgentModel)
	}

	if err := s.deps.RecordCustody(path, "", "worktree own", declared); err != nil {
		return OwnershipResult{}, err
	}
	return OwnershipResult{path, declared}, nil
}
