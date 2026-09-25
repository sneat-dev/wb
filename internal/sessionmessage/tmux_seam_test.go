package sessionmessage

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestExecTmuxCommandRunnerUsesTheConfiguredRunner proves
// execTmuxCommandRunner.Run reaches its configured runner.Runner through
// Stream, carrying the exact stdin bytes and the caller's own stdout/stderr
// writers, rather than always falling back to the real runner.
func TestExecTmuxCommandRunnerUsesTheConfiguredRunner(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"tmux", "list-panes"}, runner.Result{}, nil)

	var stdout, stderr bytes.Buffer
	err := execTmuxCommandRunner{Runner: fake}.Run(context.Background(), "tmux", []string{"list-panes"}, []byte("stdin-payload"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Op != "Stream" {
		t.Fatalf("Calls() = %+v, want one Stream call", calls)
	}
	if calls[0].StreamOpts.Stdout != &stdout || calls[0].StreamOpts.Stderr != &stderr {
		t.Fatal("Stream did not carry the caller's own stdout/stderr writers")
	}
	sentStdin, err := io.ReadAll(calls[0].StreamOpts.Stdin)
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	if string(sentStdin) != "stdin-payload" {
		t.Fatalf("stdin = %q, want %q", sentStdin, "stdin-payload")
	}
}

// TestExecTmuxCommandRunnerPropagatesAStreamFailure covers Run's error
// branch through the fake, without a real process.
func TestExecTmuxCommandRunnerPropagatesAStreamFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	wantErr := context.DeadlineExceeded
	fake.Expect(func(c runnertest.Call) bool { return c.Name == "tmux" }, runner.Result{}, wantErr)

	var stdout, stderr bytes.Buffer
	err := execTmuxCommandRunner{Runner: fake}.Run(context.Background(), "tmux", nil, nil, &stdout, &stderr)
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
