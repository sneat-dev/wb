package cmdskills

import (
	"fmt"

	"path/filepath"

	"github.com/spf13/cobra"
)

func newSkillsHookInstallCmd(deps HookDependencies) *cobra.Command {
	var settingsPath string
	var dryRun bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Register the SessionStart hook in a Claude Code settings file",
		Long: `Add WB's SessionStart hook to a Claude Code settings file.

Merges one SessionStart entry into the settings document, preserving every
other key and every other hook -- the same merge 'wb hooks agent install'
already uses for PreToolUse. Re-running it changes nothing once the entry is
present, so it is safe to run from any automation.

wb never edits this file on its own outside this explicit subcommand.

Defaults to ~/.claude/settings.json. Use --dry-run to print the merged
document without writing it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := settingsPath
			if path == "" {
				home, err := deps.Home()
				if err != nil {
					return fmt.Errorf("locate the home directory: %w", err)
				}
				path = filepath.Join(home, ".claude", "settings.json")
			}
			shellCommand := skillsHookShellCommand(deps.Executable(), deps.Quote)
			document, changed, err := deps.MergeSettings(path, shellCommand)
			if err != nil {
				return err
			}
			if dryRun {
				_, err := cmd.OutOrStdout().Write(document)
				return err
			}
			if !changed {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: SessionStart hook already registered\n", path)
				return err
			}
			if err := deps.WriteSettings(path, document); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: SessionStart hook registered\n%s\n", path, shellCommand)
			return err
		},
	}
	command.Flags().StringVar(&settingsPath, "settings", "", "settings file to update (default ~/.claude/settings.json)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print the merged settings document instead of writing it")
	return command
}
