package deps

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

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
