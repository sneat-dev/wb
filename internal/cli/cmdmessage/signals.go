package cmdmessage

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// withSignals makes supervised message commands release their waits and stop
// their SSH children when interrupted, without replacing root pre-run policy.
func withSignals(command *cobra.Command) *cobra.Command {
	run := command.RunE
	command.RunE = func(cmd *cobra.Command, args []string) error {
		parent := cmd.Context()
		ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		defer stop()
		reset := context.AfterFunc(ctx, stop)
		defer reset()
		defer cmd.SetContext(parent)
		cmd.SetContext(ctx)
		return run(cmd, args)
	}
	return command
}

// readInput allows cancellation even when a pipe producer never closes stdin.
// The reader belongs to the caller; a buffered result lets its read finish
// without blocking after cancellation. Owned files are closed by their caller.
func readInput(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		raw []byte
		err error
	}
	ready := make(chan result, 1)
	go func() { raw, err := io.ReadAll(io.LimitReader(reader, limit)); ready <- result{raw, err} }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-ready:
		return value.raw, value.err
	}
}
