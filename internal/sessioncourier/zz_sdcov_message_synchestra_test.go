package sessioncourier

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func sdCovNewSynchestraMessageDeliverer(t *testing.T, config sessionmove.SynchestraConfig,
	options MessageSynchestraOptions, runner commandRunner, sleep func(context.Context, time.Duration) error,
) *synchestraMessageDeliverer {
	t.Helper()
	deliverer, err := newSynchestraMessageDeliverer(config, options, func(name string) (string, error) {
		if name != synchestraExecutableName {
			return "", fmt.Errorf("unexpected executable %q", name)
		}
		return testExecutable(t), nil
	}, runner, sleep)
	if err != nil {
		t.Fatal(err)
	}
	return deliverer
}

func sdCovNoSleep(context.Context, time.Duration) error { return nil }

func sdCovMessageInvocationOutput(t *testing.T, message sessionmove.Message, raw []byte, dispatchID, status, artifact string) synchestraInvocationOutput {
	t.Helper()
	encoded := encodeSynchestraMessageInvocationOutput(t, message, raw, dispatchID, status, artifact)
	var output synchestraInvocationOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func sdCovCloneInvocationOutput(t *testing.T, output synchestraInvocationOutput) synchestraInvocationOutput {
	t.Helper()
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var clone synchestraInvocationOutput
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestSDCovNewSynchestraMessageDelivererConstructorBranches(t *testing.T) {
	t.Run("no dispatch recorder", func(t *testing.T) {
		_, err := newSynchestraMessageDeliverer(sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{},
			func(string) (string, error) { return testExecutable(t), nil },
			&scriptedCommandRunner{}, sdCovNoSleep)
		if err == nil || !strings.Contains(err.Error(), "durable dispatch recorder is unavailable") {
			t.Fatalf("constructor error = %v", err)
		}
	})
	t.Run("transport config refusal", func(t *testing.T) {
		_, err := newSynchestraMessageDeliverer(sessionmove.SynchestraConfig{},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			func(string) (string, error) { return testExecutable(t), nil },
			&scriptedCommandRunner{}, sdCovNoSleep)
		if err == nil || !strings.Contains(err.Error(), "runner is required") {
			t.Fatalf("constructor error = %v", err)
		}
	})
}

func TestSDCovSynchestraMessageDelivererFailureBranches(t *testing.T) {
	message, raw := courierTestMessage(t)

	t.Run("payload refusal", func(t *testing.T) {
		runner := &scriptedCommandRunner{}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), []byte("{}")); err == nil ||
			!strings.Contains(err.Error(), "validate Synchestra session message") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("commands = %d after local refusal", len(runner.calls))
		}
	})
	t.Run("invoke command failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{err: errors.New("exit status 7")}}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "invoke message handler") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("invoke response decode failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: []byte("not-json")}}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "validate Synchestra message invoke response") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("invoke identity mismatch", func(t *testing.T) {
		output := mutateSynchestraOutput(t, encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_mv", "queued", ""),
			func(output *synchestraInvocationOutput) { output.Resolved.Runner = "other-runner" })
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: output}}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "resolved message runner") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("durable recorder failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
			stdout: encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_md", "queued", ""),
		}}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error {
				return errors.New("disk full")
			}}, runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "persist Synchestra message dispatch identity: disk full") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("terminal failure without receipt", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
			stdout: encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_mf", "failed", ""),
		}}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "ended failed") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("bounded status polls", func(t *testing.T) {
		queued := encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_mp", "queued", "")
		status := encodeSynchestraMessageStatusOutput(t, message, raw, "dsp_mp", "queued", "")
		responses := make([]scriptedCommandResponse, maxSynchestraStatusPolls+1)
		responses[0].stdout = queued
		for index := 1; index < len(responses); index++ {
			responses[index].stdout = status
		}
		runner := &scriptedCommandRunner{responses: responses}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "bounded status polls") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
		if len(runner.calls) != maxSynchestraStatusPolls+1 {
			t.Fatalf("commands = %d", len(runner.calls))
		}
	})
	t.Run("poll sleep failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
			stdout: encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_ms", "queued", ""),
		}}}
		sleepErr := errors.New("clock unavailable")
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, func(context.Context, time.Duration) error { return sleepErr })
		if _, err := deliverer.DeliverMessage(context.Background(), raw); !errors.Is(err, sleepErr) {
			t.Fatalf("DeliverMessage error = %v, want sleep failure", err)
		}
	})
	t.Run("poll command failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
			{stdout: encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_mr", "queued", "")},
			{err: errors.New("exit status 9")},
		}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "poll message dispatch") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("poll response decode failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
			{stdout: encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_mdc", "queued", "")},
			{stdout: []byte("not-json")},
		}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "decode one runner invocation result") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("poll identity mismatch", func(t *testing.T) {
		poll := mutateSynchestraOutput(t, encodeSynchestraMessageStatusOutput(t, message, raw, "dsp_mi", "queued", ""),
			func(output *synchestraInvocationOutput) { output.Resolved.Runner = "other-runner" })
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
			{stdout: encodeSynchestraMessageInvocationOutput(t, message, raw, "dsp_mi", "queued", "")},
			{stdout: poll},
		}}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{SaveDispatch: func(sessionmove.MessageSynchestraDispatch) error { return nil }},
			runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "resolved message runner") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
}

