package streamrun_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/cmdstream"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Genuine command-to-domain provenance guarantees retain private archival effects.
func TestStreamWorkLogRefusals(t *testing.T) {
	root := t.TempDir()
	execute := func(flags ...string) (worktrees.WorkLogOptions, error) {
		var prepared worktrees.WorkLogOptions
		agentMode := false
		cmd := cmdstream.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }, ExitError: func(_ int, message string) error { return errors.New(message) }}, cmdstream.Dependencies{RegisteredSession: func() bool { return false }, PrepareWorkLog: worktrees.PrepareWorkLogOptions, Start: func(_ context.Context, request streamrun.Creation, _ streams.StartOptions) (streams.StartResult, error) {
			prepared = request.WorkLog
			agentMode = request.SessionRequired
			return streams.StartResult{}, nil
		}})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetIn(strings.NewReader("the exact task request\n"))
		cmd.SetArgs(append([]string{"start", "cw", "acme/app"}, flags...))
		err := cmd.Execute()
		if err == nil && agentMode {
			t.Error("manual mode was reported as agent mode")
		}
		return preparedAfterExecute(err, &prepared)
	}
	for _, test := range []struct {
		flags []string
		want  string
	}{{[]string{"--mode", "telepathy"}, "unsupported execution mode"}, {[]string{"--mode", "manual"}, "requires --initiator"}, {[]string{"--mode", "agent", "--agent", "agent-1"}, "agent-mode stream creation requires a live registered session"}, {[]string{"--mode", "manual", "--initiator", "me@example.com", "--model", "unknown"}, "prompt"}} {
		if _, err := execute(test.flags...); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%v error=%v", test.flags, err)
		}
	}
	prepared, err := execute("--mode", "manual", "--initiator", "me@example.com", "--model", "unknown", "--original-prompt-file", "-")
	if err != nil {
		t.Fatalf("manual stdin prompt: %v", err)
	}
	if prepared.OriginalPrompt == "" {
		t.Error("the archived prompt is empty")
	}
}
func preparedAfterExecute(err error, prepared *worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
	return *prepared, err
}
