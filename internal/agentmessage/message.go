// Package agentmessage transports durable, explicitly acknowledged agent inboxes.
// A delivery receipt is not evidence that an agent processed the message.
package agentmessage

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

const MaxBodyBytes = 64 << 10
const MaxEnvelopeBytes = 6*MaxBodyBytes + 4096

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Message struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Body    string `json:"body"`
}

type Receipt struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Status string `json:"status"`
	Wake   string `json:"wake"`
}

// validFor treats delivery and wake status as one closed receipt protocol.
// Unknown or absent outcomes cannot authorize a transport acknowledgment.
func (r Receipt) validFor(m Message) bool {
	switch r.Wake {
	case "not-bound", "submitted", "acknowledged", "preflight-failed", "rejected", "ambiguous":
		return r.ID == m.ID && r.Digest == m.Digest() && r.Status == "durable"
	default:
		return false
	}
}

func ValidateAgent(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("agent name must be 1-64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	return nil
}

func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("message ID must be 32 lowercase hexadecimal characters")
	}
	return nil
}

func (m Message) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported message version %d", m.Version)
	}
	if err := ValidateID(m.ID); err != nil {
		return err
	}
	if err := ValidateAgent(m.From); err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	if err := ValidateAgent(m.To); err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	if len(m.Body) == 0 || len(m.Body) > MaxBodyBytes || !utf8.ValidString(m.Body) {
		return fmt.Errorf("message must contain 1-%d UTF-8 bytes", MaxBodyBytes)
	}
	return nil
}

func NewID() string { var id [16]byte; _, _ = rand.Read(id[:]); return hex.EncodeToString(id[:]) }

func (m Message) Digest() string {
	raw, _ := json.Marshal(m)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func Decode(reader io.Reader) (Message, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxEnvelopeBytes+1))
	if err != nil {
		return Message{}, err
	}
	if len(raw) > MaxEnvelopeBytes || !utf8.Valid(raw) {
		return Message{}, fmt.Errorf("message envelope is oversized or not UTF-8")
	}
	var message Message
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&message); err != nil {
		return Message{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Message{}, fmt.Errorf("message envelope must contain exactly one JSON value")
	}
	return message, message.Validate()
}
