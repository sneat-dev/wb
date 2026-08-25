package sessioncourier

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

type MessageSynchestraOptions struct {
	RequestDigest sessionmove.Digest
	Dispatch      *sessionmove.MessageSynchestraDispatch
	SaveDispatch  func(sessionmove.MessageSynchestraDispatch) error
}

type synchestraMessageDeliverer struct {
	transport *synchestraDeliverer
	options   MessageSynchestraOptions
}

func NewSynchestraMessageDeliverer(config sessionmove.SynchestraConfig, options MessageSynchestraOptions) (MessageDeliverer, error) {
	return newSynchestraMessageDeliverer(config, options, exec.LookPath, execCommandRunner{}, sleepWithContext)
}

func newSynchestraMessageDeliverer(config sessionmove.SynchestraConfig, options MessageSynchestraOptions,
	lookPath func(string) (string, error), runner commandRunner, sleep func(context.Context, time.Duration) error,
) (*synchestraMessageDeliverer, error) {
	if options.Dispatch == nil && options.SaveDispatch == nil {
		return nil, fmt.Errorf("construct Synchestra message courier: durable dispatch recorder is unavailable")
	}
	transport, err := newSynchestraDeliverer(config, SynchestraOptions{SaveDispatch: func(sessionmove.SynchestraDispatch) error { return nil }}, lookPath, runner, sleep)
	if err != nil {
		return nil, err
	}
	if options.Dispatch != nil {
		copy := *options.Dispatch
		options.Dispatch = &copy
	}
	return &synchestraMessageDeliverer{transport: transport, options: options}, nil
}

func (deliverer *synchestraMessageDeliverer) DeliverMessage(ctx context.Context, raw []byte) (sessionmove.MessageReceipt, error) {
	message, err := validateMessagePayload(raw)
	if err != nil {
		return sessionmove.MessageReceipt{}, fmt.Errorf("validate Synchestra session message: %w", err)
	}
	deliveryContext, cancel := context.WithTimeout(ctx, synchestraDeliveryTimeout)
	defer cancel()
	var output synchestraInvocationOutput
	if deliverer.options.Dispatch == nil {
		args := []string{
			"runner", "invoke", "@/dev/stdin", "--runner", deliverer.transport.config.Runner,
			"--handler", sessionmove.SynchestraSessionMessageHandler, "--invocation-id", message.MessageID,
			"--format", "json",
		}
		rawOutput, runErr := deliverer.transport.run(deliveryContext, args, raw, "invoke message handler")
		if runErr != nil {
			return sessionmove.MessageReceipt{}, runErr
		}
		output, err = decodeSynchestraInvocationOutput(rawOutput)
		if err != nil {
			return sessionmove.MessageReceipt{}, fmt.Errorf("validate Synchestra message invoke response: %w", err)
		}
		identity, identityErr := validateSynchestraMessageInvocationOutput(output, deliverer.transport.config.Runner, message, raw, "", deliverer.options.RequestDigest)
		if identityErr != nil {
			return sessionmove.MessageReceipt{}, identityErr
		}
		if deliverer.options.SaveDispatch != nil {
			if err := deliverer.options.SaveDispatch(identity); err != nil {
				return sessionmove.MessageReceipt{}, fmt.Errorf("persist Synchestra message dispatch identity: %w", err)
			}
		}
		deliverer.options.Dispatch = &identity
	} else {
		if err := validateSynchestraMessageResumeIdentity(*deliverer.options.Dispatch, deliverer.transport.config.Runner, message, raw, deliverer.options.RequestDigest); err != nil {
			return sessionmove.MessageReceipt{}, err
		}
		rawOutput, runErr := deliverer.transport.run(deliveryContext, synchestraStatusArgs(deliverer.options.Dispatch.DispatchID), nil, "observe message dispatch")
		if runErr != nil {
			return sessionmove.MessageReceipt{}, runErr
		}
		output, err = decodeSynchestraInvocationOutput(rawOutput)
		if err != nil {
			return sessionmove.MessageReceipt{}, err
		}
		if _, err := validateSynchestraMessageInvocationOutput(output, deliverer.transport.config.Runner, message, raw,
			deliverer.options.Dispatch.DispatchID, deliverer.options.RequestDigest); err != nil {
			return sessionmove.MessageReceipt{}, err
		}
	}

	for polls := 0; ; polls++ {
		receipt, pending, terminalErr := synchestraMessageTerminalReceipt(output, message, raw)
		if terminalErr != nil {
			return sessionmove.MessageReceipt{}, terminalErr
		}
		if !pending {
			return receipt, nil
		}
		if polls >= maxSynchestraStatusPolls {
			return sessionmove.MessageReceipt{}, fmt.Errorf("Synchestra message dispatch %s did not complete after %d bounded status polls",
				deliverer.options.Dispatch.DispatchID, maxSynchestraStatusPolls)
		}
		if err := deliverer.transport.sleep(deliveryContext, synchestraPollInterval); err != nil {
			return sessionmove.MessageReceipt{}, err
		}
		rawOutput, runErr := deliverer.transport.run(deliveryContext, synchestraStatusArgs(deliverer.options.Dispatch.DispatchID), nil, "poll message dispatch")
		if runErr != nil {
			return sessionmove.MessageReceipt{}, runErr
		}
		output, err = decodeSynchestraInvocationOutput(rawOutput)
		if err != nil {
			return sessionmove.MessageReceipt{}, err
		}
		if _, err := validateSynchestraMessageInvocationOutput(output, deliverer.transport.config.Runner, message, raw,
			deliverer.options.Dispatch.DispatchID, deliverer.options.RequestDigest); err != nil {
			return sessionmove.MessageReceipt{}, err
		}
	}
}

