package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
)

// runCommand is this package's retrying, timeout-bounded external-command
// call: most of internal/orchestrate's git and gh invocations -- across
// engine.go, pr_create.go, worktree_merge.go and every neighbouring file --
// go through it. It runs run.RunOpts (spec/plans/coverage-to-100 task-17's
// migration of this package's own exec site) rather than exec.CommandContext
// directly, so every caller's git/gh traffic goes through task-24's runtime
// guard once run is production's runner.New() -- a unit test that reaches a
// migrated call site substitutes a runnertest.Fake instead (via each
// entry point's resolveRunner() accessor), or names itself on
// runnertest.AllowRealProcess when it deliberately exercises real git or a
// fake gh on PATH.
//
// It calls RunOpts with an explicit console.Env(), rather than plain Run,
// because the exec.CommandContext call this replaced always set
// command.Env = console.Env() itself (the non-interactive settings
// -- nonInteractiveChildEnv, GIT_SSH_COMMAND -- every direct git/gh exec
// site in this repository sets by hand): Run's own default is an
// unmodified inherited environment (matching os/exec.Cmd's own default
// when Env is nil), which is the right default for a generic runner but
// not byte-identical to what this specific call site did before migrating
// (coverage-to-100 rule 1).
//
// run.RunOpts reports stdout and stderr separately, unlike the
// exec.CommandContext().CombinedOutput() this replaced; output concatenates
// them in that order, the same approximation pr_land_keep.go's buildAt
// already uses for a non-git-port runner call. Every command this package
// runs through runCommand writes its meaningful result to only one of the
// two streams (its argv-derived value to stdout on success, its diagnostic
// text to stderr on failure), so the concatenation reproduces the same
// bytes CombinedOutput returned for every existing call site.
func runCommand(ctx context.Context, run runner.Runner, timeout time.Duration, retry int, dir, name string, args ...string) (string, int, error) {
	attempts := 0
	for {
		attempts++
		attemptCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		result, err := run.RunOpts(attemptCtx, dir, runner.RunOptions{Env: console.Env()}, name, args...)
		output := result.Stdout + result.Stderr
		timedOut := attemptCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			err = fmt.Errorf("timed out after %s", timeout)
		}
		if err == nil || attempts > retry {
			if err != nil {
				return output, attempts, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(output))
			}
			return output, attempts, nil
		}
	}
}

func lastNonEmptyLine(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}
