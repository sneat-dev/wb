package worktrees

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Environment variables through which an agent session declares who it is.
// They are read on every mutating worktree operation, so a session exports
// them once rather than passing flags to each command.
const (
	EnvAgentPID     = "WB_AGENT_PID"
	EnvAgentRuntime = "WB_AGENT_RUNTIME"
	EnvAgentModel   = "WB_AGENT_MODEL"
	EnvAgentID      = "WB_AGENT_ID"
	EnvSessionID    = "WB_SESSION_ID"
)

// AgentIdentity is who WB was told is driving it. Every field is declared by
// the caller; WB infers none of them. In particular PID is the agent
// session's process, never WB's own: WB is a short-lived CLI whose PID is
// dead moments after it would be written, and a recycled PID would later
// report a long-abandoned worktree as active.
type AgentIdentity struct {
	Runtime string
	AgentID string
	Model   string
	PID     int
	// WBSessionID links a new Work Log claim to the registered session that
	// created it. A PID is only a liveness coordinate, never the identity.
	WBSessionID string
	// Registered distinguishes a resolver-backed identity from ambient
	// WB_AGENT_* declarations. Environment declarations are provenance, not
	// proof that a live session registered.
	Registered bool
}

// Declared reports whether anything at all was declared. An undeclared
// identity still produces an owner entry — carrying the WB version and
// command — but with no PID, so liveness reads as unknown rather than as a
// confident lie.
func (a AgentIdentity) Declared() bool {
	return a.Runtime != "" || a.AgentID != "" || a.Model != "" || a.PID > 0
}

// Agent renders the identity for the owner record's Agent field.
//
// It composes "runtime/id" when both are declared, because which session of a
// harness is driving is exactly what distinguishes two concurrent agents on
// one machine. It falls back to the existing ownerAgent behaviour — runtime,
// else id — when only one is known, so a partial declaration reads the same
// way records written by the create path already do.
func (a AgentIdentity) Agent() string {
	runtime, id := strings.TrimSpace(a.Runtime), strings.TrimSpace(a.AgentID)
	if runtime != "" && id != "" {
		return runtime + "/" + id
	}
	return ownerAgent(runtime, id)
}

// sessionResolver, when installed, returns the identity of a registered agent
// session that owns this process. It is injected rather than imported so the
// worktree layer keeps no dependency on how sessions are stored.
var (
	resolverMu      sync.RWMutex
	sessionResolver func() (AgentIdentity, bool)
)

// SetSessionResolver installs the lookup used when the environment carries no
// declaration.
func SetSessionResolver(resolve func() (AgentIdentity, bool)) {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	sessionResolver = resolve
}

// CurrentIdentity is who WB should attribute this invocation to.
//
// An explicit environment declaration wins: it is the most specific thing the
// caller said, and a session that overrides it for one command means it. Only
// when nothing is declared does WB consult the registered sessions, which is
// still a declaration — made once at session start rather than per command.
func CurrentIdentity() AgentIdentity {
	if identity := IdentityFromEnv(); identity.Declared() {
		return identity
	}
	resolverMu.RLock()
	resolve := sessionResolver
	resolverMu.RUnlock()
	if resolve == nil {
		return AgentIdentity{}
	}
	if identity, ok := resolve(); ok {
		return identity
	}
	return AgentIdentity{}
}

// RegisteredIdentity resolves only the live session registry, bypassing the
// per-command WB_AGENT_* override. This is the authoritative admission query:
// an ambient environment declaration can describe provenance, but cannot
// prove that a live harness registered before the mutation.
func RegisteredIdentity() (AgentIdentity, bool) {
	resolverMu.RLock()
	resolve := sessionResolver
	resolverMu.RUnlock()
	if resolve == nil {
		return AgentIdentity{}, false
	}
	identity, ok := resolve()
	if !ok || !identity.Registered || strings.TrimSpace(identity.WBSessionID) == "" {
		return AgentIdentity{}, false
	}
	return identity, true
}

// IdentityFromEnv reads a declaration from the process environment. A PID
// that is absent, non-numeric, or non-positive is treated as undeclared
// rather than as an error: a malformed declaration must not block the work,
// only leave liveness unknown.
func IdentityFromEnv() AgentIdentity {
	identity := AgentIdentity{
		Runtime:     strings.TrimSpace(os.Getenv(EnvAgentRuntime)),
		AgentID:     strings.TrimSpace(os.Getenv(EnvAgentID)),
		Model:       strings.TrimSpace(os.Getenv(EnvAgentModel)),
		WBSessionID: strings.TrimSpace(os.Getenv(EnvSessionID)),
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvAgentPID))); err == nil && pid > 0 {
		identity.PID = pid
	}
	return identity
}

// invokedCommand is the WB command currently running, published by the command
// layer so the write path can record which command touched a worktree without
// every call site threading it through.
var (
	invokedMu      sync.RWMutex
	invokedCommand string
)

// SetInvokedCommand records the command path, e.g. "worktree set".
func SetInvokedCommand(command string) {
	invokedMu.Lock()
	defer invokedMu.Unlock()
	invokedCommand = command
}

// InvokedCommand returns the command path the command layer published.
func InvokedCommand() string {
	invokedMu.RLock()
	defer invokedMu.RUnlock()
	return invokedCommand
}

// UndeclaredOwnerWarning is the message shown when a mutating operation runs
// against a worktree whose owner is unknown. It names both routes so the
// reader can pick the one that fits: a one-shot command, or the environment
// for a whole session.
func UndeclaredOwnerWarning(worktree string) string {
	return fmt.Sprintf(`warning: %s has no declared agent owner, so WB cannot tell whether work here is still live.
  Register:  wb worktree own %s --pid <agent-pid> --runtime <harness> --model <model>
  Or export: %s %s %s [%s]`,
		worktree, worktree,
		EnvAgentPID, EnvAgentRuntime, EnvAgentModel, EnvAgentID)
}