func TestSDCovSynchestraMessageDelivererResumeBranches(t *testing.T) {
	message, raw := courierTestMessage(t)
	requestDigest := sessionmove.DigestBytes([]byte("exact request"))
	dispatchID := "dsp_message_resume_branch"
	identity := func() sessionmove.MessageSynchestraDispatch {
		return sessionmove.MessageSynchestraDispatch{
			SchemaVersion: sessionmove.MessageSynchestraDispatchSchemaVersion,
			HandoffID:     message.HandoffID, RequestDigest: requestDigest, MessageID: message.MessageID,
			MessageDigest: sessionmove.DigestBytes(raw), Runner: "hetzner-vm1", InvocationID: message.MessageID,
			Handler: sessionmove.SynchestraSessionMessageHandler, DispatchID: dispatchID,
		}
	}

	t.Run("persisted identity mismatch", func(t *testing.T) {
		mismatched := identity()
		mismatched.Runner = "other-runner"
		runner := &scriptedCommandRunner{}
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{RequestDigest: requestDigest, Dispatch: &mismatched}, runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "persisted Synchestra message dispatch does not match") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("commands = %d after identity refusal", len(runner.calls))
		}
	})
	t.Run("status command failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{err: errors.New("exit status 4")}}}
		dispatch := identity()
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{RequestDigest: requestDigest, Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "observe message dispatch") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("status response decode failure", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: []byte("not-json")}}}
		dispatch := identity()
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{RequestDigest: requestDigest, Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "decode one runner invocation result") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("status dispatch id mismatch", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
			stdout: encodeSynchestraMessageStatusOutput(t, message, raw, "dsp_other", "queued", ""),
		}}}
		dispatch := identity()
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{RequestDigest: requestDigest, Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "does not match persisted dispatch") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
	t.Run("status identity mismatch", func(t *testing.T) {
		poll := mutateSynchestraOutput(t, encodeSynchestraMessageStatusOutput(t, message, raw, dispatchID, "queued", ""),
			func(output *synchestraInvocationOutput) { output.Resolved.Runner = "other-runner" })
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: poll}}}
		dispatch := identity()
		deliverer := sdCovNewSynchestraMessageDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
			MessageSynchestraOptions{RequestDigest: requestDigest, Dispatch: &dispatch}, runner, sdCovNoSleep)
		if _, err := deliverer.DeliverMessage(context.Background(), raw); err == nil ||
			!strings.Contains(err.Error(), "resolved message runner") {
			t.Fatalf("DeliverMessage error = %v", err)
		}
	})
}

