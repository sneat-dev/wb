package sessioncourier

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

type scriptedCommandResponse struct {
	stdout []byte
	stderr []byte
	err    error
}

type recordedCommand struct {
	executable string
	args       []string
	stdin      []byte
}

type scriptedCommandRunner struct {
	responses []scriptedCommandResponse
	calls     []recordedCommand
}

func (r *scriptedCommandRunner) Run(_ context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	r.calls = append(r.calls, recordedCommand{
		executable: executable,
		args:       append([]string(nil), args...),
		stdin:      append([]byte(nil), stdin...),
	})
	if len(r.calls) > len(r.responses) {
		return errors.New("unexpected command")
	}
	response := r.responses[len(r.calls)-1]
	_, _ = stdout.Write(response.stdout)
	_, _ = stderr.Write(response.stderr)
	return response.err
}

func TestSynchestraDelivererInvokesAndPollsFixedHandlerWithExactBytes(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	dispatchID := "dsp_handoff_123"
	runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{
		{stdout: encodeSynchestraInvocationOutput(t, request, raw, dispatchID, "queued", "")},
		{stdout: encodeSynchestraStatusOutput(t, request, raw, dispatchID, "running", "")},
		{stdout: encodeSynchestraStatusOutput(t, request, raw, dispatchID, "completed", encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes))},
	}}
	var persisted []sessionmove.SynchestraDispatch
	var sleeps []time.Duration
	deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{
		SaveDispatch: func(dispatch sessionmove.SynchestraDispatch) error {
			persisted = append(persisted, dispatch)
			return nil
		},
	}, runner, func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	})

	result, err := deliverer.Deliver(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, validCourierResult(request, raw)) {
		t.Fatalf("Synchestra receiver result drifted from SSH contract:\n got: %#v\nwant: %#v", result, validCourierResult(request, raw))
	}
	if len(runner.calls) != 3 {
		t.Fatalf("command calls = %d, want invoke plus two bounded observations", len(runner.calls))
	}
	wantInvoke := []string{
		"runner", "invoke", "@/dev/stdin", "--runner", "hetzner-vm1",
		"--handler", synchestraSessionAcceptHandler, "--invocation-id", request.HandoffID, "--format", "json",
	}
	if runner.calls[0].executable != testExecutable(t) || !reflect.DeepEqual(runner.calls[0].args, wantInvoke) {
		t.Fatalf("invoke = executable %q args %#v", runner.calls[0].executable, runner.calls[0].args)
	}
	if !bytes.Equal(runner.calls[0].stdin, raw) {
		t.Fatalf("invoke stdin changed:\n got: %q\nwant: %q", runner.calls[0].stdin, raw)
	}
	wantStatus := []string{"runner", "dispatch", "status", dispatchID, "--format", "json"}
	for index, call := range runner.calls[1:] {
		if !reflect.DeepEqual(call.args, wantStatus) || len(call.stdin) != 0 {
			t.Fatalf("status call %d = args %#v stdin %q", index+1, call.args, call.stdin)
		}
	}
	if len(persisted) != 1 || persisted[0].HandoffID != request.HandoffID ||
		persisted[0].InvocationID != request.HandoffID || persisted[0].DispatchID != dispatchID ||
		persisted[0].Runner != "hetzner-vm1" || persisted[0].RequestDigest != sessionmove.DigestBytes(raw) {
		t.Fatalf("persisted dispatch identity = %#v", persisted)
	}
	if len(sleeps) != 2 || sleeps[0] != synchestraPollInterval || sleeps[1] != synchestraPollInterval {
		t.Fatalf("poll sleeps = %v", sleeps)
	}
}

func TestSynchestraDelivererRequiresDurableDispatchRecorderBeforeFreshInvocation(t *testing.T) {
	runner := &scriptedCommandRunner{}
	_, err := newSynchestraDeliverer(
		sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
		SynchestraOptions{},
		func(string) (string, error) { return testExecutable(t), nil },
		runner,
		func(context.Context, time.Duration) error { return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "durable dispatch recorder") {
		t.Fatalf("newSynchestraDeliverer error = %v, want durable dispatch recorder refusal", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("commands = %d, want no invocation before durable recorder", len(runner.calls))
	}
}

func TestSynchestraDelivererResumesExactPersistedDispatchWithoutReinvoking(t *testing.T) {
	request, raw := courierTestRequest(t)
	dispatchID := "dsp_existing"
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
		stdout: encodeSynchestraStatusOutput(t, request, raw, dispatchID, "completed", encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes)),
	}}}
	deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{
		Dispatch: &sessionmove.SynchestraDispatch{
			SchemaVersion: sessionmove.SynchestraDispatchSchemaVersion,
			HandoffID:     request.HandoffID, RequestDigest: sessionmove.DigestBytes(raw), Runner: "hetzner-vm1",
			InvocationID: request.HandoffID, Handler: synchestraSessionAcceptHandler, DispatchID: dispatchID,
		},
		SaveDispatch: func(sessionmove.SynchestraDispatch) error {
			t.Fatal("persisted dispatch was unexpectedly rewritten")
			return nil
		},
	}, runner, func(context.Context, time.Duration) error { return nil })

	if _, err := deliverer.Deliver(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args,
		[]string{"runner", "dispatch", "status", dispatchID, "--format", "json"}) {
		t.Fatalf("resume commands = %#v", runner.calls)
	}
}

