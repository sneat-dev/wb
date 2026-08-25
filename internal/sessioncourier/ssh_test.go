package sessioncourier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type fakeCommandRunner struct {
	calls      int
	executable string
	args       []string
	stdin      []byte
	response   []byte
	stderr     []byte
	err        error
	deadline   time.Time
}

func (f *fakeCommandRunner) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	f.calls++
	f.executable = executable
	f.args = append([]string(nil), args...)
	f.stdin = append([]byte(nil), stdin...)
	f.deadline, _ = ctx.Deadline()
	_, _ = stdout.Write(f.response)
	_, _ = stderr.Write(f.stderr)
	return f.err
}

func TestSSHDelivererUsesFixedArgvAndExactRequestStdin(t *testing.T) {
	request, raw := courierTestRequest(t)
	runner := &fakeCommandRunner{response: encodeCourierResult(t, validCourierResult(request, raw))}
	deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{
		Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb",
	}, runner)

	result, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--",
		"hetzner-vm1", "/home/ai/go/bin/wb", "--non-interactive", "session", "receive", "--format", "json",
	}
	if runner.calls != 1 || runner.executable != testExecutable(t) || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("ssh invocation: calls=%d executable=%q args=%#v", runner.calls, runner.executable, runner.args)
	}
	if !bytes.Equal(runner.stdin, raw) {
		t.Fatalf("ssh stdin changed:\n got: %q\nwant: %q", runner.stdin, raw)
	}
	if strings.Contains(strings.Join(runner.args, " "), request.HandoffID) ||
		strings.Contains(strings.Join(runner.args, " "), request.RepositoryRemote) {
		t.Fatalf("request data leaked into ssh argv: %#v", runner.args)
	}
	if runner.deadline.IsZero() || time.Until(runner.deadline) > sshDeliveryTimeout {
		t.Fatalf("ssh command deadline = %v", runner.deadline)
	}
	if result.Phase != sessionmove.PhaseSuccessorStarted || result.Request != request {
		t.Fatalf("result = %#v", result)
	}
}

func TestSSHDelivererUsesFixedRemoteWBCommandByDefault(t *testing.T) {
	request, raw := courierTestRequest(t)
	runner := &fakeCommandRunner{response: encodeCourierResult(t, validCourierResult(request, raw))}
	deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target-alias"}, runner)
	if _, err := deliverer.Deliver(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if runner.args[7] != defaultRemoteWBCommand {
		t.Fatalf("remote command = %q, want %q in args %#v", runner.args[7], defaultRemoteWBCommand, runner.args)
	}
}

func TestSSHDelivererAcceptsCrossHarnessSuccessorIdentity(t *testing.T) {
	request, _ := courierTestRequest(t)
	request.RequestedHarness = "claude-code"
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{response: encodeCourierResult(t, validCourierResult(request, raw))}
	deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	result, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Successor == nil || result.Successor.Runtime != "claude-code" || result.Successor.Model != "" {
		t.Fatalf("cross-harness successor = %#v", result.Successor)
	}
}

func TestSSHDelivererAcceptsVerifiedStartedReplayWithoutWorktreeProjection(t *testing.T) {
	request, raw := courierTestRequest(t)
	response := validCourierResult(request, raw)
	response.Worktree = nil
	response.Replay = true
	runner := &fakeCommandRunner{response: encodeCourierResult(t, response)}
	deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	result, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replay || result.Successor == nil || result.Successor.PinnedCommit != request.BundleCommit {
		t.Fatalf("started replay = %#v", result)
	}
}

func TestSSHDelivererRefusesNoncanonicalRequestBeforeSSH(t *testing.T) {
	_, raw := courierTestRequest(t)
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{}
	deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	if _, err := deliverer.Deliver(context.Background(), compact.Bytes()); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
		t.Fatalf("Deliver error = %v, want canonical-encoding refusal", err)
	}
	if runner.calls != 0 {
		t.Fatalf("ssh calls = %d after local canonical-encoding refusal", runner.calls)
	}
}

