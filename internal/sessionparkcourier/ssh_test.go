package sessionparkcourier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
)

type parkCourierRunner struct {
	calls      int
	executable string
	args       []string
	stdin      []byte
	response   []byte
	stderr     []byte
	err        error
}

func (runner *parkCourierRunner) Run(_ context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	runner.calls++
	runner.executable = executable
	runner.args = append([]string(nil), args...)
	runner.stdin = append([]byte(nil), stdin...)
	_, _ = stdout.Write(runner.response)
	_, _ = stderr.Write(runner.stderr)
	return runner.err
}

func TestSSHDelivererUsesFixedReceiveParkArgvAndExactEnvelope(t *testing.T) {
	request, raw := parkCourierRequest(t)
	runner := &parkCourierRunner{response: encodeParkCourierResult(t, validParkCourierResult(t, request, raw))}
	deliverer := newTestParkSSHDeliverer(t, sessionmove.SSHConfig{Host: "target-vm", User: "ai", WBPath: "/opt/wb"}, runner)

	result, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-l", "ai", "--", "target-vm", "/opt/wb", "--non-interactive", "session", "receive-park", "--format", "json"}
	if runner.calls != 1 || runner.executable != parkTestExecutable(t) || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("ssh invocation calls=%d executable=%q args=%#v", runner.calls, runner.executable, runner.args)
	}
	if !bytes.Equal(runner.stdin, raw) {
		t.Fatalf("ssh stdin changed:\n got %q\nwant %q", runner.stdin, raw)
	}
	if strings.Contains(strings.Join(runner.args, " "), request.ResumeID) || strings.Contains(strings.Join(runner.args, " "), request.Continuation) {
		t.Fatalf("private/request data leaked into ssh argv: %#v", runner.args)
	}
	if result.Receipt.ResumeID != request.ResumeID || len(result.Receipt.Members) != len(request.Members) {
		t.Fatalf("delivery result = %#v", result)
	}
}

func TestSSHDelivererRefusesNoncanonicalEnvelopeBeforeSSH(t *testing.T) {
	_, raw := parkCourierRequest(t)
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	runner := &parkCourierRunner{}
	deliverer := newTestParkSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	if _, err := deliverer.Deliver(context.Background(), compact.Bytes()); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
		t.Fatalf("Deliver error=%v, want canonical-encoding refusal", err)
	}
	if runner.calls != 0 {
		t.Fatalf("ssh calls=%d after local refusal", runner.calls)
	}
}

func TestSSHDelivererStrictlyRefusesInvalidTargetReceipts(t *testing.T) {
	request, raw := parkCourierRequest(t)
	tests := map[string]struct {
		response func() []byte
		want     string
	}{
		"unknown field": {
			response: func() []byte {
				encoded := encodeParkCourierResult(t, validParkCourierResult(t, request, raw))
				return bytes.Replace(encoded, []byte("{\n"), []byte("{\n  \"unexpected\": true,\n"), 1)
			},
			want: "unknown field",
		},
		"wrong digest": {
			response: func() []byte {
				result := validParkCourierResult(t, request, raw)
				result.Digest = sessionmove.DigestBytes([]byte("different"))
				return encodeParkCourierResult(t, result)
			},
			want: "exact completed target receipt",
		},
		"missing receipt": {
			response: func() []byte {
				result := validParkCourierResult(t, request, raw)
				result.Receipt = nil
				return encodeParkCourierResult(t, result)
			},
			want: "exact completed target receipt",
		},
		"receipt member mismatch": {
			response: func() []byte {
				result := validParkCourierResult(t, request, raw)
				result.Receipt.Members[1].Commit = strings.Repeat("d", 40)
				return encodeParkCourierResult(t, result)
			},
			want: "member 1 conflicts",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &parkCourierRunner{response: test.response()}
			deliverer := newTestParkSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
			if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Deliver error=%v, want %q", err, test.want)
			}
			if runner.calls != 1 {
				t.Fatalf("ssh calls=%d, want exactly one", runner.calls)
			}
		})
	}
}

