package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSSH struct {
	calls      int
	executable string
	args       []string
	stdin      []byte
	response   []byte
	stderr     string
	err        error
	deadline   bool
}

func (fake *fakeSSH) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	fake.calls++
	fake.executable = executable
	fake.args = append([]string(nil), args...)
	fake.stdin = append([]byte(nil), stdin...)
	if deadline, ok := ctx.Deadline(); ok && !deadline.IsZero() {
		fake.deadline = true
	}
	if fake.stderr != "" {
		_, _ = io.WriteString(stderr, fake.stderr)
	}
	if len(fake.response) > 0 {
		_, _ = stdout.Write(fake.response)
	}
	return fake.err
}

func fakeRemoteDeps(t *testing.T, runner *fakeSSH) RemoteDeps {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return RemoteDeps{
		LookPath: func(string) (string, error) { return executable, nil },
		Runner:   runner,
		Timeout:  time.Minute,
	}
}

func testTarget() RemoteTarget {
	return RemoteTarget{Machine: "hetzner-vm1", Host: "178.104.41.143", User: "ai", WBPath: "/home/ai/go/bin/wb"}
}

func TestRemoteRequestValidationIsIndependentOfTheFlagParser(t *testing.T) {
	t.Parallel()
	valid := RemoteRequest{
		SchemaVersion: 1, Operation: RemoteDispatch, Mode: ModeNew,
		Worktree: "task-one", Profile: "cheap", Task: "do it", TimeoutMS: 1000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a well-formed request must validate: %v", err)
	}
	cases := map[string]func(*RemoteRequest){
		"wrong schema":      func(r *RemoteRequest) { r.SchemaVersion = 99 },
		"unknown operation": func(r *RemoteRequest) { r.Operation = "delete-everything" },
		"missing mode":      func(r *RemoteRequest) { r.Mode = "" },
		"bad mode":          func(r *RemoteRequest) { r.Mode = "sideways" },
		"missing worktree":  func(r *RemoteRequest) { r.Worktree = "  " },
		"missing task":      func(r *RemoteRequest) { r.Task = "" },
		"missing profile":   func(r *RemoteRequest) { r.Profile = "" },
		"non-positive time": func(r *RemoteRequest) { r.TimeoutMS = 0 },
		"negative time":     func(r *RemoteRequest) { r.TimeoutMS = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := valid
			mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", name)
			}
		})
	}
}

func TestRemoteRequestValidationCoversEveryOperation(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{RemoteStatus, RemoteAwait, RemoteLogs, RemoteStop} {
		request := RemoteRequest{SchemaVersion: 1, Operation: operation}
		if err := request.Validate(); err == nil {
			t.Fatalf("%s must require an agent id", operation)
		}
		request.AgentID = "agt-00000000000000000000000000000000"
		if err := request.Validate(); err != nil {
			t.Fatalf("%s with an id: %v", operation, err)
		}
	}
	if err := (RemoteRequest{SchemaVersion: 1, Operation: RemoteAwait, AgentID: "agt-00000000000000000000000000000000", WaitTimeoutMS: -1}).Validate(); err == nil {
		t.Fatal("a negative wait bound must be refused")
	}
	if err := (RemoteRequest{SchemaVersion: 1, Operation: RemoteLogs, AgentID: "agt-00000000000000000000000000000000", Tail: -1}).Validate(); err == nil {
		t.Fatal("a negative tail must be refused")
	}
	if err := (RemoteRequest{SchemaVersion: 1, Operation: RemoteList}).Validate(); err != nil {
		t.Fatalf("list needs no other field: %v", err)
	}
}

