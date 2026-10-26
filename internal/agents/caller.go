package agents

import (
	"fmt"
	"strings"
	"unicode"
)

// SafeCallerID returns only short, non-sensitive identifier-shaped metadata.
// It is used for harness environment values and the remote dispatch boundary.
func SafeCallerID(raw string) string {
	id := strings.TrimSpace(raw)
	if len(id) == 0 || len(id) > 128 {
		return ""
	}
	for _, char := range id {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.' {
			continue
		}
		return ""
	}
	return id
}

// Validate constrains caller metadata before another machine persists it.
func (caller CallerSession) Validate() error {
	if caller.ID == "" {
		if caller == (CallerSession{}) {
			return nil
		}
		return fmt.Errorf("remote caller session has metadata without a session_id")
	}
	for _, field := range []struct{ name, value string }{
		{"session_id", caller.ID},
		{"wb_session_id", caller.WBSessionID},
		{"native_harness_id", caller.NativeHarnessID},
		{"runtime", caller.Runtime},
		{"source", caller.Source},
	} {
		if field.value != "" && SafeCallerID(field.value) != field.value {
			return fmt.Errorf("remote caller %s is not a safe identifier", field.name)
		}
	}
	if len(caller.Name) > 128 || strings.IndexFunc(caller.Name, unicode.IsControl) >= 0 {
		return fmt.Errorf("remote caller name is too long or contains control characters")
	}
	return nil
}
