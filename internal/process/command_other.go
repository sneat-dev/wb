//go:build !darwin && !linux

package process

import (
	"context"
	"os/exec"
)

// commandContext preserves normal exec semantics on platforms where WB has no
// process-group implementation. WB's supported local runners are Darwin and
// Linux, both of which use command_unix.go.
func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