func TestSDCovValidateSynchestraMessageInvocationOutputBranches(t *testing.T) {
	message, raw := courierTestMessage(t)
	baseline := sdCovMessageInvocationOutput(t, message, raw, "dsp_valid", "queued", "")
	requestDigest := sessionmove.DigestBytes([]byte("request"))

	tests := map[string]struct {
		mutate func(*synchestraInvocationOutput)
		want   string
	}{
		"missing dispatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Dispatch = nil },
			want:   "lacks typed message invocation",
		},
		"missing invocation": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation = nil },
			want:   "lacks typed message invocation",
		},
		"unsupported dispatch protocol": {
			mutate: func(output *synchestraInvocationOutput) { output.Dispatch.ProtocolVersion = "synchestra.dispatch.v0" },
			want:   "dispatch identity is invalid",
		},
		"invalid dispatch id": {
			mutate: func(output *synchestraInvocationOutput) { output.Dispatch.ID = "-bad" },
			want:   "dispatch identity is invalid",
		},
		"resolved operation mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Operation = "status" },
			want:   "resolved message runner or dispatch identity",
		},
		"resolved runner mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Runner = "other-runner" },
			want:   "resolved message runner or dispatch identity",
		},
		"resolved source present": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Source = &struct{}{} },
			want:   "resolved message runner or dispatch identity",
		},
		"resolved repository present": {
			mutate: func(output *synchestraInvocationOutput) {
				output.Resolved.Repository = &synchestraRepositoryOutput{CanonicalID: "acme/app"}
			},
			want: "resolved message runner or dispatch identity",
		},
		"resolved requested execution present": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.RequestedExecution = &struct{}{} },
			want:   "resolved message runner or dispatch identity",
		},
		"invocation protocol mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.ProtocolVersion = "v0" },
			want:   "typed message invocation identity",
		},
		"invocation id mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.ID = "other-message" },
			want:   "typed message invocation identity",
		},
		"invocation handler mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.Handler = "other.handler" },
			want:   "typed message invocation identity",
		},
		"invocation payload digest mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.PayloadDigest = "sha256:other" },
			want:   "typed message invocation identity",
		},
		"invocation payload size mismatch": {
			mutate: func(output *synchestraInvocationOutput) { output.Resolved.Invocation.PayloadSize++ },
			want:   "typed message invocation identity",
		},
		"invocation deadline present": {
			mutate: func(output *synchestraInvocationOutput) {
				deadline := time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC)
				output.Resolved.Invocation.Deadline = &deadline
			},
			want: "typed message invocation identity",
		},
		"attempt identity mismatch": {
			mutate: func(output *synchestraInvocationOutput) {
				attempt := synchestraAttemptOutput{ProtocolVersion: "v0", ID: "att_1", DispatchID: "dsp_valid", Number: 1, Status: "queued"}
				output.Dispatch.AttemptIDs = []string{"att_1"}
				output.Attempts = []synchestraAttemptOutput{attempt}
			},
			want: "attempt identity does not match dispatch",
		},
		"attempt history mismatch": {
			mutate: func(output *synchestraInvocationOutput) {
				attempt := synchestraAttemptOutput{ProtocolVersion: synchestraDispatchProtocolVersion, ID: "att_1", DispatchID: "dsp_valid", Number: 1, Status: "queued"}
				output.Dispatch.AttemptIDs = []string{"att_1", "att_2"}
				output.Attempts = []synchestraAttemptOutput{attempt}
			},
			want: "attempt history cardinality",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			output := sdCovCloneInvocationOutput(t, baseline)
			test.mutate(&output)
			_, err := validateSynchestraMessageInvocationOutput(output, "hetzner-vm1", message, raw, "", requestDigest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("expected dispatch mismatch", func(t *testing.T) {
		output := sdCovCloneInvocationOutput(t, baseline)
		_, err := validateSynchestraMessageInvocationOutput(output, "hetzner-vm1", message, raw, "dsp_expected", requestDigest)
		if err == nil || !strings.Contains(err.Error(), "does not match persisted dispatch") {
			t.Fatalf("validate error = %v", err)
		}
	})
	t.Run("status route resolves", func(t *testing.T) {
		encoded := encodeSynchestraMessageStatusOutput(t, message, raw, "dsp_valid", "queued", "")
		var output synchestraInvocationOutput
		if err := json.Unmarshal(encoded, &output); err != nil {
			t.Fatal(err)
		}
		identity, err := validateSynchestraMessageInvocationOutput(output, "hetzner-vm1", message, raw, "dsp_valid", requestDigest)
		if err != nil {
			t.Fatal(err)
		}
		if identity.DispatchID != "dsp_valid" || identity.MessageID != message.MessageID ||
			identity.MessageDigest != sessionmove.DigestBytes(raw) || identity.RequestDigest != requestDigest ||
			identity.Handler != sessionmove.SynchestraSessionMessageHandler {
			t.Fatalf("identity = %#v", identity)
		}
	})
}

func TestSDCovValidateSynchestraMessageResumeIdentityBranches(t *testing.T) {
	message, raw := courierTestMessage(t)
	requestDigest := sessionmove.DigestBytes([]byte("request"))
	valid := sessionmove.MessageSynchestraDispatch{
		SchemaVersion: sessionmove.MessageSynchestraDispatchSchemaVersion,
		HandoffID:     message.HandoffID, RequestDigest: requestDigest, MessageID: message.MessageID,
		MessageDigest: sessionmove.DigestBytes(raw), Runner: "hetzner-vm1", InvocationID: message.MessageID,
		Handler: sessionmove.SynchestraSessionMessageHandler, DispatchID: "dsp_resume_valid",
	}
	if err := validateSynchestraMessageResumeIdentity(valid, "hetzner-vm1", message, raw, requestDigest); err != nil {
		t.Fatalf("valid identity error = %v", err)
	}
	tests := map[string]func(*sessionmove.MessageSynchestraDispatch){
		"schema":     func(value *sessionmove.MessageSynchestraDispatch) { value.SchemaVersion = 99 },
		"handoff":    func(value *sessionmove.MessageSynchestraDispatch) { value.HandoffID = "other" },
		"digest":     func(value *sessionmove.MessageSynchestraDispatch) { value.RequestDigest = "sha256:other" },
		"message":    func(value *sessionmove.MessageSynchestraDispatch) { value.MessageID = "other" },
		"bytes":      func(value *sessionmove.MessageSynchestraDispatch) { value.MessageDigest = "sha256:other" },
		"runner":     func(value *sessionmove.MessageSynchestraDispatch) { value.Runner = "other" },
		"invocation": func(value *sessionmove.MessageSynchestraDispatch) { value.InvocationID = "other" },
		"handler":    func(value *sessionmove.MessageSynchestraDispatch) { value.Handler = "other" },
		"dispatch":   func(value *sessionmove.MessageSynchestraDispatch) { value.DispatchID = "-bad" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			identity := valid
			mutate(&identity)
			err := validateSynchestraMessageResumeIdentity(identity, "hetzner-vm1", message, raw, requestDigest)
			if err == nil || !strings.Contains(err.Error(), "does not match exact message and runner") {
				t.Fatalf("validate error = %v", err)
			}
		})
	}
}

