// Package worktreecollab owns the opt-in current owner, participants, and
// private local inbox for one corroborated worktree. It never changes a Work
// Log claim or interprets historical owner events as simultaneous owners.
package worktreecollab

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	SchemaVersion       = 1
	MaxMessageBodyBytes = 64 << 10
	MaxPendingPerPeer   = 128
	MaxStoredMessages   = 4096
	NoOwner             = "none"
)

var ErrConflict = errors.New("worktree coordination conflict")

// Checkout binds coordination state to Git's registered worktree identity.
// Root is presentation/relocation evidence; GitDir and CommonDir identify the
// linked checkout and canonical repository independently of path aliases.
type Checkout struct {
	ID        string `json:"id"`
	Root      string `json:"root"`
	GitDir    string `json:"git_dir"`
	CommonDir string `json:"common_dir"`
}

// ObservedOwner is an exact observation of legacy custody before the first
// coordination state is published. ID is opaque when WB cannot bind a live
// registered session to the historical event.
type ObservedOwner struct {
	ID        string
	SessionID string
	Status    string // live, inactive, or unknown
}

type Member struct {
	JoinedAt time.Time `json:"joined_at"`
}

type OwnerChange struct {
	Epoch    uint64    `json:"epoch"`
	Previous string    `json:"previous"`
	Current  string    `json:"current"`
	Actor    string    `json:"actor"`
	Reason   string    `json:"reason,omitempty"`
	Forced   bool      `json:"forced,omitempty"`
	At       time.Time `json:"at"`
}

// CheckoutRebind records an explicit recovery when Git reuses a linked
// worktree's administrative directory after the former checkout root has
// been retired. It changes only the presentation root; the checkout ID,
// GitDir, and CommonDir remain identical.
type CheckoutRebind struct {
	PreviousRoot string    `json:"previous_root"`
	CurrentRoot  string    `json:"current_root"`
	Actor        string    `json:"actor"`
	At           time.Time `json:"at"`
}

type Message struct {
	ID         string    `json:"id"`
	Sequence   uint64    `json:"sequence"`
	Sender     string    `json:"sender"`
	Recipient  string    `json:"recipient"`
	Kind       string    `json:"kind"`
	Body       string    `json:"body"`
	Digest     string    `json:"digest"`
	OwnerEpoch uint64    `json:"owner_epoch"`
	RecordedAt time.Time `json:"recorded_at"`
	ConsumedAt time.Time `json:"consumed_at,omitempty"`
}

type SendReceipt struct {
	MessageID  string   `json:"message_id"`
	Sender     string   `json:"sender"`
	Recipients []string `json:"recipients"`
	Kind       string   `json:"kind"`
	Body       string   `json:"body"`
	Digest     string   `json:"digest"`
	OwnerEpoch uint64   `json:"owner_epoch"`
}

// State is one atomically published private snapshot. Owner notices and audit
// are separate from ordinary inbox capacity, so a full user inbox cannot
// prevent a transfer or recovery.
type State struct {
	Version         int                    `json:"version"`
	Checkout        Checkout               `json:"checkout"`
	Revision        uint64                 `json:"revision"`
	Owner           string                 `json:"owner"`
	OwnerEpoch      uint64                 `json:"owner_epoch"`
	Members         map[string]Member      `json:"members"`
	OwnerChanges    []OwnerChange          `json:"owner_changes"`
	CheckoutRebinds []CheckoutRebind       `json:"checkout_rebinds,omitempty"`
	Notices         map[string]OwnerChange `json:"notices"`
	Inboxes         map[string][]Message   `json:"inboxes"`
	Requests        map[string]SendReceipt `json:"requests"`
	Cursors         map[string]uint64      `json:"cursors"`
}

type TakeRequest struct {
	Caller        string
	ExpectedOwner string
	Legacy        ObservedOwner
	OwnerStatus   string
	Force         bool
	Reason        string
	At            time.Time
}

func validID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < '!' || character > '~' || character == '/' || character == '\\' {
			return false
		}
	}
	return true
}

