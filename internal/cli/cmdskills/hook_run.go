package cmdskills

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSkillsHookRunCmd(deps HookDependencies) *cobra.Command {
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
			_, err := fmt.Fprintln(cmd.OutOrStdout(), deps.Announcement())
			return err
		},
	}
	return command
}
