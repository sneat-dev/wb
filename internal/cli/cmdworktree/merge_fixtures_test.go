package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"os"
	"testing"
)

func mergeTestInvocation(flags shared.Flags) *mergeInvocation {
	return &mergeInvocation{runtime: shared.Runtime{Flags: func() shared.Flags { return flags }, ExitError: func(code int, message string) error { return errors.New(message) }}, operations: DefaultMergeOperations(), hostLoad: defaultMergeHostLoad(), bindings: MergeBindings{
		Admission: func(_ *cobra.Command, apply bool) (worktrees.AgentIdentity, func(), error) {
			if apply {
				panic("native admission compound belongs to root")
			}
			return worktrees.AgentIdentity{}, func() {}, nil
		},
		Initiator: func(*cobra.Command) string { return "" }, Discovery: func(*cobra.Command, string) {}, Quiet: func(*cobra.Command) {}, Landing: func(c *cobra.Command, _ string) *cobra.Command { return c },
		RefusePaths: landingcontext.CheckWorktrees, RefuseReceipt: landingcontext.CheckReceipt,
		LaneRequest: func(root, command, reason string, takeOver bool) orchestrate.LaneGuardRequest {
			return landingcontext.LaneRequest(root, command, reason, takeOver, os.Getpid())
		},
		ReleaseLane: func(root string, r orchestrate.WorktreeMergeReceipt) {
			landingcontext.ReleaseWorktreeLane(root, r, os.Getpid())
		}, CheckoutUpdated: func(out io.Writer) func(context.Context, orchestrate.CheckoutUpdate) {
			return landingcontext.CheckoutUpdated(out, lifecyclehooks.Dispatch)
		},
	}}
}
func mergeExecuteOriginal(t *testing.T, _ string, build func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	command := build()
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetContext(context.Background())
	command.SetArgs(args)
	command.SilenceErrors = true
	command.SilenceUsage = true
	err := command.Execute()
	return out.String(), errOut.String(), err
}

func cwWtMergeSaturateHost(t *testing.T) {
	t.Helper()
	t.Setenv(hostload.EnvLoadFloor, "1")
	previous := hostload.System
	hostload.System = func() (float64, error) { return 10000, nil }
	t.Cleanup(func() { hostload.System = previous })
}
func cwWtMergeHostloadReader(t *testing.T, reader hostload.Reader) {
	t.Helper()
	previous := hostload.System
	hostload.System = reader
	t.Cleanup(func() { hostload.System = previous })
}

type cwWtMergeAckConstructor struct {
	name  string
	build func() *cobra.Command
	args  func(string) []string
}
type mergeOriginalFailWriter struct{ Allow, Writes int }

var errMergeOriginalWrite = errors.New("cwWt: injected write failure")

func (w *mergeOriginalFailWriter) Write(b []byte) (int, error) {
	if w.Writes >= w.Allow {
		return 0, errMergeOriginalWrite
	}
	w.Writes++
	return len(b), nil
}

func withHostLoad(t *testing.T, load float64) {
	t.Helper()
	t.Setenv(hostload.EnvLoadFloor, "4")
	previous := hostload.System
	hostload.System = func() (float64, error) { return load, nil }
	t.Cleanup(func() { hostload.System = previous })
}
