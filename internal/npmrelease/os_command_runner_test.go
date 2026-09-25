package npmrelease

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestOSCommandRunnerRunUsesAnInjectedRunner covers OSCommandRunner.Run
// against a scripted runner.Runner (the resolveRunner "r != nil" branch),
// including that stdout and stderr are concatenated into Output.
func TestOSCommandRunnerRunUsesAnInjectedRunner(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"npm", "view", "pkg"}, runner.Result{Stdout: "out\n", Stderr: "warn\n"}, nil)

	result := OSCommandRunner{Runner: fake}.Run(context.Background(), "/repo", "npm", "view", "pkg")
	if result.Err != nil || result.Code != 0 || result.Output != "out\nwarn\n" {
		t.Fatalf("result = %+v", result)
	}
}

// TestOSCommandRunnerRunReportsAnUncodedFailureAsExitOne covers the branch
// where the runner's own error does not implement ExitCode (unlike
// *os/exec.ExitError) -- for example, a guard refusal or a start failure.
func TestOSCommandRunnerRunReportsAnUncodedFailureAsExitOne(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"npm", "view", "pkg"}, runner.Result{}, errors.New("exec: \"npm\": executable file not found in $PATH"))

	result := OSCommandRunner{Runner: fake}.Run(context.Background(), "/repo", "npm", "view", "pkg")
	if result.Err == nil || result.Code != 1 {
		t.Fatalf("result = %+v, want code 1 for an error without ExitCode", result)
	}
}

// codedError implements exitCoder, standing in for what *os/exec.ExitError
// carries after a real child exits non-zero, without starting one.
type codedError struct{ code int }

func (e codedError) Error() string { return "exit status" }
func (e codedError) ExitCode() int { return e.code }

// TestOSCommandRunnerRunReportsACodedFailuresExitCode covers the branch
// where the runner's own error does implement ExitCode -- the shape
// *os/exec.ExitError takes after a real child exits non-zero, exercised
// here through a scripted error rather than a real process (the real
// process integration itself is internal/runner's own contract, proven by
// that package's tests).
func TestOSCommandRunnerRunReportsACodedFailuresExitCode(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"npm", "view", "pkg"}, runner.Result{Stderr: "bad\n"}, codedError{code: 7})

	result := OSCommandRunner{Runner: fake}.Run(context.Background(), "/repo", "npm", "view", "pkg")
	if result.Err == nil || result.Code != 7 || !strings.Contains(result.Output, "bad") {
		t.Fatalf("result = %+v, want exit 7 with the captured output", result)
	}
}
