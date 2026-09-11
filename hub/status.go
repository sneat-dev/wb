package hub

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

const StatusPath = APIPrefix + "/github/status"

type StatusConnection struct {
	State      string `json:"state"`
	AppName    string `json:"app_name,omitempty"`
	Account    string `json:"account,omitempty"`
	ConnectURL string `json:"connect_url,omitempty"`
}
type StatusInstallation struct {
	ID                  string `json:"id"`
	Account             string `json:"account"`
	AccountType         string `json:"account_type,omitempty"`
	State               string `json:"state"`
	RepositorySelection string `json:"repository_selection,omitempty"`
	Repositories        int    `json:"repositories,omitempty"`
	ManageURL           string `json:"manage_url,omitempty"`
}
type StatusMachine struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	State      string     `json:"state,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}
type StatusDeliveryMarker struct {
	DeliveryID string    `json:"delivery_id,omitempty"`
	Event      string    `json:"event,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}
type StatusDelivery struct {
	LastReceived     *StatusDeliveryMarker `json:"last_received,omitempty"`
	LastAcknowledged *StatusDeliveryMarker `json:"last_acknowledged,omitempty"`
}
type PendingRefresh struct {
	ID             string    `json:"id"`
	Repository     string    `json:"repository"`
	Event          string    `json:"event,omitempty"`
	QueuedAt       time.Time `json:"queued_at"`
	InstallationID string    `json:"installation_id,omitempty"`
	MachineID      string    `json:"machine_id,omitempty"`
}
type StatusError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Action         string `json:"action,omitempty"`
	ActionURL      string `json:"action_url,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
}
type StatusResponse struct {
	GeneratedAt      time.Time            `json:"generated_at"`
	Connection       StatusConnection     `json:"connection"`
	Installations    []StatusInstallation `json:"installations"`
	Machines         []StatusMachine      `json:"machines"`
	Delivery         *StatusDelivery      `json:"delivery,omitempty"`
	PendingRefreshes []PendingRefresh     `json:"pending_refreshes"`
	Errors           []StatusError        `json:"errors"`
}

type RepositoryEventStatusStore interface {
	IdentityRepositoryEventStatus(context.Context, string) (*StatusDelivery, []PendingRefresh, []StatusError, error)
}

type StatusService struct {
	Bindings  InstallationBindingStore
	Snapshots MachineSnapshotStore
	Events    RepositoryEventStatusStore
	AppName   string
	Now       func() time.Time
}

func (service StatusService) Read(ctx context.Context, viewer Viewer) (StatusResponse, error) {
	if !viewer.Authenticated || strings.TrimSpace(viewer.IdentityID) == "" {
		return StatusResponse{}, ErrUnauthorized
	}
	if service.Bindings == nil || service.Snapshots == nil || service.Events == nil {
		return StatusResponse{}, ErrUnavailable
	}
	bindings, err := service.Bindings.ListIdentityInstallationBindings(ctx, viewer.IdentityID)
	if err != nil {
		return StatusResponse{}, errors.New("list installation bindings")
	}
	snapshots, err := service.Snapshots.ListLatest(ctx)
	if err != nil {
		return StatusResponse{}, errors.New("list machine snapshots")
	}
	delivery, pending, statusErrors, err := service.Events.IdentityRepositoryEventStatus(ctx, viewer.IdentityID)
	if err != nil {
		return StatusResponse{}, errors.New("read repository event status")
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	response := StatusResponse{
		GeneratedAt:      now,
		Connection:       StatusConnection{State: "disconnected", AppName: service.AppName},
		Installations:    make([]StatusInstallation, 0, len(bindings)),
		Machines:         make([]StatusMachine, 0),
		Delivery:         delivery,
		PendingRefreshes: append([]PendingRefresh(nil), pending...),
		Errors:           append([]StatusError(nil), statusErrors...),
	}
	hasActiveInstallation := false
	hasInactiveInstallation := false
	for _, binding := range bindings {
		if binding.IdentityID != viewer.IdentityID || binding.Installation.ID <= 0 {
			return StatusResponse{}, errors.New("invalid installation binding")
		}
		installationState := binding.Installation.State
		if installationState == "" {
			installationState = "installed"
		}
		if installationState == "installed" {
			hasActiveInstallation = true
		} else {
			hasInactiveInstallation = true
		}
		response.Installations = append(response.Installations, StatusInstallation{
			ID:                  strconv.FormatInt(binding.Installation.ID, 10),
			Account:             binding.Installation.Account,
			AccountType:         strings.ToLower(binding.Installation.AccountType),
			State:               installationState,
			RepositorySelection: binding.Installation.RepositorySelection,
			Repositories:        len(binding.Installation.Repositories),
			ManageURL:           binding.Installation.ManageURL,
		})
	}
	slices.SortFunc(response.Installations, func(a, b StatusInstallation) int {
		if byAccount := strings.Compare(strings.ToLower(a.Account), strings.ToLower(b.Account)); byAccount != 0 {
			return byAccount
		}
		return strings.Compare(a.ID, b.ID)
	})
	if hasActiveInstallation {
		response.Connection.State = "connected"
		account := response.Installations[0].Account
		oneAccount := true
		for _, installation := range response.Installations[1:] {
			if !strings.EqualFold(account, installation.Account) {
				oneAccount = false
				break
			}
		}
		if oneAccount {
			response.Connection.Account = account
		}
	}
	if hasInactiveInstallation || len(response.Errors) > 0 {
		response.Connection.State = "attention"
	}
	for _, record := range snapshots {
		if record.IdentityID != viewer.IdentityID {
			continue
		}
		if record.MachineID == "" || record.Snapshot.Validate() != nil {
			return StatusResponse{}, errors.New("invalid machine snapshot")
		}
		machine := StatusMachine{ID: record.MachineID, Name: record.Snapshot.Machine, State: "unknown"}
		if !record.Snapshot.LastSeenAt.IsZero() {
			lastSeenAt := record.Snapshot.LastSeenAt.UTC()
			machine.LastSeenAt = &lastSeenAt
			if now.Sub(record.Snapshot.LastSeenAt) <= 5*time.Minute {
				machine.State = "online"
			} else {
				machine.State = "offline"
			}
		}
		response.Machines = append(response.Machines, machine)
	}
	slices.SortFunc(response.Machines, func(a, b StatusMachine) int {
		if byName := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); byName != 0 {
			return byName
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(response.PendingRefreshes, func(a, b PendingRefresh) int {
		if byQueuedAt := a.QueuedAt.Compare(b.QueuedAt); byQueuedAt != 0 {
			return byQueuedAt
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(response.Errors, func(a, b StatusError) int {
		if byCode := strings.Compare(a.Code, b.Code); byCode != 0 {
			return byCode
		}
		if byInstallation := strings.Compare(a.InstallationID, b.InstallationID); byInstallation != 0 {
			return byInstallation
		}
		return strings.Compare(a.Message, b.Message)
	})
	return response, nil
}