func TestRemoteTargetValidationBlocksShellMetacharacters(t *testing.T) {
	t.Parallel()
	cases := map[string]RemoteTarget{
		"no machine": {Host: "h"},
		"no host":    {Machine: "m"},
		"host with a semicolon": {
			Machine: "m", Host: "h; rm -rf /",
		},
		"host with a space": {Machine: "m", Host: "two words"},
		"host with a command substitution": {
			Machine: "m", Host: "$(whoami)",
		},
		"user with a semicolon": {
			Machine: "m", Host: "h", User: "ai; reboot",
		},
		"relative wb_path": {
			Machine: "m", Host: "h", WBPath: "bin/wb",
		},
		"unclean wb_path": {
			Machine: "m", Host: "h", WBPath: "/home/ai/../ai/bin/wb",
		},
		"wb_path with shell text": {
			Machine: "m", Host: "h", WBPath: "/home/ai/bin/wb; rm -rf /",
		},
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := target.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", name)
			}
		})
	}
	if err := testTarget().Validate(); err != nil {
		t.Fatalf("a well-formed target must validate: %v", err)
	}
}

func writeRemoteConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRemoteTargetsReusesTheSessionMoveMachineMap(t *testing.T) {
	t.Parallel()
	path := writeRemoteConfig(t, `
session_move:
  targets:
    hetzner-vm1:
      default_courier: ssh
      ssh:
        host: 178.104.41.143
        user: ai
        wb_path: /home/ai/go/bin/wb
    synchestra-only:
      default_courier: synchestra
      synchestra:
        runner: some-runner
`)
	targets, err := LoadRemoteTargets(path)
	if err != nil {
		t.Fatalf("LoadRemoteTargets: %v", err)
	}
	// A machine with no SSH address is not dispatchable and must not appear.
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	target := targets["hetzner-vm1"]
	if target.Host != "178.104.41.143" || target.User != "ai" || target.WBPath != "/home/ai/go/bin/wb" {
		t.Fatalf("target = %#v", target)
	}

	resolved, err := ResolveRemoteTarget(path, "hetzner-vm1")
	if err != nil || resolved.Host != target.Host {
		t.Fatalf("ResolveRemoteTarget = %#v, %v", resolved, err)
	}
	if _, err := ResolveRemoteTarget(path, "nope"); err == nil || !IsRequestError(err) || !strings.Contains(err.Error(), "configured machines: hetzner-vm1") {
		t.Fatalf("unknown machine error = %v", err)
	}
	if _, err := ResolveRemoteTarget(path, ""); err == nil || !IsRequestError(err) {
		t.Fatalf("an empty machine must be refused: %v", err)
	}
}

