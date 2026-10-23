package quality

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/process"
	"strings"
	"time"
)

// runCommandAttempts keeps one retry/deadline policy for ordinary and covered commands.
func runCommandAttempts(ctx context.Context, timeout time.Duration, retry int, execute func(context.Context) (string, error)) (string, int, error) {
	attempts := 0
	for {
		attempts++
		attemptCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		output, err := execute(attemptCtx)
		timedOut := attemptCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			err = preserveNativeCoverageFailure(err, fmt.Errorf("timed out after %s", timeout))
		}
		if err == nil || isNativeCoverageFailure(err) || attempts > retry || ctx.Err() != nil {
			return output, attempts, err
		}
	}
}

// runStdoutWithEnv keeps discovery diagnostics separate with explicit per-command inputs.
func runStdoutWithEnv(ctx context.Context, env []string, dir, name string, args ...string) (string, error) {
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = commandEnv(dir, name, env)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(output), err
}
