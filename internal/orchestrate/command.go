package orchestrate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// runCommand is this package's retrying, timeout-bounded external-command
// call: most of internal/orchestrate's git and gh invocations -- across
// engine.go, pr_create.go, worktree_merge.go and every neighbouring file --
// go through it, not just this task's five target files. It keeps calling
// exec.CommandContext directly rather than routing through
// orchestrateRunner (spec/plans/coverage-to-100 task-17): unlike this
// file's single, isolated exec site, migrating this one shared call would
// route the whole package's git/gh traffic through task-24's runtime guard
// at once, and every existing test across the package that exercises any
// of those call sites with real git would need its own
// runnertest.AllowRealProcess or an e2e-tier move in the same PR. That is
// judged out of scope for this migration PR; this file's exec_sites.pending
// entry (internal/quality/testdata/exec_sites.pending) stays at 1 pending a
// follow-up that budgets for the wider test fallout.
func runCommand(ctx context.Context, timeout time.Duration, retry int, dir, name string, args ...string) (string, int, error) {
	attempts := 0
	for {
		attempts++
		attemptCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		command := exec.CommandContext(attemptCtx, name, args...)
		command.Dir = dir
		command.Env = console.Env()
		output, err := command.CombinedOutput()
		timedOut := attemptCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			err = fmt.Errorf("timed out after %s", timeout)
		}
		if err == nil || attempts > retry {
			if err != nil {
				return string(output), attempts, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
			}
			return string(output), attempts, nil
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
