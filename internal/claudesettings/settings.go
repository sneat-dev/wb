package claudesettings

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EntryPresent reports whether one hooks-list entry already carries
// a handler running shellCommand. It only inspects the "hooks" handlers, not
// "matcher", so it applies to any hook event's entry shape — SessionStart's
// entries carry no matcher at all (see skills_hook_install.go).
func EntryPresent(entry any, shellCommand string) bool {
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

func Encode(settings map[string]any) ([]byte, error) {
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// MergeSessionStart returns the settings document with the
// SessionStart hook registered, and reports whether anything changed.
//
// Decoded into generic maps, the same as mergeAgentHookSettings, so any key
// this command has never heard of -- including every other hook event --
// survives the round trip untouched.
func MergeSessionStart(path, shellCommand string) ([]byte, bool, error) {
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
		// EntryPresent (hooks_agent.go) only inspects each entry's
		// "hooks" handlers for a matching command, so it applies to any hook
		// event's entry shape, not just PreToolUse's.
		if EntryPresent(entry, shellCommand) {
			encoded, err := Encode(settings)
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
	encoded, err := Encode(settings)
	return encoded, true, err
}
