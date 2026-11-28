package cmddesk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deskrun"
)

type exit struct {
	code    int
	message string
}

func (e *exit) Error() string { return e.message }

var now = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func run(t *testing.T, operations Operations, args ...string) (string, string, error) {
	t.Helper()
	if operations.IsTerminal == nil {
		operations.IsTerminal = func(any) bool { return false }
	}
	operations.Now = func() time.Time { return now }
	runtime := shared.Runtime{
		Flags:     func() shared.Flags { return shared.Flags{ProjectsRoot: "/projects"} },
		ExitError: func(code int, message string) error { return &exit{code: code, message: message} },
	}
	command := New(runtime, operations)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader("piped"))
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func code(err error) int {
	var e *exit
	if errors.As(err, &e) {
		return e.code
	}
	return 0
}

func TestConnectPassesFlagsAndRendersTheResult(t *testing.T) {
	t.Parallel()
	var got deskrun.ConnectRequest
	operations := Operations{IsTerminal: func(any) bool { return true },
		Connect: func(_ context.Context, request deskrun.ConnectRequest) (deskrun.ConnectResult, error) {
			got = request
			_, _ = io.WriteString(request.Notice, "open the address\n")
			return deskrun.ConnectResult{Machine: "macbook", DeskURL: "https://desk.sneat.dev", PersonalSpaceID: "p1",
				Shares: []desk.Share{{SpaceID: "team1", Level: desk.ShareFull, State: desk.SharePending}}, Detail: desk.LevelFull,
				CredentialFile: "/c/desk.token", TaskKeyID: "abcd", TaskKeyCreated: true, ConfigPath: "/c/wb.yaml", DaemonRestarted: true}, nil
		}}
	stdout, stderr, err := run(t, operations, "connect", "--detail", "full", "--space", "team1", "--machine", "macbook",
		"--desk", "https://desk.example", "--no-browser", "--no-restart", "--task-key-stdin")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectsRoot != "/projects" || got.Detail != desk.LevelFull || !got.DetailChanged || got.SpaceHint != "team1" ||
		got.Machine != "macbook" || got.DeskURL != "https://desk.example" || !got.NoBrowser || !got.NoRestart || !got.TaskKeyStdin || !got.CanOpen {
		t.Fatalf("request = %+v", got)
	}
	if piped, _ := io.ReadAll(got.Input); string(piped) != "piped" {
		t.Fatalf("stdin is passed through, got %q", piped)
	}
	if !strings.Contains(stdout, "team1 (full, pending)") || !strings.Contains(stdout, "abcd (new)") || !strings.Contains(stderr, "open the address") {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
	stdout, _, _ = run(t, operations, "connect", "--json")
	var decoded deskrun.ConnectResult
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil || decoded.Machine != "macbook" {
		t.Fatalf("json = %q", stdout)
	}
	if got.DetailChanged {
		t.Fatal("--detail is changed only when given")
	}
}

func TestConnectRendersADetailChangeAndPassesErrors(t *testing.T) {
	t.Parallel()
	detailOnly := Operations{Connect: func(context.Context, deskrun.ConnectRequest) (deskrun.ConnectResult, error) {
		return deskrun.ConnectResult{DetailOnly: true, Detail: desk.LevelFull, ConfigPath: "/c/wb.yaml"}, nil
	}}
	if stdout, _, err := run(t, detailOnly, "connect", "--detail", "full"); err != nil || !strings.Contains(stdout, "Desk detail is now full") || !strings.Contains(stdout, "run wb daemon restart") {
		t.Fatalf("detail change = %q, %v", stdout, err)
	}
	failing := Operations{Connect: func(context.Context, deskrun.ConnectRequest) (deskrun.ConnectResult, error) {
		return deskrun.ConnectResult{}, &exit{code: 1, message: "denied"}
	}}
	if _, _, err := run(t, failing, "connect"); code(err) != 1 {
		t.Fatalf("error = %v", err)
	}
	if _, _, err := run(t, failing, "connect", "extra"); err == nil {
		t.Fatal("connect takes no arguments")
	}
}

func TestStatusExitsOneOnFindings(t *testing.T) {
	t.Parallel()
	at := now.Add(-90 * time.Second)
	cases := []struct {
		name   string
		result deskrun.StatusResult
		code   int
		want   []string
	}{
		{"not connected", deskrun.StatusResult{Fix: "wb desk connect"}, 1, []string{"Not connected", "wb desk connect"}},
		{"healthy", deskrun.StatusResult{Connected: true, DeskURL: "https://desk.sneat.dev", Machine: "macbook", MachineID: "m1", Detail: desk.LevelRepositories,
			PersonalSpaceID: "p1", Shares: []desk.Share{{SpaceID: "team1", Level: desk.ShareSummary, State: desk.ShareActive}},
			LastSnapshotAt: &at}, 0, []string{"machine     macbook (m1)", "team1 (summary, active)", "published   1m30s ago", "heartbeat   never"}},
		{"revoked", deskrun.StatusResult{Connected: true, MachineID: "m1", DeskError: desk.ErrorRevoked, Fix: "wb desk connect"}, 1, []string{"machine     m1", "desk_error  revoked", "fix         wb desk connect"}},
		{"unreachable", deskrun.StatusResult{Connected: true, DeskError: desk.ErrorUnreachable}, 1, []string{"desk_error  unreachable"}},
		{"paused", deskrun.StatusResult{Connected: true, Machine: "macbook", Paused: true}, 0, []string{"machine     macbook\n", "publishing  paused"}},
		{"local problem", deskrun.StatusResult{Connected: true, Problem: "the credential file is bad", Fix: "chmod 600 x"}, 1, []string{"problem     the credential file is bad", "fix         chmod 600 x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			operations := Operations{Status: func(context.Context) (deskrun.StatusResult, error) { return tc.result, nil }}
			stdout, _, err := run(t, operations, "status")
			if code(err) != tc.code {
				t.Fatalf("exit = %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if tc.name == "unreachable" && strings.Contains(stdout, "fix") {
				t.Error("no fix line without a fix")
			}
		})
	}
	jsonOps := Operations{Status: func(context.Context) (deskrun.StatusResult, error) { return deskrun.StatusResult{Connected: true}, nil }}
	if stdout, _, err := run(t, jsonOps, "status", "--format", "json"); err != nil || !strings.Contains(stdout, `"connected":true`) {
		t.Fatalf("json = %q, %v", stdout, err)
	}
	failing := Operations{Status: func(context.Context) (deskrun.StatusResult, error) { return deskrun.StatusResult{}, &exit{code: 2} }}
	if _, _, err := run(t, failing, "status"); code(err) != 2 {
		t.Fatalf("error = %v", err)
	}
}

func TestDisconnectExitsOneWhenDeskCouldNotRevoke(t *testing.T) {
	t.Parallel()
	for _, revoked := range []bool{true, false} {
		var noRestart bool
		operations := Operations{Disconnect: func(_ context.Context, root string, skip bool) (deskrun.DisconnectResult, error) {
			noRestart = skip
			return deskrun.DisconnectResult{Revoked: revoked, CredentialFile: "/c/desk.token", ConfigPath: "/c/wb.yaml"}, nil
		}}
		stdout, _, err := run(t, operations, "disconnect", "--no-restart")
		if (code(err) == 1) == revoked || !noRestart || !strings.Contains(stdout, "Disconnected from Desk") {
			t.Fatalf("revoked=%t: %q, %v", revoked, stdout, err)
		}
	}
	jsonOps := Operations{Disconnect: func(context.Context, string, bool) (deskrun.DisconnectResult, error) {
		return deskrun.DisconnectResult{Revoked: true}, nil
	}}
	if stdout, _, err := run(t, jsonOps, "disconnect", "--json"); err != nil || !strings.Contains(stdout, `"revoked":true`) {
		t.Fatalf("json = %q, %v", stdout, err)
	}
	failing := Operations{Disconnect: func(context.Context, string, bool) (deskrun.DisconnectResult, error) {
		return deskrun.DisconnectResult{}, &exit{code: 1}
	}}
	if _, _, err := run(t, failing, "disconnect"); code(err) != 1 {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskKeyExportTellsTheOperationWhetherStdoutIsATerminal(t *testing.T) {
	t.Parallel()
	var terminal bool
	operations := Operations{IsTerminal: func(any) bool { return true }, ExportTaskKey: func(out io.Writer, isTerminal bool) error {
		terminal = isTerminal
		_, err := io.WriteString(out, "key\n")
		return err
	}}
	if stdout, _, err := run(t, operations, "task-key", "export"); err != nil || !terminal || stdout != "key\n" {
		t.Fatalf("export = %q, %v, terminal %t", stdout, err, terminal)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

func TestOutputFailuresAreReported(t *testing.T) {
	t.Parallel()
	operations := Operations{IsTerminal: func(any) bool { return false }, Now: func() time.Time { return now },
		Status: func(context.Context) (deskrun.StatusResult, error) { return deskrun.StatusResult{Connected: true}, nil },
		Disconnect: func(context.Context, string, bool) (deskrun.DisconnectResult, error) {
			return deskrun.DisconnectResult{Revoked: true}, nil
		}}
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }, ExitError: func(code int, message string) error { return &exit{code, message} }}
	for _, verb := range []string{"status", "disconnect"} {
		command := New(runtime, operations)
		command.SetOut(brokenWriter{})
		command.SetErr(io.Discard)
		command.SetArgs([]string{verb})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "closed pipe") {
			t.Errorf("%s with a broken stdout = %v", verb, err)
		}
	}
}

func TestPushPublishesThroughTheOperationAndRendersIt(t *testing.T) {
	t.Parallel()
	var root string
	operations := Operations{Push: func(_ context.Context, projectsRoot string) (deskrun.PushResult, error) {
		root = projectsRoot
		return deskrun.PushResult{DeskURL: "https://desk.sneat.dev", Detail: desk.LevelRepositories, Hash: "sha256:ab", Heartbeat: false}, nil
	}}
	stdout, _, err := run(t, operations, "push")
	if err != nil || root != "/projects" || !strings.Contains(stdout, "sha256:ab") || !strings.Contains(stdout, "the daemon will retry") {
		t.Fatalf("push = %q, %v", stdout, err)
	}
	if stdout, _, _ := run(t, operations, "push", "--json"); !strings.Contains(stdout, `"hash":"sha256:ab"`) {
		t.Fatalf("json = %q", stdout)
	}
	failing := Operations{Push: func(context.Context, string) (deskrun.PushResult, error) { return deskrun.PushResult{}, &exit{code: 1} }}
	if _, _, err := run(t, failing, "push"); code(err) != 1 {
		t.Fatalf("error = %v", err)
	}
}
