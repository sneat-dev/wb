package herdr

import (
	"fmt"
	"regexp"
	"unicode"
)

// ValidatePromptText enforces the boundary
// spec/ideas/daemon-as-coordinator.md calls "the newline is the authority
// boundary": herdr submits a prompt by sending the text followed by its own
// encoded Enter, so a newline embedded in text would let a caller smuggle
// an extra, unreviewed line of input into the founder's session. This
// package refuses any text containing a newline, carriage return, or other
// C0/C1 control character, so [Client.AgentPrompt] can only ever submit
// exactly the one line the caller passed. [Client.PaneSendText] — literal
// text into a pane, advisory rather than submitted — reuses this same
// check: a control character has no legitimate reason to be in either.
func ValidatePromptText(text string) error {
	if text == "" {
		return fmt.Errorf("%w: prompt text is empty", ErrInvalidPromptText)
	}
	for _, r := range text {
		switch {
		case r == '\n' || r == '\r':
			return fmt.Errorf("%w: prompt text contains a newline", ErrInvalidPromptText)
		case unicode.IsControl(r):
			return fmt.Errorf("%w: prompt text contains control character %U", ErrInvalidPromptText, r)
		}
	}
	return nil
}

// keyNamePattern matches herdr's own logical key names, such as "esc" and
// "ctrl+c" (see --skill: "Use logical keys for interactive agent UI
// controls"). It intentionally rejects anything herdr's own send-keys
// vocabulary would not recognize, rather than passing an arbitrary string
// through to a live pane.
var keyNamePattern = regexp.MustCompile(`^[A-Za-z0-9]+(\+[A-Za-z0-9]+)*$`)

// ValidateKeyName reports whether key is an explicit herdr key name:
// non-empty, and built only from alphanumeric segments joined by "+", as
// "esc" and "ctrl+c" are. [Client.AgentSendKeys] never sends a key this
// rejects.
func ValidateKeyName(key string) error {
	if key == "" {
		return fmt.Errorf("%w: key name is empty", ErrInvalidKeyName)
	}
	if !keyNamePattern.MatchString(key) {
		return fmt.Errorf("%w: %q is not an explicit key name", ErrInvalidKeyName, key)
	}
	return nil
}
