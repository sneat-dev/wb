package npmrelease

import (
	"context"
	"errors"
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
