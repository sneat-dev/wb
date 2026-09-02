package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newSkillsHookCmd groups WB's Claude Code SessionStart hook: the printer,
// the explicit installer, and the runner the hook itself invokes.
//
// A SessionStart hook is the other half of closing the skills-discovery gap
// alongside `wb skills sync`: sync gets the skills onto disk, but an
// orchestrator still has to be told, at the start of its session, both to
// register itself with WB and that its skills might be stale. Nothing here
// ever edits a settings file except the explicit 'install' subcommand --
// 'print' only prints, and 'run' only prints session-start context.
func newSkillsHookCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "hook",
		Short: "Print, install, or run WB's Claude Code SessionStart hook",
	}
	command.AddCommand(newSkillsHookPrintCmd())
	command.AddCommand(newSkillsHookInstallCmd())
	command.AddCommand(newSkillsHookRunCmd())
	return command
}

// skillsHookInvocation is the subcommand a settings file must run.
const skillsHookInvocation = "skills hook run"

// skillsHookShellCommand is the exact string wired into a Claude Code
// SessionStart entry, following the same fail-open shape
// 'wb hooks agent install' already uses for PreToolUse
// (agentHookShellCommand): stderr discarded, exit forced to 0. A
// SessionStart hook cannot block the session on any exit code, but a wb too
// old to know this subcommand should still degrade to silence rather than
// print cobra's own usage error into the new session's context.
func skillsHookShellCommand(executable string) string {
	return fmt.Sprintf("%s %s 2>/dev/null; exit 0", shellQuote(executable), skillsHookInvocation)
}

// skillsHookSettingsSnippet is the JSON document 'wb skills hook print' and
// 'wb skills hook install' both build from, so the two can never drift
// apart on the shape they merge or print.
func skillsHookSettingsSnippet(executable string) map[string]any {
	return map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": skillsHookShellCommand(executable),
							"timeout": 10,
						},
					},
				},
			},
		},
	}
}