func TestSSHDelivererStrictlyValidatesResponse(t *testing.T) {
	request, raw := courierTestRequest(t)
	tests := map[string]struct {
		response func() []byte
		want     string
	}{
		"unknown field": {
			response: func() []byte {
				encoded := encodeCourierResult(t, validCourierResult(request, raw))
				return bytes.Replace(encoded, []byte("{\n"), []byte("{\n  \"unexpected\": true,\n"), 1)
			},
			want: "unknown field",
		},
		"trailing value": {
			response: func() []byte {
				return append(encodeCourierResult(t, validCourierResult(request, raw)), []byte("{}\n")...)
			},
			want: "trailing JSON value",
		},
		"changed request": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Request.SourceModel = "different-model"
				return encodeCourierResult(t, result)
			},
			want: "does not match",
		},
		"wrong digest": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Digest = sessionmove.DigestBytes([]byte("different exact bytes"))
				return encodeCourierResult(t, result)
			},
			want: "request_digest",
		},
		"premature phase": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Phase = sessionmove.PhaseWorktreeReady
				return encodeCourierResult(t, result)
			},
			want: "successor_started",
		},
		"wrong pinned commit": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Worktree.Commit = strings.Repeat("c", 40)
				return encodeCourierResult(t, result)
			},
			want: "pinned worktree commit",
		},
		"missing successor": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor = nil
				return encodeCourierResult(t, result)
			},
			want: "does not include a successor",
		},
		"wrong successor handoff": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor.HandoffID = "handoff-other"
				return encodeCourierResult(t, result)
			},
			want: "successor handoff_id",
		},
		"wrong successor session": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor.WBSessionID = "wbs-other"
				return encodeCourierResult(t, result)
			},
			want: "successor wb_session_id",
		},
		"wrong predecessor session": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor.PredecessorWBSessionID = "wbs-other"
				return encodeCourierResult(t, result)
			},
			want: "predecessor_wb_session_id",
		},
		"wrong target machine": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor.TargetMachine = "other-machine"
				return encodeCourierResult(t, result)
			},
			want: "successor target_machine",
		},
		"wrong runtime": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor.Runtime = "claude-code"
				return encodeCourierResult(t, result)
			},
			want: "successor runtime",
		},
		"wrong tmux": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor.TmuxName = "wb-session-other"
				return encodeCourierResult(t, result)
			},
			want: "successor tmux_name",
		},
		"wrong successor commit": {
			response: func() []byte {
				result := validCourierResult(request, raw)
				result.Successor.PinnedCommit = strings.Repeat("c", 40)
				return encodeCourierResult(t, result)
			},
			want: "successor pinned_commit",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &fakeCommandRunner{response: test.response()}
			deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
			if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Deliver error = %v, want containing %q", err, test.want)
			}
			if runner.calls != 1 {
				t.Fatalf("ssh calls = %d, want exactly one and no fallback", runner.calls)
			}
		})
	}
}

func TestSSHDelivererBoundsOutputAndSanitizesFailure(t *testing.T) {
	request, raw := courierTestRequest(t)
	t.Run("stdout", func(t *testing.T) {
		runner := &fakeCommandRunner{response: bytes.Repeat([]byte("x"), maxSSHStdoutBytes+1)}
		deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("Deliver error = %v, want bounded-output refusal", err)
		}
	})
	t.Run("stderr", func(t *testing.T) {
		stderr := append([]byte("first line\nsecond line\r\n"), bytes.Repeat([]byte("x"), maxSSHDiagnosticBytes+100)...)
		diagnostic := sanitizeDiagnostic(stderr, false)
		if len(diagnostic) > maxSSHDiagnosticBytes || strings.ContainsAny(diagnostic, "\r\n") || !strings.HasSuffix(diagnostic, "...") {
			t.Fatalf("sanitized diagnostic = %q (%d bytes)", diagnostic, len(diagnostic))
		}
		runner := &fakeCommandRunner{stderr: stderr, err: errors.New("exit status 255")}
		deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.Deliver(context.Background(), raw)
		if err == nil {
			t.Fatal("Deliver succeeded")
		}
		if strings.ContainsAny(err.Error(), "\r\n") || !strings.Contains(err.Error(), "first line second line") || !strings.HasSuffix(err.Error(), "...") {
			t.Fatalf("failure diagnostic was not bounded and single-line: %q", err)
		}
		if runner.calls != 1 {
			t.Fatalf("ssh calls = %d, want no fallback", runner.calls)
		}
	})
	_ = request
}

