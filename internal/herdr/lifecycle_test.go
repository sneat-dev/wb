package herdr

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type lifecycleRunner struct {
	t     *testing.T
	want  []string
	call  fakeCall
	calls int
}

func (r *lifecycleRunner) Run(_ context.Context, _ string, argv, _ []string) ([]byte, []byte, error) {
	r.t.Helper()
	r.calls++
	if !reflect.DeepEqual(argv, r.want) || r.calls > 1 {
		r.t.Fatalf("unexpected command arguments: %#v", argv)
	}
	return r.call.stdout, r.call.stderr, r.call.err
}

func TestWorkspaceCreateBindsTheReturnedRootPane(t *testing.T) {
	t.Parallel()
	for _, item := range []struct {
		name, reply string
		valid       bool
	}{
		{"created", `{"type":"workspace_created","workspace":{"workspace_id":"w4"},"root_pane":{"pane_id":"w4:p1","workspace_id":"w4"}}`, true},
		{"foreign-pane", `{"type":"workspace_created","workspace":{"workspace_id":"w4"},"root_pane":{"pane_id":"w5:p1","workspace_id":"w5"}}`, false},
		{"missing-workspace", `{"type":"workspace_created","root_pane":{"pane_id":"w4:p1","workspace_id":"w4"}}`, false},
		{"wrong-reply", `{"type":"workspace_get","workspace":{"workspace_id":"w4"},"root_pane":{"pane_id":"w4:p1","workspace_id":"w4"}}`, false},
		{"malformed", `{`, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			runner := &lifecycleRunner{t: t, want: []string{"workspace", "create", "--cwd", "/tmp/owned work", "--label", "wb-owned", "--no-focus"}, call: fakeCall{stdout: []byte(`{"result":` + item.reply + `}`)}}
			created, err := newTestClient(t, runner).WorkspaceCreate(t.Context(), "/tmp/owned work", "wb-owned")
			if item.valid {
				if err != nil || created.WorkspaceID != "w4" || created.Pane.PaneID != "w4:p1" {
					t.Fatalf("created = %#v, error = %v", created, err)
				}
			} else if !errors.Is(err, ErrUnparseableOutput) {
				t.Fatalf("untrusted reply accepted: %#v, %v", created, err)
			}
		})
	}
}

func TestWorkspaceCreateRejectsUnsafeInputsBeforeIO(t *testing.T) {
	t.Parallel()
	for _, input := range [][2]string{{"relative", "owned"}, {"/tmp/../tmp/owned", "owned"}, {"/tmp/owned", ""}, {"/tmp/owned", strings.Repeat("x", 129)}, {"/tmp/owned\nother", "owned"}, {"/tmp/owned", "split\u2028line"}} {
		runner := &lifecycleRunner{t: t}
		if _, err := newTestClient(t, runner).WorkspaceCreate(t.Context(), input[0], input[1]); err == nil || runner.calls != 0 {
			t.Fatalf("unsafe creation admitted: %#v, %v", input, err)
		}
	}
}

func TestPaneExecQuotesEveryArgumentAsOneShellCommand(t *testing.T) {
	t.Parallel()
	want := "exec '/tmp/WB helper' '' 'path with spaces' 'single'\"'\"'quote' '$HOME;$(touch /tmp/unsafe)' '`touch /tmp/unsafe`'"
	runner := &lifecycleRunner{t: t, want: []string{"pane", "run", "w4:p1", want}}
	err := newTestClient(t, runner).PaneExec(t.Context(), "w4:p1", "/tmp/WB helper", []string{"", "path with spaces", "single'quote", "$HOME;$(touch /tmp/unsafe)", "`touch /tmp/unsafe`"})
	if err != nil || runner.calls != 1 {
		t.Fatalf("PaneExec error = %v, calls = %d", err, runner.calls)
	}
}

func TestPaneExecRejectsControlCharactersAndBoundsBeforeIO(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"\n", "\r", "\x00", "\x1b", "\u0085", "\u2028", "\u2029", "\u202e", strings.Repeat("x", 128<<10)} {
		runner := &lifecycleRunner{t: t}
		if err := newTestClient(t, runner).PaneExec(t.Context(), "w4:p1", "/tmp/wb", []string{value}); err == nil || runner.calls != 0 {
			t.Fatalf("unsafe command admitted, error = %v", err)
		}
	}
	for _, executable := range []string{"wb", "/tmp/../tmp/wb", "/tmp/wb\n"} {
		runner := &lifecycleRunner{t: t}
		if err := newTestClient(t, runner).PaneExec(t.Context(), "w4:p1", executable, nil); err == nil || runner.calls != 0 {
			t.Fatalf("unsafe executable admitted: %q", executable)
		}
	}
}