func TestSynchestraDelivererRejectsMalformedTypedOutputFields(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	valid := encodeSynchestraInvocationOutput(t, request, raw, "dsp_typed", "completed",
		encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes))
	tests := map[string]func(map[string]any){
		"resolved source": func(output map[string]any) {
			output["resolved"].(map[string]any)["source"] = true
		},
		"resolved repository": func(output map[string]any) {
			output["resolved"].(map[string]any)["repository"] = true
		},
		"resolved requested execution": func(output map[string]any) {
			output["resolved"].(map[string]any)["requested_execution"] = true
		},
		"dispatch cancellation": func(output map[string]any) {
			output["dispatch"].(map[string]any)["cancellation"] = true
		},
		"attempt worker": func(output map[string]any) {
			output["attempts"].([]any)[0].(map[string]any)["worker"] = true
		},
		"attempt lease": func(output map[string]any) {
			output["attempts"].([]any)[0].(map[string]any)["lease"] = true
		},
		"attempt session": func(output map[string]any) {
			output["attempts"].([]any)[0].(map[string]any)["session"] = true
		},
		"attempt logs": func(output map[string]any) {
			output["attempts"].([]any)[0].(map[string]any)["logs"] = true
		},
		"failure logs": func(output map[string]any) {
			output["attempts"].([]any)[0].(map[string]any)["failure"] = map[string]any{
				"stage": "receive", "code": "failed", "retryable": false, "logs": true,
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
				stdout: mutateSynchestraJSONOutput(t, valid, mutate),
			}}}
			deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner,
				func(context.Context, time.Duration) error { return nil })
			if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "decode") {
				t.Fatalf("Deliver error = %v, want typed decode refusal", err)
			}
		})
	}
}

func TestSynchestraDelivererRejectsArtifactTamperingWithoutAcceptingReceipt(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	valid := encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes)
	tests := map[string]struct {
		artifact string
		want     string
	}{
		"invocation": {artifact: mutateSynchestraArtifact(t, valid, func(value *synchestraReceiptArtifact) { value.InvocationID = "handoff-other" }), want: "invocation"},
		"handler":    {artifact: mutateSynchestraArtifact(t, valid, func(value *synchestraReceiptArtifact) { value.Handler = "wb.session.message.v1" }), want: "handler"},
		"payload":    {artifact: mutateSynchestraArtifact(t, valid, func(value *synchestraReceiptArtifact) { value.PayloadDigest = "sha256:" + strings.Repeat("0", 64) }), want: "payload_digest"},
		"receipt": {artifact: mutateSynchestraArtifact(t, valid, func(value *synchestraReceiptArtifact) {
			value.Receipt = []byte(`{"phase":"forged"}`)
		}), want: "receipt_digest"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
				stdout: encodeSynchestraInvocationOutput(t, request, raw, "dsp_tampered", "completed", test.artifact),
			}}}
			deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner,
				func(context.Context, time.Duration) error { return nil })
			if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Deliver error = %v, want containing %q", err, test.want)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("commands = %d, want no fallback", len(runner.calls))
			}
		})
	}
}

