package sessioncourier

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSDCovNewSynchestraDelivererConstructorBranches(t *testing.T) {
	t.Parallel()
	executable := testExecutable(t)
	missing := filepath.Join(t.TempDir(), "missing-synchestra")
	plain := filepath.Join(t.TempDir(), "plain-synchestra")
	if err := os.WriteFile(plain, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	recorder := func(sessionmove.SynchestraDispatch) error { return nil }

	tests := map[string]struct {
		config   sessionmove.SynchestraConfig
		options  SynchestraOptions
		lookPath func(string) (string, error)
		runner   commandRunner
		sleep    func(context.Context, time.Duration) error
		want     string
	}{
		"invalid config": {
			config: sessionmove.SynchestraConfig{}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return executable, nil }, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "runner is required",
		},
		"nil lookup": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: nil, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "executable lookup is unavailable",
		},
		"lookup failure": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return "", errors.New("not found") }, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "resolve synchestra executable",
		},
		"relative resolution": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return "synchestra", nil }, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "clean absolute",
		},
		"unstattable resolution": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return missing, nil }, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "resolve synchestra executable",
		},
		"non-executable file": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return plain, nil }, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "regular executable",
		},
		"directory": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return directory, nil }, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "regular executable",
		},
		"nil runner": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return executable, nil }, runner: nil, sleep: sdCovNoSleep,
			want: "command runner is unavailable",
		},
		"nil clock": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{SaveDispatch: recorder},
			lookPath: func(string) (string, error) { return executable, nil }, runner: &scriptedCommandRunner{}, sleep: nil,
			want: "clock is unavailable",
		},
		"no dispatch recorder": {
			config: sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, options: SynchestraOptions{},
			lookPath: func(string) (string, error) { return executable, nil }, runner: &scriptedCommandRunner{}, sleep: sdCovNoSleep,
			want: "durable dispatch recorder is unavailable",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			deliverer, err := newSynchestraDeliverer(test.config, test.options, test.lookPath, test.runner, test.sleep)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("newSynchestraDeliverer error = %v, want containing %q", err, test.want)
			}
			if deliverer != nil {
				t.Fatalf("newSynchestraDeliverer returned %#v on error", deliverer)
			}
		})
	}
}

func TestSDCovSleepWithContextBranches(t *testing.T) {
	t.Parallel()
	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("zero-delay sleep error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleepWithContext(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleep error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelled sleep waited %s", elapsed)
	}

	expiring, expire := context.WithTimeout(context.Background(), 5*time.Millisecond)
	t.Cleanup(func() { expire() })
	if err := sleepWithContext(expiring, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expiring sleep error = %v", err)
	}
}

