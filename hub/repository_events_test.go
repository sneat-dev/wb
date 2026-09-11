package hub

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

type snapshotMemory []StoredMachineSnapshot

func (snapshotMemory) StoreLatest(context.Context, StoredMachineSnapshot) (MachineSnapshotStoreResult, error) {
	panic("not used")
}
func (s snapshotMemory) ListLatest(context.Context) ([]StoredMachineSnapshot, error) { return s, nil }

type entitlementMemory map[string]map[int64]bool

func (e entitlementMemory) IdentityHasRepositoryEntitlement(_ context.Context, identity string, _ int64, repositoryID int64) (bool, error) {
	return e[identity][repositoryID], nil
}

type entitlementRecorder struct {
	identityID                   string
	installationID, repositoryID int64
	allowed                      bool
}

func (e *entitlementRecorder) IdentityHasRepositoryEntitlement(_ context.Context, identityID string, installationID, repositoryID int64) (bool, error) {
	e.identityID = identityID
	e.installationID = installationID
	e.repositoryID = repositoryID
	return e.allowed, nil
}

type lifecycleMemory struct {
	events []InstallationLifecycleEvent
	err    error
}

func (s *lifecycleMemory) ApplyInstallationLifecycle(_ context.Context, event InstallationLifecycleEvent) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	return nil
}

type eventStoreMemory struct {
	event    repositoryevent.Event
	machines []Machine
}

func (s *eventStoreMemory) EnqueueForMachines(_ context.Context, event repositoryevent.Event, machines []Machine) (EnqueueResult, error) {
	s.event = event
	s.machines = append([]Machine(nil), machines...)
	return EnqueueResult{Enqueued: len(machines)}, nil
}
func (*eventStoreMemory) Poll(context.Context, Machine, string, int) (repositoryevent.PollResponse, error) {
	panic("not used")
}
func (*eventStoreMemory) Acknowledge(context.Context, Machine, repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
	panic("not used")
}

