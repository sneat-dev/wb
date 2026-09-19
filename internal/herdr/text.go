package herdr

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// ValidatePromptText enforces the boundary
// spec/ideas/daemon-as-coordinator.md calls "the newline is the authority
// boundary": herdr submits a prompt by sending the text followed by its own
// encoded Enter, so a newline embedded in text would let a caller smuggle
// an extra, unreviewed line of input into the founder's session. This
// package refuses any text containing:
//
//   - a newline, carriage return, or other C0/C1 control character
//     (unicode.IsControl);
//   - a Unicode line or paragraph separator, U+2028/U+2029
//     (unicode.Zl/unicode.Zp) — visually invisible line breaks a control
//     check alone would miss;
//   - a Unicode format character (unicode.Cf) such as U+202E RIGHT-TO-LEFT
//     OVERRIDE or U+200B ZERO WIDTH SPACE, which can make submitted text
//     render differently than it reads;
//   - a leading "-", because herdr's own CLI parser has no `--`
//     end-of-options separator to escape one (`herdr agent get -- x`
//     exits 2 with a usage error rather than treating "x" as the target,
//     confirmed live 2026-09-19 via the read-only `agent get`) — a leading
//     hyphen in the text argument risks being parsed as a flag instead of
//     positional text.
//
// so [Client.AgentPrompt] can only ever submit exactly the one line the
// caller passed. [Client.PaneSendText] — literal text into a pane, advisory
// rather than submitted — reuses this same check: none of the above has a
// legitimate reason to be in either.
func ValidatePromptText(text string) error {
	if text == "" {
		return fmt.Errorf("%w: prompt text is empty", ErrInvalidPromptText)
	}
	if strings.HasPrefix(text, "-") {
		return fmt.Errorf("%w: prompt text starts with %q; herdr's CLI has no -- separator to escape it", ErrInvalidPromptText, "-")
	}
	for _, r := range text {
		switch {
		case r == '\n' || r == '\r':
			return fmt.Errorf("%w: prompt text contains a newline", ErrInvalidPromptText)
		case unicode.IsControl(r):
			return fmt.Errorf("%w: prompt text contains control character %U", ErrInvalidPromptText, r)
		case unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
			return fmt.Errorf("%w: prompt text contains a Unicode line/paragraph separator %U", ErrInvalidPromptText, r)
		case unicode.Is(unicode.Cf, r):
			return fmt.Errorf("%w: prompt text contains a Unicode format character %U", ErrInvalidPromptText, r)
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
