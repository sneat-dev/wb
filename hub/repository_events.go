package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/hub/narrate"
)

type RepositoryEventService struct {
	Snapshots    MachineSnapshotStore
	Entitlements RepositoryEntitlementResolver
	Lifecycle    InstallationLifecycleStore
	Store        RepositoryEventStore
	PollInterval time.Duration
	Sleep        func(context.Context, time.Duration) error
	Now          func() time.Time
	// Narrate receives one line for every delivery the service decides about:
	// queued, ignored, dropped as a duplicate, or applied as a lifecycle
	// change. A delivery the service returns an error for is narrated by the
	// HTTP handler instead, so every delivery produces exactly one line.
	// Optional; the hosted instance leaves it unset.
	Narrate func(narrate.Line)
}

// narrateLine is the single place the service decides whether there is
// anywhere to narrate to, so every call site stays one statement.
func (service RepositoryEventService) narrateLine(event, subject, action string) {
	if service.Narrate == nil {
		return
	}
	at := time.Now()
	if service.Now != nil {
		at = service.Now()
	}
	service.Narrate(narrate.Line{At: at, Event: event, Subject: subject, Action: action})
}

// narrationSubject is the organisation or repository a line is about. A
// delivery that names no repository (an installation lifecycle event, for
// example) is narrated against github.com rather than against an empty
// column.
func narrationSubject(delivery WebhookDelivery) string {
	if strings.TrimSpace(delivery.Repository) != "" {
		return delivery.Repository
	}
	return "github.com"
}

type routedRepositoryEvent struct {
	event          repositoryevent.Event
	installationID int64
	repositoryID   int64
}

func (service RepositoryEventService) EnqueueWebhook(ctx context.Context, delivery WebhookDelivery) (EnqueueResult, error) {
	lifecycle, lifecycleEvent, err := translateInstallationLifecycle(delivery)
	if err != nil {
		return EnqueueResult{}, err
	}
	if lifecycle {
		if service.Lifecycle == nil {
			return EnqueueResult{}, ErrUnavailable
		}
		if err := service.Lifecycle.ApplyInstallationLifecycle(ctx, lifecycleEvent); err != nil {
			return EnqueueResult{}, ErrUnavailable
		}
		service.narrateLine(delivery.Event, narrationSubject(delivery), "entitlements refreshed")
		return EnqueueResult{}, nil
	}
	routed, supported, ignored, err := translateWebhook(delivery)
	if err != nil {
		return EnqueueResult{}, err
	}
	if !supported {
		service.narrateLine(delivery.Event, narrationSubject(delivery), "ignored: "+ignored)
		return EnqueueResult{}, nil
	}
	if service.Snapshots == nil || service.Entitlements == nil || service.Store == nil {
		return EnqueueResult{}, ErrUnavailable
	}
	lookupRepository := routed.event.Repository
	if routed.event.Reason == repositoryevent.ReasonRepositoryRenamed {
		lookupRepository = routed.event.PreviousRepository
	}
	machines, err := service.machinesForRepository(ctx, lookupRepository)
	if err != nil {
		return EnqueueResult{}, err
	}
	eligible := make([]Machine, 0, len(machines))
	for _, machine := range machines {
		allowed, resolveErr := service.Entitlements.IdentityHasRepositoryEntitlement(ctx, machine.IdentityID, routed.installationID, routed.repositoryID)
		if resolveErr != nil {
			return EnqueueResult{}, errors.New("resolve repository entitlement")
		}
		if allowed {
			eligible = append(eligible, machine)
		}
	}
	if len(eligible) == 0 {
		service.narrateLine(delivery.Event, routed.event.Repository, "ignored: no entitled machine")
		return service.Store.EnqueueForMachines(ctx, routed.event, eligible)
	}
	result, err := service.Store.EnqueueForMachines(ctx, routed.event, eligible)
	if err != nil {
		return EnqueueResult{}, err
	}
	if result.Duplicate {
		service.narrateLine(delivery.Event, routed.event.Repository, "duplicate delivery "+routed.event.ID+"; dropped")
		return result, nil
	}
	service.narrateLine(delivery.Event, routed.event.Repository, fmt.Sprintf("queued for %d machines", result.Enqueued))
	return result, nil
}