func (checkout Checkout) Validate() error {
	if !validID(checkout.ID) || strings.IndexFunc(checkout.ID, func(character rune) bool {
		return character != '-' && character != '_' && (character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') && (character < 'a' || character > 'z')
	}) >= 0 || checkout.Root == "" || checkout.GitDir == "" || checkout.CommonDir == "" {
		return fmt.Errorf("coordination checkout identity is incomplete")
	}
	return nil
}

func New(checkout Checkout) (State, error) {
	if err := checkout.Validate(); err != nil {
		return State{}, err
	}
	return State{Version: SchemaVersion, Checkout: checkout, Members: map[string]Member{},
		Notices: map[string]OwnerChange{}, Inboxes: map[string][]Message{},
		Requests: map[string]SendReceipt{}, Cursors: map[string]uint64{}}, nil
}

func (state State) Validate(checkout Checkout) error {
	if err := state.validateEnvelope(); err != nil {
		return err
	}
	if state.Checkout != checkout {
		return fmt.Errorf("coordination state does not match the exact checkout")
	}
	return nil
}

func (state State) validateEnvelope() error {
	if state.Version != SchemaVersion || !validID(state.Owner) {
		return fmt.Errorf("coordination state version or owner is invalid")
	}
	if _, ok := state.Members[state.Owner]; !ok {
		return fmt.Errorf("coordination owner is not joined")
	}
	if state.Members == nil || state.Inboxes == nil || state.Requests == nil || state.Cursors == nil || state.Notices == nil {
		return fmt.Errorf("coordination state collections are incomplete")
	}
	return nil
}

// ValidateRebind permits only an exact root-path correction for the same
// checkout identity, after the caller supplies the root observed before the
// worktree path changed. All other checkout fields remain strictly bound.
func (state State) ValidateRebind(checkout Checkout, expectedRoot string) error {
	if err := state.validateEnvelope(); err != nil {
		return err
	}
	if expectedRoot == "" || !filepath.IsAbs(expectedRoot) || filepath.Clean(expectedRoot) != expectedRoot ||
		expectedRoot == checkout.Root || state.Checkout.Root != expectedRoot ||
		state.Checkout.ID != checkout.ID || state.Checkout.GitDir != checkout.GitDir || state.Checkout.CommonDir != checkout.CommonDir {
		return fmt.Errorf("coordination rebind does not match the exact prior and current checkout identities")
	}
	return nil
}

func (state *State) Take(request TakeRequest) error {
	if state == nil || !validID(request.Caller) || request.Caller == NoOwner || request.At.IsZero() ||
		!validID(request.ExpectedOwner) {
		return fmt.Errorf("take ownership requires caller, expected owner, and time")
	}
	if state.Version != SchemaVersion || state.Members == nil || state.Notices == nil || state.Inboxes == nil || state.Requests == nil || state.Cursors == nil {
		return fmt.Errorf("take ownership requires initialized coordination state")
	}
	observed, status := state.Owner, request.OwnerStatus
	if observed == "" {
		if request.Legacy.ID == "" && (request.Legacy.Status != "" || request.Legacy.SessionID != "") {
			return fmt.Errorf("legacy ownership evidence is unresolved without an exact observation ID")
		}
		observed, status = request.Legacy.ID, request.Legacy.Status
		if observed == "" {
			observed = NoOwner
		}
	}
	if observed != request.ExpectedOwner {
		return fmt.Errorf("%w: expected owner %s, observed %s", ErrConflict, request.ExpectedOwner, observed)
	}
	if observed == request.Caller && state.Owner == request.Caller {
		return nil
	}
	selfLegacy := state.Owner == "" && request.Legacy.SessionID == request.Caller && observed != NoOwner
	if observed != NoOwner && !selfLegacy && status != "inactive" {
		if !request.Force || strings.TrimSpace(request.Reason) == "" {
			return fmt.Errorf("live or uncertain owner takeover requires --force and --reason")
		}
	}
	state.Members[request.Caller] = Member{JoinedAt: request.At.UTC()}
	state.Owner = request.Caller
	state.OwnerEpoch++
	state.Revision++
	state.recordChange(OwnerChange{Epoch: state.OwnerEpoch, Previous: observed, Current: request.Caller,
		Actor: request.Caller, Reason: strings.TrimSpace(request.Reason), Forced: request.Force, At: request.At.UTC()})
	return nil
}

func (state *State) Join(caller string, at time.Time) error {
	if state == nil || !validID(caller) || caller == NoOwner || at.IsZero() || state.Owner == "" {
		return fmt.Errorf("join requires an initialized owner and registered session")
	}
	if _, found := state.Members[caller]; found {
		return nil
	}
	state.Members[caller] = Member{JoinedAt: at.UTC()}
	state.Revision++
	return nil
}

func (state *State) Leave(caller string) error {
	if state == nil || !validID(caller) || state.Owner == "" {
		return fmt.Errorf("leave requires an initialized owner and registered session")
	}
	if state.Owner == caller {
		return fmt.Errorf("current owner must transfer ownership before leaving")
	}
	if _, found := state.Members[caller]; !found {
		return nil
	}
	delete(state.Members, caller)
	state.Revision++
	return nil
}

func (state *State) Transfer(caller, successor string, successorLive bool, at time.Time) error {
	if state == nil || !validID(caller) || !validID(successor) || at.IsZero() || state.Owner != caller {
		return fmt.Errorf("transfer requires the authenticated current owner and successor")
	}
	if !successorLive {
		return fmt.Errorf("transfer successor is not live")
	}
	if _, found := state.Members[successor]; !found {
		return fmt.Errorf("transfer successor is not joined")
	}
	if successor == caller {
		return nil
	}
	state.Owner = successor
	state.OwnerEpoch++
	state.Revision++
	state.recordChange(OwnerChange{Epoch: state.OwnerEpoch, Previous: caller, Current: successor,
		Actor: caller, At: at.UTC()})
	return nil
}

func (state *State) recordChange(change OwnerChange) {
	state.OwnerChanges = append(state.OwnerChanges, change)
	for member := range state.Members {
		state.Notices[member] = change
	}
}

type SendRequest struct {
	Sender         string
	Recipients     []string
	IdempotencyKey string
	MessageID      string
	Kind           string
	Body           string
	At             time.Time
}

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (state *State) Send(request SendRequest) (SendReceipt, bool, error) {
	if state == nil || !validID(request.Sender) || !validID(request.IdempotencyKey) ||
		!validID(request.MessageID) || request.At.IsZero() || request.Kind != "text" ||
		len(request.Body) > MaxMessageBodyBytes || len(request.Recipients) == 0 {
		return SendReceipt{}, false, fmt.Errorf("message request is invalid or exceeds its size limit")
	}
	if _, joined := state.Members[request.Sender]; !joined {
		return SendReceipt{}, false, fmt.Errorf("message sender is not joined")
	}
	recipients := slices.Clone(request.Recipients)
	slices.Sort(recipients)
	for index, recipient := range recipients {
		if !validID(recipient) || (index > 0 && recipient == recipients[index-1]) {
			return SendReceipt{}, false, fmt.Errorf("message recipients must be unique session IDs")
		}
	}
	key := request.IdempotencyKey
	if prior, found := state.Requests[key]; found {
		if prior.Sender != request.Sender || prior.Kind != request.Kind || prior.Body != request.Body ||
			!slices.Equal(prior.Recipients, recipients) {
			return SendReceipt{}, false, fmt.Errorf("%w: idempotency key denotes different message bytes or recipients", ErrConflict)
		}
		return prior, true, nil
	}
	if len(state.Requests) >= MaxStoredMessages {
		return SendReceipt{}, false, fmt.Errorf("message storage is full")
	}
	for _, prior := range state.Requests {
		if prior.MessageID == request.MessageID {
			return SendReceipt{}, false, fmt.Errorf("%w: message ID already recorded", ErrConflict)
		}
	}
	for _, recipient := range recipients {
		if _, joined := state.Members[recipient]; !joined {
			return SendReceipt{}, false, fmt.Errorf("message recipient %s is not joined", recipient)
		}
		pending := 0
		for _, message := range state.Inboxes[recipient] {
			if message.ConsumedAt.IsZero() {
				pending++
			}
		}
		if pending >= MaxPendingPerPeer {
			return SendReceipt{}, false, fmt.Errorf("recipient %s inbox is full", recipient)
		}
	}
	receipt := SendReceipt{MessageID: request.MessageID, Sender: request.Sender,
		Recipients: recipients, Kind: request.Kind, Body: request.Body,
		Digest: digest(request.Body), OwnerEpoch: state.OwnerEpoch}
	for _, recipient := range recipients {
		state.Inboxes[recipient] = append(state.Inboxes[recipient], Message{
			ID: request.MessageID, Sequence: uint64(len(state.Inboxes[recipient]) + 1), Sender: request.Sender,
			Recipient: recipient, Kind: request.Kind, Body: request.Body, Digest: receipt.Digest,
			OwnerEpoch: state.OwnerEpoch, RecordedAt: request.At.UTC(),
		})
	}
	state.Requests[key] = receipt
	state.Revision++
	return receipt, false, nil
}

func (state *State) Inbox(recipient string) ([]Message, error) {
	if state == nil || !validID(recipient) {
		return nil, fmt.Errorf("recipient session ID is required")
	}
	if _, joined := state.Members[recipient]; !joined {
		return nil, fmt.Errorf("recipient is not joined")
	}
	var unread []Message
	for _, message := range state.Inboxes[recipient] {
		if message.ConsumedAt.IsZero() {
			unread = append(unread, message)
		}
	}
	return unread, nil
}

func (state *State) Ack(recipient, messageID, messageDigest string, at time.Time) (uint64, error) {
	if state == nil || !validID(recipient) || !validID(messageID) || at.IsZero() {
		return 0, fmt.Errorf("ack requires recipient, message ID and time")
	}
	if _, joined := state.Members[recipient]; !joined {
		return 0, fmt.Errorf("ack recipient is not joined")
	}
	for index := range state.Inboxes[recipient] {
		message := &state.Inboxes[recipient][index]
		if message.ID != messageID {
			continue
		}
		if message.Digest != messageDigest {
			return 0, fmt.Errorf("%w: message digest differs", ErrConflict)
		}
		if !message.ConsumedAt.IsZero() {
			return state.Cursors[recipient], nil
		}
		message.ConsumedAt = at.UTC()
		for _, item := range state.Inboxes[recipient] {
			if item.Sequence <= state.Cursors[recipient] {
				continue
			}
			if item.Sequence != state.Cursors[recipient]+1 || item.ConsumedAt.IsZero() {
				break
			}
			state.Cursors[recipient]++
		}
		state.Revision++
		return state.Cursors[recipient], nil
	}
	return 0, fmt.Errorf("message %s is not in recipient inbox", messageID)
}