func TestSDCovSynchestraMessageTerminalReceiptBranches(t *testing.T) {
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	artifact := encodeSynchestraMessageReceiptArtifact(t, message, raw, receiptRaw)

	if _, _, err := synchestraMessageTerminalReceipt(synchestraInvocationOutput{}, message, raw); err == nil ||
		!strings.Contains(err.Error(), "has no dispatch") {
		t.Fatalf("missing dispatch error = %v", err)
	}

	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			output := sdCovMessageInvocationOutput(t, message, raw, "dsp_terminal", status, "")
			if _, _, err := synchestraMessageTerminalReceipt(output, message, raw); err == nil ||
				!strings.Contains(err.Error(), "ended "+status) {
				t.Fatalf("terminal error = %v", err)
			}
		})
	}
	t.Run("unsupported status", func(t *testing.T) {
		output := sdCovMessageInvocationOutput(t, message, raw, "dsp_terminal", "paused", "")
		if _, _, err := synchestraMessageTerminalReceipt(output, message, raw); err == nil ||
			!strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("terminal error = %v", err)
		}
	})
	t.Run("skips unfinished attempts", func(t *testing.T) {
		output := sdCovMessageInvocationOutput(t, message, raw, "dsp_terminal", "completed", artifact)
		output.Attempts = append([]synchestraAttemptOutput{{
			ProtocolVersion: synchestraDispatchProtocolVersion, ID: "att_1", DispatchID: "dsp_terminal",
			Number: 1, Status: "running",
		}}, output.Attempts...)
		output.Dispatch.AttemptIDs = []string{"att_1", "att_2"}
		got, pending, err := synchestraMessageTerminalReceipt(output, message, raw)
		if err != nil || pending {
			t.Fatalf("terminal receipt pending=%v err=%v", pending, err)
		}
		if got != receipt {
			t.Fatalf("terminal receipt = %#v, want %#v", got, receipt)
		}
	})
	t.Run("completed attempts without one artifact", func(t *testing.T) {
		output := sdCovMessageInvocationOutput(t, message, raw, "dsp_terminal", "completed", artifact)
		output.Attempts[0].Result.ArtifactReferences = []string{artifact, artifact}
		if _, _, err := synchestraMessageTerminalReceipt(output, message, raw); err == nil ||
			!strings.Contains(err.Error(), "exactly one receipt artifact") {
			t.Fatalf("terminal error = %v", err)
		}
	})
	t.Run("no completed attempt", func(t *testing.T) {
		output := sdCovMessageInvocationOutput(t, message, raw, "dsp_terminal", "completed", artifact)
		output.Attempts[0].Status = "running"
		if _, _, err := synchestraMessageTerminalReceipt(output, message, raw); err == nil ||
			!strings.Contains(err.Error(), "exactly one completed receipt attempt") {
			t.Fatalf("terminal error = %v", err)
		}
	})
	t.Run("artifact refusal", func(t *testing.T) {
		output := sdCovMessageInvocationOutput(t, message, raw, "dsp_terminal", "completed", "bogus-reference")
		if _, _, err := synchestraMessageTerminalReceipt(output, message, raw); err == nil {
			t.Fatal("terminal receipt accepted an invalid artifact reference")
		}
	})
	t.Run("receipt refusal", func(t *testing.T) {
		garbage := encodeSynchestraMessageReceiptArtifact(t, message, raw, []byte("not-a-receipt"))
		output := sdCovMessageInvocationOutput(t, message, raw, "dsp_terminal", "completed", garbage)
		if _, _, err := synchestraMessageTerminalReceipt(output, message, raw); err == nil {
			t.Fatal("terminal receipt accepted an invalid receipt")
		}
	})
}