func (service RepositoryEventService) machinesForRepository(ctx context.Context, repository string) ([]Machine, error) {
	records, err := service.Snapshots.ListLatest(ctx)
	if err != nil {
		return nil, errors.New("list machine snapshots")
	}
	byID := make(map[string]Machine)
	for _, record := range records {
		if record.IdentityID == "" || record.MachineID == "" || record.ReceivedAt.IsZero() || record.Digest == "" || record.Snapshot.Validate() != nil {
			return nil, errors.New("stored machine snapshot is invalid")
		}
		if _, found := slices.BinarySearch(record.Snapshot.Repositories, strings.ToLower(repository)); !found {
			continue
		}
		machine := Machine{ID: record.MachineID, Name: record.Snapshot.Machine, IdentityID: record.IdentityID}
		if existing, found := byID[machine.ID]; found && (existing.IdentityID != machine.IdentityID || existing.Name != machine.Name) {
			return nil, errors.New("stored machine identity conflicts")
		}
		byID[machine.ID] = machine
	}
	machines := make([]Machine, 0, len(byID))
	for _, machine := range byID {
		machines = append(machines, machine)
	}
	slices.SortFunc(machines, func(a, b Machine) int { return strings.Compare(a.ID, b.ID) })
	return machines, nil
}

func (service RepositoryEventService) Poll(ctx context.Context, machine Machine, cursor string, limit int, wait time.Duration) (repositoryevent.PollResponse, error) {
	if service.Store == nil || !machine.valid() || !hasScope(machine.Scopes, ScopeEventsPoll) {
		return repositoryevent.PollResponse{}, ErrUnauthorized
	}
	if repositoryevent.ValidateCursor(cursor, false) != nil || limit < 1 || limit > repositoryevent.MaxLimit || wait < 0 || wait > repositoryevent.MaxWaitSeconds*time.Second {
		return repositoryevent.PollResponse{}, errors.New("invalid repository event poll")
	}
	interval := service.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	sleep := service.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	now := time.Now
	if service.Now != nil {
		now = service.Now
	}
	deadline := now().Add(wait)
	for {
		response, err := service.Store.Poll(ctx, machine, cursor, limit)
		if err != nil {
			return repositoryevent.PollResponse{}, err
		}
		if response.Validate(cursor) != nil {
			return repositoryevent.PollResponse{}, errors.New("invalid repository event store response")
		}
		if len(response.Events) > 0 || wait == 0 {
			return response, nil
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return response, nil
		}
		delay := interval
		if delay > remaining {
			delay = remaining
		}
		if err := sleep(ctx, delay); err != nil {
			return repositoryevent.PollResponse{}, err
		}
	}
}

func (service RepositoryEventService) Acknowledge(ctx context.Context, machine Machine, request repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
	if service.Store == nil || !machine.valid() || !hasScope(machine.Scopes, ScopeEventsAck) {
		return repositoryevent.AckResponse{}, ErrUnauthorized
	}
	if request.Validate() != nil {
		return repositoryevent.AckResponse{}, repositoryevent.ErrInvalidCursor
	}
	response, err := service.Store.Acknowledge(ctx, machine, request)
	if err != nil {
		return repositoryevent.AckResponse{}, err
	}
	if response.Version != repositoryevent.ContractVersion || response.Cursor != request.Cursor {
		return repositoryevent.AckResponse{}, errors.New("invalid repository event acknowledgement")
	}
	return response, nil
}