func TestSDCovSynchestraDeliverFailureBranches(t *testing.T) {
	request, raw := courierTestRequest(t)
	completed := func(dispatchID string) []byte {
		return encodeSynchestraInvocationOutput(t, request, raw, dispatchID, "completed",
			encodeSynchestraReceiptArtifact(t, request, raw, encodeCourierResult(t, validCourierResult(request, raw))))
	}
	resumeIdentity := func() sessionmove.SynchestraDispatch {
		return sessionmove.SynchestraDispatch{
			SchemaVersion: sessionmove.SynchestraDispatchSchemaVersion,
			HandoffID:     request.HandoffID, RequestDigest: sessionmove.DigestBytes(raw), Runner: "hetzner-vm1",
			InvocationID: request.HandoffID, Handler: synchestraSessionAcceptHandler, DispatchID: "dsp_resume_branch",
		}
	}

	t.Run("request refusal", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedCommandRunner{}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), []byte("not-json")); err == nil ||
			!strings.Contains(err.Error(), "validate synchestra session request") {
			t.Fatalf("Deliver error = %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("commands = %d after local refusal", len(runner.calls))
		}
	})
	t.Run("durable recorder failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: completed("dsp_save")}}}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{
			SaveDispatch: func(sessionmove.SynchestraDispatch) error { return errors.New("disk full") },
		}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "persist synchestra dispatch identity: disk full") {
			t.Fatalf("Deliver error = %v", err)
		}
	})
	t.Run("resume identity mismatch", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedCommandRunner{}
		dispatch := resumeIdentity()
		dispatch.Runner = "other-runner"
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			SynchestraOptions{Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "validate persisted synchestra dispatch identity") {
			t.Fatalf("Deliver error = %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("commands = %d after identity refusal", len(runner.calls))
		}
	})
	t.Run("resume command failure", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{err: errors.New("exit status 5")}}}
		dispatch := resumeIdentity()
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			SynchestraOptions{Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "observe dispatch") {
			t.Fatalf("Deliver error = %v", err)
		}
	})
	t.Run("resume response decode failure", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: []byte("not-json")}}}
		dispatch := resumeIdentity()
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			SynchestraOptions{Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "validate synchestra status response") {
			t.Fatalf("Deliver error = %v", err)
		}
	})
	t.Run("resume response identity mismatch", func(t *testing.T) {
		t.Parallel()
		status := mutateSynchestraOutput(t, encodeSynchestraStatusOutput(t, request, raw, "dsp_resume_branch", "queued", ""),
			func(output *synchestraInvocationOutput) { output.Resolved.Runner = "other-runner" })
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: status}}}
		dispatch := resumeIdentity()
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			SynchestraOptions{Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "validate synchestra status response") {
			t.Fatalf("Deliver error = %v", err)
		}
	})
	t.Run("cancelled delivery", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{err: errors.New("signal: killed")}}}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner, sdCovNoSleep)
		_, err := deliverer.Deliver(ctx, raw)
		if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "synchestra session delivery") {
			t.Fatalf("Deliver error = %v, want wrapped cancellation", err)
		}
	})
	t.Run("invoke stderr diagnostic", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
			stderr: []byte("runner unavailable\n"), err: errors.New("exit status 3"),
		}}}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner, sdCovNoSleep)
		_, err := deliverer.Deliver(context.Background(), raw)
		if err == nil || err.Error() != "synchestra invoke handler: exit status 3: runner unavailable" {
			t.Fatalf("Deliver error = %v", err)
		}
	})
	t.Run("poll sleep failure", func(t *testing.T) {
		t.Parallel()
		queued := encodeSynchestraInvocationOutput(t, request, raw, "dsp_sleep", "queued", "")
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: queued}}}
		sleepErr := errors.New("clock unavailable")
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner,
			func(context.Context, time.Duration) error { return sleepErr })
		if _, err := deliverer.Deliver(context.Background(), raw); !errors.Is(err, sleepErr) {
			t.Fatalf("Deliver error = %v, want sleep failure", err)
		}
	})
	t.Run("poll command failure", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
			{stdout: encodeSynchestraInvocationOutput(t, request, raw, "dsp_poll_run", "queued", "")},
			{err: errors.New("exit status 6")},
		}}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "observe dispatch") {
			t.Fatalf("Deliver error = %v", err)
		}
	})
	t.Run("poll decode failure", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
			{stdout: encodeSynchestraInvocationOutput(t, request, raw, "dsp_poll_decode", "queued", "")},
			{stdout: []byte("not-json")},
		}}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "validate synchestra status response") {
			t.Fatalf("Deliver error = %v", err)
		}
	})
	t.Run("poll identity failure", func(t *testing.T) {
		t.Parallel()
		poll := mutateSynchestraOutput(t, encodeSynchestraStatusOutput(t, request, raw, "dsp_poll_id", "queued", ""),
			func(output *synchestraInvocationOutput) { output.Resolved.Runner = "other-runner" })
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
			{stdout: encodeSynchestraInvocationOutput(t, request, raw, "dsp_poll_id", "queued", "")},
			{stdout: poll},
		}}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner, sdCovNoSleep)
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "validate synchestra status response") {
			t.Fatalf("Deliver error = %v", err)
		}
	})
}

