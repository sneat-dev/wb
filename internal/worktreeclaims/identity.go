package worktreeclaims

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const (
	EnvAgentPID     = "WB_AGENT_PID"
	EnvAgentRuntime = "WB_AGENT_RUNTIME"
	EnvAgentModel   = "WB_AGENT_MODEL"
	EnvAgentID      = "WB_AGENT_ID"
	EnvSessionID    = "WB_SESSION_ID"
)

type AgentIdentity struct {
	Runtime     string
	AgentID     string
	Model       string
	PID         int
	WBSessionID string
	Registered  bool
}

func (a AgentIdentity) Declared() bool {
	return a.Runtime != "" || a.AgentID != "" || a.Model != "" || a.PID > 0
}
func (a AgentIdentity) Agent() string {
	runtime, id := strings.TrimSpace(a.Runtime), strings.TrimSpace(a.AgentID)
	if runtime != "" && id != "" {
		return runtime + "/" + id
	}
	return OwnerAgent(runtime, id)
}

// IdentityState is the invocation-scoped compatibility state. The facade owns
// its instance; the claims package has no process-wide mutable identity.
type IdentityState struct {
	mu                sync.RWMutex
	resolver          func() (AgentIdentity, bool)
	mutationInitiator string
	invokedCommand    string
}

func (s *IdentityState) SetMutationInitiator(value string) func() {
	s.mu.Lock()
	previous := s.mutationInitiator
	s.mutationInitiator = strings.TrimSpace(value)
	s.mu.Unlock()
	return func() { s.mu.Lock(); s.mutationInitiator = previous; s.mu.Unlock() }
}
func (s *IdentityState) MutationInitiator() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mutationInitiator
}
func (s *IdentityState) SetSessionResolver(resolve func() (AgentIdentity, bool)) {
	s.mu.Lock()
	s.resolver = resolve
	s.mu.Unlock()
}
func (s *IdentityState) CurrentIdentity(environment AgentIdentity) AgentIdentity {
	s.mu.RLock()
	resolve := s.resolver
	s.mu.RUnlock()
	if resolve != nil {
		identity, ok := resolve()
		if ok && identity.Registered && strings.TrimSpace(identity.WBSessionID) != "" {
			return identity
		}
		if ok && !environment.Declared() {
			return identity
		}
	}
	if environment.Declared() {
		return environment
	}
	return AgentIdentity{}
}
func (s *IdentityState) RegisteredIdentity() (AgentIdentity, bool) {
	s.mu.RLock()
	resolve := s.resolver
	s.mu.RUnlock()
	if resolve == nil {
		return AgentIdentity{}, false
	}
	identity, ok := resolve()
	if !ok || !identity.Registered || strings.TrimSpace(identity.WBSessionID) == "" {
		return AgentIdentity{}, false
	}
	return identity, true
}
func IdentityFromEnv(getenv func(string) string) AgentIdentity {
	identity := AgentIdentity{Runtime: strings.TrimSpace(getenv(EnvAgentRuntime)), AgentID: strings.TrimSpace(getenv(EnvAgentID)), Model: strings.TrimSpace(getenv(EnvAgentModel)), WBSessionID: strings.TrimSpace(getenv(EnvSessionID))}
	if pid, err := strconv.Atoi(strings.TrimSpace(getenv(EnvAgentPID))); err == nil && pid > 0 {
		identity.PID = pid
	}
	return identity
}
func (s *IdentityState) SetInvokedCommand(command string) {
	s.mu.Lock()
	s.invokedCommand = command
	s.mu.Unlock()
}
func (s *IdentityState) InvokedCommand() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.invokedCommand
}
func UndeclaredOwnerWarning(worktree string) string {
	return fmt.Sprintf(`warning: %s has no declared agent owner, so WB cannot tell whether work here is still live.
  Register:  wb worktree own %s --pid <agent-pid> --runtime <harness> --model <model>
  Or export: %s %s %s [%s]`, worktree, worktree, EnvAgentPID, EnvAgentRuntime, EnvAgentModel, EnvAgentID)
}
