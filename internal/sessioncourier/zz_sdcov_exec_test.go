package sessioncourier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

// sdCov exercises the production exec seam without depending on a real ssh or
// synchestra binary. It copies the test binary into a temp directory under the
// executable name the courier resolves (ssh / synchestra) and re-enters
// TestMain in "fake executable" mode, where it replays one scripted response.
// The technique is portable to every platform Go tests run on: no shell script
// or platform-specific binary is required.
const (
	sdCovFakeExecModeEnv      = "SDCOV_FAKE_EXEC_MODE"
	sdCovFakeExecStdoutEnv    = "SDCOV_FAKE_EXEC_STDOUT"
	sdCovFakeExecStderrEnv    = "SDCOV_FAKE_EXEC_STDERR"
	sdCovFakeExecExitEnv      = "SDCOV_FAKE_EXEC_EXIT"
	sdCovFakeExecStdinFileEnv = "SDCOV_FAKE_EXEC_STDIN_FILE"
)

func TestMain(m *testing.M) {
	if os.Getenv(sdCovFakeExecModeEnv) != "" {
		os.Exit(sdCovFakeExecProcess())
	}
	os.Exit(m.Run())
}

func sdCovFakeExecProcess() int {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 93
	}
	if path := os.Getenv(sdCovFakeExecStdinFileEnv); path != "" {
		if err := os.WriteFile(path, input, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 91
		}
	}
	if path := os.Getenv(sdCovFakeExecStdoutEnv); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 90
		}
		if _, err := os.Stdout.Write(data); err != nil {
			return 92
		}
	}
	if text := os.Getenv(sdCovFakeExecStderrEnv); text != "" {
		fmt.Fprint(os.Stderr, text)
	}
	if code := os.Getenv(sdCovFakeExecExitEnv); code != "" {
		parsed, err := strconv.Atoi(code)
		if err == nil && parsed != 0 {
			return parsed
		}
	}
	return 0
}

// sdCovUseFakeExecutable makes exec.LookPath(name) resolve to a runnable copy
// of this test binary for the duration of the calling test.
func sdCovUseFakeExecutable(t *testing.T, name string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if err := os.WriteFile(file, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sdCovFakeExecModeEnv, "1")
	t.Setenv("PATH", dir)
}

func sdCovWriteTempFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "response")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSDCovDeliverSSHThroughResolvedExecutable(t *testing.T) {
	sdCovUseFakeExecutable(t, "ssh")
	request, raw := courierTestRequest(t)
	t.Setenv(sdCovFakeExecStdoutEnv, sdCovWriteTempFile(t, encodeCourierResult(t, validCourierResult(request, raw))))
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	t.Setenv(sdCovFakeExecStdinFileEnv, stdinPath)

	result, err := DeliverSSH(context.Background(), sessionmove.SSHConfig{Host: "hetzner-vm1", WBPath: "/opt/wb/bin/wb"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, validCourierResult(request, raw)) {
		t.Fatalf("DeliverSSH result = %#v", result)
	}
	delivered, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(delivered, raw) {
		t.Fatalf("exec runner stdin = %q, want exact request %q", delivered, raw)
	}
}

func TestSDCovDeliverSSHFailureBranchesThroughResolvedExecutable(t *testing.T) {
	sdCovUseFakeExecutable(t, "ssh")
	_, raw := courierTestRequest(t)

	t.Run("already cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := DeliverSSH(ctx, sessionmove.SSHConfig{Host: "target"}, raw)
		if err == nil || !strings.Contains(err.Error(), "ssh session delivery to target") || !errors.Is(err, context.Canceled) {
			t.Fatalf("DeliverSSH error = %v, want wrapped context cancellation", err)
		}
	})
	t.Run("silent process failure", func(t *testing.T) {
		t.Setenv(sdCovFakeExecExitEnv, "3")
		_, err := DeliverSSH(context.Background(), sessionmove.SSHConfig{Host: "target"}, raw)
		if err == nil || err.Error() != "ssh session delivery to target: exit status 3" {
			t.Fatalf("DeliverSSH error = %v, want undecorated exit status", err)
		}
	})
	t.Run("stderr diagnostic", func(t *testing.T) {
		t.Setenv(sdCovFakeExecExitEnv, "255")
		t.Setenv(sdCovFakeExecStderrEnv, "Permission denied (publickey).\n")
		_, err := DeliverSSH(context.Background(), sessionmove.SSHConfig{Host: "target"}, raw)
		if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey).") ||
			strings.ContainsAny(err.Error(), "\r\n") {
			t.Fatalf("DeliverSSH error = %v, want sanitized single-line diagnostic", err)
		}
	})
}

