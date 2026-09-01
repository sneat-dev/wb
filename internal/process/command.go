// Package process starts bounded subprocesses with a lifecycle that owns their
// descendants as well as their direct child.
package process

import (
	"context"
	"os/exec"
)

// CommandContext returns a command whose cancellation owns the process tree on
// the platforms where WB supports process-tree cancellation. Callers configure
// its directory, environment, and stdio exactly as they would an exec.Command.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return commandContext(ctx, name, args...)
}
