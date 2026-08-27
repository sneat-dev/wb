package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/spf13/cobra"
)

// newHooksAgentCmd groups the hooks that an AI coding agent runs around its
// own tool calls, as opposed to the Git hooks the rest of `wb hooks` installs.
//
// They sit under the same noun because they answer the same question at
// different moments: Git hooks judge a commit, agent hooks judge the write
// that would have produced it. The agent layer is the one that matters for a
// canonical clone, because a clone can be ruined without ever reaching a
// commit.
func newHooksAgentCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "Hooks an AI coding agent runs around its own tool calls",
	}
	command.AddCommand(newHooksAgentPreToolUseCmd())
	command.AddCommand(newHooksAgentInstallCmd())
	return command
}

const agentHookInvocation = "hooks agent pre-tool-use"

// agentHookShellCommand is the exact string a settings file must run.
//
// The two suffixes are the fail-open guarantee, and they are not decoration.
// Claude Code blocks a tool call when a PreToolUse hook exits 2 and uses its
// stderr as the reason. WB already spends exit 2 on usage errors, so a WB too
// old to know this subcommand — or any typo in the settings file — would exit
// 2 and refuse every tool call on the machine, quoting cobra's help text.
// Discarding stderr and forcing exit 0 removes that channel entirely, leaving
// the JSON document on stdout as the only way this hook can ever say no.
func agentHookShellCommand(executable string) string {
	return fmt.Sprintf("%s %s 2>/dev/null; exit 0", shellQuote(executable), agentHookInvocation)
}

func shellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\"'\\$`*?[]{}();&|<>#~!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func newHooksAgentPreToolUseCmd() *cobra.Command {
	var inputPath string
	command := &cobra.Command{
		Use:   "pre-tool-use",
		Short: "Judge one agent tool call before it runs (reads a PreToolUse payload on stdin)",
		Long: `Refuse an agent tool call that would write into a canonical clone.

Reads a Claude Code PreToolUse payload as JSON on stdin and writes a deny
document on stdout when the call would write inside <projects-root>/<owner>/
<repository>. It writes nothing at all for every other outcome.

This command fails open without exception. An unreadable payload, an
unrecognised tool, a shell construct it cannot model, a path it cannot
resolve, and an internal panic all produce silence, which Claude Code reads as
"proceed". It runs ahead of every tool call of every agent on the machine, so
a guard that could fail closed would be a worse defect than the one it exists
to prevent.

It never blocks a read, and never blocks a linked worktree — including a
worktree nested inside a canonical clone. In a canonical clone it leaves the
operations a clone exists for alone: git fetch, git merge --ff-only, git
status, git log, git show, git ls-tree, and every other read.

Install it with 'wb hooks agent install'.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := cmd.InOrStdin()
			if inputPath != "" && inputPath != "-" {
				file, err := os.Open(inputPath)
				if err != nil {
					// Even an explicitly named payload that cannot be read is
					// an allow: this command has exactly one safe failure.
					return nil
				}
				defer func() { _ = file.Close() }()
				reader = file
			}
			decision := agentguard.Inspect(
				agentguard.DecodeToolCall(reader),
				agentguard.Options{ProjectsRoot: projectsRoot},
			)
			if _, err := agentguard.WriteDecision(cmd.OutOrStdout(), decision); err != nil {
				return nil
			}
			return nil
		},
	}
	command.Flags().StringVar(&inputPath, "input", "", "read the payload from a file instead of stdin (for testing)")
	return command
}

// claudeSettingsRelativePath is where a user-level Claude Code settings file
// lives.
var claudeSettingsRelativePath = filepath.Join(".claude", "settings.json")

func newHooksAgentInstallCmd() *cobra.Command {
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
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("locate the home directory: %w", err)
				}
				path = filepath.Join(home, claudeSettingsRelativePath)
			}
			shellCommand := agentHookShellCommand(hookExecutable())
			document, changed, err := mergeAgentHookSettings(path, shellCommand)
			if err != nil {
				return err
			}
			if dryRun {
				_, err := cmd.OutOrStdout().Write(document)
				return err
			}
			if !changed {
				return writeFormat(cmd.OutOrStdout(), "%s: pre-tool-use guard already registered\n", path)
			}
			if err := writeSettingsAtomically(path, document); err != nil {
				return err
			}
			return writeFormat(cmd.OutOrStdout(), "%s: pre-tool-use guard registered\n%s\n", path, shellCommand)
		},
	}
	command.Flags().StringVar(&settingsPath, "settings", "", "settings file to update (default ~/.claude/settings.json)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print the merged settings document instead of writing it")
	return command
}

const agentHookMatcher = "Bash|Write|Edit|MultiEdit|NotebookEdit"

// mergeAgentHookSettings returns the settings document with the guard
// registered, and reports whether anything changed.
//
// The document is decoded into generic maps rather than a typed struct so an
// unrelated key WB has never heard of survives the round trip untouched. A
// settings file is the user's, not WB's.
func mergeAgentHookSettings(path, shellCommand string) ([]byte, bool, error) {
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

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks["PreToolUse"].([]any)
	for _, entry := range entries {
		if agentHookEntryPresent(entry, shellCommand) {
			encoded, err := encodeSettings(settings)
			return encoded, false, err
		}
	}
	entries = append(entries, map[string]any{
		"matcher": agentHookMatcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": shellCommand,
			"timeout": 10,
		}},
	})
	hooks["PreToolUse"] = entries
	settings["hooks"] = hooks
	encoded, err := encodeSettings(settings)
	return encoded, true, err
}

func agentHookEntryPresent(entry any, shellCommand string) bool {
	object, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	handlers, _ := object["hooks"].([]any)
	for _, handler := range handlers {
		handlerObject, ok := handler.(map[string]any)
		if !ok {
			continue
		}
		if command, ok := handlerObject["command"].(string); ok && command == shellCommand {
			return true
		}
	}
	return false
}

func encodeSettings(settings map[string]any) ([]byte, error) {
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// writeSettingsAtomically replaces the settings file through a temporary file
// in the same directory, so an interrupted write can never leave the user
// without a settings file.
func writeSettingsAtomically(path string, document []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".wb-settings-*")
	if err != nil {
		return fmt.Errorf("stage a replacement for %s: %w", path, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.Write(document); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("secure %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