func TestSDCovNewSSHMessageDelivererThroughResolvedExecutable(t *testing.T) {
	sdCovUseFakeExecutable(t, "ssh")
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(sdCovFakeExecStdoutEnv, sdCovWriteTempFile(t, receiptRaw))

	deliverer, err := NewSSHMessageDeliverer(sessionmove.SSHConfig{Host: "hetzner-vm1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := deliverer.DeliverMessage(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != receipt {
		t.Fatalf("DeliverMessage = %#v, want %#v", got, receipt)
	}
}

func TestSDCovNewSynchestraDelivererThroughResolvedExecutable(t *testing.T) {
	sdCovUseFakeExecutable(t, "synchestra")
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	artifact := encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes)
	output := encodeSynchestraInvocationOutput(t, request, raw, "dsp_exec", "completed", artifact)
	t.Setenv(sdCovFakeExecStdoutEnv, sdCovWriteTempFile(t, output))

	var saved []sessionmove.SynchestraDispatch
	deliverer, err := NewSynchestraDeliverer(sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{
		SaveDispatch: func(identity sessionmove.SynchestraDispatch) error {
			saved = append(saved, identity)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, validCourierResult(request, raw)) {
		t.Fatalf("Synchestra Deliver result = %#v", result)
	}
	if len(saved) != 1 || saved[0].DispatchID != "dsp_exec" || saved[0].Runner != "hetzner-vm1" {
		t.Fatalf("persisted dispatch = %#v", saved)
	}
}

func TestSDCovNewSynchestraMessageDelivererThroughResolvedExecutable(t *testing.T) {
	sdCovUseFakeExecutable(t, "synchestra")
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	artifact := encodeSynchestraMessageReceiptArtifact(t, message, raw, receiptRaw)
	output := encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_msg_exec", "completed", artifact)
	t.Setenv(sdCovFakeExecStdoutEnv, sdCovWriteTempFile(t, output))

	var saved []sessionmove.MessageSynchestraDispatch
	deliverer, err := NewSynchestraMessageDeliverer(sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
		MessageSynchestraOptions{SaveDispatch: func(identity sessionmove.MessageSynchestraDispatch) error {
			saved = append(saved, identity)
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := deliverer.DeliverMessage(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != receipt {
		t.Fatalf("DeliverMessage = %#v, want %#v", got, receipt)
	}
	if len(saved) != 1 || saved[0].DispatchID != "dsp_msg_exec" || saved[0].MessageID != message.MessageID {
		t.Fatalf("persisted message dispatch = %#v", saved)
	}
}

// TestSDCovMessageTransportRecordsAcceptDispatch drives the accept-handler
// transport embedded in a message deliverer. The transport's own durable
// recorder is the no-op closure installed by newSynchestraMessageDeliverer, so
// a completed accept dispatch must be accepted without writing anything.
func TestSDCovMessageTransportRecordsAcceptDispatch(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	artifact := encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes)
	output := encodeSynchestraInvocationOutput(t, request, raw, "dsp_transport", "completed", artifact)
	runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: output}}}
	deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
		MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
		runner, func(context.Context, time.Duration) error { return nil })

	result, err := deliverer.transport.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, validCourierResult(request, raw)) {
		t.Fatalf("transport Deliver result = %#v", result)
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args,
		[]string{"runner", "invoke", "@/dev/stdin", "--runner", "hetzner-vm1",
			"--handler", synchestraSessionAcceptHandler, "--invocation-id", request.HandoffID, "--format", "json"}) {
		t.Fatalf("transport calls = %#v", runner.calls)
	}
}