func validateSynchestraMessageInvocationOutput(output synchestraInvocationOutput, runner string, message sessionmove.Message,
	raw []byte, expectedDispatchID string, requestDigest sessionmove.Digest,
) (sessionmove.MessageSynchestraDispatch, error) {
	if output.Dispatch == nil || output.Resolved.Invocation == nil {
		return sessionmove.MessageSynchestraDispatch{}, fmt.Errorf("runner output lacks typed message invocation and dispatch identity")
	}
	dispatch, invocation := output.Dispatch, output.Resolved.Invocation
	if dispatch.ProtocolVersion != synchestraDispatchProtocolVersion || !synchestraDispatchIDPattern.MatchString(dispatch.ID) {
		return sessionmove.MessageSynchestraDispatch{}, fmt.Errorf("message dispatch identity is invalid")
	}
	if expectedDispatchID != "" && dispatch.ID != expectedDispatchID {
		return sessionmove.MessageSynchestraDispatch{}, fmt.Errorf("message dispatch does not match persisted dispatch")
	}
	expectedOperation, resolvedDispatchID := "invoke", ""
	if expectedDispatchID != "" {
		expectedOperation, resolvedDispatchID = "status", dispatch.ID
	}
	if output.Resolved.Operation != expectedOperation || output.Resolved.DispatchID != resolvedDispatchID ||
		output.Resolved.Runner != runner || output.Resolved.Source != nil || output.Resolved.Repository != nil ||
		output.Resolved.RequestedExecution != nil {
		return sessionmove.MessageSynchestraDispatch{}, fmt.Errorf("resolved message runner or dispatch identity does not match selected route")
	}
	if invocation.ProtocolVersion != synchestraInvocationProtocolVersion || invocation.ID != message.MessageID ||
		invocation.Handler != sessionmove.SynchestraSessionMessageHandler || invocation.PayloadDigest != string(sessionmove.DigestBytes(raw)) ||
		invocation.PayloadSize != int64(len(raw)) || invocation.Deadline != nil {
		return sessionmove.MessageSynchestraDispatch{}, fmt.Errorf("typed message invocation identity, handler, payload_digest, or payload_size does not match exact message")
	}
	for _, attempt := range output.Attempts {
		if attempt.ProtocolVersion != synchestraDispatchProtocolVersion || attempt.DispatchID != dispatch.ID {
			return sessionmove.MessageSynchestraDispatch{}, fmt.Errorf("message attempt identity does not match dispatch")
		}
	}
	if err := validateSynchestraAttemptHistory(*dispatch, output.Attempts); err != nil {
		return sessionmove.MessageSynchestraDispatch{}, err
	}
	return sessionmove.MessageSynchestraDispatch{
		SchemaVersion: sessionmove.MessageSynchestraDispatchSchemaVersion,
		HandoffID:     message.HandoffID, RequestDigest: requestDigest, MessageID: message.MessageID,
		MessageDigest: sessionmove.DigestBytes(raw), Runner: runner, InvocationID: message.MessageID,
		Handler: sessionmove.SynchestraSessionMessageHandler, DispatchID: dispatch.ID,
	}, nil
}