func TestSDCovDecodeSynchestraInvocationOutputBranches(t *testing.T) {
	t.Parallel()
	if _, err := decodeSynchestraInvocationOutput([]byte(`{"resolved":{}} {}`)); err == nil ||
		!strings.Contains(err.Error(), "trailing JSON value") {
		t.Fatalf("trailing value error = %v", err)
	}
	if _, err := decodeSynchestraInvocationOutput([]byte(`{"resolved":{}} {`)); err == nil ||
		!strings.Contains(err.Error(), "decode trailing runner invocation output") {
		t.Fatalf("trailing decode error = %v", err)
	}
	if _, err := decodeSynchestraInvocationOutput([]byte(`{"error":{"code":"x","message":"y"}}`)); err == nil ||
		!strings.Contains(err.Error(), "reported an error") {
		t.Fatalf("runner error field = %v", err)
	}
	request, raw := courierTestRequest(t)
	valid := encodeSynchestraInvocationOutput(t, request, raw, "dsp_decode", "queued", "")
	output, err := decodeSynchestraInvocationOutput(valid)
	if err != nil || output.Dispatch == nil || output.Resolved.Invocation == nil {
		t.Fatalf("valid decode = %#v err=%v", output, err)
	}
}

func sdCovSynchestraInvocationOutput(t *testing.T, request sessionmove.Request, raw []byte, dispatchID, status, artifact string) synchestraInvocationOutput {
	t.Helper()
	encoded := encodeSynchestraInvocationOutput(t, request, raw, dispatchID, status, artifact)
	var output synchestraInvocationOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestSDCovValidateSynchestraInvocationOutputBranches(t *testing.T) {
	t.Parallel()
	request, raw := courierTestRequest(t)
	baseline := sdCovSynchestraInvocationOutput(t, request, raw, "dsp_valid", "queued", "")

	tests := map[string]struct {
		mutate func(*synchestraInvocationOutput)
		want   string
	}{
		"missing dispatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Dispatch = nil },
			want:   "lacks typed invocation",
		},
		"missing invocation": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation = nil },
			want:   "lacks typed invocation",
		},
		"unsupported dispatch protocol": {
			mutate: func(output *synchestraInvocationOutput) { output.Dispatch.ProtocolVersion = "v0" },
			want:   "unsupported",
		},
		"invalid dispatch id": {
			mutate: func(output *synchestraInvocationOutput) { output.Dispatch.ID = "-bad" },
			want:   "dispatch id is invalid",
		},
		"resolved operation mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Operation = "status" },
			want:   "resolved runner or dispatch identity",
		},
		"resolved runner mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Runner = "other-runner" },
			want:   "resolved runner or dispatch identity",
		},
		"resolved source present": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Source = &struct{}{} },
			want:   "resolved runner or dispatch identity",
		},
		"resolved requested execution present": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.RequestedExecution = &struct{}{} },
			want:   "resolved runner or dispatch identity",
		},
		"invocation protocol mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.ProtocolVersion = "v0" },
			want:   "typed invocation identity",
		},
		"invocation id mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.ID = "other-handoff" },
			want:   "typed invocation identity",
		},
		"invocation handler mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.Handler = "other.handler" },
			want:   "typed invocation identity",
		},
		"invocation payload digest mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.PayloadDigest = "sha256:other" },
			want:   "typed invocation identity",
		},
		"invocation payload size mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.PayloadSize++ },
			want:   "typed invocation identity",
		},
		"invocation deadline present": {
			mutate: func(output *synchestraInvocationOutput) {
				deadline := time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC)
				output.Resolved.Invocation.Deadline = &deadline
			},
			want: "typed invocation identity",
		},
		"attempt identity mismatch": {
			mutate: func(output *synchestraInvocationOutput) {
				output.Dispatch.AttemptIDs = []string{"att_1"}
				output.Attempts = []synchestraAttemptOutput{{
					ProtocolVersion: "v0", ID: "att_1", DispatchID: "dsp_valid", Number: 1, Status: "queued",
				}}
			},
			want: "attempt identity does not match dispatch",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			output := sdCovCloneInvocationOutput(t, baseline)
			test.mutate(&output)
			_, err := validateSynchestraInvocationOutput(output, "hetzner-vm1", request, raw, "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("expected dispatch mismatch", func(t *testing.T) {
		t.Parallel()
		output := sdCovCloneInvocationOutput(t, baseline)
		_, err := validateSynchestraInvocationOutput(output, "hetzner-vm1", request, raw, "dsp_expected")
		if err == nil || !strings.Contains(err.Error(), "does not match persisted dispatch") {
			t.Fatalf("validate error = %v", err)
		}
	})
	t.Run("status route resolves", func(t *testing.T) {
		t.Parallel()
		output := sdCovSynchestraInvocationOutput(t, request, raw, "dsp_status", "queued", "")
		output.Resolved.Operation = "status"
		output.Resolved.DispatchID = "dsp_status"
		identity, err := validateSynchestraInvocationOutput(output, "hetzner-vm1", request, raw, "dsp_status")
		if err != nil {
			t.Fatal(err)
		}
		if identity.DispatchID != "dsp_status" || identity.HandoffID != request.HandoffID ||
			identity.RequestDigest != sessionmove.DigestBytes(raw) || identity.Handler != synchestraSessionAcceptHandler {
			t.Fatalf("identity = %#v", identity)
		}
	})
}