// translateWebhook maps a delivery onto the repository event it implies. The
// third return value is the plain-words reason the delivery was ignored, set
// only when the second is false: an ignored delivery is still narrated, and
// the operator needs to know why this push produced nothing.
func translateWebhook(delivery WebhookDelivery) (routedRepositoryEvent, bool, string, error) {
	if !validWebhookDeliveryID(delivery.ID) {
		return routedRepositoryEvent{}, false, "", errors.New("invalid webhook delivery ID")
	}
	switch delivery.Event {
	case "push":
		var payload struct {
			Ref, After   string
			Installation struct {
				ID int64 `json:"id"`
			} `json:"installation"`
			Repository struct {
				ID            int64  `json:"id"`
				FullName      string `json:"full_name"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repository"`
			HeadCommit *struct {
				Timestamp time.Time `json:"timestamp"`
			} `json:"head_commit"`
		}
		if err := decodeWebhook(delivery.Payload, &payload); err != nil {
			return routedRepositoryEvent{}, false, "", err
		}
		if payload.Repository.ID <= 0 || payload.Installation.ID <= 0 {
			return routedRepositoryEvent{}, false, "", errors.New("webhook repository ID is invalid")
		}
		if payload.Ref != "refs/heads/"+payload.Repository.DefaultBranch {
			return routedRepositoryEvent{}, false, "not on default branch", nil
		}
		var occurredAt *time.Time
		if payload.HeadCommit != nil && !payload.HeadCommit.Timestamp.IsZero() {
			at := payload.HeadCommit.Timestamp.UTC()
			occurredAt = &at
		}
		event := repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: delivery.ID + ":default", Repository: canonicalRepository(payload.Repository.FullName), Ref: payload.Ref, Reason: repositoryevent.ReasonDefaultBranchUpdated, TargetSHA: payload.After, OccurredAt: occurredAt}
		if err := event.Validate(); err != nil {
			return routedRepositoryEvent{}, false, "", err
		}
		return routedRepositoryEvent{event: event, installationID: payload.Installation.ID, repositoryID: payload.Repository.ID}, true, "", nil
	case "repository":
		var payload struct {
			Action       string `json:"action"`
			Installation struct {
				ID int64 `json:"id"`
			} `json:"installation"`
			Repository struct {
				ID            int64  `json:"id"`
				FullName      string `json:"full_name"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repository"`
			Changes struct {
				Repository struct {
					Name struct {
						From string `json:"from"`
					} `json:"name"`
				} `json:"repository"`
				Owner struct {
					From struct {
						User *struct {
							Login string `json:"login"`
						} `json:"user"`
					} `json:"from"`
				} `json:"owner"`
			} `json:"changes"`
		}
		if err := decodeWebhook(delivery.Payload, &payload); err != nil {
			return routedRepositoryEvent{}, false, "", err
		}
		if payload.Action != "renamed" && payload.Action != "transferred" {
			return routedRepositoryEvent{}, false, "not a rename or transfer", nil
		}
		if payload.Repository.ID <= 0 || payload.Installation.ID <= 0 {
			return routedRepositoryEvent{}, false, "", errors.New("webhook repository ID is invalid")
		}
		current := canonicalRepository(payload.Repository.FullName)
		owner, currentName, found := strings.Cut(strings.TrimPrefix(current, "github.com/"), "/")
		if !found {
			return routedRepositoryEvent{}, false, "", errors.New("invalid renamed repository")
		}
		previousOwner := owner
		previousName := payload.Changes.Repository.Name.From
		if payload.Action == "transferred" {
			if payload.Changes.Owner.From.User == nil {
				return routedRepositoryEvent{}, false, "", errors.New("transferred repository has no previous owner")
			}
			previousOwner = payload.Changes.Owner.From.User.Login
			previousName = currentName
		}
		event := repositoryevent.Event{
			Version:            repositoryevent.ContractVersion,
			ID:                 delivery.ID + ":renamed",
			Repository:         current,
			PreviousRepository: "github.com/" + previousOwner + "/" + previousName,
			Ref:                "refs/heads/" + payload.Repository.DefaultBranch,
			Reason:             repositoryevent.ReasonRepositoryRenamed,
		}
		if err := event.Validate(); err != nil {
			return routedRepositoryEvent{}, false, "", err
		}
		return routedRepositoryEvent{event: event, installationID: payload.Installation.ID, repositoryID: payload.Repository.ID}, true, "", nil
	default:
		return routedRepositoryEvent{}, false, "unsupported event", nil
	}
}

func translateInstallationLifecycle(delivery WebhookDelivery) (bool, InstallationLifecycleEvent, error) {
	if delivery.Event != "installation" && delivery.Event != "installation_repositories" && delivery.Event != "membership" && delivery.Event != "organization" && delivery.Event != "github_app_authorization" && delivery.Event != "member" {
		return false, InstallationLifecycleEvent{}, nil
	}
	if !validWebhookDeliveryID(delivery.ID) {
		return true, InstallationLifecycleEvent{}, errors.New("invalid webhook delivery ID")
	}
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		RepositoriesRemoved []struct {
			ID int64 `json:"id"`
		} `json:"repositories_removed"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		Member struct {
			ID int64 `json:"id"`
		} `json:"member"`
		Membership struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		} `json:"membership"`
		Sender struct {
			ID int64 `json:"id"`
		} `json:"sender"`
	}
	if err := decodeWebhook(delivery.Payload, &payload); err != nil {
		return true, InstallationLifecycleEvent{}, err
	}
	event := InstallationLifecycleEvent{DeliveryID: delivery.ID, InstallationID: payload.Installation.ID}
	switch {
	case delivery.Event == "installation" && payload.Action == "suspend":
		event.Action = InstallationSuspended
	case delivery.Event == "installation" && payload.Action == "deleted":
		event.Action = InstallationRevoked
	case delivery.Event == "installation_repositories" && payload.Action == "removed":
		event.Action = InstallationRepositoriesRemoved
		event.RepositoryIDs = make([]int64, 0, len(payload.RepositoriesRemoved))
		for _, repository := range payload.RepositoriesRemoved {
			event.RepositoryIDs = append(event.RepositoryIDs, repository.ID)
		}
		slices.Sort(event.RepositoryIDs)
		event.RepositoryIDs = slices.Compact(event.RepositoryIDs)
	case delivery.Event == "membership" && payload.Action == "removed":
		event.Action = InstallationUserAccessRemoved
		event.GitHubUserID = payload.Member.ID
	case delivery.Event == "organization" && payload.Action == "member_removed":
		event.Action = InstallationUserAccessRemoved
		event.GitHubUserID = payload.Membership.User.ID
	case delivery.Event == "github_app_authorization" && payload.Action == "revoked":
		event.Action = GitHubUserAuthorizationRevoked
		event.GitHubUserID = payload.Sender.ID
	case delivery.Event == "member" && payload.Action == "removed":
		event.Action = RepositoryUserAccessRemoved
		event.RepositoryID = payload.Repository.ID
		event.GitHubUserID = payload.Member.ID
	default:
		return false, InstallationLifecycleEvent{}, nil
	}
	installationRequired := event.Action != GitHubUserAuthorizationRevoked
	if (installationRequired && event.InstallationID <= 0) || (event.Action == InstallationRepositoriesRemoved && (len(event.RepositoryIDs) == 0 || event.RepositoryIDs[0] <= 0)) || ((event.Action == InstallationUserAccessRemoved || event.Action == GitHubUserAuthorizationRevoked || event.Action == RepositoryUserAccessRemoved) && event.GitHubUserID <= 0) || (event.Action == RepositoryUserAccessRemoved && event.RepositoryID <= 0) {
		return true, InstallationLifecycleEvent{}, errors.New("invalid installation lifecycle event")
	}
	return true, event, nil
}

func validWebhookDeliveryID(deliveryID string) bool {
	return deliveryID != "" && len(deliveryID) <= repositoryevent.MaxEventIDBytes-32 && !strings.ContainsAny(deliveryID, " \t\r\n/")
}

func canonicalRepository(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "github.com/") {
		return value
	}
	return "github.com/" + value
}

func decodeWebhook(payload []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode webhook: %w", err)
	}
	return nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
