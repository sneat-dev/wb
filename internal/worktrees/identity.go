package worktrees

import (
	"os"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

const (
	EnvAgentPID     = worktreeclaims.EnvAgentPID
	EnvAgentRuntime = worktreeclaims.EnvAgentRuntime
	EnvAgentModel   = worktreeclaims.EnvAgentModel
	EnvAgentID      = worktreeclaims.EnvAgentID
	EnvSessionID    = worktreeclaims.EnvSessionID
)

type AgentIdentity = worktreeclaims.AgentIdentity

// invocationIdentity is the facade's compatibility state for command runners.
var invocationIdentity worktreeclaims.IdentityState

func SetMutationInitiator(value string) func() { return invocationIdentity.SetMutationInitiator(value) }
func MutationInitiator() string                { return invocationIdentity.MutationInitiator() }
func SetSessionResolver(resolve func() (AgentIdentity, bool)) {
	invocationIdentity.SetSessionResolver(resolve)
}
func CurrentIdentity() AgentIdentity            { return invocationIdentity.CurrentIdentity(IdentityFromEnv()) }
func RegisteredIdentity() (AgentIdentity, bool) { return invocationIdentity.RegisteredIdentity() }
func IdentityFromEnv() AgentIdentity            { return worktreeclaims.IdentityFromEnv(os.Getenv) }
func SetInvokedCommand(command string)          { invocationIdentity.SetInvokedCommand(command) }
func InvokedCommand() string                    { return invocationIdentity.InvokedCommand() }
func UndeclaredOwnerWarning(worktree string) string {
	return worktreeclaims.UndeclaredOwnerWarning(worktree)
}
