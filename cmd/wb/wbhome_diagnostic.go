package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// hookExecutionCommand reports whether commandID is a machine hook-execution
// leaf: `wb hooks run` (invoked by the WB-generated git shim installed in every
// repository) and `wb hooks agent ...` (invoked around every agent tool call).
// Those two fan-outs are the visibility exceptions for the retired-WB_HOME
// diagnostic, and they share the same three properties: the caller is a
// WB-generated artifact rather than an operator, the WB_HOME the shim carries
// was planted by WB itself rather than chosen, and the invocation repeats often
// enough that a warning the caller cannot act on is pure noise — the agent
// settings shim even discards stderr (`2>/dev/null; exit 0`).
//
// The operator-facing hook commands deliberately still warn: `wb hooks check`,
// `wb hooks repair` and `wb hooks install` are where a stale shim pinning
// WB_HOME is diagnosed and fixed, so that is exactly where the diagnostic has
// to survive.
func hookExecutionCommand(commandID string) bool {
	if commandID == "hooks run" {
		return true
	}
	return commandID == "hooks agent" || strings.HasPrefix(commandID, "hooks agent ")
}

// warnIgnoredWBHome prints the retired-WB_HOME diagnostic once per invocation,
// before any work starts, to stderr. It never returns an error and never
// changes an exit code: WB_HOME is ignored, not rejected, so the command it
// accompanied still runs. Only the machine hook-execution path is suppressed —
// see hookExecutionCommand — and every human- or agent-invoked command,
// including the hook-management verbs that can actually remove a stale pin,
// still reports the ignored value.
func warnIgnoredWBHome(cmd *cobra.Command) {
	if hookExecutionCommand(persistentCommandID(cmd)) {
		return
	}
	diagnostic, err := wbhome.IgnoredHomeEnvDiagnostic(projectsRoot)
	if err != nil || diagnostic == "" {
		return
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", diagnostic)
}
