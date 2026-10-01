package fleet

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Every Git command the snapshotter runs goes through gitOutput, so one place
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

// cappedBuffer collects output up to max bytes and refuses the rest, so an
// oversized output is never held in full.
type cappedBuffer struct {
	data     bytes.Buffer
	max      int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > b.max {
		b.exceeded = true
		return 0, errGitOutputTooLarge
	}
	return b.data.Write(p)
}

// gitOutput runs the Git binary with args in dir and returns its standard
// output, up to maxGitOutput bytes.
func gitOutput(ctx context.Context, binary, dir string, args ...string) ([]byte, error) {
	return gitOutputLimited(ctx, binary, dir, maxGitOutput, args...)
}

// gitOutputLimited is gitOutput with an output cap: past limit bytes the
// command is stopped and errGitOutputTooLarge returned, with nothing beyond
// the cap ever buffered. The command runs in its own process group, which is
// killed when ctx ends, and its pipes are abandoned after gitWaitDelay, so a
// descendant that holds stdout open cannot keep the call from returning.
func gitOutputLimited(ctx context.Context, binary, dir string, limit int, args ...string) ([]byte, error) {
	return runCapped(ctx, binary, gitEnvironment(os.Environ()), limit, gitArguments(dir, args))
}

// runCapped runs binary with args and env, in its own process group, and
// returns its standard output, up to limit bytes: past it the command is
// stopped and errGitOutputTooLarge returned. The group is killed when ctx ends,
// and the pipes are abandoned after gitWaitDelay. Standard input is empty and
// standard error is discarded, so what a command prints to it, which can carry
// a path, never comes back. A non-zero exit is an exitError.
func runCapped(ctx context.Context, binary string, env []string, limit int, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = env
	killWithDescendants(command)
	command.WaitDelay = gitWaitDelay
	out := &cappedBuffer{max: limit}
	command.Stdout = out
	if err := command.Run(); err != nil {
		if out.exceeded {
			return nil, errGitOutputTooLarge
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, exitError{code: exit.ExitCode()}
		}
		return nil, errGit
	}
	return out.data.Bytes(), nil
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
