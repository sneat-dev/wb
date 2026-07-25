// Package console reports whether wb is attached to a human terminal.
//
// wb is driven by AI agents and CI at least as often as by people. Neither has
// a terminal, neither can answer a prompt, and both give up after a deadline,
// so a command that waits for input it will never receive is indistinguishable
// from a crash. Every interactive affordance — live progress UIs, draining
// stdin, anything that assumes a human is watching — must therefore be gated
// on a real terminal rather than assumed, and must degrade to plain,
// terminating output when there is none.
package console

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// EnvDisable names the environment variable that forces non-interactive
// behavior even when a terminal is present. Agents that allocate a pty (so
// that TTY detection alone would report a human) set it to opt out.
const EnvDisable = "WB_NON_INTERACTIVE"

// IsTerminal reports whether stream is an open terminal device.
//
// It accepts any value so that callers holding an io.Reader or io.Writer can
// ask without a type switch of their own. Anything that is not an *os.File — a
// pipe, a bytes.Buffer, a test double, a nil interface — is never a terminal,
// which is the safe answer: it makes callers choose the non-blocking path.
func IsTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok || file == nil {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

// Disabled reports whether the environment forbids interactive behavior.
// Any value other than an explicit false ("0", "false") counts as set, so
// WB_NON_INTERACTIVE=1 and WB_NON_INTERACTIVE=true both work.
func Disabled() bool { return disabled(os.Getenv(EnvDisable)) }

func disabled(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return true
	}
	return parsed
}

// Interactive reports whether wb may use a terminal-only affordance on the
// given output stream: the stream must be a terminal, the caller must not have
// asked for non-interactive mode, and the environment must not forbid it.
func Interactive(out any, forced bool) bool {
	if forced || Disabled() {
		return false
	}
	return IsTerminal(out)
}

// nonInteractiveChildEnv is the environment wb imposes on every git, gh, and go
// subprocess so that none of them can stop and wait for a human.
//
// Each entry closes a hang that redirecting stdin does not: git and ssh ask for
// credentials, passphrases, and host-key confirmation on /dev/tty directly, and
// both git and gh page their output through less, which waits for a keypress
// that will never come. Failing fast with a diagnostic is always better than
// blocking forever with none.
var nonInteractiveChildEnv = []string{
	"GIT_TERMINAL_PROMPT=0", // git fails instead of asking for a username or password
	"GIT_PAGER=cat",         // never hand output to a pager that waits for a keypress
	"GH_PAGER=cat",
	"GH_PROMPT_DISABLED=1", // gh fails instead of opening an interactive picker
	"GH_NO_UPDATE_NOTIFIER=1",
}

// sshBatchCommand makes ssh report an error rather than prompt for a passphrase
// or an unknown host key. BatchMode alone is enough: it turns every prompt into
// an immediate failure without weakening host-key verification.
const sshBatchCommand = "GIT_SSH_COMMAND=ssh -o BatchMode=yes"

// CommandEnv returns base extended with the settings above.
//
// A GIT_SSH_COMMAND already present in base is left untouched: the user may
// have configured a specific key or proxy there, and silently replacing it
// would break their setup in a way that looks like an authentication failure.
func CommandEnv(base []string) []string {
	environment := make([]string, 0, len(base)+len(nonInteractiveChildEnv)+1)
	environment = append(environment, base...)
	environment = append(environment, nonInteractiveChildEnv...)
	if !hasEnv(base, "GIT_SSH_COMMAND") {
		environment = append(environment, sshBatchCommand)
	}
	return environment
}

// Env returns the current process environment with the same settings applied.
func Env() []string { return CommandEnv(os.Environ()) }

func hasEnv(environment []string, name string) bool {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}
