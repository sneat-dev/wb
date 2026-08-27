package sessionparkcourier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
)

type courierRunner struct {
	calls      int
	executable string
	args       []string
	stdin      []byte
	stdout     []byte
	stderr     []byte
	err        error
}

func (runner *courierRunner) Run(_ context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	runner.calls++
	runner.executable = executable
	runner.args = append([]string(nil), args...)
	runner.stdin = append([]byte(nil), stdin...)
	_, _ = stdout.Write(runner.stdout)
	_, _ = stderr.Write(runner.stderr)
	return runner.err
}

func TestSSHDelivererUsesFixedWBArgvAndCanonicalPrivateStdin(t *testing.T) {
	request, raw := courierEnvelope(t)
	runner := &courierRunner{stdout: courierResultJSON(t, request, raw)}
	deliverer := testSSHDeliverer(t, sessionmove.SSHConfig{Host: "target-vm", User: "ai"}, runner)
	result, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-l", "ai", "--", "target-vm", "wb", "--non-interactive", "session", "receive-park", "--format", "json"}
	if runner.calls != 1 || !reflect.DeepEqual(runner.args, wantArgs) || !bytes.Equal(runner.stdin, raw) {
		t.Fatalf("SSH invocation calls=%d args=%#v stdin_equal=%t", runner.calls, runner.args, bytes.Equal(runner.stdin, raw))
	}
	joined := strings.Join(runner.args, "\x00")
	if strings.Contains(joined, request.Continuation) || strings.Contains(joined, request.ResumeID) {
		t.Fatal("private envelope content appeared in SSH argv")
	}
	if result.Receipt.ResumeID != request.ResumeID || len(result.Receipt.Members) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

// Park always uses a fixed non-shell argv, so ssh.wb_path cannot apply. It is
// ignored rather than rejected, because an operator may legitimately have set
// it for session move -- but ignoring it silently would let the key read as
// configured while behaving as not, so the operator is warned.
func TestSSHDelivererIgnoresConfiguredRemoteWBPathWithWarning(t *testing.T) {
	request, raw := courierEnvelope(t)
	warn := new(bytes.Buffer)
	runner := &courierRunner{stdout: courierResultJSON(t, request, raw)}
	deliverer, err := newSSHDeliverer(sessionmove.SSHConfig{Host: "target", WBPath: "/tmp/wb"}, executableLookup(t), runner, Options{Warn: warn})
	if err != nil {
		t.Fatalf("configured ssh.wb_path must be ignored, not rejected: %v", err)
	}
	if _, err := deliverer.Deliver(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ssh.wb_path", "session park", "session move", "fixed non-shell argv"} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("warning %q is missing %q", warn.String(), want)
		}
	}
	joined := strings.Join(runner.args, "\x00")
	if strings.Contains(joined, "/tmp/wb") {
		t.Fatalf("ignored ssh.wb_path still reached argv: %#v", runner.args)
	}
	if !strings.Contains(joined, "\x00"+remoteWBCommand+"\x00") {
		t.Fatalf("argv did not use the fixed remote command: %#v", runner.args)
	}
}

// Remote stderr is kept, but never disclosed in terminal output or CI logs:
// sanitizing control characters makes text safe to print, not safe to
// disclose. It goes to the private local journal and only the path is shown.
func TestSSHDelivererWritesRemoteStderrToPrivateJournalWithoutDisclosing(t *testing.T) {
	request, raw := courierEnvelope(t)
	secret := request.Continuation
	diagnostic := "remote failure leaked " + secret
	journal := filepath.Join(t.TempDir(), "worklogs", "park-resumes", "park-1")
	runner := &courierRunner{stderr: []byte(diagnostic), err: errors.New("exit status 1")}
	deliverer, err := newSSHDeliverer(sessionmove.SSHConfig{Host: "target"}, executableLookup(t), runner, Options{DiagnosticDir: journal})
	if err != nil {
		t.Fatal(err)
	}
	_, err = deliverer.Deliver(context.Background(), raw)
	if err == nil {
		t.Fatal("failed delivery must error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "remote failure") {
		t.Fatalf("error disclosed remote stderr: %v", err)
	}
	path := filepath.Join(journal, remoteStderrFileName)
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error did not name the journal path: %v", err)
	}
	saved, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	// Full and unredacted on disk: the point is to keep the diagnostic.
	if string(saved) != diagnostic {
		t.Fatalf("journal did not retain unredacted remote stderr: %q", saved)
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal file permissions = %v (%v)", info.Mode().Perm(), statErr)
	}
}