func TestPaneProcessInfoRejectsForeignAndContradictoryProcesses(t *testing.T) {
	t.Parallel()
	for _, item := range []struct {
		name, reply string
		valid       bool
	}{
		{"owned", `{"type":"pane_process_info","process_info":{"pane_id":"w4:p1","shell_pid":12,"foreground_processes":[{"pid":42,"name":"wb","argv":["/tmp/wb","--fixed"]}]}}`, true},
		{"foreign", `{"type":"pane_process_info","process_info":{"pane_id":"w9:p1"}}`, false},
		{"duplicate-pid", `{"type":"pane_process_info","process_info":{"pane_id":"w4:p1","foreground_processes":[{"pid":42,"name":"wb"},{"pid":42,"name":"other"}]}}`, false},
		{"invalid-pid", `{"type":"pane_process_info","process_info":{"pane_id":"w4:p1","foreground_processes":[{"pid":0,"name":"wb"}]}}`, false},
		{"missing-name", `{"type":"pane_process_info","process_info":{"pane_id":"w4:p1","foreground_processes":[{"pid":42}]}}`, false},
		{"malformed", `{`, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			runner := &lifecycleRunner{t: t, want: []string{"pane", "process-info", "--pane", "w4:p1"}, call: fakeCall{stdout: []byte(`{"result":` + item.reply + `}`)}}
			info, err := newTestClient(t, runner).PaneProcessInfo(t.Context(), "w4:p1")
			if item.valid {
				if err != nil || len(info.Foreground) != 1 || info.Foreground[0].PID != 42 || info.Foreground[0].Argv[1] != "--fixed" {
					t.Fatalf("process info = %#v, error = %v", info, err)
				}
			} else if !errors.Is(err, ErrUnparseableOutput) {
				t.Fatalf("untrusted process evidence accepted: %#v, %v", info, err)
			}
		})
	}
}

func TestPrivateHerdrCommandFailuresDoNotExposePayload(t *testing.T) {
	t.Parallel()
	const payload = "PRIVATE_COMMAND_SENTINEL"
	for _, reply := range []fakeCall{
		{stdout: []byte(payload)},
		{stderr: []byte(payload), err: errors.New("failed")},
		{stderr: []byte(`{"error":{"code":"` + payload + `","message":"` + payload + `"}}`), err: errors.New("failed")},
	} {
		runner := &lifecycleRunner{t: t, want: []string{"pane", "run", "w4:p1", "exec '/tmp/wb' '" + payload + "'"}, call: reply}
		err := newTestClient(t, runner).PaneExec(t.Context(), "w4:p1", "/tmp/wb", []string{payload})
		if err == nil || strings.Contains(err.Error(), payload) {
			t.Fatalf("private command diagnostic = %v", err)
		}
	}
}

// This is an actual shell-boundary proof, not an assertion that duplicates
// quoting implementation. It runs only in the Linux verification environment.
func TestPaneExecRoundTripsLiteralArgumentsThroughTheShell(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("owned shell execution belongs to Linux verification")
	}
	directory := t.TempDir()
	helper := filepath.Join(directory, "argv 'capture' helper")
	output := filepath.Join(directory, "captured")
	sentinel := filepath.Join(directory, "must-not-exist")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$WB_ARGV_CAPTURE\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"", "with spaces", "single'quote", "$(touch " + sentinel + ")", "`touch " + sentinel + "`", "; touch " + sentinel, "$HOME"}
	runner := &captureLifecycleRunner{}
	if err := newTestClient(t, runner).PaneExec(t.Context(), "w4:p1", helper, arguments); err != nil {
		t.Fatal(err)
	}
	if len(runner.argv) != 4 || !reflect.DeepEqual(runner.argv[:3], []string{"pane", "run", "w4:p1"}) {
		t.Fatalf("unexpected herdr command: %#v", runner.argv)
	}
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", runner.argv[3])
	command.Env = append(os.Environ(), "WB_ARGV_CAPTURE="+output)
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("owned shell boundary: %v: %s", err, raw)
	}
	raw, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(raw, []byte(strings.Join(arguments, "\x00")+"\x00")) {
		t.Fatalf("argv was changed by the shell: %q, error %v", raw, err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell injection produced a side effect: %v", err)
	}
}

type captureLifecycleRunner struct{ argv []string }

func (r *captureLifecycleRunner) Run(_ context.Context, _ string, argv, _ []string) ([]byte, []byte, error) {
	r.argv = append([]string(nil), argv...)
	return nil, nil, nil
}

func TestPaneMutationsRequireExactEmptySuccessAcknowledgment(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{"", " ", "{}", `{"result":{"type":"ok"}}`, "PRIVATE_UNEXPECTED_REPLY"} {
		t.Run(reply, func(t *testing.T) {
			for _, operation := range []string{"run", "send-text", "send-keys"} {
				runner := &lifecycleRunner{t: t, want: []string{"pane", operation, "w1:p1", "literal"}, call: fakeCall{stdout: []byte(reply)}}
				err := newTestClient(t, runner).callEmptyAck(t.Context(), "pane", operation, "w1:p1", "literal")
				if (err != nil) != (reply != "") {
					t.Fatalf("mutation acknowledgment %q: %v", reply, err)
				}
				if err != nil && strings.Contains(err.Error(), "PRIVATE_UNEXPECTED_REPLY") {
					t.Fatal("private mutation output leaked")
				}
			}
		})
	}
	if err := newTestClient(t, &lifecycleRunner{t: t}).callEmptyAck(t.Context(), "workspace", "create"); err == nil {
		t.Fatal("JSON-bearing command accepted empty-ack route")
	}
}
