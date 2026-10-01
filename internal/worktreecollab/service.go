package worktreecollab

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
)

// ServicePorts bind one CLI invocation to a corroborated checkout, its
// registered ancestor, recipient liveness, and the historical custody store.
// None of these observations is supplied by an untrusted command argument.
type ServicePorts struct {
	Resolve                    func(context.Context, string) (Checkout, error)
	Caller                     func() (string, error)
	Live                       func(string) (bool, error)
	OwnerStatus                func(string) (string, error) // live, inactive, or unknown
	ObserveLegacy              func(Checkout) (ObservedOwner, error)
	ObserveLegacyForInspection func(Checkout) (ObservedOwner, error)
	Now                        func() time.Time
	NewMessageID               func() (string, error)
}

type Service struct {
	Store Store
	Ports ServicePorts
}

type Participant struct {
	SessionID string    `json:"session_id"`
	JoinedAt  time.Time `json:"joined_at"`
	Live      bool      `json:"live"`
}

type View struct {
	Checkout    Checkout      `json:"checkout"`
	Owner       string        `json:"owner"`
	OwnerStatus string        `json:"owner_status"`
	OwnerEpoch  uint64        `json:"owner_epoch"`
	Joined      []Participant `json:"joined_sessions"`
}

type InboxView struct {
	Messages []Message    `json:"messages"`
	Notice   *OwnerChange `json:"owner_notice,omitempty"`
}

func (service Service) validate() error {
	if service.Ports.Resolve == nil || service.Ports.Caller == nil || service.Ports.Live == nil ||
		service.Ports.OwnerStatus == nil || service.Ports.ObserveLegacy == nil || service.Ports.ObserveLegacyForInspection == nil || service.Ports.Now == nil || service.Ports.NewMessageID == nil {
		return fmt.Errorf("coordination invocation boundaries are incomplete")
	}
	return nil
}

func (service Service) checkout(ctx context.Context, idOrPath string) (Checkout, error) {
	if err := service.validate(); err != nil {
		return Checkout{}, err
	}
	return service.Ports.Resolve(ctx, idOrPath)
}

func (service Service) caller() (string, error) {
	caller, err := service.Ports.Caller()
	if err != nil {
		return "", err
	}
	if !validID(caller) || caller == NoOwner {
		return "", fmt.Errorf("registered invoking session is required")
	}
	return caller, nil
}

func (service Service) Join(ctx context.Context, idOrPath string) (State, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return State{}, err
	}
	caller, err := service.caller()
	if err != nil {
		return State{}, err
	}
	return service.Store.WithLocked(ctx, checkout, func(state *State, found bool) error {
		if !found {
			return fmt.Errorf("coordination is not initialized; take ownership explicitly first")
		}
		return state.Join(caller, service.Ports.Now())
	})
}

func (service Service) Leave(ctx context.Context, idOrPath string) (State, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return State{}, err
	}
	caller, err := service.caller()
	if err != nil {
		return State{}, err
	}
	return service.Store.WithLocked(ctx, checkout, func(state *State, found bool) error {
		if !found {
			return fmt.Errorf("coordination is not initialized")
		}
		return state.Leave(caller)
	})
}

func (service Service) Take(ctx context.Context, idOrPath, expected string, force bool, reason string) (State, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return State{}, err
	}
	caller, err := service.caller()
	if err != nil {
		return State{}, err
	}
	// Refuse an invalid first takeover before creating coordination metadata or
	// a historical Work Log lock. The locked transaction below remains the
	// authoritative comparison if another process changes ownership meanwhile.
	current, found, err := service.Store.Load(checkout)
	if err != nil {
		return State{}, err
	}
	if !found {
		legacy, observeErr := service.Ports.ObserveLegacyForInspection(checkout)
		if observeErr != nil {
			return State{}, observeErr
		}
		preview := current
		if err := preview.Take(TakeRequest{Caller: caller, ExpectedOwner: expected, Force: force,
			Reason: reason, At: service.Ports.Now(), Legacy: legacy}); err != nil {
			return State{}, err
		}
	}
	return service.Store.WithLocked(ctx, checkout, func(state *State, found bool) error {
		request := TakeRequest{Caller: caller, ExpectedOwner: expected, Force: force, Reason: reason, At: service.Ports.Now()}
		if !found {
			request.Legacy, err = service.Ports.ObserveLegacy(checkout)
			if err != nil {
				return err
			}
		} else {
			request.OwnerStatus, err = service.Ports.OwnerStatus(state.Owner)
			if err != nil {
				return err
			}
			if request.OwnerStatus != "live" && request.OwnerStatus != "inactive" && request.OwnerStatus != "unknown" {
				return fmt.Errorf("current owner liveness is not corroborated")
			}
		}
		return state.Take(request)
	})
}

