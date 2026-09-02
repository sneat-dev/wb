package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newSkillsHookInstallCmd() *cobra.Command {
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
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("locate the home directory: %w", err)
				}
				path = filepath.Join(home, claudeSettingsRelativePath)
			}
			shellCommand := skillsHookShellCommand(hookExecutable())
			document, changed, err := mergeSkillsHookSettings(path, shellCommand)
			if err != nil {
				return err
			}
			if dryRun {
				_, err := cmd.OutOrStdout().Write(document)
				return err
			}
			if !changed {
				return writeFormat(cmd.OutOrStdout(), "%s: SessionStart hook already registered\n", path)
			}
			if err := writeSettingsAtomically(path, document); err != nil {
				return err
			}
			return writeFormat(cmd.OutOrStdout(), "%s: SessionStart hook registered\n%s\n", path, shellCommand)
		},
	}
	command.Flags().StringVar(&settingsPath, "settings", "", "settings file to update (default ~/.claude/settings.json)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print the merged settings document instead of writing it")
	return command
}

// mergeSkillsHookSettings returns the settings document with the
// SessionStart hook registered, and reports whether anything changed.
//
// Decoded into generic maps, the same as mergeAgentHookSettings, so any key
// this command has never heard of -- including every other hook event --
// survives the round trip untouched.
func mergeSkillsHookSettings(path, shellCommand string) ([]byte, bool, error) {
	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil && len(strings.TrimSpace(string(raw))) > 0:
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, false, fmt.Errorf("parse %s: %w", path, err)
		}
	case err != nil && !os.IsNotExist(err):
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	hooksField, _ := settings["hooks"].(map[string]any)
	if hooksField == nil {
		hooksField = map[string]any{}
	}
	entries, _ := hooksField["SessionStart"].([]any)
	for _, entry := range entries {
		// agentHookEntryPresent (hooks_agent.go) only inspects each entry's
		// "hooks" handlers for a matching command, so it applies to any hook
		// event's entry shape, not just PreToolUse's.
		if agentHookEntryPresent(entry, shellCommand) {
			encoded, err := encodeSettings(settings)
			return encoded, false, err
		}
	}
	entries = append(entries, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": shellCommand,
			"timeout": 10,
		}},
	})
	hooksField["SessionStart"] = entries
	settings["hooks"] = hooksField
	encoded, err := encodeSettings(settings)
	return encoded, true, err
}
