//go:build !darwin && !linux

package process

import (
	"context"
	"os"
	"os/exec"
)

// commandContext preserves normal exec semantics on platforms where WB has no
// process-group implementation. WB's supported local runners are Darwin and
// Linux, both of which use command_unix.go.
func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func commandContextInteractive(ctx context.Context, _ bool, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// trackStart/trackStop/signalLiveGroups are no-ops here: there is no
// process-group primitive to register or signal. Windows has no Setsid
// equivalent wired up by this package, so it relies on console's ssh
// BatchMode injection (see internal/console) as its only guard against an
// interactive prompt from a wb-started child -- unchanged by this file.
func trackStart(int) uint64      { return 0 }
func trackStop(uint64)           {}
func signalLiveGroups(os.Signal) {}