// When the journal cannot be written the diagnostic is dropped, never dumped.
func TestSSHDelivererDegradesToSuppressedWhenJournalUnavailable(t *testing.T) {
	request, raw := courierEnvelope(t)
	secret := request.Continuation
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("regular file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"unset": "", "unwritable": filepath.Join(blocked, "park-1")} {
		t.Run(name, func(t *testing.T) {
			runner := &courierRunner{stderr: []byte("remote failure leaked " + secret), err: errors.New("exit status 1")}
			deliverer, err := newSSHDeliverer(sessionmove.SSHConfig{Host: "target"}, executableLookup(t), runner, Options{DiagnosticDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			_, err = deliverer.Deliver(context.Background(), raw)
			if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "remote failure") {
				t.Fatalf("unavailable journal disclosed remote stderr: %v", err)
			}
			if !strings.Contains(err.Error(), "journal unavailable") {
				t.Fatalf("error did not report suppression: %v", err)
			}
		})
	}
}

func TestSSHDelivererStrictlyBoundsAndValidatesReceiverResult(t *testing.T) {
	request, raw := courierEnvelope(t)
	valid := courierResultJSON(t, request, raw)
	for name, response := range map[string][]byte{
		"trailing JSON": append(append([]byte(nil), valid...), []byte("{}\n")...),
		"oversized":     bytes.Repeat([]byte("x"), maxSSHStdoutBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			runner := &courierRunner{stdout: response}
			deliverer := testSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
			if _, err := deliverer.Deliver(context.Background(), raw); err == nil {
				t.Fatal("invalid receiver response accepted")
			}
		})
	}
}

func testSSHDeliverer(t *testing.T, config sessionmove.SSHConfig, runner commandRunner) *SSHDeliverer {
	t.Helper()
	deliverer, err := newSSHDeliverer(config, executableLookup(t), runner, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return deliverer
}

func executableLookup(t *testing.T) func(string) (string, error) {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return func(string) (string, error) { return path, nil }
}

func courierEnvelope(t *testing.T) (sessionpark.RemoteRequest, []byte) {
	t.Helper()
	headA, headB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bundle := sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-courier", Continuation: "PRIVATE-COURIER-CONTEXT",
		ParkedAt: time.Unix(20, 0).UTC(),
		Source:   session.Record{PID: 42, WBSessionID: "wbs-source", Machine: "source", Runtime: "codex", Model: "gpt-5.6-luna", StartedAt: time.Unix(10, 0).UTC()},
		Worktrees: []sessionpark.Worktree{
			{Repository: "acme/alpha", RepositoryRemote: "https://github.com/acme/alpha.git", WorktreeDir: "/source/alpha", Branch: "feature/alpha", Head: headA, RemoteHead: headA, WorkLogReference: "worklog:park/run/" + strings.Repeat("c", 64)},
			{Repository: "acme/beta", RepositoryRemote: "https://github.com/acme/beta.git", WorktreeDir: "/source/beta", Branch: "feature/beta", Head: headB, RemoteHead: headB, WorkLogReference: "worklog:park/run/" + strings.Repeat("d", 64)},
		},
	}
	request := sessionpark.BuildRemoteRequest(bundle, "target", "", time.Unix(30, 0).UTC())
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	return request, raw
}

func courierResultJSON(t *testing.T, request sessionpark.RemoteRequest, raw []byte) []byte {
	t.Helper()
	digest := sessionmove.DigestBytes(raw)
	receipt := sessionpark.Receipt{
		SchemaVersion: sessionpark.ReceiptSchemaVersion, ResumeID: request.ResumeID, RequestDigest: digest,
		ParkedSessionID: request.ParkedSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
		TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: "codex", Model: request.SourceModel,
		AttemptID: "000001-" + strings.Repeat("e", 32), AttemptIndex: 1, PID: 123, StartedAt: time.Unix(40, 0).UTC(),
		Members: make([]sessionpark.ReceiptMember, len(request.Members)),
	}
	for index, member := range request.Members {
		reference, err := sessionpark.TargetWorkLogReference(request, digest, member)
		if err != nil {
			t.Fatal(err)
		}
		receipt.Members[index] = sessionpark.ReceiptMember{MemberID: member.MemberID, Repository: member.Repository,
			TargetPath: "/target/" + member.MemberID, Pin: sessionpark.MemberPin(request.ResumeID, member.MemberID),
			Commit: member.Commit, TargetWorkLogReference: reference}
	}
	result := sessionparkreceive.Result{ResumeID: request.ResumeID, Digest: digest, Phase: sessionparkreceive.PhaseCompleted, Receipt: &receipt}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}