func sdCovMessageArtifactRef(t *testing.T, artifact synchestraReceiptArtifact) string {
	t.Helper()
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func TestSDCovDecodeSynchestraMessageReceiptArtifactBranches(t *testing.T) {
	message, raw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, raw)
	receiptRaw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	valid := synchestraReceiptArtifact{
		ProtocolVersion: synchestraReceiptArtifactVersion, InvocationID: message.MessageID,
		Handler: sessionmove.SynchestraSessionMessageHandler, PayloadDigest: string(sessionmove.DigestBytes(raw)),
		ReceiptDigest: string(sessionmove.DigestBytes(receiptRaw)), Receipt: receiptRaw,
		CompletedAt: time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC),
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
			reference: synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(append([]byte(" "), func() []byte {
				encoded, err := json.Marshal(valid)
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			}()...)),
			want: "not canonical",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSynchestraMessageReceiptArtifact(test.reference, message, raw); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("identity mismatches", func(t *testing.T) {
		mutations := map[string]func(*synchestraReceiptArtifact){
			"protocol":       func(value *synchestraReceiptArtifact) { value.ProtocolVersion = "v0" },
			"invocation":     func(value *synchestraReceiptArtifact) { value.InvocationID = "other" },
			"handler":        func(value *synchestraReceiptArtifact) { value.Handler = "other.handler" },
			"payload":        func(value *synchestraReceiptArtifact) { value.PayloadDigest = "sha256:other" },
			"receipt digest": func(value *synchestraReceiptArtifact) { value.ReceiptDigest = "sha256:other" },
			"completed at":   func(value *synchestraReceiptArtifact) { value.CompletedAt = time.Time{} },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				artifact := valid
				mutate(&artifact)
				if _, err := decodeSynchestraMessageReceiptArtifact(sdCovMessageArtifactRef(t, artifact), message, raw); err == nil ||
					!strings.Contains(err.Error(), "identity does not match") {
					t.Fatalf("decode error = %v", err)
				}
			})
		}
	})

	if got, err := decodeSynchestraMessageReceiptArtifact(sdCovMessageArtifactRef(t, valid), message, raw); err != nil {
		t.Fatal(err)
	} else if string(got) != string(receiptRaw) {
		t.Fatalf("decoded receipt = %q, want %q", got, receiptRaw)
	}
}
