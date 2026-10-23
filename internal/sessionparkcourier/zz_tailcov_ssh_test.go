package sessionparkcourier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestTailCovNewSSHDelivererRejectsUnusableResolutions(t *testing.T) {
	t.Parallel()
	validConfig := sessionmove.SSHConfig{Host: "target"}
	runner := &courierRunner{}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("invalid config", func(t *testing.T) {
		t.Parallel()
		if _, err := newSSHDeliverer(sessionmove.SSHConfig{Host: "-bad"}, executableLookup(t), runner, Options{}); err == nil || !strings.Contains(err.Error(), "ssh.host") {
			t.Fatalf("err = %v, want the invalid ssh.host to be rejected", err)
		}
	})
	t.Run("no executable lookup", func(t *testing.T) {
		t.Parallel()
		if _, err := newSSHDeliverer(validConfig, nil, runner, Options{}); err == nil || !strings.Contains(err.Error(), "executable lookup is unavailable") {
			t.Fatalf("err = %v, want the missing lookup to be reported", err)
		}
	})
	t.Run("lookup fails", func(t *testing.T) {
		t.Parallel()
		fail := func(string) (string, error) { return "", errors.New("no ssh on this host") }
		if _, err := newSSHDeliverer(validConfig, fail, runner, Options{}); err == nil || !strings.Contains(err.Error(), "no ssh on this host") {
			t.Fatalf("err = %v, want the lookup failure to be wrapped", err)
		}
	})
	t.Run("relative executable", func(t *testing.T) {
		t.Parallel()
		relative := func(string) (string, error) { return "ssh", nil }
		if _, err := newSSHDeliverer(validConfig, relative, runner, Options{}); err == nil || !strings.Contains(err.Error(), "not one clean absolute path") {
			t.Fatalf("err = %v, want a relative executable to be rejected", err)
		}
	})
	t.Run("unclean executable", func(t *testing.T) {
		t.Parallel()
		unclean := func(string) (string, error) { return "/usr/bin/../bin/ssh", nil }
		if _, err := newSSHDeliverer(validConfig, unclean, runner, Options{}); err == nil || !strings.Contains(err.Error(), "not one clean absolute path") {
			t.Fatalf("err = %v, want an unclean executable to be rejected", err)
		}
	})
	t.Run("missing executable", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "ssh")
		lookup := func(string) (string, error) { return missing, nil }
		if _, err := newSSHDeliverer(validConfig, lookup, runner, Options{}); err == nil || !strings.Contains(err.Error(), "not one executable regular file") {
			t.Fatalf("err = %v, want a missing executable to be rejected", err)
		}
	})
	t.Run("non-executable file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "ssh")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		lookup := func(string) (string, error) { return path, nil }
		if _, err := newSSHDeliverer(validConfig, lookup, runner, Options{}); err == nil || !strings.Contains(err.Error(), "not one executable regular file") {
			t.Fatalf("err = %v, want a non-executable file to be rejected", err)
		}
	})
	t.Run("no runner", func(t *testing.T) {
		t.Parallel()
		if _, err := newSSHDeliverer(validConfig, func(string) (string, error) { return executable, nil }, nil, Options{}); err == nil || !strings.Contains(err.Error(), "command runner is unavailable") {
			t.Fatalf("err = %v, want the missing runner to be reported", err)
		}
	})
}

