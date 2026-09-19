package herdr

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func newTestClient(t *testing.T, runner Runner, opts ...ClientOption) *Client {
	t.Helper()
	client, err := NewClient(lookupFromMap(map[string]string{envBinPath: "/fake/herdr"}), append([]ClientOption{WithRunner(runner)}, opts...)...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestNewClientResolvesBinaryAndRejectsMissingOne(t *testing.T) {
	client, err := NewClient(lookupFromMap(map[string]string{envBinPath: "/fake/herdr"}))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.Binary() != "/fake/herdr" {
		t.Fatalf("Binary() = %q, want /fake/herdr", client.Binary())
	}

	directory := t.TempDir()
	t.Setenv("PATH", directory)
	if _, err := NewClient(lookupFromMap(nil)); !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("NewClient() error = %v, want ErrBinaryNotFound", err)
	}
}

func TestClientBinaryOnNilClient(t *testing.T) {
	var client *Client
	if got := client.Binary(); got != "" {
		t.Fatalf("nil Client.Binary() = %q, want empty", got)
	}
	if got := client.SocketPath(); got != "" {
		t.Fatalf("nil Client.SocketPath() = %q, want empty", got)
	}
	if got := client.SessionName(); got != "" {
		t.Fatalf("nil Client.SessionName() = %q, want empty", got)
	}
}

func TestWithSocketPathAndSessionName(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t), WithSocketPath("/tmp/other.sock"), WithSessionName("reviewer-session"))
	if got := client.SocketPath(); got != "/tmp/other.sock" {
		t.Fatalf("SocketPath() = %q", got)
	}
	if got := client.SessionName(); got != "reviewer-session" {
		t.Fatalf("SessionName() = %q", got)
	}
}

func TestClientCallUsesConfiguredSocketPathNeverAmbient(t *testing.T) {
	// The ambient environment points at the wrong socket; WithSocketPath
	// must win, proving the Client never silently relies on the ambient
	// HERDR_SOCKET_PATH once a caller has been explicit.
	t.Setenv(envSocketPath, "/tmp/wrong-ambient.sock")

	runner := newFakeRunner(t).on([]string{"pane", "current", "--current"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_current.json"),
	})
	client := newTestClient(t, runner, WithSocketPath("/tmp/configured.sock"))

	if _, err := client.PaneCurrent(context.Background()); err != nil {
		t.Fatalf("PaneCurrent() error = %v", err)
	}
	if len(runner.envs) != 1 {
		t.Fatalf("recorded %d envs, want 1", len(runner.envs))
	}
	env := runner.envs[0]
	wantEntry := envSocketPath + "=/tmp/configured.sock"
	found := false
	for _, entry := range env {
		if entry == wantEntry {
			found = true
		}
		if entry == envSocketPath+"=/tmp/wrong-ambient.sock" {
			t.Fatalf("subprocess env still carries the wrong ambient socket path: %v", env)
		}
	}
	if !found {
		t.Fatalf("subprocess env = %v, missing %q", env, wantEntry)
	}
}

func TestClientCallWithoutSocketPathConfiguredPassesNilEnv(t *testing.T) {
	// No WithSocketPath: the Client must not touch the environment at all,
	// so os/exec's own "inherit the ambient environment" default applies.
	runner := newFakeRunner(t).on([]string{"pane", "current", "--current"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_current.json"),
	})
	client := newTestClient(t, runner)

	if _, err := client.PaneCurrent(context.Background()); err != nil {
		t.Fatalf("PaneCurrent() error = %v", err)
	}
	if len(runner.envs) != 1 || runner.envs[0] != nil {
		t.Fatalf("recorded envs = %#v, want a single nil entry", runner.envs)
	}
}

func TestClientCallPrependsSessionFlag(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"--session", "reviewer-session", "pane", "current", "--current"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_current.json"),
	})
	client := newTestClient(t, runner, WithSessionName("reviewer-session"))

	if _, err := client.PaneCurrent(context.Background()); err != nil {
		t.Fatalf("PaneCurrent() error = %v", err)
	}
}

func TestWithTimeoutIgnoresNonPositive(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t), WithTimeout(0))
	if client.effectiveTimeout() != DefaultTimeout {
		t.Fatalf("effectiveTimeout() = %v, want DefaultTimeout after WithTimeout(0)", client.effectiveTimeout())
	}
	client = newTestClient(t, newFakeRunner(t), WithTimeout(-1))
	if client.effectiveTimeout() != DefaultTimeout {
		t.Fatalf("effectiveTimeout() = %v, want DefaultTimeout after WithTimeout(negative)", client.effectiveTimeout())
	}
	client = newTestClient(t, newFakeRunner(t), WithTimeout(5*time.Second))
	if client.effectiveTimeout() != 5*time.Second {
		t.Fatalf("effectiveTimeout() = %v, want 5s", client.effectiveTimeout())
	}
}

func TestEffectiveTimeoutFallsBackWhenZero(t *testing.T) {
	// A Client's timeout is only ever zero here through direct struct
	// construction (NewClient always seeds DefaultTimeout, and
	// WithTimeout(0) is a documented no-op) — this is the defensive
	// fallback for that construction path.
	client := &Client{binary: "/fake/herdr", runner: newFakeRunner(t)}
	if got := client.effectiveTimeout(); got != DefaultTimeout {
		t.Fatalf("effectiveTimeout() = %v, want DefaultTimeout for a zero-value Client", got)
	}
}

