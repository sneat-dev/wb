package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// smokeDeadline bounds every probe below. It is generous enough that a slow
// shared CI runner never trips it, and short enough that a genuine hang fails
// the suite instead of running until the whole job is killed.
const smokeDeadline = 60 * time.Second

var (
	smokeBinary    string
	smokeBuildErr  error
	smokeBuildOnce = make(chan struct{}, 1)
)

// buildWB compiles the real wb binary once per test run.
//
// These probes deliberately exercise the built command rather than calling run
// in-process: the failure they guard against is "the program produces no output
// and never exits", which only a separate process with its own stdio can
// observe honestly.
func buildWB(t *testing.T) string {
	t.Helper()
	smokeBuildOnce <- struct{}{}
	defer func() { <-smokeBuildOnce }()
	if smokeBinary != "" || smokeBuildErr != nil {
		if smokeBuildErr != nil {
			t.Fatalf("build wb: %v", smokeBuildErr)
		}
		return smokeBinary
	}
	directory, err := os.MkdirTemp("", "wb-smoke-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	binary := filepath.Join(directory, "wb")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		smokeBuildErr = errors.New(string(output))
		t.Fatalf("build wb: %v", smokeBuildErr)
	}
	smokeBinary = binary
	return smokeBinary
}

type smokeResult struct {
	stdout, stderr string
	exitCode       int
}

// runWB executes the built binary the way a script or an AI agent does: no
// terminal on any stream, stdin already at end of file, and a hard deadline.
// It fails the test — rather than blocking the suite — if the process has to be
// killed, because that is precisely the defect being guarded against.
func runWB(t *testing.T, args ...string) smokeResult {
	t.Helper()
	return runWBIn(t, "", args...)
}

// runWBIn is runWB with an explicit working directory. Commands that default
// to "." must be exercised from a scratch directory: run from the suite's own
// cwd they would operate on the wb checkout itself.
func runWBIn(t *testing.T, dir string, args ...string) smokeResult {
	t.Helper()
	binary := buildWB(t)

	ctx, cancel := context.WithTimeout(context.Background(), smokeDeadline)
	defer cancel()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = dir
	command.Stdin = devNull // never a terminal, and already at EOF
	command.Stdout = &stdout
	command.Stderr = &stderr
	// A terminal-detecting command must see no terminal here, and must not be
	// nudged into interactive mode by the developer's own environment. The git
	// identity keeps `wb repo init-remote` from depending on whether the
	// machine running the suite has one configured.
	command.Env = append(os.Environ(), "TERM=dumb",
		"GIT_AUTHOR_NAME=wb-test", "GIT_AUTHOR_EMAIL=wb-test@example.com",
		"GIT_COMMITTER_NAME=wb-test", "GIT_COMMITTER_EMAIL=wb-test@example.com")

	started := time.Now()
	runErr := command.Run()
	elapsed := time.Since(started)

	if ctx.Err() != nil {
		t.Fatalf("wb %s did not exit within %s (killed after %s); it produced %d bytes on stdout and %d on stderr. "+
			"A command that blocks with no output is unusable by any non-interactive caller.",
			strings.Join(args, " "), smokeDeadline, elapsed, stdout.Len(), stderr.Len())
	}

	result := smokeResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		result.exitCode = 0
	case errors.As(runErr, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("wb %s: %v", strings.Join(args, " "), runErr)
	}
	return result
}

// TestHelpAndVersionAlwaysAnswerWithoutATerminal is the regression test for the
// defect that made wb unusable: `wb --help` printed nothing and never exited.
// Every probe here must terminate well inside the deadline, succeed, and put
// something on stdout — with no terminal attached to any stream.
func TestHelpAndVersionAlwaysAnswerWithoutATerminal(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"help"},
		{},          // bare `wb` must describe itself, not exit silently
		{"version"}, // the founder's build rejected every spelling of this
		{"--version"},
		{"version", "--json"},
	} {
		t.Run(strings.Join(append([]string{"wb"}, args...), "_"), func(t *testing.T) {
			t.Parallel()
			result := runWB(t, args...)
			if result.exitCode != exitOK {
				t.Errorf("exit code = %d, want %d; stderr: %s", result.exitCode, exitOK, result.stderr)
			}
			if strings.TrimSpace(result.stdout) == "" {
				t.Errorf("stdout was empty; stderr: %s", result.stderr)
			}
		})
	}
}

