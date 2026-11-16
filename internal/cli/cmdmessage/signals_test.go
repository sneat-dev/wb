package cmdmessage

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

func TestMessageSignalScopePreservesParentContextWhenCommandIsReused(t *testing.T) {
	t.Parallel()
	parent := context.Background()
	var first context.Context
	calls := 0
	command := withSignals(&cobra.Command{RunE: func(command *cobra.Command, _ []string) error {
		calls++
		if err := command.Context().Err(); err != nil {
			t.Fatalf("new invocation inherited cancelled scope: %v", err)
		}
		if calls == 1 {
			first = command.Context()
		}
		return nil
	}})
	command.SetContext(parent)
	for range 2 {
		if err := command.RunE(command, nil); err != nil {
			t.Fatal(err)
		}
		if command.Context() != parent {
			t.Fatal("parent context not restored")
		}
	}
	if !errors.Is(first.Err(), context.Canceled) {
		t.Fatal("signal scope leaked after completion")
	}
}

func TestMessageSignalScopeHonorsExistingCallerCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := withSignals(&cobra.Command{RunE: func(command *cobra.Command, _ []string) error { return command.Context().Err() }})
	command.SetContext(ctx)
	if err := command.RunE(command, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
