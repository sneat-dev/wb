package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/skills"
)

func newSkillsHookRunCmd() *cobra.Command {
	command := &cobra.Command{
		Use:    "run",
		Short:  "Print SessionStart context (normally invoked by the installed hook)",
		Hidden: true,
		Long: `Print SessionStart context for Claude Code.

Not meant to be run by hand -- 'wb skills hook install' (or the snippet from
'wb skills hook print') wires this into a Claude Code SessionStart hook.
Claude Code injects this command's plain-text stdout into the new session's
context automatically; it never blocks the session on any exit code.

It cannot register the session itself: a SessionStart hook has no reliable
way to name the agent process's own PID on its behalf (see 'wb session
register --help'), so it only reminds the agent to do so. It separately
warns when the installed Agent Skills predate the running wb.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), sessionStartAnnouncement())
			return err
		},
	}
	return command
}

// sessionStartAnnouncement never returns an error and never panics: it is
// injected into every Claude Code session's opening context, so every
// failure along the way -- no resolvable home directory, no marker yet, a
// corrupt one -- degrades to the registration reminder alone rather than
// producing nothing or crashing the hook.
func sessionStartAnnouncement() string {
	lines := []string{
		"WB session start: if this session has not already run `wb session register` for itself, run it now with --pid $PPID from your own tool-call shell (see `wb session register --help`).",
	}
	dir, err := defaultHarnessSkillsDir()
	if err != nil {
		return strings.Join(lines, "\n")
	}
	status, err := skills.ReadStatus(dir)
	if err != nil {
		return strings.Join(lines, "\n")
	}
	current := collectVersion().Version
	if status.Drifted(current) {
		lines = append(lines, skillsDriftMessage(dir, status, current))
	}
	return strings.Join(lines, "\n")
}
