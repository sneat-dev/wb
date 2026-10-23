package claudesettings

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const AgentMatcher = "Bash|Write|Edit|MultiEdit|NotebookEdit|Agent|Task"

// MergeAgentHook returns the settings document with the guard
// registered, and reports whether anything changed.
//
// The document is decoded into generic maps rather than a typed struct so an
// unrelated key WB has never heard of survives the round trip untouched. A
// settings file is the user's, not WB's.
func MergeAgentHook(path, shellCommand string) ([]byte, bool, error) {
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
		if !EntryPresent(entry, shellCommand) {
			continue
		}
		if !agentHookEntryMatcherStale(entry) {
			encoded, err := Encode(settings)
			return encoded, false, err
		}
		// The command is already registered but under an older, narrower
		// matcher — e.g. one predating the Agent/Task policies. Widening the
		// matcher in place, rather than appending a second entry, is what
		// makes `wb hooks agent install` idempotent across a policy rollout:
		// a fleet that already ran install once picks up the wider coverage
		// the next time it runs install again, with no duplicate hook.
		entry.(map[string]any)["matcher"] = AgentMatcher
		hooks["PreToolUse"] = entries
		settings["hooks"] = hooks
		encoded, err := Encode(settings)
		return encoded, true, err
	}
	entries = append(entries, map[string]any{
		"matcher": AgentMatcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": shellCommand,
			"timeout": 10,
		}},
	})
	hooks["PreToolUse"] = entries
	settings["hooks"] = hooks
	encoded, err := Encode(settings)
	return encoded, true, err
}

// agentHookEntryMatcherStale reports whether a PreToolUse entry's matcher is
// narrower than the one this install would write. Call only after
// claudesettings.EntryPresent has confirmed the entry is WB's own.
func agentHookEntryMatcherStale(entry any) bool {
	object, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	matcher, _ := object["matcher"].(string)
	return matcher != AgentMatcher
}