func (service Service) Transfer(ctx context.Context, idOrPath, successor string) (State, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return State{}, err
	}
	caller, err := service.caller()
	if err != nil {
		return State{}, err
	}
	return service.Store.WithLocked(ctx, checkout, func(state *State, found bool) error {
		if !found {
			return fmt.Errorf("coordination is not initialized")
		}
		if state.Owner != caller {
			return fmt.Errorf("only the current owner may transfer ownership")
		}
		live, err := service.Ports.Live(successor)
		if err != nil {
			return err
		}
		return state.Transfer(caller, successor, live, service.Ports.Now())
	})
}

func (service Service) Send(ctx context.Context, idOrPath, key string, recipients []string, body string) (SendReceipt, bool, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return SendReceipt{}, false, err
	}
	caller, err := service.caller()
	if err != nil {
		return SendReceipt{}, false, err
	}
	var receipt SendReceipt
	var replay bool
	_, err = service.Store.WithLocked(ctx, checkout, func(state *State, found bool) error {
		if !found {
			return fmt.Errorf("coordination is not initialized")
		}
		messageID := ""
		if prior, exists := state.Requests[key]; exists {
			messageID = prior.MessageID
		} else {
			var idErr error
			messageID, idErr = service.Ports.NewMessageID()
			if idErr != nil {
				return idErr
			}
		}
		var sendErr error
		receipt, replay, sendErr = state.Send(SendRequest{Sender: caller, Recipients: recipients, IdempotencyKey: key,
			MessageID: messageID, Kind: "text", Body: body, At: service.Ports.Now()})
		return sendErr
	})
	return receipt, replay, err
}

func (service Service) Inbox(ctx context.Context, idOrPath string) (InboxView, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return InboxView{}, err
	}
	caller, err := service.caller()
	if err != nil {
		return InboxView{}, err
	}
	var view InboxView
	_, err = service.Store.WithLocked(ctx, checkout, func(state *State, found bool) error {
		if !found {
			return fmt.Errorf("coordination is not initialized")
		}
		view.Messages, err = state.Inbox(caller)
		if err == nil {
			if notice, found := state.Notices[caller]; found {
				view.Notice = &notice
			}
		}
		return err
	})
	return view, err
}

func (service Service) Ack(ctx context.Context, idOrPath, messageID string) (uint64, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return 0, err
	}
	caller, err := service.caller()
	if err != nil {
		return 0, err
	}
	var cursor uint64
	_, err = service.Store.WithLocked(ctx, checkout, func(state *State, found bool) error {
		if !found {
			return fmt.Errorf("coordination is not initialized")
		}
		var digest string
		for _, message := range state.Inboxes[caller] {
			if message.ID == messageID {
				digest = message.Digest
				break
			}
		}
		cursor, err = state.Ack(caller, messageID, digest, service.Ports.Now())
		return err
	})
	return cursor, err
}

func (service Service) Inspect(ctx context.Context, idOrPath string) (View, error) {
	checkout, err := service.checkout(ctx, idOrPath)
	if err != nil {
		return View{}, err
	}
	view := View{Checkout: checkout, Owner: NoOwner, OwnerStatus: "none", Joined: []Participant{}}
	state, found, err := service.Store.Load(checkout)
	if err != nil {
		return View{}, err
	}
	if !found {
		legacy, err := service.Ports.ObserveLegacyForInspection(checkout)
		if err != nil {
			return View{}, err
		}
		if legacy.ID == "" && (legacy.Status != "" || legacy.SessionID != "") {
			return View{}, fmt.Errorf("legacy ownership evidence is unresolved without an exact observation ID")
		}
		if legacy.ID != "" {
			view.Owner = legacy.ID
			view.OwnerStatus = legacy.Status
		}
		return view, nil
	}
	view.Owner, view.OwnerEpoch = state.Owner, state.OwnerEpoch
	view.OwnerStatus, err = service.Ports.OwnerStatus(state.Owner)
	if err != nil {
		return View{}, err
	}
	if view.OwnerStatus != "live" && view.OwnerStatus != "inactive" && view.OwnerStatus != "unknown" {
		return View{}, fmt.Errorf("current owner liveness is not corroborated")
	}
	for id, member := range state.Members {
		live, err := service.Ports.Live(id)
		if err != nil {
			return View{}, err
		}
		view.Joined = append(view.Joined, Participant{SessionID: id, JoinedAt: member.JoinedAt, Live: live})
	}
	slices.SortFunc(view.Joined, func(a, b Participant) int { return cmp.Compare(a.SessionID, b.SessionID) })
	return view, nil
}
