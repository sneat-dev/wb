package cmdhooks

import (
	"fmt"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func (family commands) newHooksAgentInstallCmd() *cobra.Command {
	var settingsPath string
	var dryRun bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Register the pre-tool-use guard in a Claude Code settings file",
		Long: `Add the WB pre-tool-use guard to a Claude Code settings file.

Merges one PreToolUse entry into the settings document, preserving every other
key and every other hook. Re-running it changes nothing once the entry is
present, so it is safe to run from any automation.

Defaults to ~/.claude/settings.json. Use --dry-run to print the merged
document without writing it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := settingsPath
			if path == "" {
				home, err := family.agent.Home()
				if err != nil {
					return fmt.Errorf("locate the home directory: %w", err)
				}
				path = filepath.Join(home, ".claude", "settings.json")
			}
			shellCommand := agentHookShellCommand(family.agent.Executable(), family.agent.Quote)
			document, changed, err := family.agent.MergeSettings(path, shellCommand)
			if err != nil {
				return err
			}
			if dryRun {
				_, err := cmd.OutOrStdout().Write(document)
				return err
			}
			if !changed {
				return shared.WriteFormat(cmd.OutOrStdout(), "%s: pre-tool-use guard already registered\n", path)
			}
			if err := family.agent.WriteSettings(path, document); err != nil {
				return err
			}
			return shared.WriteFormat(cmd.OutOrStdout(), "%s: pre-tool-use guard registered\n%s\n", path, shellCommand)
		},
	}
	command.Flags().StringVar(&settingsPath, "settings", "", "settings file to update (default ~/.claude/settings.json)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print the merged settings document instead of writing it")
	return command
}

// agentHookShellCommand is the exact string a settings file must run.
//
// The two suffixes are the fail-open guarantee, and they are not decoration.
// Claude Code blocks a tool call when a PreToolUse hook exits 2 and uses its
// stderr as the reason. WB already spends exit 2 on usage errors, so a WB too
// old to know this subcommand — or any typo in the settings file — would exit
// 2 and refuse every tool call on the machine, quoting cobra's help text.
// Discarding stderr and forcing exit 0 removes that channel entirely, leaving
// the JSON document on stdout as the only way this hook can ever say no.
func agentHookShellCommand(executable string, quote func(string) string) string {
	return fmt.Sprintf("%s %s 2>/dev/null; exit 0", quote(executable), agentHookInvocation)
}

const agentHookInvocation = "hooks agent pre-tool-use"
