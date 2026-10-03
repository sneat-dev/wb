package main

import (
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/cmdci"
	"github.com/sneat-dev/wb/internal/cli/cmdwait"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/prselector"
	"github.com/sneat-dev/wb/internal/waitrun"
	"github.com/spf13/cobra"
)

func newWaitCmd(inv *invocation) *cobra.Command {
	observer := waitrun.Observer{}
	registry := waitrun.DefaultRegistry()
	return cmdwait.New(newCLIRuntime(inv), cmdwait.Dependencies{
		Wait: observer.Wait, RegisterWait: registry.RegisterWait, Inspect: registry.Inspect, ParseSelector: prselector.Parse,
		Interactive: func(out io.Writer, nonInteractive bool) bool { return console.Interactive(out, nonInteractive) }, Discovery: setDiscoveryTerms,
	}, cmdwait.Children{Checks: func() *cobra.Command { return newWaitChecksCmd(inv) }, Agent: func() *cobra.Command { return newWaitAgentCmd(inv) }, Operation: func() *cobra.Command { return newWaitOperationCmd(inv) }})
}

// The three commands below are the verb-first spelling of waits WB already had,
// scattered under `ci`, `agent` and `daemon operation`. Nothing listed them
// together, which is part of why the measured adoption of `wb ci wait` was what
// it was: an agent cannot choose a verb it never sees.
//
// Each builds from the SAME constructor as the original, so the two spellings
// are one implementation and cannot drift. A command belongs to one parent, so
// the constructor is called again here rather than the instance being shared.

// newWaitChecksCmd is `wb ci wait` under the verb. The object is an exact
// commit's checks — "ci" names a domain, not a thing a caller can point at.
// This remains the authoritative merge-evidence waiter; `wb wait pr` does not.
func newWaitChecksCmd(inv *invocation) *cobra.Command {
	return cmdci.NewChecks(newCLIRuntime(inv), ciDependencies())
}

// newWaitAgentCmd is `wb agent await` under the verb. `await` is kept as an
// alias here because that is the spelling agents already know.
func newWaitAgentCmd(inv *invocation) *cobra.Command {
	command := newAgentAwaitCmd(inv)
	command.Use = strings.Replace(command.Use, "await ", "agent ", 1)
	command.Short = "Block until a dispatched agent run is terminal (was: wb agent await)"
	command.Aliases = append(command.Aliases, "await")
	return command
}

// newWaitOperationCmd is `wb daemon operation wait` under the verb.
func newWaitOperationCmd(inv *invocation) *cobra.Command {
	command := newDaemonOperationWaitCmd(inv, defaultDaemonDependencies())
	command.Use = strings.Replace(command.Use, "wait ", "operation ", 1)
	command.Short = "Wait for a durable operation to reach a terminal state (was: wb daemon operation wait)"
	return command
}