func TestWithRunnerIgnoresNil(t *testing.T) {
	runner := newFakeRunner(t)
	client := newTestClient(t, runner, WithRunner(nil))
	if client.runner != runner {
		t.Fatal("WithRunner(nil) replaced the previously configured runner")
	}
}

func TestClientCallSuccessDecodesEnvelope(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "current", "--current"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_current.json"),
	})
	client := newTestClient(t, runner)

	pane, err := client.PaneCurrent(context.Background())
	if err != nil {
		t.Fatalf("PaneCurrent() error = %v", err)
	}
	if pane.PaneID != "w1:p2" || pane.AgentStatus != StatusWorking {
		t.Fatalf("PaneCurrent() = %#v", pane)
	}
}

func TestClientCallMapsKnownErrorCodes(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		wantSent error
	}{
		{name: "agent not found", fixture: "error_agent_not_found.json", wantSent: ErrUnknownTarget},
		{name: "pane not found", fixture: "error_pane_not_found.json", wantSent: ErrUnknownTarget},
		{name: "server not running", fixture: "error_server_not_running.json", wantSent: ErrServerUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner(t).on([]string{"agent", "get", "probe"}, fakeCall{
				stdout: mustReadTestdata(t, tc.fixture),
			})
			client := newTestClient(t, runner)

			_, err := client.AgentGet(context.Background(), "probe")
			if !errors.Is(err, tc.wantSent) {
				t.Fatalf("AgentGet() error = %v, want wrapping %v", err, tc.wantSent)
			}
		})
	}
}

func TestClientCallMapsUnknownErrorCode(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "get", "probe"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:get","error":{"code":"something_new","message":"unrecognized"}}`),
	})
	client := newTestClient(t, runner)

	_, err := client.AgentGet(context.Background(), "probe")
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("AgentGet() error = %v, want ErrCommandFailed", err)
	}
}

func TestClientCallUnparseableSuccessOutput(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "current", "--current"}, fakeCall{
		stdout: []byte("not json at all"),
	})
	client := newTestClient(t, runner)

	_, err := client.PaneCurrent(context.Background())
	if !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("PaneCurrent() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestClientRawCallExecNotFoundMidFlight(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"--version"}, fakeCall{err: exec.ErrNotFound})
	client := newTestClient(t, runner)

	_, err := client.Version(context.Background())
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("Version() error = %v, want ErrBinaryNotFound", err)
	}
}

func TestClientRawCallUsageStyleFailure(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "get"}, fakeCall{
		stderr: []byte("usage: herdr agent get <target>"),
		err:    errors.New("exit status 2"),
	})
	client := newTestClient(t, runner)

	_, err := client.call(context.Background(), "agent", "get")
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("call() error = %v, want ErrCommandFailed", err)
	}
}

func TestClientRawCallContextAlreadyCanceled(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "get", "probe"}, fakeCall{
		err: errors.New("would have run, but the context was already done"),
	})
	client := newTestClient(t, runner)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.AgentGet(ctx, "probe")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AgentGet() error = %v, want wrapping context.Canceled", err)
	}
}

func TestClientCallNilClient(t *testing.T) {
	var client *Client
	if _, err := client.call(context.Background(), "pane", "current"); !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("nil Client.call() error = %v, want ErrBinaryNotFound", err)
	}
}

func TestClientCallNoRunnerConfigured(t *testing.T) {
	client := &Client{binary: "/fake/herdr"}
	if _, err := client.call(context.Background(), "pane", "current"); !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("call() with nil runner error = %v, want ErrBinaryNotFound", err)
	}
}

func TestClientVersion(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"--version"}, fakeCall{stdout: mustReadTestdata(t, "version.txt")})
	client := newTestClient(t, runner)

	version, err := client.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if version.String() != "herdr 0.9.1" {
		t.Fatalf("Version() = %v", version)
	}
}

func TestClientVersionUnparseable(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"--version"}, fakeCall{stdout: []byte("garbage")})
	client := newTestClient(t, runner)

	_, err := client.Version(context.Background())
	if !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("Version() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestClientEnsureMinimumVersionSatisfied(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"--version"}, fakeCall{stdout: []byte("herdr 0.9.1")})
	client := newTestClient(t, runner)

	if err := client.EnsureMinimumVersion(context.Background()); err != nil {
		t.Fatalf("EnsureMinimumVersion() error = %v", err)
	}
}

func TestClientEnsureMinimumVersionTooOld(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"--version"}, fakeCall{stdout: []byte("herdr 0.8.0")})
	client := newTestClient(t, runner)

	err := client.EnsureMinimumVersion(context.Background())
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("EnsureMinimumVersion() error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestClientEnsureMinimumVersionPropagatesVersionError(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"--version"}, fakeCall{
		stderr: []byte(`{"error":{"code":"server_not_running","message":"no server"}}`),
		err:    errors.New("exit status 1"),
	})
	client := newTestClient(t, runner)

	err := client.EnsureMinimumVersion(context.Background())
	if !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("EnsureMinimumVersion() error = %v, want ErrServerUnreachable", err)
	}
}

func TestDescribeParseFailureWithoutUnmarshalError(t *testing.T) {
	got := describeParseFailure(nil, []byte(`{"unexpected":"shape"}`))
	if got == "" {
		t.Fatal("describeParseFailure(nil, ...) returned empty string")
	}
}

func TestDescribeParseFailureWithUnmarshalError(t *testing.T) {
	unmarshalErr := errors.New("boom")
	if got := describeParseFailure(unmarshalErr, nil); got != "boom" {
		t.Fatalf("describeParseFailure(err, ...) = %q, want %q", got, "boom")
	}
}