func TestSSHDelivererSanitizesFailureAndBoundsOutput(t *testing.T) {
	_, raw := parkCourierRequest(t)
	runner := &parkCourierRunner{response: bytes.Repeat([]byte("x"), maxSSHStdoutBytes+1)}
	deliverer := newTestParkSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bounded stdout error=%v", err)
	}
	stderr := append([]byte("first line\nsecond line\r\n"), bytes.Repeat([]byte("x"), maxSSHDiagnosticBytes+100)...)
	runner = &parkCourierRunner{stderr: stderr, err: fmt.Errorf("exit status 255")}
	deliverer = newTestParkSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	if _, err := deliverer.Deliver(context.Background(), raw); err == nil || strings.ContainsAny(err.Error(), "\r\n") || !strings.Contains(err.Error(), "first line second line") || !strings.HasSuffix(err.Error(), "...") {
		t.Fatalf("sanitized failure=%v", err)
	}
}

func newTestParkSSHDeliverer(t *testing.T, config sessionmove.SSHConfig, runner commandRunner) *sshDeliverer {
	t.Helper()
	deliverer, err := newSSHDeliverer(config, func(name string) (string, error) {
		if name != sshExecutableName {
			return "", fmt.Errorf("unexpected executable %q", name)
		}
		return parkTestExecutable(t), nil
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	return deliverer
}

func parkTestExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func parkCourierRequest(t *testing.T) (sessionpark.RemoteRequest, []byte) {
	t.Helper()
	bundle := sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-courier", Continuation: "private continuation", ParkedAt: time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC),
		Source: session.Record{PID: 42, WBSessionID: "wbs-source", Machine: "source", Runtime: "codex", Model: "gpt-5.6-luna", StartedAt: time.Date(2026, time.August, 26, 11, 0, 0, 0, time.UTC)},
		Worktrees: []sessionpark.Worktree{
			{Repository: "acme/alpha", RepositoryRemote: "https://github.com/acme/alpha.git", WorktreeDir: "/source/alpha", Branch: "feature/alpha", Head: strings.Repeat("a", 40), RemoteHead: strings.Repeat("a", 40), WorkLogReference: "worklog:park/run/" + strings.Repeat("b", 64)},
			{Repository: "acme/beta", RepositoryRemote: "https://github.com/acme/beta.git", WorktreeDir: "/source/beta", Branch: "feature/beta", Head: strings.Repeat("c", 40), RemoteHead: strings.Repeat("c", 40), WorkLogReference: "worklog:park/run/" + strings.Repeat("d", 64)},
		},
	}
	request := sessionpark.BuildRemoteRequest(bundle, "target", "", time.Date(2026, time.August, 26, 12, 1, 0, 0, time.UTC))
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	return request, raw
}

func validParkCourierResult(t *testing.T, request sessionpark.RemoteRequest, raw []byte) sessionparkreceive.Result {
	t.Helper()
	digest := sessionmove.DigestBytes(raw)
	receipt := sessionpark.Receipt{
		SchemaVersion: sessionpark.ReceiptSchemaVersion, ResumeID: request.ResumeID, RequestDigest: digest, ParkedSessionID: request.ParkedSessionID,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
		TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: "codex", Model: "gpt-5.6-luna",
		AttemptID: "000001-" + strings.Repeat("e", 32), AttemptIndex: 1, PID: 123, StartedAt: time.Date(2026, time.August, 26, 12, 2, 0, 0, time.UTC),
		Members: make([]sessionpark.ReceiptMember, len(request.Members)),
	}
	for index, member := range request.Members {
		reference, err := sessionpark.TargetWorkLogReference(request, digest, member)
		if err != nil {
			t.Fatal(err)
		}
		receipt.Members[index] = sessionpark.ReceiptMember{MemberID: member.MemberID, Repository: member.Repository, TargetPath: "/target/" + member.MemberID, Pin: sessionpark.MemberPin(request.ResumeID, member.MemberID), Commit: member.Commit, TargetWorkLogReference: reference}
	}
	return sessionparkreceive.Result{ResumeID: request.ResumeID, Digest: digest, Phase: sessionparkreceive.PhaseCompleted, Receipt: &receipt}
}

func encodeParkCourierResult(t *testing.T, result sessionparkreceive.Result) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}