func sdCovAttempt(ids []string, attempts []synchestraAttemptOutput, active string) synchestraDispatchOutput {
	return synchestraDispatchOutput{
		ProtocolVersion: synchestraDispatchProtocolVersion, ID: "dsp_history", Status: "running",
		AttemptIDs: ids, ActiveAttemptID: active,
	}
}

func TestSDCovValidateSynchestraAttemptHistoryBranches(t *testing.T) {
	t.Parallel()
	queued := func(id string, number int) synchestraAttemptOutput {
		return synchestraAttemptOutput{
			ProtocolVersion: synchestraDispatchProtocolVersion, ID: id, DispatchID: "dsp_history",
			Number: number, Status: "queued",
		}
	}
	tests := map[string]struct {
		dispatch synchestraDispatchOutput
		attempts []synchestraAttemptOutput
		want     string
	}{
		"empty attempt id": {
			dispatch: sdCovAttempt([]string{""}, []synchestraAttemptOutput{queued("", 1)}, ""),
			attempts: []synchestraAttemptOutput{queued("", 1)},
			want:     "empty attempt id",
		},
		"duplicate declared id": {
			dispatch: sdCovAttempt([]string{"a", "a"}, []synchestraAttemptOutput{queued("a", 1), queued("b", 2)}, ""),
			attempts: []synchestraAttemptOutput{queued("a", 1), queued("b", 2)},
			want:     "duplicate attempt id",
		},
		"duplicate returned id": {
			dispatch: sdCovAttempt([]string{"a", "b"}, []synchestraAttemptOutput{queued("a", 1), queued("a", 2)}, ""),
			attempts: []synchestraAttemptOutput{queued("a", 1), queued("a", 2)},
			want:     "duplicate id",
		},
		"invalid number": {
			dispatch: sdCovAttempt([]string{"a"}, []synchestraAttemptOutput{queued("a", 0)}, ""),
			attempts: []synchestraAttemptOutput{queued("a", 0)},
			want:     "invalid number",
		},
		"duplicate number": {
			dispatch: sdCovAttempt([]string{"a", "b"}, []synchestraAttemptOutput{queued("a", 1), queued("b", 1)}, ""),
			attempts: []synchestraAttemptOutput{queued("a", 1), queued("b", 1)},
			want:     "duplicate number",
		},
		"unsupported status": {
			dispatch: sdCovAttempt([]string{"a"}, []synchestraAttemptOutput{queued("a", 1)}, ""),
			attempts: []synchestraAttemptOutput{{
				ProtocolVersion: synchestraDispatchProtocolVersion, ID: "a", DispatchID: "dsp_history", Number: 1, Status: "weird",
			}},
			want: "unsupported status",
		},
		"active attempt absent": {
			dispatch: sdCovAttempt([]string{"a"}, []synchestraAttemptOutput{queued("a", 1)}, "z"),
			attempts: []synchestraAttemptOutput{queued("a", 1)},
			want:     "active attempt",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateSynchestraAttemptHistory(test.dispatch, test.attempts)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %v, want containing %q", err, test.want)
			}
		})
	}
	t.Run("valid history", func(t *testing.T) {
		t.Parallel()
		attempts := []synchestraAttemptOutput{queued("a", 1), queued("b", 2)}
		if err := validateSynchestraAttemptHistory(sdCovAttempt([]string{"a", "b"}, attempts, "a"), attempts); err != nil {
			t.Fatalf("valid history error = %v", err)
		}
	})
}