func validateSynchestraMessageResumeIdentity(identity sessionmove.MessageSynchestraDispatch, runner string,
	message sessionmove.Message, raw []byte, requestDigest sessionmove.Digest,
) error {
	if identity.SchemaVersion != sessionmove.MessageSynchestraDispatchSchemaVersion || identity.HandoffID != message.HandoffID ||
		identity.RequestDigest != requestDigest || identity.MessageID != message.MessageID || identity.MessageDigest != sessionmove.DigestBytes(raw) ||
		identity.Runner != runner || identity.InvocationID != message.MessageID || identity.Handler != sessionmove.SynchestraSessionMessageHandler ||
		!synchestraDispatchIDPattern.MatchString(identity.DispatchID) {
		return fmt.Errorf("persisted Synchestra message dispatch does not match exact message and runner")
	}
	return nil
}

func synchestraMessageTerminalReceipt(output synchestraInvocationOutput, message sessionmove.Message, raw []byte) (sessionmove.MessageReceipt, bool, error) {
	if output.Dispatch == nil {
		return sessionmove.MessageReceipt{}, false, fmt.Errorf("Synchestra message response has no dispatch")
	}
	switch output.Dispatch.Status {
	case "queued", "leased", "running":
		return sessionmove.MessageReceipt{}, true, nil
	case "failed", "cancelled":
		return sessionmove.MessageReceipt{}, false, fmt.Errorf("Synchestra message dispatch %s ended %s without a WB receipt", output.Dispatch.ID, output.Dispatch.Status)
	case "completed":
	default:
		return sessionmove.MessageReceipt{}, false, fmt.Errorf("Synchestra message dispatch status %q is unsupported", output.Dispatch.Status)
	}
	completed, reference := 0, ""
	for _, attempt := range output.Attempts {
		if attempt.Status != "completed" {
			continue
		}
		completed++
		if attempt.Result == nil || len(attempt.Result.ArtifactReferences) != 1 {
			return sessionmove.MessageReceipt{}, false, fmt.Errorf("completed Synchestra message attempt must contain exactly one receipt artifact")
		}
		reference = attempt.Result.ArtifactReferences[0]
	}
	if completed != 1 || reference == "" {
		return sessionmove.MessageReceipt{}, false, fmt.Errorf("completed Synchestra message dispatch must contain exactly one completed receipt attempt")
	}
	receiptRaw, err := decodeSynchestraMessageReceiptArtifact(reference, message, raw)
	if err != nil {
		return sessionmove.MessageReceipt{}, false, err
	}
	receipt, err := decodeMessageReceipt(receiptRaw, message, raw)
	return receipt, false, err
}

func decodeSynchestraMessageReceiptArtifact(reference string, message sessionmove.Message, raw []byte) ([]byte, error) {
	if !strings.HasPrefix(reference, synchestraReceiptArtifactPrefix) || len(reference) > maxSynchestraReceiptArtifactRefBytes {
		return nil, fmt.Errorf("message receipt artifact reference is invalid")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(reference, synchestraReceiptArtifactPrefix))
	if err != nil || len(encoded) == 0 || len(encoded) > maxSynchestraReceiptArtifactBytes {
		return nil, fmt.Errorf("message receipt artifact reference is invalid")
	}
	var artifact synchestraReceiptArtifact
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return nil, fmt.Errorf("message receipt artifact is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("message receipt artifact is invalid")
	}
	canonical, err := json.Marshal(artifact)
	if err != nil || synchestraReceiptArtifactPrefix+base64.RawURLEncoding.EncodeToString(canonical) != reference {
		return nil, fmt.Errorf("message receipt artifact is not canonical")
	}
	if artifact.ProtocolVersion != synchestraReceiptArtifactVersion || artifact.InvocationID != message.MessageID ||
		artifact.Handler != sessionmove.SynchestraSessionMessageHandler || artifact.PayloadDigest != string(sessionmove.DigestBytes(raw)) ||
		artifact.ReceiptDigest != string(sessionmove.DigestBytes(artifact.Receipt)) || artifact.CompletedAt.IsZero() {
		return nil, fmt.Errorf("message receipt artifact identity does not match exact invocation and bytes")
	}
	return append([]byte(nil), artifact.Receipt...), nil
}
