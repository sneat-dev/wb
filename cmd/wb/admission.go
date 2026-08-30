package main

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// addMutationAdmissionFlags gives every command that can publish or change
// agent-owned state the same explicit admission contract. Auto preserves the
// long-standing human CLI behavior; agent mode is fail-closed on the live
// session registry, and manual mode records who authorized the exception.
func addMutationAdmissionFlags(command *cobra.Command) {
	if command.Flags().Lookup("mode") == nil {
		command.Flags().String("mode", "auto", "execution mode: auto, agent (requires a live registered session), or manual (requires --initiator)")
	}
	if command.Flags().Lookup("initiator") == nil {
		command.Flags().String("initiator", "", "human or agent that authorized a manual mutation")
	}
}

// requireMutationAdmission must run immediately before a mutating backend is
// called. It intentionally performs no WB_HOME, Git, or Work Log access: an
// unregistered agent is refused before any mutation can be observable.
func requireMutationAdmission(command *cobra.Command, mutate bool) (worktrees.AgentIdentity, func(), error) {
	if !mutate {
		return worktrees.AgentIdentity{}, func() {}, nil
	}
	mode, err := command.Flags().GetString("mode")
	if err != nil {
		return worktrees.AgentIdentity{}, func() {}, err
	}
	initiator, err := command.Flags().GetString("initiator")
	if err != nil {
		return worktrees.AgentIdentity{}, func() {}, err
	}
	mode = strings.TrimSpace(mode)
	var registered worktrees.AgentIdentity
	registeredOK := false
	if mode == "auto" {
		if commandHasAgentIdentity(command) {
			mode = "agent"
		} else if registered, registeredOK = worktrees.RegisteredIdentity(); registeredOK {
			// A live session is positive evidence of agent invocation even when
			// the caller relies on the session's exported identity instead of
			// repeating flags on every command.
			mode = "agent"
		}
	}
	switch mode {
	case "", "auto":
		return worktrees.AgentIdentity{}, func() {}, nil
	case "manual":
		if strings.TrimSpace(initiator) == "" {
			return worktrees.AgentIdentity{}, func() {}, fmt.Errorf("manual execution mode requires --initiator so the non-agent mutation is auditable")
		}
		return worktrees.AgentIdentity{}, worktrees.SetMutationInitiator(initiator), nil
	case "agent":
		identity, ok := registered, registeredOK
		if !ok {
			identity, ok = worktrees.RegisteredIdentity()
		}
		if !ok {
			return worktrees.AgentIdentity{}, func() {}, fmt.Errorf("agent-mode mutation requires a live registered session; register before the first mutation with `wb session register --pid $PPID --runtime <harness> --model <model>`, or select --mode manual --initiator <human>")
		}
		return identity, func() {}, nil
	default:
		return worktrees.AgentIdentity{}, func() {}, fmt.Errorf("unsupported execution mode %q; use auto, agent, or manual", mode)
	}
}

// commandHasAgentIdentity matches create's compatibility rule: supplying an
// agent/runtime identity opts into agent semantics even when --mode is omitted.
// Ambient WB_AGENT_* declarations are equivalent evidence. Human-only commands
// with neither declaration remain on the historical auto path until an
// operator explicitly selects --mode agent or manual.
func commandHasAgentIdentity(command *cobra.Command) bool {
	for _, name := range []string{"agent", "agent-id", "agent-runtime", "runtime"} {
		flag := command.Flags().Lookup(name)
		if flag != nil && strings.TrimSpace(flag.Value.String()) != "" {
			return true
		}
	}
	return worktrees.IdentityFromEnv().Declared()
}

func mutationInitiator(command *cobra.Command) string {
	initiator, err := command.Flags().GetString("initiator")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(initiator)
}