func TestSDCovValidateSynchestraResumeIdentityBranches(t *testing.T) {
	t.Parallel()
	request, raw := courierTestRequest(t)
	valid := sessionmove.SynchestraDispatch{
		SchemaVersion: sessionmove.SynchestraDispatchSchemaVersion,
		HandoffID:     request.HandoffID, RequestDigest: sessionmove.DigestBytes(raw), Runner: "hetzner-vm1",
		InvocationID: request.HandoffID, Handler: synchestraSessionAcceptHandler, DispatchID: "dsp_resume_valid",
	}
	if err := validateSynchestraResumeIdentity(valid, "hetzner-vm1", request, raw); err != nil {
		t.Fatalf("valid identity error = %v", err)
	}
	tests := map[string]func(*sessionmove.SynchestraDispatch){
		"schema":     func(value *sessionmove.SynchestraDispatch) { value.SchemaVersion = 99 },
		"handoff":    func(value *sessionmove.SynchestraDispatch) { value.HandoffID = "other" },
		"digest":     func(value *sessionmove.SynchestraDispatch) { value.RequestDigest = "sha256:other" },
		"runner":     func(value *sessionmove.SynchestraDispatch) { value.Runner = "other" },
		"invocation": func(value *sessionmove.SynchestraDispatch) { value.InvocationID = "other" },
		"handler":    func(value *sessionmove.SynchestraDispatch) { value.Handler = "other" },
		"dispatch":   func(value *sessionmove.SynchestraDispatch) { value.DispatchID = "-bad" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			identity := valid
			mutate(&identity)
			err := validateSynchestraResumeIdentity(identity, "hetzner-vm1", request, raw)
			if err == nil || !strings.Contains(err.Error(), "does not match exact request and runner") {
				t.Fatalf("validate error = %v", err)
			}
		})
	}
}

func TestSDCovSynchestraTerminalReceiptBranches(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	artifact := encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes)

	if _, _, err := synchestraTerminalReceipt(synchestraInvocationOutput{}, request, raw); err == nil ||
		!strings.Contains(err.Error(), "has no dispatch") {
		t.Fatalf("missing dispatch error = %v", err)
	}
	t.Run("unsupported status", func(t *testing.T) {
		t.Parallel()
		output := sdCovSynchestraInvocationOutput(t, request, raw, "dsp_terminal", "paused", "")
		if _, _, err := synchestraTerminalReceipt(output, request, raw); err == nil ||
			!strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("terminal error = %v", err)
		}
	})
	t.Run("skips unfinished attempts", func(t *testing.T) {
		output := sdCovSynchestraInvocationOutput(t, request, raw, "dsp_terminal", "completed", artifact)
		output.Attempts = append([]synchestraAttemptOutput{{
			ProtocolVersion: synchestraDispatchProtocolVersion, ID: "att_1", DispatchID: "dsp_terminal",
			Number: 1, Status: "running",
		}}, output.Attempts...)
		output.Dispatch.AttemptIDs = []string{"att_1", "att_2"}
		result, pending, err := synchestraTerminalReceipt(output, request, raw)
		if err != nil || pending {
			t.Fatalf("terminal receipt pending=%v err=%v", pending, err)
		}
		if !reflect.DeepEqual(result, validCourierResult(request, raw)) {
			t.Fatalf("terminal receipt = %#v", result)
		}
	})
	t.Run("receipt is not a result", func(t *testing.T) {
		t.Parallel()
		garbage := []byte(`{"unexpected":true}`)
		badArtifact := encodeSynchestraReceiptArtifact(t, request, raw, garbage)
		output := sdCovSynchestraInvocationOutput(t, request, raw, "dsp_terminal", "completed", badArtifact)
		if _, _, err := synchestraTerminalReceipt(output, request, raw); err == nil ||
			!strings.Contains(err.Error(), "decode one session receive result") {
			t.Fatalf("terminal error = %v", err)
		}
	})
	t.Run("receipt does not match request", func(t *testing.T) {
		result := validCourierResult(request, raw)
		result.Digest = sessionmove.DigestBytes([]byte("other request bytes"))
		badArtifact := encodeSynchestraReceiptArtifact(t, request, raw, encodeCourierResult(t, result))
		output := sdCovSynchestraInvocationOutput(t, request, raw, "dsp_terminal", "completed", badArtifact)
		if _, _, err := synchestraTerminalReceipt(output, request, raw); err == nil ||
			!strings.Contains(err.Error(), "request_digest") {
			t.Fatalf("terminal error = %v", err)
		}
	})
}

