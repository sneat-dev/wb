package cmdmessage

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

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

func TestMessageInputCancellationUnblocksStalledPipeWithoutWaitingForEOF(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"send", "--local", "--from", "vm", "--to", "mac", "--message-file", "-"},
		{"deliver"},
	} {
		t.Run(args[0], func(t *testing.T) {
			t.Parallel()
			reader, writer := io.Pipe()
			defer func() { _ = reader.Close() }()
			defer func() { _ = writer.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			command := New(bindingRuntime(), Dependencies{})
			command.SetContext(ctx)
			command.SetIn(reader)
			command.SetArgs(args)
			command.SilenceErrors = true
			command.SilenceUsage = true
			done := make(chan error, 1)
			go func() { done <- command.Execute() }()
			// An unclosed producer contributes input before cancellation, proving
			// the command has entered its bounded reader rather than an early guard.
			if _, err := writer.Write([]byte("{")); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("command remained blocked on stdin")
			}
		})
	}
}

func TestMessageInputDoesNotReadWhenCallerIsAlreadyCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readInput(ctx, nil, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
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
