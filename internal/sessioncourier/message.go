package sessioncourier

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

const (
	maxMessageCourierBytes = sessionmove.MaxMessageBodyBytes + (16 << 10)
	messageDeliveryTimeout = 2 * time.Minute
)

// MessageDeliverer is the courier-neutral fixed typed-message boundary.
type MessageDeliverer interface {
	DeliverMessage(context.Context, []byte) (sessionmove.MessageReceipt, error)
}

type sshMessageDeliverer struct{ ssh *sshDeliverer }

func NewSSHMessageDeliverer(config sessionmove.SSHConfig) (MessageDeliverer, error) {
	return newSSHMessageDeliverer(config, exec.LookPath, execCommandRunner{})
}

func newSSHMessageDeliverer(config sessionmove.SSHConfig, lookPath func(string) (string, error), runner commandRunner) (*sshMessageDeliverer, error) {
	deliverer, err := newSSHDeliverer(config, lookPath, runner)
	if err != nil {
		return nil, err
	}
	return &sshMessageDeliverer{ssh: deliverer}, nil
}

func (deliverer *sshMessageDeliverer) DeliverMessage(ctx context.Context, raw []byte) (sessionmove.MessageReceipt, error) {
	message, err := validateMessagePayload(raw)
	if err != nil {
		return sessionmove.MessageReceipt{}, fmt.Errorf("validate SSH session message: %w", err)
	}
	remoteWB := deliverer.ssh.config.WBPath
	if remoteWB == "" {
		remoteWB = defaultRemoteWBCommand
	}
	args := []string{
		"-T", "-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", sshConnectTimeout), "--",
		deliverer.ssh.config.Host, remoteWB, "--non-interactive", "session", "receive-message", "--format", "json",
	}
	var stdout, stderr boundedBuffer
	stdout.limit = maxMessageCourierBytes
	stderr.limit = maxSSHStderrBytes
	deliveryContext, cancel := context.WithTimeout(ctx, messageDeliveryTimeout)
	defer cancel()
	if err := deliverer.ssh.runner.Run(deliveryContext, deliverer.ssh.executable, args, raw, &stdout, &stderr); err != nil {
		if deliveryContext.Err() != nil {
			return sessionmove.MessageReceipt{}, fmt.Errorf("SSH session message delivery to %s: %w", deliverer.ssh.config.Host, deliveryContext.Err())
		}
		diagnostic := sanitizeDiagnostic(stderr.Bytes(), stderr.exceeded)
		if diagnostic == "" {
			return sessionmove.MessageReceipt{}, fmt.Errorf("SSH session message delivery to %s: %w", deliverer.ssh.config.Host, err)
		}
		return sessionmove.MessageReceipt{}, fmt.Errorf("SSH session message delivery to %s: %w: %s", deliverer.ssh.config.Host, err, diagnostic)
	}
	if stdout.exceeded {
		return sessionmove.MessageReceipt{}, fmt.Errorf("SSH message receipt from %s exceeds %d bytes", deliverer.ssh.config.Host, maxMessageCourierBytes)
	}
	receipt, err := decodeMessageReceipt(stdout.Bytes(), message, raw)
	if err != nil {
		return sessionmove.MessageReceipt{}, fmt.Errorf("validate SSH message receipt from %s: %w", deliverer.ssh.config.Host, err)
	}
	return receipt, nil
}

func validateMessagePayload(raw []byte) (sessionmove.Message, error) {
	if len(raw) == 0 || len(raw) > maxMessageCourierBytes {
		return sessionmove.Message{}, fmt.Errorf("session message must be non-empty and at most %d bytes", maxMessageCourierBytes)
	}
	message, err := sessionmove.DecodeMessage(raw)
	if err != nil {
		return sessionmove.Message{}, err
	}
	canonical, err := sessionmove.EncodeMessage(message)
	if err != nil {
		return sessionmove.Message{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return sessionmove.Message{}, fmt.Errorf("session message must use WB's canonical JSON encoding")
	}
	return message, nil
}

func decodeMessageReceipt(raw []byte, message sessionmove.Message, messageRaw []byte) (sessionmove.MessageReceipt, error) {
	receipt, err := sessionmove.DecodeMessageReceipt(raw)
	if err != nil {
		return sessionmove.MessageReceipt{}, err
	}
	canonical, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		return sessionmove.MessageReceipt{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return sessionmove.MessageReceipt{}, fmt.Errorf("message receipt must use WB's canonical JSON encoding")
	}
	if err := sessionmove.ValidateMessageReceipt(receipt, message, sessionmove.DigestBytes(messageRaw),
		"wb-session-"+message.RecipientWBSessionID, receipt.PID); err != nil {
		return sessionmove.MessageReceipt{}, err
	}
	return receipt, nil
}
