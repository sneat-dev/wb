package herdr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// execRunner is the production [Runner]: os/exec, argv only.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args []string, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// osEnviron is os.Environ, seamed so a test can prove [buildEnv] overrides
// an ambient HERDR_SOCKET_PATH without needing a real subprocess.
var osEnviron = os.Environ

// buildEnv returns the environment one herdr invocation should run with.
// When socketPath is empty it returns nil — "inherit the ambient
// environment unchanged", today's single-server default. When socketPath
// is set, it returns an explicit environment: the ambient environment with
// any existing HERDR_SOCKET_PATH entry removed and the configured one
// appended, so a caller that names a socket explicitly — as a daemon
// coordinating more than one herdr session must — never silently falls
// back to whatever socket the ambient process happened to have.
func buildEnv(ambient []string, socketPath string) []string {
	if socketPath == "" {
		return nil
	}
	prefix := envSocketPath + "="
	filtered := make([]string, 0, len(ambient)+1)
	for _, entry := range ambient {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, prefix+socketPath)
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
// learn whether it actually works from their first real call.
func ResolveBinary(lookup EnvLookup) (string, error) {
	if lookup == nil {
		return "", fmt.Errorf("%w: no environment lookup was provided", ErrBinaryNotFound)
	}
	if configured, ok := lookup(envBinPath); ok && configured != "" {
		return configured, nil
	}
	resolved, err := lookPath("herdr")
	if err != nil {
		return "", fmt.Errorf("%w: %s not found on PATH: %w", ErrBinaryNotFound, "herdr", err)
	}
	return resolved, nil
}

// lookPath is exec.LookPath, seamed for env_os_test.go; production callers
// always get the real PATH search. It is a var, not a wrapper function,
// only so a test can restore it exactly with defer.
var lookPath = exec.LookPath

// isExecNotFound reports whether err is the kind of failure ResolveBinary
// itself would have already caught — a binary that vanished between
// resolution and use — so [Client] can still map a late failure to
// [ErrBinaryNotFound] instead of a generic error.
func isExecNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}