func TestRepresentativeGitHubPushIgnoresUnknownFieldsAndRoutesByStableID(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	snapshots := snapshotMemory{
		{IdentityID: "uid-a", MachineID: "machine-a", Snapshot: machinesnapshot.Snapshot{SchemaVersion: machinesnapshot.SchemaVersion, Login: "alex", Machine: "laptop", PublishedAt: at, Repositories: []string{"github.com/acme/app"}, Worktrees: []machinesnapshot.Worktree{}}, ReceivedAt: at, Digest: "a"},
		{IdentityID: "uid-b", MachineID: "machine-b", Snapshot: machinesnapshot.Snapshot{SchemaVersion: machinesnapshot.SchemaVersion, Login: "bob", Machine: "desktop", PublishedAt: at, Repositories: []string{"github.com/acme/app"}, Worktrees: []machinesnapshot.Worktree{}}, ReceivedAt: at, Digest: "b"},
	}
	store := &eventStoreMemory{}
	entitlements := &entitlementRecorder{allowed: true}
	service := RepositoryEventService{Snapshots: snapshots[:1], Entitlements: entitlements, Store: store}
	payload := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","before":"ffffffffffffffffffffffffffffffffffffffff","repository":{"id":987,"full_name":"Acme/App","default_branch":"main","private":true,"owner":{"login":"Acme"}},"sender":{"login":"octocat"},"installation":{"id":123},"head_commit":{"id":"0123","timestamp":"2026-09-06T12:00:00Z","message":"private commit message"}}`)
	result, err := service.EnqueueWebhook(context.Background(), WebhookDelivery{ID: "delivery-1", Event: "push", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if result.Enqueued != 1 || len(store.machines) != 1 || store.machines[0].ID != "machine-a" {
		t.Fatalf("result=%+v machines=%+v", result, store.machines)
	}
	if entitlements.identityID != "uid-a" || entitlements.installationID != 123 || entitlements.repositoryID != 987 {
		t.Fatalf("entitlement lookup=%+v", entitlements)
	}
	if store.event.Repository != "github.com/acme/app" || store.event.TargetSHA == "" || store.event.OccurredAt == nil {
		t.Fatalf("event=%+v", store.event)
	}
}

func TestInstallationLifecycleWebhooksRevokeEntitlementsBeforeReturning(t *testing.T) {
	tests := []struct {
		event, payload string
		want           InstallationLifecycleEvent
	}{
		{"installation", `{"action":"suspend","installation":{"id":7}}`, InstallationLifecycleEvent{Action: InstallationSuspended, InstallationID: 7}},
		{"installation", `{"action":"deleted","installation":{"id":7}}`, InstallationLifecycleEvent{Action: InstallationRevoked, InstallationID: 7}},
		{"installation_repositories", `{"action":"removed","installation":{"id":7},"repositories_removed":[{"id":99},{"id":98},{"id":99}]}`, InstallationLifecycleEvent{Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{98, 99}}},
		{"membership", `{"action":"removed","installation":{"id":7},"member":{"id":42}}`, InstallationLifecycleEvent{Action: InstallationUserAccessRemoved, InstallationID: 7, GitHubUserID: 42}},
		{"organization", `{"action":"member_removed","installation":{"id":7},"membership":{"user":{"id":42}}}`, InstallationLifecycleEvent{Action: InstallationUserAccessRemoved, InstallationID: 7, GitHubUserID: 42}},
		{"github_app_authorization", `{"action":"revoked","sender":{"id":42}}`, InstallationLifecycleEvent{Action: GitHubUserAuthorizationRevoked, GitHubUserID: 42}},
		{"member", `{"action":"removed","installation":{"id":7},"repository":{"id":99},"member":{"id":42}}`, InstallationLifecycleEvent{Action: RepositoryUserAccessRemoved, InstallationID: 7, RepositoryID: 99, GitHubUserID: 42}},
	}
	for index, test := range tests {
		store := &lifecycleMemory{}
		service := RepositoryEventService{Lifecycle: store}
		deliveryID := "lifecycle-" + string(rune('a'+index))
		if result, err := service.EnqueueWebhook(context.Background(), WebhookDelivery{ID: deliveryID, Event: test.event, Payload: []byte(test.payload)}); err != nil || result != (EnqueueResult{}) {
			t.Fatalf("%s result=%+v err=%v", test.event, result, err)
		}
		if len(store.events) != 1 {
			t.Fatalf("%s events=%+v", test.event, store.events)
		}
		want := test.want
		want.DeliveryID = deliveryID
		if !reflect.DeepEqual(store.events[0], want) {
			t.Fatalf("%s event=%+v want=%+v", test.event, store.events[0], want)
		}
	}
}

func TestMachinesForRepositoryMatchesGitHubIdentityCaseInsensitively(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	service := RepositoryEventService{Snapshots: snapshotMemory{{
		IdentityID: "uid",
		MachineID:  "machine",
		Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion,
			Login:         "alex",
			Machine:       "laptop",
			PublishedAt:   at,
			Repositories:  []string{"github.com/octocoders/hello-world"},
			Worktrees:     []machinesnapshot.Worktree{},
		},
		ReceivedAt: at,
		Digest:     "digest",
	}, {
		IdentityID: "uid-2",
		MachineID:  "machine-2",
		Snapshot: machinesnapshot.Snapshot{SchemaVersion: machinesnapshot.SchemaVersion, Login: "casey", Machine: "desktop", PublishedAt: at,
			Repositories: []string{"github.com/octocoders/hello-world"}, Worktrees: []machinesnapshot.Worktree{}},
		ReceivedAt: at,
		Digest:     "digest-2",
	}}}

	machines, err := service.machinesForRepository(context.Background(), "github.com/Octocoders/Hello-World")
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 2 || machines[0].ID != "machine" || machines[1].ID != "machine-2" {
		t.Fatalf("machines=%+v", machines)
	}
}

func TestRenameUsesOldSnapshotNameAndStableRepositoryEntitlement(t *testing.T) {
	at := time.Now().UTC()
	snapshots := snapshotMemory{{IdentityID: "uid", MachineID: "machine", Snapshot: machinesnapshot.Snapshot{SchemaVersion: machinesnapshot.SchemaVersion, Login: "alex", Machine: "laptop", PublishedAt: at, Repositories: []string{"github.com/acme/old"}, Worktrees: []machinesnapshot.Worktree{}}, ReceivedAt: at, Digest: "digest"}}
	store := &eventStoreMemory{}
	service := RepositoryEventService{Snapshots: snapshots, Entitlements: entitlementMemory{"uid": {987: true}}, Store: store}
	payload := []byte(`{"action":"renamed","repository":{"id":987,"full_name":"acme/new","default_branch":"main","owner":{"login":"acme"}},"changes":{"repository":{"name":{"from":"old"}}},"installation":{"id":123}}`)
	if _, err := service.EnqueueWebhook(context.Background(), WebhookDelivery{ID: "delivery-rename", Event: "repository", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(store.machines) != 1 || store.event.PreviousRepository != "github.com/acme/old" || store.event.Repository != "github.com/acme/new" {
		t.Fatalf("event=%+v machines=%+v", store.event, store.machines)
	}
}

func TestTransferUsesPreviousOwnerAndCurrentRepositoryName(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	snapshots := snapshotMemory{{
		IdentityID: "uid",
		MachineID:  "machine",
		Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion,
			Login:         "alex",
			Machine:       "laptop",
			PublishedAt:   at,
			Repositories:  []string{"github.com/sneat-co/wb"},
			Worktrees:     []machinesnapshot.Worktree{},
		},
		ReceivedAt: at,
		Digest:     "digest",
	}}
	store := &eventStoreMemory{}
	service := RepositoryEventService{
		Snapshots:    snapshots,
		Entitlements: entitlementMemory{"uid": {987: true}},
		Store:        store,
	}
	payload := []byte(`{"action":"transferred","repository":{"id":987,"full_name":"sneat-dev/wb","default_branch":"main","private":true,"owner":{"login":"sneat-dev"}},"changes":{"owner":{"from":{"user":{"login":"sneat-co"}}}},"sender":{"login":"octocat"},"installation":{"id":123}}`)
	if _, err := service.EnqueueWebhook(context.Background(), WebhookDelivery{ID: "delivery-transfer", Event: "repository", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(store.machines) != 1 || store.event.PreviousRepository != "github.com/sneat-co/wb" || store.event.Repository != "github.com/sneat-dev/wb" {
		t.Fatalf("event=%+v machines=%+v", store.event, store.machines)
	}

	missingOwner := []byte(`{"action":"transferred","repository":{"id":987,"full_name":"sneat-dev/wb","default_branch":"main"},"changes":{},"installation":{"id":123}}`)
	if _, err := service.EnqueueWebhook(context.Background(), WebhookDelivery{ID: "delivery-transfer-invalid", Event: "repository", Payload: missingOwner}); err == nil {
		t.Fatal("transfer without previous owner accepted")
	}
}