func TestSynchestraDelivererRejectsFailedCancelledAndAmbiguousTerminalResults(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	artifact := encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes)
	completed := encodeSynchestraInvocationOutput(t, request, raw, "dsp_terminal", "completed", artifact)
	tests := map[string]struct {
		output []byte
		want   string
	}{
		"failed": {
			output: encodeSynchestraInvocationOutput(t, request, raw, "dsp_terminal", "failed", ""),
			want:   "ended failed",
		},
		"cancelled": {
			output: encodeSynchestraInvocationOutput(t, request, raw, "dsp_terminal", "cancelled", ""),
			want:   "ended cancelled",
		},
		"missing artifact": {
			output: mutateSynchestraOutput(t, completed, func(output *synchestraInvocationOutput) {
				output.Attempts[0].Result.ArtifactReferences = nil
			}),
			want: "exactly one WB receipt artifact",
		},
		"multiple artifacts": {
			output: mutateSynchestraOutput(t, completed, func(output *synchestraInvocationOutput) {
				output.Attempts[0].Result.ArtifactReferences = []string{artifact, artifact}
			}),
			want: "exactly one WB receipt artifact",
		},
		"multiple completed attempts": {
			output: mutateSynchestraOutput(t, completed, func(output *synchestraInvocationOutput) {
				second := output.Attempts[0]
				second.ID = "att_2"
				second.Number = 2
				output.Dispatch.AttemptIDs = append(output.Dispatch.AttemptIDs, second.ID)
				output.Attempts = append(output.Attempts, second)
			}),
			want: "exactly one completed WB receipt attempt",
		},
		"completed attempt absent from dispatch history": {
			output: mutateSynchestraOutput(t, completed, func(output *synchestraInvocationOutput) {
				output.Dispatch.AttemptIDs = []string{"att_other"}
			}),
			want: "attempt history",
		},
		"duplicate dispatch attempt ids": {
			output: mutateSynchestraOutput(t, completed, func(output *synchestraInvocationOutput) {
				output.Dispatch.AttemptIDs = []string{"att_1", "att_1"}
			}),
			want: "attempt history",
		},
		"dispatch attempt cardinality differs": {
			output: mutateSynchestraOutput(t, completed, func(output *synchestraInvocationOutput) {
				output.Dispatch.AttemptIDs = []string{"att_1", "att_2"}
			}),
			want: "attempt history",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: test.output}}}
			deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner,
				func(context.Context, time.Duration) error { return nil })
			if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Deliver error = %v, want containing %q", err, test.want)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("commands = %d, want no fallback or retry", len(runner.calls))
			}
		})
	}
}

func TestSynchestraDelivererOmitsUntrustedFailureDetails(t *testing.T) {
	request, raw := courierTestRequest(t)
	receiptBytes := encodeCourierResult(t, validCourierResult(request, raw))
	output := encodeSynchestraInvocationOutput(t, request, raw, "dsp_failed", "completed",
		encodeSynchestraReceiptArtifact(t, request, raw, receiptBytes))
	output = mutateSynchestraOutput(t, output, func(output *synchestraInvocationOutput) {
		output.Dispatch.Status = "failed"
		output.Attempts[0].Status = "failed"
		output.Attempts[0].Result = nil
		output.Attempts[0].Failure = &synchestraAttemptFailure{
			Stage: "\nUNTRUSTED-STAGE", Code: strings.Repeat("UNTRUSTED-CODE", 1024),
		}
	})
	runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{stdout: output}}}
	deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner,
		func(context.Context, time.Duration) error { return nil })
	_, err := deliverer.Deliver(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "ended failed") {
		t.Fatalf("Deliver error = %v, want failed terminal refusal", err)
	}
	if strings.Contains(err.Error(), "UNTRUSTED") || strings.ContainsRune(err.Error(), '\n') {
		t.Fatalf("Deliver error reflected untrusted failure details: %q", err)
	}
}

func TestSynchestraDelivererBoundsCommandOutputAndPolling(t *testing.T) {
	request, raw := courierTestRequest(t)
	t.Run("output", func(t *testing.T) {
		runner := &scriptedCommandRunner{responses: []scriptedCommandResponse{{
			stdout: bytes.Repeat([]byte("x"), maxSynchestraCommandStdoutBytes+1),
		}}}
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner,
			func(context.Context, time.Duration) error { return nil })
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("Deliver error = %v, want bounded-output refusal", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("commands = %d, want no fallback", len(runner.calls))
		}
	})
	t.Run("poll count", func(t *testing.T) {
		queued := encodeSynchestraInvocationOutput(t, request, raw, "dsp_pending", "queued", "")
		status := encodeSynchestraStatusOutput(t, request, raw, "dsp_pending", "queued", "")
		responses := make([]scriptedCommandResponse, maxSynchestraStatusPolls+1)
		responses[0].stdout = queued
		for index := 1; index < len(responses); index++ {
			responses[index].stdout = status
		}
		runner := &scriptedCommandRunner{responses: responses}
		sleeps := 0
		deliverer := newTestSynchestraDeliverer(t, sessionmove.SynchestraConfig{Runner: "hetzner-vm1"}, SynchestraOptions{}, runner,
			func(context.Context, time.Duration) error { sleeps++; return nil })
		if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "bounded status polls") {
			t.Fatalf("Deliver error = %v, want bounded-poll refusal", err)
		}
		if len(runner.calls) != maxSynchestraStatusPolls+1 || sleeps != maxSynchestraStatusPolls {
			t.Fatalf("commands=%d sleeps=%d, want invoke + %d status polls", len(runner.calls), sleeps, maxSynchestraStatusPolls)
		}
	})
}