// TestTailCovNewSSHDelivererDrivesTheRealExecRunner exercises the production
// executable resolution and process runner end to end: it puts a fake `ssh` on
// PATH that journals its stdin and answers with a valid receiver result, then
// asserts the courier resolved that binary, forwarded the exact canonical
// envelope bytes on stdin, and parsed the receipt.
func TestTailCovNewSSHDelivererDrivesTheRealExecRunner(t *testing.T) {
	request, raw := courierEnvelope(t)
	dir := t.TempDir()
	result := filepath.Join(dir, "result.json")
	if err := os.WriteFile(result, courierResultJSON(t, request, raw), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "ssh")
	body := "#!/bin/sh\ncat > \"$0.stdin\"\ncat '" + result + "'\n"
	if err := testenv.WriteExecutableFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	warn := new(bytes.Buffer)
	deliverer, err := NewSSHDeliverer(sessionmove.SSHConfig{Host: "target-vm", User: "ai", WBPath: "/opt/wb"}, Options{Warn: warn})
	if err != nil {
		t.Fatal(err)
	}
	if deliverer.executable != script {
		t.Fatalf("resolved executable = %q, want the fake ssh %q", deliverer.executable, script)
	}
	got, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt.ResumeID != request.ResumeID || len(got.Receipt.Members) != len(request.Members) {
		t.Fatalf("receipt = %#v, want the delivered request's reply", got.Receipt)
	}
	if !strings.Contains(warn.String(), "ssh.wb_path") || !strings.Contains(warn.String(), "session park") {
		t.Fatalf("warning = %q, want the ignored ssh.wb_path reported", warn.String())
	}
	forwarded, err := os.ReadFile(script + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(forwarded, raw) {
		t.Fatalf("stdin carried %d bytes, want the exact canonical envelope (%d bytes)", len(forwarded), len(raw))
	}
}

// TestTailCovExecCommandRunnerPipesStdinAndReportsExitStatus pins the one
// runner that actually launches a process, which the fake-runner tests never
// touch.
func TestTailCovExecCommandRunnerPipesStdinAndReportsExitStatus(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("this platform has no sh: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner := execCommandRunner{}
	if err := runner.Run(context.Background(), shell, []string{"-c", "cat"}, []byte("canonical envelope"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "canonical envelope" {
		t.Fatalf("stdout = %q, want the stdin payload", stdout.String())
	}
	stdout.Reset()
	if err := runner.Run(context.Background(), shell, []string{"-c", "cat >/dev/null; exit 7"}, []byte("ignored"), &stdout, &stderr); err == nil {
		t.Fatal("a non-zero child exit was reported as success")
	}
}

func TestTailCovDeliverRejectsUnusableEnvelopes(t *testing.T) {
	t.Parallel()
	_, raw := courierEnvelope(t)
	deliverer := testSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, &courierRunner{})

	t.Run("not an envelope", func(t *testing.T) {
		t.Parallel()
		if _, err := deliverer.Deliver(context.Background(), []byte("{not json")); err == nil || !strings.Contains(err.Error(), "validate SSH parked-session envelope") {
			t.Fatalf("err = %v, want a malformed envelope to be rejected", err)
		}
	})
	t.Run("not canonical", func(t *testing.T) {
		t.Parallel()
		var envelope sessionpark.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		compact, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deliverer.Deliver(context.Background(), compact); err == nil || !strings.Contains(err.Error(), "canonical JSON encoding") {
			t.Fatalf("err = %v, want a non-canonical envelope to be rejected", err)
		}
	})
}

func TestTailCovDeliverReportsCancelledDeliveryContext(t *testing.T) {
	t.Parallel()
	_, raw := courierEnvelope(t)
	runner := &courierRunner{err: errors.New("exit status 1")}
	deliverer := testSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := deliverer.Deliver(ctx, raw); err == nil || !strings.Contains(err.Error(), "SSH parked-session delivery failed") {
		t.Fatalf("err = %v, want the cancelled delivery context reported instead of the child error", err)
	}
}

func TestTailCovDeliverRejectsMismatchedOrInvalidReceipts(t *testing.T) {
	t.Parallel()
	request, raw := courierEnvelope(t)
	digest := sessionmove.DigestBytes(raw)

	t.Run("incomplete phase", func(t *testing.T) {
		t.Parallel()
		body, err := json.Marshal(sessionparkreceive.Result{ResumeID: request.ResumeID, Digest: digest, Phase: sessionparkreceive.PhaseReceived})
		if err != nil {
			t.Fatal(err)
		}
		deliverer := testSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, &courierRunner{stdout: body})
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "exact completed target receipt") {
			t.Fatalf("err = %v, want an incomplete response to be rejected", err)
		}
	})
	t.Run("conflicting receipt", func(t *testing.T) {
		t.Parallel()
		body, err := json.Marshal(sessionparkreceive.Result{
			ResumeID: request.ResumeID, Digest: digest, Phase: sessionparkreceive.PhaseCompleted, Receipt: &sessionpark.Receipt{},
		})
		if err != nil {
			t.Fatal(err)
		}
		deliverer := testSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, &courierRunner{stdout: body})
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "validate SSH parked-session response") {
			t.Fatalf("err = %v, want a receipt that conflicts with the request to be rejected", err)
		}
	})
}

func TestTailCovDecodeReceiverResultRejectsMalformedResponses(t *testing.T) {
	t.Parallel()
	cases := map[string][]byte{
		"unknown field":      []byte(`{"unexpected":true}`),
		"syntax error":       []byte(`{"resume_id":`),
		"trailing bad value": append([]byte(`{"resume_id":"x"}`), []byte(`{"oops`)...),
		"trailing value":     append([]byte(`{"resume_id":"x"}`), []byte(`{}`)...),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeReceiverResult(raw); err == nil {
				t.Fatalf("decodeReceiverResult(%q) was accepted", raw)
			}
		})
	}
}

func TestTailCovBoundedBufferDropsEverythingPastItsLimit(t *testing.T) {
	t.Parallel()
	buffer := boundedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcd")); n != 4 || err != nil || buffer.exceeded {
		t.Fatalf("first write = (%d, %v), exceeded=%t", n, err, buffer.exceeded)
	}
	if n, err := buffer.Write([]byte("xy")); n != 2 || err != nil || !buffer.exceeded {
		t.Fatalf("second write = (%d, %v), exceeded=%t", n, err, buffer.exceeded)
	}
	if string(buffer.Bytes()) != "abcd" {
		t.Fatalf("buffer = %q, want only the first 4 bytes retained", buffer.Bytes())
	}
}

// TestTailCovDeliverSuppressesStderrWhenJournalCannotBeWritten drives the
// journal-write failure through the public delivery path: the diagnostic must
// be dropped rather than disclosed when the journal path is unusable.
func TestTailCovDeliverSuppressesStderrWhenJournalCannotBeWritten(t *testing.T) {
	t.Parallel()
	request, raw := courierEnvelope(t)
	secret := request.Continuation
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, remoteStderrFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &courierRunner{stderr: []byte("leaked " + secret), err: errors.New("exit status 1")}
	deliverer, err := newSSHDeliverer(sessionmove.SSHConfig{Host: "target"}, executableLookup(t), runner, Options{DiagnosticDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = deliverer.Deliver(context.Background(), raw)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "leaked") {
		t.Fatalf("err = %v, want the diagnostic withheld", err)
	}
	if !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("err = %v, want the unusable journal to be reported", err)
	}
}