func TestNewSSHDelivererRefusesUnsafeConfigBeforeExecutableLookup(t *testing.T) {
	for name, config := range map[string]sessionmove.SSHConfig{
		"host": {Host: "target;touch"},
		"path": {Host: "target", WBPath: "/opt/$HOME/wb"},
	} {
		t.Run(name, func(t *testing.T) {
			lookups := 0
			_, err := newSSHDeliverer(config, func(string) (string, error) {
				lookups++
				return "/usr/bin/ssh", nil
			}, &fakeCommandRunner{})
			if err == nil || lookups != 0 {
				t.Fatalf("newSSHDeliverer error=%v lookups=%d", err, lookups)
			}
		})
	}
}

func TestNewSSHDelivererRequiresResolvedAbsoluteExecutable(t *testing.T) {
	for _, resolved := range []string{"ssh", "/usr/bin/../bin/ssh"} {
		_, err := newSSHDeliverer(sessionmove.SSHConfig{Host: "target"}, func(string) (string, error) {
			return resolved, nil
		}, &fakeCommandRunner{})
		if err == nil || !strings.Contains(err.Error(), "clean absolute") {
			t.Fatalf("resolved %q error = %v", resolved, err)
		}
	}
	plainFile := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(plainFile, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, resolved := range []string{t.TempDir(), plainFile} {
		_, err := newSSHDeliverer(sessionmove.SSHConfig{Host: "target"}, func(string) (string, error) {
			return resolved, nil
		}, &fakeCommandRunner{})
		if err == nil || !strings.Contains(err.Error(), "regular executable") {
			t.Fatalf("resolved %q error = %v", resolved, err)
		}
	}
}

func newTestSSHDeliverer(t *testing.T, config sessionmove.SSHConfig, runner commandRunner) *sshDeliverer {
	t.Helper()
	deliverer, err := newSSHDeliverer(config, func(name string) (string, error) {
		if name != sshExecutableName {
			return "", fmt.Errorf("unexpected executable %q", name)
		}
		return testExecutable(t), nil
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	return deliverer
}

func testExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func validCourierResult(request sessionmove.Request, raw []byte) sessionreceive.Result {
	runtime := request.RequestedHarness
	if runtime == "" {
		runtime = request.SourceRuntime
	}
	model := ""
	if runtime == request.SourceRuntime {
		model = request.SourceModel
	}
	return sessionreceive.Result{
		Request: request,
		Digest:  sessionmove.DigestBytes(raw),
		Phase:   sessionmove.PhaseSuccessorStarted,
		Worktree: &worktrees.SessionReceiveResult{
			Repository: "acme/app", CanonicalDir: "/target/acme/app", WorktreeDir: "/target/worktree", Commit: request.BundleCommit,
		},
		Successor: &sessionlaunch.Result{
			HandoffID: request.HandoffID, WBSessionID: request.SuccessorWBSessionID,
			PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
			PID: 1234, TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: runtime, Model: model,
			WorktreeDir: "/target/worktree", PinnedCommit: request.BundleCommit,
			StartedAt: time.Date(2026, time.August, 25, 13, 0, 0, 0, time.UTC),
		},
	}
}

func encodeCourierResult(t *testing.T, result sessionreceive.Result) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func courierTestRequest(t *testing.T) (sessionmove.Request, []byte) {
	t.Helper()
	request := sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion,
		HandoffID:     "handoff-123", SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "source", TargetMachine: "target-vm", RepositoryRemote: "git@github.com:acme/app.git",
		Branch: "feature/session", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-123.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover\n")),
		SourceRuntime: "codex", SourceModel: "gpt-5", CreatedAt: time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC),
	}
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return request, raw
}