func newTestSynchestraDeliverer(
	t *testing.T,
	config sessionmove.SynchestraConfig,
	options SynchestraOptions,
	runner commandRunner,
	sleep func(context.Context, time.Duration) error,
) *synchestraDeliverer {
	t.Helper()
	if options.Dispatch == nil && options.SaveDispatch == nil {
		options.SaveDispatch = func(sessionmove.SynchestraDispatch) error { return nil }
	}
	deliverer, err := newSynchestraDeliverer(config, options, func(name string) (string, error) {
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

func encodeSynchestraInvocationOutput(t *testing.T, request sessionmove.Request, raw []byte, dispatchID, status, artifact string) []byte {
	t.Helper()
	now := time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC)
	attempts := []any{}
	attemptIDs := []string{}
	if status == "completed" {
		attemptIDs = []string{"att_1"}
		worker := map[string]any{"worker_id": "worker-1", "host_id": "host-1", "runner_id": "hetzner-vm1"}
		logs := map[string]any{"session_id": "session-1", "stream_id": "stream-1", "last_sequence": 7}
		attempts = append(attempts, map[string]any{
			"protocol_version": synchestraDispatchProtocolVersion,
			"id":               "att_1", "dispatch_id": dispatchID, "number": 1, "status": "completed",
			"worker": worker,
			"lease": map[string]any{
				"owner": worker, "generation": 1, "acquired_at": now,
				"expires_at": now.Add(time.Minute), "last_heartbeat_at": now,
			},
			"session":    map[string]any{"id": "session-1", "runtime": "runner", "started_at": now, "logs": logs},
			"logs":       logs,
			"result":     map[string]any{"artifact_references": []string{artifact}, "published_at": now},
			"created_at": now, "started_at": now, "finished_at": now,
		})
	}
	value := map[string]any{
		"resolved": map[string]any{
			"operation": "invoke", "runner": "hetzner-vm1",
			"repository": map[string]any{
				"canonical_id": "github.com/acme/app", "clone_url": "https://example.invalid/acme/app.git",
				"base_revision": request.BundleCommit, "base_ref": request.Branch,
			},
			"invocation": map[string]any{
				"protocol_version": synchestraInvocationProtocolVersion,
				"id":               request.HandoffID, "handler": synchestraSessionAcceptHandler,
				"payload_digest": sessionmove.DigestBytes(raw), "payload_size": len(raw), "created_at": now,
			},
		},
		"dispatch": map[string]any{
			"protocol_version": synchestraDispatchProtocolVersion,
			"id":               dispatchID, "status": status, "attempt_ids": attemptIDs,
			"created_at": now, "updated_at": now,
		},
		"attempts": attempts,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func encodeSynchestraStatusOutput(t *testing.T, request sessionmove.Request, raw []byte, dispatchID, status, artifact string) []byte {
	t.Helper()
	return mutateSynchestraOutput(t, encodeSynchestraInvocationOutput(t, request, raw, dispatchID, status, artifact),
		func(output *synchestraInvocationOutput) {
			output.Resolved.Operation = "status"
			output.Resolved.DispatchID = dispatchID
		})
}

func encodeSynchestraReceiptArtifact(t *testing.T, request sessionmove.Request, raw, receipt []byte) string {
	t.Helper()
	artifact := synchestraReceiptArtifact{
		ProtocolVersion: synchestraReceiptArtifactVersion,
		InvocationID:    request.HandoffID,
		Handler:         synchestraSessionAcceptHandler,
		PayloadDigest:   string(sessionmove.DigestBytes(raw)),
		ReceiptDigest:   string(sessionmove.DigestBytes(receipt)),
		Receipt:         receipt,
		CompletedAt:     time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	return synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func mutateSynchestraArtifact(t *testing.T, reference string, mutate func(*synchestraReceiptArtifact)) string {
	t.Helper()
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(reference, synchestraReceiptArtifactPrefix))
	if err != nil {
		t.Fatal(err)
	}
	var value synchestraReceiptArtifact
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	mutate(&value)
	encoded, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return synchestraReceiptArtifactPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func mutateSynchestraOutput(t *testing.T, raw []byte, mutate func(*synchestraInvocationOutput)) []byte {
	t.Helper()
	var output synchestraInvocationOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	mutate(&output)
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func mutateSynchestraJSONOutput(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	mutate(output)
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}