// TestEverySubcommandHelpTerminates walks the whole command tree, because a
// hang or a silent exit in one rarely-used subcommand is just as fatal to an
// agent as one in the root.
func TestEverySubcommandHelpTerminates(t *testing.T) {
	t.Parallel()
	for _, path := range subcommandPaths(newRootCmd(), nil) {
		t.Run(strings.Join(path, "_"), func(t *testing.T) {
			t.Parallel()
			result := runWB(t, append(append([]string{}, path...), "--help")...)
			if result.exitCode != exitOK {
				t.Errorf("wb %s --help: exit code = %d, want %d; stderr: %s",
					strings.Join(path, " "), result.exitCode, exitOK, result.stderr)
			}
			if strings.TrimSpace(result.stdout) == "" {
				t.Errorf("wb %s --help printed nothing to stdout; stderr: %s", strings.Join(path, " "), result.stderr)
			}
		})
	}
}

// TestUnknownFlagFailsFastWithUsageExitCode pins the contract an agent branches
// on: a rejected invocation exits 2, says so on stderr, and leaves stdout clean.
func TestUnknownFlagFailsFastWithUsageExitCode(t *testing.T) {
	t.Parallel()
	result := runWB(t, "--no-such-flag")
	if result.exitCode != exitUsage {
		t.Errorf("exit code = %d, want %d (usage); stderr: %s", result.exitCode, exitUsage, result.stderr)
	}
	if !strings.Contains(result.stderr, "no-such-flag") {
		t.Errorf("stderr does not name the rejected flag: %q", result.stderr)
	}
	if strings.TrimSpace(result.stdout) != "" {
		t.Errorf("a failed invocation wrote to stdout, which would corrupt a pipe: %q", result.stdout)
	}
}

// TestUnknownCommandFailsFastWithUsageExitCode covers the other half of the
// usage contract and checks the error points at how to recover.
func TestUnknownCommandFailsFastWithUsageExitCode(t *testing.T) {
	t.Parallel()
	result := runWB(t, "definitely-not-a-command")
	if result.exitCode != exitUsage {
		t.Errorf("exit code = %d, want %d (usage); stderr: %s", result.exitCode, exitUsage, result.stderr)
	}
	if !strings.Contains(result.stderr, "wb --help") {
		t.Errorf("stderr does not tell the caller how to recover: %q", result.stderr)
	}
}

// TestVersionJSONIsParseable makes the machine-readable spelling load-bearing:
// an agent should never have to scrape the human version banner.
func TestVersionJSONIsParseable(t *testing.T) {
	t.Parallel()
	result := runWB(t, "version", "--json")
	if result.exitCode != exitOK {
		t.Fatalf("exit code = %d; stderr: %s", result.exitCode, result.stderr)
	}
	for _, key := range []string{`"version"`, `"go"`, `"platform"`} {
		if !strings.Contains(result.stdout, key) {
			t.Errorf("version JSON is missing %s: %s", key, result.stdout)
		}
	}
}

// TestJSONReportsAreCleanOnStdout is the piping contract. A directory that is
// not a repository is a guaranteed finding, so this exercises the failure path
// specifically: stdout must still be nothing but the JSON document, the human
// explanation must be on stderr, and the exit code must say "findings" rather
// than "usage" so an agent does not retry a perfectly valid invocation.
func TestJSONReportsAreCleanOnStdout(t *testing.T) {
	t.Parallel()
	result := runWB(t, "status", t.TempDir(), "--format", "json")

	if result.exitCode != exitFindings {
		t.Errorf("exit code = %d, want %d (findings); stderr: %s", result.exitCode, exitFindings, result.stderr)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &document); err != nil {
		t.Fatalf("stdout is not a single JSON document: %v\nstdout: %s", err, result.stdout)
	}
	if _, ok := document["repositories"]; !ok {
		t.Errorf("JSON report is missing `repositories`: %s", result.stdout)
	}
	if strings.TrimSpace(result.stderr) == "" {
		t.Error("a failing run explained nothing on stderr")
	}
}

// subcommandPaths returns the argument path of every runnable subcommand,
// skipping cobra's generated help and completion trees.
func subcommandPaths(command *cobra.Command, prefix []string) [][]string {
	var paths [][]string
	for _, child := range command.Commands() {
		name := child.Name()
		if name == "help" || name == "completion" {
			continue
		}
		path := append(append([]string{}, prefix...), name)
		paths = append(paths, path)
		paths = append(paths, subcommandPaths(child, path)...)
	}
	return paths
}
