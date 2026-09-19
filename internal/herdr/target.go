package herdr

import (
	"fmt"
	"strings"
)

// ValidateTarget reports whether target — a pane id, agent id, or other
// value a [Client] method passes to herdr as a positional argv argument —
// is safe to send: non-empty, and not starting with "-". kind labels the
// argument in the returned error ("agent target", "pane id").
//
// herdr's CLI parser has no `--` end-of-options separator to escape a
// leading hyphen: confirmed live, 2026-09-19, via the read-only
// `agent get` (safety.md permits it; `agent prompt`/`send-keys` do not) —
// `herdr agent get -- x` exits 2 with a usage error rather than treating
// "x" as the target. A target argument beginning with "-" therefore risks
// being parsed as an unrecognized flag instead of positional text, the
// same argv-shape risk [ValidatePromptText] guards against for the text
// argument. Every Client method that passes a target or pane id as argv —
// AgentGet, AgentRead, AgentPrompt, AgentSendKeys, AgentWait, PaneGet,
// PaneSendText — validates it with this function before invoking herdr.
func ValidateTarget(kind, target string) error {
	if target == "" {
		return fmt.Errorf("%w: %s is empty", ErrUnknownTarget, kind)
	}
	if strings.HasPrefix(target, "-") {
		return fmt.Errorf("%w: %s %q starts with \"-\"; herdr's CLI has no -- separator to escape it", ErrUnknownTarget, kind, target)
	}
	return nil
}
