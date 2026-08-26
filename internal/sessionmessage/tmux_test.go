package sessionmessage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"testing"
)

type tmuxRun struct {
	args   []string
	stdin  []byte
	stdout []byte
	err    error
}

type scriptedTmuxRunner struct {
	runs []tmuxRun
	next int
}

func (runner *scriptedTmuxRunner) Run(_ context.Context, executable string, args []string, stdin []byte, stdout, _ io.Writer) error {
	if executable != "/usr/bin/tmux" || runner.next >= len(runner.runs) {
		return fmt.Errorf("unexpected tmux invocation %q %#v", executable, args)
	}
	run := &runner.runs[runner.next]
	runner.next++
	run.args = append([]string(nil), args...)
	run.stdin = append([]byte(nil), stdin...)
	_, _ = stdout.Write(run.stdout)
	return run.err
}

func TestOSTmuxUsesOnlyFixedArgvAndExactStdinBufferBytes(t *testing.T) {
	raw := []byte("{\n  \"kind\": \"request_handoff\",\n  \"body\": \"; $(touch /tmp/nope)\"\n}\n")
	runner := &scriptedTmuxRunner{runs: []tmuxRun{
		{stdout: []byte("wb-session-wbs-successor\t%7\t1234\n")},
		{},
		{stdout: raw},
		{},
		{},
		{},
	}}
	client := &osTmux{executable: "/usr/bin/tmux", runner: runner}
	pane, err := client.Inspect(context.Background(), "wb-session-wbs-successor")
	if err != nil || pane != (Pane{SessionName: "wb-session-wbs-successor", ID: "%7", PID: 1234, Count: 1}) {
		t.Fatalf("Inspect = %#v, err=%v", pane, err)
	}
	if err := client.LoadBuffer(context.Background(), "wb-message-message-123", raw); err != nil {
		t.Fatal(err)
	}
	verified, err := client.SaveBuffer(context.Background(), "wb-message-message-123")
	if err != nil || !bytes.Equal(verified, raw) {
		t.Fatalf("SaveBuffer = %q, err=%v", verified, err)
	}
	if err := client.PasteBuffer(context.Background(), "wb-message-message-123", "%7"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteBuffer(context.Background(), "wb-message-message-123"); err != nil {
		t.Fatal(err)
	}
	if err := client.Submit(context.Background(), "%7"); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"list-panes", "-s", "-t", "=wb-session-wbs-successor", "-F", "#{session_name}\t#{pane_id}\t#{pane_pid}"},
		{"load-buffer", "-b", "wb-message-message-123", "-"},
		{"save-buffer", "-b", "wb-message-message-123", "-"},
		{"paste-buffer", "-p", "-r", "-b", "wb-message-message-123", "-t", "%7"},
		{"delete-buffer", "-b", "wb-message-message-123"},
		{"send-keys", "-t", "%7", "Enter"},
	}
	for index, args := range want {
		if !reflect.DeepEqual(runner.runs[index].args, args) {
			t.Errorf("call %d args = %#v, want %#v", index, runner.runs[index].args, args)
		}
		if index == 1 {
			if !bytes.Equal(runner.runs[index].stdin, raw) {
				t.Errorf("load-buffer stdin changed: %q", runner.runs[index].stdin)
			}
		} else if len(runner.runs[index].stdin) != 0 {
			t.Errorf("call %d received unexpected stdin", index)
		}
		for _, arg := range args {
			if bytes.Contains(raw, []byte(arg)) && arg != "-" {
				t.Fatalf("message content leaked into argv %q", arg)
			}
		}
	}
}

func TestOSTmuxRefusesToSubmitToNonCanonicalPaneIdentity(t *testing.T) {
	client := &osTmux{executable: "/usr/bin/tmux", runner: &scriptedTmuxRunner{}}
	if err := client.Submit(context.Background(), "$(touch /tmp/nope)"); err == nil {
		t.Fatal("Submit accepted a non-canonical pane identity")
	}
}

func TestOSTmuxRefusesMoreThanOnePane(t *testing.T) {
	runner := &scriptedTmuxRunner{runs: []tmuxRun{{stdout: []byte("wb-session-wbs-successor\t%7\t1234\nwb-session-wbs-successor\t%8\t1235\n")}}}
	client := &osTmux{executable: "/usr/bin/tmux", runner: runner}
	if _, err := client.Inspect(context.Background(), "wb-session-wbs-successor"); err == nil {
		t.Fatal("Inspect accepted more than one pane")
	}
}

func TestOSTmuxRefusesNonCanonicalPaneIdentity(t *testing.T) {
	runner := &scriptedTmuxRunner{runs: []tmuxRun{{stdout: []byte("wb-session-wbs-successor\t%--evil\t1234\n")}}}
	client := &osTmux{executable: "/usr/bin/tmux", runner: runner}
	if _, err := client.Inspect(context.Background(), "wb-session-wbs-successor"); err == nil {
		t.Fatal("Inspect accepted a non-canonical pane identity")
	}
}