func TestLoadRemoteTargetsReportsUnconfiguredAndUnusableState(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if _, err := LoadRemoteTargets(missing); err == nil {
		t.Fatal("an absent configuration must be reported")
	}
	// Configured, but nothing SSH-addressable.
	path := writeRemoteConfig(t, "session_move:\n  targets:\n    only:\n      default_courier: ssh\n      ssh:\n        host: h\n        user: ai\n")
	targets, err := LoadRemoteTargets(path)
	if err != nil {
		t.Fatalf("LoadRemoteTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	path = writeRemoteConfig(t, "session_move:\n  targets:\n    only:\n      default_courier: synchestra\n      synchestra:\n        runner: r\n")
	if _, err := LoadRemoteTargets(path); err == nil || !strings.Contains(err.Error(), "no ssh-addressable machine") {
		t.Fatalf("a map with no ssh target must be refused: %v", err)
	}
}

func TestSplitAgentRefAcceptsBothForms(t *testing.T) {
	t.Parallel()
	machine, id, err := SplitAgentRef("hetzner-vm1:agt-00000000000000000000000000000000")
	if err != nil || machine != "hetzner-vm1" || id != "agt-00000000000000000000000000000000" {
		t.Fatalf("qualified ref = %q %q %v", machine, id, err)
	}
	machine, id, err = SplitAgentRef(" agt-00000000000000000000000000000000 ")
	if err != nil || machine != "" || id != "agt-00000000000000000000000000000000" {
		t.Fatalf("bare ref = %q %q %v", machine, id, err)
	}
	for _, bad := range []string{":agt-00", "hetzner-vm1:", ":"} {
		if _, _, err := SplitAgentRef(bad); err == nil {
			t.Fatalf("SplitAgentRef(%q) accepted a malformed reference", bad)
		}
	}
	if got := StripAgentID("hetzner-vm1:agt-00"); got != "agt-00" {
		t.Fatalf("StripAgentID = %q", got)
	}
	if got := StripAgentID("nonsense"); got != "nonsense" {
		t.Fatalf("StripAgentID fallback = %q", got)
	}
}

func TestCallRemoteSendsAFixedArgvAndTheRequestOnStdin(t *testing.T) {
	t.Parallel()
	runner := &fakeSSH{}
	result := Result{AgentID: "agt-00000000000000000000000000000000", State: StateCompleted}
	response, _ := json.Marshal(RemoteResponse{SchemaVersion: 1, Operation: RemoteDispatch, Result: &result})
	runner.response = response

	request := RemoteRequest{
		SchemaVersion: 1, Operation: RemoteDispatch, Mode: ModeNew,
		Worktree: "task-one", Profile: "cheap", Task: "secret task text", TimeoutMS: 60000,
	}
	got, err := CallRemote(context.Background(), testTarget(), request, fakeRemoteDeps(t, runner))
	if err != nil {
		t.Fatalf("CallRemote: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("ssh calls = %d", runner.calls)
	}
	want := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-l", "ai", "--", "178.104.41.143", "/home/ai/go/bin/wb", RemoteArgument}
	if strings.Join(runner.args, " ") != strings.Join(want, " ") {
		t.Fatalf("ssh argv = %#v, want %#v", runner.args, want)
	}
	if !runner.deadline {
		t.Fatal("the remote call must carry a deadline")
	}
	// The whole point of the stdin channel: no request data in the argv.
	for _, forbidden := range []string{"task-one", "cheap", "secret task text", "60000"} {
		if strings.Contains(strings.Join(runner.args, " "), forbidden) {
			t.Fatalf("request data leaked into ssh argv: %#v", runner.args)
		}
	}
	var sent RemoteRequest
	if err := json.Unmarshal(runner.stdin, &sent); err != nil {
		t.Fatalf("stdin is not the request document: %v", err)
	}
	if sent.Task != "secret task text" || sent.Worktree != "task-one" || sent.Profile != "cheap" {
		t.Fatalf("stdin request = %#v", sent)
	}
	if got.Result == nil || got.Result.State != StateCompleted {
		t.Fatalf("response = %#v", got)
	}
}

func TestCallRemoteDefaultsTheRemoteCommandName(t *testing.T) {
	t.Parallel()
	runner := &fakeSSH{}
	response, _ := json.Marshal(RemoteResponse{SchemaVersion: 1, Operation: RemoteList, Results: []Result{}})
	runner.response = response
	target := testTarget()
	target.WBPath = ""
	if _, err := CallRemote(context.Background(), target, RemoteRequest{SchemaVersion: 1, Operation: RemoteList}, fakeRemoteDeps(t, runner)); err != nil {
		t.Fatalf("CallRemote: %v", err)
	}
	// The remote command is whatever follows the host, wherever the optional
	// login user put it.
	hostIndex := -1
	for index, arg := range runner.args {
		if arg == target.Host {
			hostIndex = index
			break
		}
	}
	if hostIndex < 0 || hostIndex+1 >= len(runner.args) || runner.args[hostIndex+1] != "wb" {
		t.Fatalf("remote command in %#v, want the default %q after the host", runner.args, "wb")
	}
}

func TestCallRemoteDistinguishesRefusalFromTransportFailure(t *testing.T) {
	t.Parallel()
	refusal, _ := json.Marshal(RemoteResponse{SchemaVersion: 1, Operation: RemoteStatus, Failure: "no dispatched agent run"})
	runner := &fakeSSH{response: refusal}
	_, err := CallRemote(context.Background(), testTarget(), RemoteRequest{SchemaVersion: 1, Operation: RemoteStatus, AgentID: "agt-00000000000000000000000000000000"}, fakeRemoteDeps(t, runner))
	if err == nil || !strings.Contains(err.Error(), "no dispatched agent run") {
		t.Fatalf("a structured refusal must surface as an error naming it: %v", err)
	}
	if !strings.Contains(err.Error(), "hetzner-vm1") {
		t.Fatalf("the refusal must name the machine: %v", err)
	}

	// A transport failure quotes the remote stderr diagnostic.
	runner = &fakeSSH{err: errors.New("exit status 255"), stderr: "ssh: connect to host x port 22: timed out"}
	_, err = CallRemote(context.Background(), testTarget(), RemoteRequest{SchemaVersion: 1, Operation: RemoteList}, fakeRemoteDeps(t, runner))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("a transport failure must quote the diagnostic: %v", err)
	}
	if !strings.Contains(err.Error(), "hetzner-vm1") {
		t.Fatalf("a transport failure must name the machine: %v", err)
	}

	// A transport failure with no diagnostic still names the machine.
	runner = &fakeSSH{err: errors.New("exit status 255")}
	if _, err = CallRemote(context.Background(), testTarget(), RemoteRequest{SchemaVersion: 1, Operation: RemoteList}, fakeRemoteDeps(t, runner)); err == nil || !strings.Contains(err.Error(), "hetzner-vm1") {
		t.Fatalf("bare transport failure = %v", err)
	}
}

func TestCallRemoteRejectsAnUnusableRemoteAnswer(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		response string
		want     string
	}{
		"nothing at all":  {"", "wrote nothing"},
		"not a document":  {"command not found\n", "did not return an agent protocol document"},
		"future schema":   {`{"schema_version":99,"operation":"list","results":[]}`, "schema_version 99 unsupported"},
		"wrong operation": {`{"schema_version":1,"operation":"status","results":[]}`, "answered \"status\""},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeSSH{response: []byte(testCase.response)}
			_, err := CallRemote(context.Background(), testTarget(), RemoteRequest{SchemaVersion: 1, Operation: RemoteList}, fakeRemoteDeps(t, runner))
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestCallRemoteRefusesAnOversizedAnswer(t *testing.T) {
	t.Parallel()
	runner := &fakeSSH{response: []byte(strings.Repeat("x", maxRemoteStdoutBytes+16))}
	_, err := CallRemote(context.Background(), testTarget(), RemoteRequest{SchemaVersion: 1, Operation: RemoteList}, fakeRemoteDeps(t, runner))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("an oversized response must be refused: %v", err)
	}
}

func TestCallRemoteRefusesAnInvalidRequestBeforeReachingSSH(t *testing.T) {
	t.Parallel()
	runner := &fakeSSH{}
	_, err := CallRemote(context.Background(), testTarget(), RemoteRequest{SchemaVersion: 1, Operation: RemoteDispatch}, fakeRemoteDeps(t, runner))
	if err == nil {
		t.Fatal("an invalid request must be refused")
	}
	if runner.calls != 0 {
		t.Fatal("nothing may be sent for an invalid request")
	}
	if _, err := CallRemote(context.Background(), RemoteTarget{Host: "h; rm"}, RemoteRequest{SchemaVersion: 1, Operation: RemoteList}, fakeRemoteDeps(t, runner)); err == nil {
		t.Fatal("an invalid target must be refused")
	}
	if runner.calls != 0 {
		t.Fatal("nothing may be sent to an invalid target")
	}
}

func TestCallRemoteGivesAwaitRoomForItsWaitBound(t *testing.T) {
	t.Parallel()
	response, _ := json.Marshal(RemoteResponse{SchemaVersion: 1, Operation: RemoteAwait})
	runner := &fakeSSH{response: response}
	request := RemoteRequest{
		SchemaVersion: 1, Operation: RemoteAwait,
		AgentID: "agt-00000000000000000000000000000000", WaitTimeoutMS: int64(time.Hour / time.Millisecond),
	}
	deps := fakeRemoteDeps(t, runner)
	deps.Timeout = time.Second
	if _, err := CallRemote(context.Background(), testTarget(), request, deps); err != nil {
		t.Fatalf("CallRemote: %v", err)
	}
	if !runner.deadline {
		t.Fatal("the await call must carry a deadline")
	}
}