func sdCovReceiptArtifactRef(t *testing.T, artifact synchestraReceiptArtifact) string {
	t.Helper()
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func TestSDCovDecodeSynchestraReceiptArtifactBranches(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	valid := synchestraReceiptArtifact{
		ProtocolVersion: synchestraReceiptArtifactVersion, InvocationID: request.HandoffID,
		Handler: synchestraSessionAcceptHandler, PayloadDigest: string(sessionmove.DigestBytes(raw)),
		ReceiptDigest: string(sessionmove.DigestBytes(receiptBytes)), Receipt: receiptBytes,
		CompletedAt: time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC),
	}
	canonical, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		reference string
		want      string
	}{
		"bad prefix":          {reference: "not-an-artifact", want: "reference is invalid"},
		"oversized reference": {reference: synchestraReceiptArtifactPrefix + strings.Repeat("A", maxSynchestraReceiptArtifactRefBytes), want: "reference is invalid"},
		"invalid base64":      {reference: synchestraReceiptArtifactPrefix + "!!!", want: "reference is invalid"},
		"empty payload":       {reference: synchestraReceiptArtifactPrefix, want: "reference is invalid"},
		"undecodable artifact": {
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString([]byte("not-json")),
			want:      "artifact is invalid",
		},
		"trailing artifact value": {
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"protocol_version":"x"} trailing`)),
			want:      "artifact is invalid",
		},
		"non canonical artifact": {
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(append([]byte(" "), canonical...)),
			want:      "not canonical",
		},
		"unsupported protocol": {
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(func() []byte {
				value := valid
				value.ProtocolVersion = "v0"
				encoded, marshalErr := json.Marshal(value)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				return encoded
			}()),
			want: "protocol_version is unsupported",
		},
		"receipt is not a bounded object": {
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(func() []byte {
				value := valid
				value.Receipt = []byte("  ")
				value.ReceiptDigest = string(sessionmove.DigestBytes(value.Receipt))
				encoded, marshalErr := json.Marshal(value)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				return encoded
			}()),
			want: "not one bounded JSON object",
		},
		"completed_at is not UTC": {
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(func() []byte {
				value := valid
				value.CompletedAt = time.Date(2026, time.August, 25, 15, 0, 0, 0, time.FixedZone("X", 3600))
				encoded, marshalErr := json.Marshal(value)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				return encoded
			}()),
			want: "completed_at is not canonical UTC",
		},
		"zero completed_at": {
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(func() []byte {
				value := valid
				value.CompletedAt = time.Time{}
				encoded, marshalErr := json.Marshal(value)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				return encoded
			}()),
			want: "completed_at is not canonical UTC",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeSynchestraReceiptArtifact(test.reference, request, raw); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("identity mismatches", func(t *testing.T) {
		t.Parallel()
		mutations := map[string]func(*synchestraReceiptArtifact){
			"invocation":     func(value *synchestraReceiptArtifact) { value.InvocationID = "other" },
			"handler":        func(value *synchestraReceiptArtifact) { value.Handler = "other.handler" },
			"payload":        func(value *synchestraReceiptArtifact) { value.PayloadDigest = "sha256:other" },
			"receipt digest": func(value *synchestraReceiptArtifact) { value.ReceiptDigest = "sha256:other" },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				artifact := valid
				mutate(&artifact)
				if _, err := decodeSynchestraReceiptArtifact(sdCovReceiptArtifactRef(t, artifact), request, raw); err == nil {
					t.Fatal("decode accepted a mismatched artifact")
				}
			})
		}
	})

	if got, err := decodeSynchestraReceiptArtifact(sdCovReceiptArtifactRef(t, valid), request, raw); err != nil {
		t.Fatal(err)
	} else if string(got) != string(receiptBytes) {
		t.Fatalf("decoded receipt = %q, want %q", got, receiptBytes)
	}
}
