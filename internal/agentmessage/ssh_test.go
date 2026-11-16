package agentmessage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestSSHRunnerCarriesBoundedInputOutputAndPipeDrainPolicy(t *testing.T) {
	t.Parallel()
	processRunner := runnertest.New(t)
	processRunner.ExpectArgv([]string{"ssh", "host", "wb"}, runner.Result{Stdout: "receipt", Stderr: "private diagnostics"}, nil)
	var stdout boundedOutput
	if err := runCommandWithRunner(context.Background(), processRunner, "ssh", []string{"host", "wb"}, strings.NewReader("message"), &stdout); err != nil {
		t.Fatal(err)
	}
	if string(stdout.Bytes()) != "receipt" {
		t.Fatal("unexpected output", string(stdout.Bytes()))
	}
	calls := processRunner.Calls()
	if len(calls) != 1 || calls[0].Op != "RunOpts" {
		t.Fatal("wrong runner operation", calls)
	}
	options := calls[0].Opts
	if string(options.Stdin) != "message" || options.WaitDelay != 3*time.Second || options.StdoutLimit != MaxEnvelopeBytes || !options.DiscardStderr || options.CaptureCombined || options.Env != nil {
		t.Fatalf("unsafe runner policy: %+v", options)
	}
}

func TestSSHRunnerRejectsInvalidInputWithoutStartingProcess(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]io.Reader{"unreadable": errorReader{}, "oversized": strings.NewReader(strings.Repeat("x", MaxEnvelopeBytes+1))} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			processRunner := runnertest.New(t)
			if err := runCommandWithRunner(context.Background(), processRunner, "ssh", nil, input, io.Discard); err == nil {
				t.Fatal("invalid command input accepted")
			}
			if processRunner.CallCount() != 0 {
				t.Fatal("process started with invalid input")
			}
		})
	}
}

type failingCommandOutput struct{ short bool }

func (writer failingCommandOutput) Write(_ []byte) (int, error) {
	if writer.short {
		return 0, nil
	}
	return 0, errors.New("output refused")
}

func TestSSHRunnerPreservesErrorsAndRejectsIncompleteReceiptWrites(t *testing.T) {
	t.Parallel()
	for _, short := range []bool{false, true} {
		name := "write-failed"
		if short {
			name = "short-write"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			processRunner := runnertest.New(t)
			processRunner.ExpectArgv([]string{"ssh"}, runner.Result{Stdout: "receipt"}, nil)
			err := runCommandWithRunner(context.Background(), processRunner, "ssh", nil, strings.NewReader("message"), failingCommandOutput{short: short})
			if err == nil || (short && !errors.Is(err, io.ErrShortWrite)) {
				t.Fatal("incomplete receipt write accepted", err)
			}
		})
	}
	processRunner := runnertest.New(t)
	processRunner.ExpectArgv([]string{"ssh"}, runner.Result{Stdout: "partial"}, runner.ErrOutputTooLarge)
	var stdout bytes.Buffer
	err := runCommandWithRunner(context.Background(), processRunner, "ssh", nil, strings.NewReader("message"), &stdout)
	if !errors.Is(err, runner.ErrOutputTooLarge) || stdout.String() != "partial" {
		t.Fatal("runner error or bounded output lost", err, stdout.String())
	}
}

func TestSSHRunnerPreservesRelayBatchOutputLimit(t *testing.T) {
	t.Parallel()
	const batchLimit = 100*MaxEnvelopeBytes + 4096
	// Larger than one envelope: relay results must not inherit the send cap.
	response := strings.Repeat("x", MaxEnvelopeBytes+1)
	for _, bounded := range []bool{true, false} {
		name := "relay-output"
		if !bounded {
			name = "unknown-writer-bounded-default"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			processRunner := runnertest.New(t)
			processRunner.ExpectArgv([]string{"ssh"}, runner.Result{Stdout: response}, nil)
			var batch boundedOutput
			batch.limit = batchLimit
			var output bytes.Buffer
			var destination io.Writer = &output
			if bounded {
				destination = &batch
			}
			if err := runCommandWithRunner(context.Background(), processRunner, "ssh", nil, strings.NewReader(""), destination); err != nil {
				t.Fatal(err)
			}
			if options := processRunner.Calls()[0].Opts; options.StdoutLimit != batchLimit || !options.DiscardStderr {
				t.Fatalf("unbounded or single-envelope relay policy: %+v", options)
			}
			got := output.String()
			if bounded {
				got = string(batch.Bytes())
			}
			if got != response {
				t.Fatal("relay output truncated")
			}
		})
	}
}

func TestUnconfirmedDeliveryClassifiesLocalFailuresWithoutPrivateErrorText(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		cause    error
		category string
	}{
		"missing from PATH":        {&exec.Error{Name: "private-executable", Err: exec.ErrNotFound}, "local SSH executable is unavailable"},
		"missing executable path":  {&os.PathError{Op: "fork/exec", Path: "private-path", Err: os.ErrNotExist}, "local SSH executable is unavailable"},
		"current directory lookup": {&exec.Error{Name: "private-executable", Err: exec.ErrDot}, "resolves through the current directory and was refused"},
		"execution denied":         {&os.PathError{Op: "fork/exec", Path: "private-path", Err: os.ErrPermission}, "local SSH execution was denied"},
		"oversized response":       {runner.ErrOutputTooLarge, "SSH response exceeded the output limit"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The wrapper itself is private too: neither it nor a path/name may leak.
			err := unconfirmedDelivery(context.Background(), errors.Join(errors.New("private-wrapper"), tc.cause))
			if !strings.Contains(err.Error(), tc.category) || !strings.Contains(err.Error(), "delivery is unconfirmed") || strings.Contains(err.Error(), "private-") {
				t.Fatal("unsafe or unhelpful diagnostic", err)
			}
		})
	}
}
