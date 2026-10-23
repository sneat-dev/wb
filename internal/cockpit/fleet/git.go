package fleet

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// Every Git command the snapshotter runs goes through readGit, so one place
// decides what a repository's own configuration and the daemon's environment
// can make Git do. A repository is untrusted input: its .git/config can name a
// filesystem monitor, a hooks directory, an ssh command or a promisor remote,
// and the daemon's environment can carry GIT_DIR or GIT_CONFIG_*.

// gitWaitDelay is how long a killed Git command's output pipes are waited for,
// so a descendant that kept stdout open cannot hold Run.
const gitWaitDelay = time.Second

// errGit is what a failed Git command returns: its own message can carry a
// path, so the caller gets none. errGitOutputTooLarge says the output passed
// its cap.
var (
	errGit               = errors.New("git command failed")
	errGitOutputTooLarge = errors.New("git output passed its cap")
)

// exitError is errGit with the command's exit status, so a caller can tell
// "not found" (a status Git documents) from a failure. It carries no text.
type exitError struct{ code int }

func (e exitError) Error() string        { return errGit.Error() }
func (e exitError) Is(target error) bool { return target == errGit }

// maxGitOutput caps the output of a Git command that has no cap of its own.
const maxGitOutput = 64 << 20

// The oldest Git whose GIT_NO_LAZY_FETCH is honoured (2.45); an older one
// would fetch a missing object from a promisor remote.
const (
	minGitMajor = 2
	minGitMinor = 45
)

// protocols are the transports each switched off by name, because a
// repository's own protocol.<name>.allow would otherwise outrank the general
// setting for that transport.
var protocols = []string{"ext", "file", "git", "ssh", "http", "https"}

// gitEnvironment is the environment of a Git command, built from an
// allow-list of parent's variables (PATH, HOME, TMPDIR, LANG and LC_*) and
// nothing else, so GIT_DIR, GIT_WORK_TREE, GIT_CONFIG_*, GIT_NAMESPACE and the
// like never pass through, plus the settings that keep Git read-only, local
// and non-interactive, and that ignore replacement refs.
func gitEnvironment(parent []string) []string {
	var env []string
	for _, variable := range parent {
		name, _, _ := strings.Cut(variable, "=")
		if name == "PATH" || name == "HOME" || name == "TMPDIR" || name == "LANG" || strings.HasPrefix(name, "LC_") {
			env = append(env, variable)
		}
	}
	return append(env,
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
	)
}

// gitArguments puts the hardening options before args: no transport protocol
// is allowed (each one by name too, since command-line settings outrank the
// repository's own), and the filesystem monitor, hooks and filter advertising
// of the repository's own configuration are switched off.
func gitArguments(dir string, args []string) []string {
	hardened := []string{
		"--no-optional-locks", "-C", dir,
		"-c", "protocol.allow=never", "-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null", "-c", "uploadpack.allowFilter=false",
	}
	for _, protocol := range protocols {
		hardened = append(hardened, "-c", "protocol."+protocol+".allow=never")
	}
	return append(hardened, args...)
}

// readGit runs the Git binary with args in dir, through run, and returns its
// standard output, up to maxGitOutput bytes. A nil run is the real runner.
func readGit(ctx context.Context, run runner.Runner, binary, dir string, args ...string) ([]byte, error) {
	return gitOutputLimited(ctx, run, binary, dir, maxGitOutput, args...)
}

// gitOutputLimited is readGit with an output cap: past limit bytes the
// command is stopped and errGitOutputTooLarge returned, with nothing beyond
// the cap ever buffered. On darwin and linux the command runs in its own
// process group, which ctx ending stops with SIGTERM, a 250 ms grace and then
// SIGKILL (other systems kill the process only). Its pipes are abandoned after
// gitWaitDelay, and a descendant that keeps stdout open after the command has
// exited cleanly does not fail the call.
func gitOutputLimited(ctx context.Context, run runner.Runner, binary, dir string, limit int, args ...string) ([]byte, error) {
	return runCapped(ctx, run, binary, gitEnvironment(os.Environ()), limit, gitArguments(dir, args))
}

// orReal is run, or the real runner when run is nil.
func orReal(run runner.Runner) runner.Runner {
	if run == nil {
		return runner.New()
	}
	return run
}

// errCommandMissing says the command could not be started because it is not
// there (or may not be run). It is errGit too, so a Git caller needs no
// distinction; the code-index provider reads it as "unavailable".
var errCommandMissing = errors.New("command not found")

type commandMissingError struct{}

func (commandMissingError) Error() string { return errGit.Error() }
func (commandMissingError) Is(target error) bool {
	return target == errGit || target == errCommandMissing
}

// runCapped runs binary with args and env through run (the real runner when
// nil), and returns its standard output, up to limit bytes: past it the command
// is stopped and errGitOutputTooLarge returned. A limit that is not positive is
// refused (the runner reads zero as uncapped). On darwin and linux the runner
// puts the command in its own process group, ended with SIGTERM, a 250 ms grace
// and then SIGKILL when ctx ends (other systems kill the process only), and
// abandons the pipes after gitWaitDelay; a descendant that keeps stdout open
// after a clean exit does not fail the call. Standard input is empty and
// standard error is discarded, so what a command prints to it, which can carry
// a path, never comes back. A call whose ctx ended is errGit whatever the
// command exited with: a child that trapped the signal and exited 1 must not
// read as Git's "no". Otherwise a non-zero exit is an exitError.
func runCapped(ctx context.Context, run runner.Runner, binary string, env []string, limit int, args []string) ([]byte, error) {
	if limit <= 0 {
		return nil, errGit
	}
	result, err := orReal(run).RunOpts(ctx, "", runner.RunOptions{
		Env: env, WaitDelay: gitWaitDelay, StdoutLimit: limit, DiscardStderr: true,
	}, binary, args...)
	switch {
	case err == nil:
		return []byte(result.Stdout), nil
	case ctx.Err() != nil:
		return nil, errGit
	case errors.Is(err, runner.ErrOutputTooLarge):
		return nil, errGitOutputTooLarge
	case result.ExitCode != 0:
		return nil, exitError{code: result.ExitCode}
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission):
		return nil, commandMissingError{}
	}
	return nil, errGit
}

// gitVersionUsable reports whether the output of `git version` names a Git of
// at least 2.45.
func gitVersionUsable(output string) bool {
	fields := strings.Fields(output)
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return false
	}
	parts := strings.SplitN(fields[2], ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return majorErr == nil && minorErr == nil && (major > minGitMajor || (major == minGitMajor && minor >= minGitMinor))
}
