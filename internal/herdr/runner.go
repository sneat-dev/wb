package herdr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	procrunner "github.com/sneat-dev/wb/internal/runner"
)

// Runner executes one herdr invocation as argv — never through a shell —
// and returns its captured stdout and stderr separately, alongside the
// process's own error (nil on exit 0). env is the exact process environment
// to run herdr with; nil means "inherit the caller's ambient environment
// unchanged" (os/exec's own default). Tests inject a fake Runner so no
// herdr call in this package's test suite ever reaches a real socket.
type Runner interface {
	Run(ctx context.Context, binary string, args []string, env []string) (stdout, stderr []byte, err error)
}

// execRunner is the production [Runner]: task-8's runner.Runner seam,
// argv only, never a shell. Runner is nil in the package's own production
// construction site (execRunner{} in client.go), which resolveRunner
// defaults to the production runner.Runner; a unit test injects
// runnertest.Fake instead of setting this field.
type execRunner struct {
	Runner procrunner.Runner
}

func (r execRunner) Run(ctx context.Context, binary string, args []string, env []string) ([]byte, []byte, error) {
	result, err := resolveRunner(r.Runner).RunOpts(ctx, "", procrunner.RunOptions{Env: env}, binary, args...)
	return []byte(result.Stdout), []byte(result.Stderr), err
}

// resolveRunner defaults r to the production runner.Runner when the caller
// left it unset.
func resolveRunner(r procrunner.Runner) procrunner.Runner {
	if r != nil {
		return r
	}
	return procrunner.New()
}

// osEnviron is os.Environ, seamed so a test can prove [buildEnv] overrides
// an ambient HERDR_SOCKET_PATH without needing a real subprocess.
var osEnviron = os.Environ

// envSession and envClientSocketPath are herdr environment variables this
// package never reads for its own [Identity] but must still scrub from a
// targeted Client's subprocess environment (see [buildEnv]): herdr itself
// consults them to resolve "the current session"/"the current client
// socket", the same ambient-identity role HERDR_PANE_ID etc. play.
const (
	envSession          = "HERDR_SESSION"
	envClientSocketPath = "HERDR_CLIENT_SOCKET_PATH"
)

// ambientIdentityEnvVars are every herdr environment variable that resolves
// "current"/ambient identity — which pane, tab, workspace, named session,
// or client socket a bare herdr invocation implicitly means. A client
// explicitly targeting a different socket or session (via [WithSocketPath]
// or [WithSessionName]) must never let these leak in from its own ambient
// environment: `herdr pane current`, for instance, resolves its target
// from HERDR_PANE_ID, so an unscrubbed ambient value would silently
// resolve against the calling process's own local pane on a server that is
// not the one the caller explicitly asked for, where IDs collide (herdr
// --skill: "IDs and live agent names are scoped to one server").
var ambientIdentityEnvVars = []string{
	envSocketPath,
	envPaneID,
	envTabID,
	envWorkspaceID,
	envSession,
	envClientSocketPath,
}

// buildEnv returns the environment one herdr invocation should run with.
// targeted is true once a [Client] has been configured with
// [WithSocketPath] or [WithSessionName]. When not targeted, it returns
// nil — "inherit the ambient environment unchanged", today's single-server
// default. When targeted, it returns an explicit environment: the ambient
// environment with every var in [ambientIdentityEnvVars] removed, then
// HERDR_SOCKET_PATH re-added when socketPath is set. Scrubbing (rather
// than only overriding HERDR_SOCKET_PATH) is what stops a targeted client
// from resolving "current" against its own ambient pane on the wrong
// server, and from herdr silently preferring an ambient named session over
// the one this Client asked for.
func buildEnv(ambient []string, targeted bool, socketPath string) []string {
	if !targeted {
		return nil
	}
	strip := make(map[string]bool, len(ambientIdentityEnvVars))
	for _, key := range ambientIdentityEnvVars {
		strip[key] = true
	}
	filtered := make([]string, 0, len(ambient)+1)
	for _, entry := range ambient {
		key, _, _ := strings.Cut(entry, "=")
		if strip[key] {
			continue
		}
		filtered = append(filtered, entry)
	}
	if socketPath != "" {
		filtered = append(filtered, envSocketPath+"="+socketPath)
	}
	return filtered
}

// withSessionFlag prepends herdr's global `--session <name>` flag ahead of
// a subcommand's own args, so every call a [Client] makes targets the same
// named persistent session when one is configured via [WithSessionName].
// An empty sessionName returns args unchanged.
func withSessionFlag(sessionName string, args []string) []string {
	if sessionName == "" {
		return args
	}
	prefixed := make([]string, 0, len(args)+2)
	prefixed = append(prefixed, "--session", sessionName)
	return append(prefixed, args...)
}

// ResolveBinary finds the herdr executable: HERDR_BIN_PATH (read through
// lookup) first, then PATH. It never runs the resolved binary; callers
// learn whether it actually works from their first real call. A configured
// HERDR_BIN_PATH that does not exist on disk — stale after an uninstall or
// a move — is not trusted blindly: ResolveBinary falls back to PATH
// instead, the same as an unset HERDR_BIN_PATH.
func ResolveBinary(lookup EnvLookup) (string, error) {
	if lookup == nil {
		return "", fmt.Errorf("%w: no environment lookup was provided", ErrBinaryNotFound)
	}
	if configured, ok := lookup(envBinPath); ok && configured != "" {
		switch _, statErr := statPath(configured); {
		case statErr == nil:
			return configured, nil
		case isStaleBinaryPath(statErr):
			// A HERDR_BIN_PATH that no longer exists — an uninstall or a
			// move — is treated the same as an unset one: fall through to
			// PATH rather than handing back a path nothing can execute.
		default:
			return "", fmt.Errorf("%w: HERDR_BIN_PATH %q: %w", ErrBinaryNotFound, configured, statErr)
		}
	}
	resolved, err := lookPath("herdr")
	if err != nil {
		return "", fmt.Errorf("%w: %s not found on PATH: %w", ErrBinaryNotFound, "herdr", err)
	}
	return resolved, nil
}

// lookPath is exec.LookPath, seamed for runner_test.go; production callers
// always get the real PATH search. It is a var, not a wrapper function,
// only so a test can restore it exactly with defer.
var lookPath = exec.LookPath

// statPath is os.Stat, seamed for runner_test.go so a stale-HERDR_BIN_PATH
// test does not depend on real filesystem timing or permissions beyond
// "the file is absent".
var statPath = os.Stat

// isStaleBinaryPath reports whether err is the "the configured path does
// not exist" case ResolveBinary falls back from, as opposed to some other
// stat failure worth surfacing differently in the future.
func isStaleBinaryPath(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// isExecNotFound reports whether err is the kind of failure ResolveBinary
// itself would have already caught — a binary that vanished between
// resolution and use — so [Client] can still map a late failure to
// [ErrBinaryNotFound] instead of a generic error.
func isExecNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}
