package cmdskills

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newSkillsHookPrintCmd(deps HookDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "print",
		Short: "Print the Claude Code SessionStart hook snippet for ~/.claude/settings.json",
		Long: `Print the Claude Code SessionStart hook snippet for ~/.claude/settings.json.

wb never edits that file on its own. Merge the printed "hooks" object into
it by hand, or run 'wb skills hook install' to merge it automatically.

The hook runs once at the start of every session and prints plain text to
stdout, which Claude Code adds to the session's context automatically. It
never blocks the session on any exit code. What it prints:

  - a reminder to run 'wb session register' for this session, since a
    SessionStart hook has no reliable way to name the agent process's own
    PID on its behalf (see 'wb session register --help')
  - a one-line warning when the installed Agent Skills were synced by a
    different wb version than the one now running, with the fix
    ('wb skills sync')`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(skillsHookSettingsSnippet(deps.Executable(), deps.Quote)); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"\nMerge this into ~/.claude/settings.json's \"hooks\" key (preserving any\nother hooks already there), or run: wb skills hook install\n")
			return err
		},
	}
	return command
}
