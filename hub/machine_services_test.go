package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

type credentialMemory struct {
	binding MachineCredentialBinding
	digest  MachineTokenDigest
	err     error
}

func (store *credentialMemory) RotateMachineCredential(_ context.Context, binding MachineCredentialBinding, digest MachineTokenDigest) (MachineCredentialBinding, error) {
	store.binding = binding
	store.digest = digest
	if store.err != nil {
		return MachineCredentialBinding{}, store.err
	}
	binding.MachineID = "machine-id"
	return binding, nil
}

func (store *credentialMemory) ResolveMachineCredential(_ context.Context, digest MachineTokenDigest) (MachineCredentialBinding, error) {
	if store.err != nil || digest != store.digest {
		return MachineCredentialBinding{}, errors.New("credential not found")
	}
	return store.binding, nil
}

type snapshotStoreMemory struct {
	records []StoredMachineSnapshot
	result  MachineSnapshotStoreResult
	err     error
}

func (store *snapshotStoreMemory) StoreLatest(_ context.Context, candidate StoredMachineSnapshot) (MachineSnapshotStoreResult, error) {
	if store.err != nil {
		return MachineSnapshotStoreResult{}, store.err
	}
	store.records = append(store.records, candidate)
	if store.result.Current.MachineID != "" {
		return store.result, nil
	}
	return MachineSnapshotStoreResult{Current: candidate, Updated: true}, nil
}

func (store *snapshotStoreMemory) ListLatest(context.Context) ([]StoredMachineSnapshot, error) {
	if store.err != nil {
		return nil, store.err
	}
	return append([]StoredMachineSnapshot(nil), store.records...), nil
}

func validMachine() Machine {
	return Machine{ID: "machine-id", Name: "laptop", IdentityID: "firebase-uid", Scopes: cloneEnrollmentScopes()}
}

func validSnapshot(at time.Time) machinesnapshot.Snapshot {
	return machinesnapshot.Snapshot{
		SchemaVersion: machinesnapshot.SchemaVersion,
		Login:         "alex",
		Machine:       "laptop",
		PublishedAt:   at,
		LastSeenAt:    at,
		Repositories:  []string{"github.com/acme/app"},
		Worktrees:     []machinesnapshot.Worktree{},
	}
}

func TestMachineEnrollmentAndBearerResolution(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := &credentialMemory{}
	service := MachineEnrollmentService{
		Store:  store,
		Pepper: bytes.Repeat([]byte("p"), minimumPepperBytes),
		Random: bytes.NewReader(bytes.Repeat([]byte("r"), machineTokenBytes)),
		Now:    func() time.Time { return now },
	}
	response, err := service.Enroll(context.Background(), Viewer{Authenticated: true, IdentityID: "firebase-uid", DisplayName: "Alex"}, MachineEnrollmentRequest{Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Token == "" || response.Machine.ID != "machine-id" || response.Identity.ID != "firebase-uid" || response.EnrolledAt != now {
		t.Fatalf("unexpected enrollment response: %+v", response)
	}
	if string(store.digest[:]) == response.Token {
		t.Fatal("store received plaintext token")
	}
	store.binding.MachineID = "machine-id"
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer "+response.Token)
	machine, err := NewMachineBearerResolver(store, service.Pepper).ResolveMachineBearer(request)
	if err != nil || machine.ID != "machine-id" || machine.IdentityID != "firebase-uid" {
		t.Fatalf("machine=%+v err=%v", machine, err)
	}
}

func TestMachineEnrollmentRejectsInvalidInputsAndStoreResults(t *testing.T) {
	service := MachineEnrollmentService{}
	if _, err := service.Enroll(context.Background(), Viewer{}, MachineEnrollmentRequest{Name: "laptop"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
	if _, err := service.Enroll(context.Background(), Viewer{Authenticated: true, IdentityID: "uid"}, MachineEnrollmentRequest{Name: "bad name"}); err == nil {
		t.Fatal("invalid machine name accepted")
	}
	service.Pepper = bytes.Repeat([]byte("p"), minimumPepperBytes)
	service.Store = &credentialMemory{}
	service.Random = bytes.NewReader(nil)
	if _, err := service.Enroll(context.Background(), Viewer{Authenticated: true, IdentityID: "uid"}, MachineEnrollmentRequest{Name: "laptop"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable random source, got %v", err)
	}
	service.Random = bytes.NewReader(bytes.Repeat([]byte("r"), machineTokenBytes))
	service.Store = &credentialMemory{err: errors.New("store")}
	if _, err := service.Enroll(context.Background(), Viewer{Authenticated: true, IdentityID: "uid"}, MachineEnrollmentRequest{Name: "laptop"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable store, got %v", err)
	}
	for _, test := range []struct {
		token  string
		pepper []byte
	}{
		{"", bytes.Repeat([]byte("p"), minimumPepperBytes)},
		{"token with space", bytes.Repeat([]byte("p"), minimumPepperBytes)},
		{"token", []byte("short")},
		{string(bytes.Repeat([]byte("x"), 1025)), bytes.Repeat([]byte("p"), minimumPepperBytes)},
	} {
		if _, err := DigestMachineToken(test.token, test.pepper); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("token %q unexpectedly accepted", test.token)
		}
	}
}

func TestMachineBearerRejectsMalformedCredentials(t *testing.T) {
	pepper := bytes.Repeat([]byte("p"), minimumPepperBytes)
	resolver := NewMachineBearerResolver(&credentialMemory{}, pepper)
	if _, err := resolver.ResolveMachineBearer(nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("nil request accepted")
	}
	for _, authorization := range []string{"", "Basic token", "Bearer", "Bearer bad token"} {
		request := httptest.NewRequest("GET", "/", nil)
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		if _, err := resolver.ResolveMachineBearer(request); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("authorization %q accepted", authorization)
		}
	}
}

func TestMachineSnapshotPublishListAndResolve(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := &snapshotStoreMemory{}
	service := MachineSnapshotService{Store: store, Now: func() time.Time { return now.Add(time.Minute) }}
	snapshot := validSnapshot(now)
	snapshot.Repositories = []string{"github.com/Acme/App", "github.com/acme/app"}
	receipt, err := service.Publish(context.Background(), validMachine(), snapshot)
	if err != nil || !receipt.Updated || receipt.MachineID != "machine-id" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if got := store.records[0].Snapshot.Repositories; len(got) != 1 || got[0] != "github.com/acme/app" {
		t.Fatalf("repositories were not canonicalized: %v", got)
	}
	store.records = append(store.records, StoredMachineSnapshot{IdentityID: "other", MachineID: "hidden", Snapshot: validSnapshot(now), ReceivedAt: now, Digest: "hidden"})
	listed, err := service.List(context.Background(), validMachine())
	if err != nil || len(listed.Snapshots) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	current := store.records[0]
	if result, err := ResolveLatestMachineSnapshot(nil, current); err != nil || !result.Updated {
		t.Fatalf("initial resolve=%+v err=%v", result, err)
	}
	if result, err := ResolveLatestMachineSnapshot(&current, current); err != nil || result.Updated {
		t.Fatalf("duplicate resolve=%+v err=%v", result, err)
	}
	newer := current
	newer.Digest = "new"
	newer.Snapshot.PublishedAt = newer.Snapshot.PublishedAt.Add(time.Minute)
	if result, err := ResolveLatestMachineSnapshot(&current, newer); err != nil || !result.Updated {
		t.Fatalf("newer resolve=%+v err=%v", result, err)
	}
	older := newer
	older.Snapshot.PublishedAt = current.Snapshot.PublishedAt.Add(-time.Minute)
	if _, err := ResolveLatestMachineSnapshot(&current, older); !errors.Is(err, machinesnapshot.ErrStaleSnapshot) {
		t.Fatalf("expected stale, got %v", err)
	}
	conflict := newer
	conflict.Snapshot.PublishedAt = current.Snapshot.PublishedAt
	if _, err := ResolveLatestMachineSnapshot(&current, conflict); !errors.Is(err, machinesnapshot.ErrSnapshotConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	wrong := current
	wrong.MachineID = "other"
	if _, err := ResolveLatestMachineSnapshot(&current, wrong); err == nil {
		t.Fatal("different machine compared")
	}
	if _, err := ResolveLatestMachineSnapshot(nil, StoredMachineSnapshot{}); err == nil {
		t.Fatal("invalid candidate accepted")
	}
}

func TestStatusServiceStableAndScoped(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	bindings := &memoryBindings{stored: []IdentityInstallationBinding{
		{IdentityID: "uid", Installation: VerifiedInstallation{ID: 20, Account: "Beta", AccountType: "Organization", RepositorySelection: "all", Repositories: []VerifiedRepository{}, State: "installed"}},
		{IdentityID: "uid", Installation: VerifiedInstallation{ID: 10, Account: "alpha", AccountType: "User", RepositorySelection: "selected", Repositories: []VerifiedRepository{{ID: 1, Repository: "github.com/alpha/app"}}, State: "installed"}},
	}}
	snapshots := &snapshotStoreMemory{records: []StoredMachineSnapshot{
		{IdentityID: "other", MachineID: "hidden", Snapshot: validSnapshot(now), ReceivedAt: now, Digest: "h"},
		{IdentityID: "uid", MachineID: "z", Snapshot: validSnapshot(now.Add(-time.Hour)), ReceivedAt: now, Digest: "z"},
		{IdentityID: "uid", MachineID: "a", Snapshot: func() machinesnapshot.Snapshot { value := validSnapshot(now); value.Machine = "alpha"; return value }(), ReceivedAt: now, Digest: "a"},
	}}
	events := statusMemory{
		pending: []PendingRefresh{{ID: "b", Repository: "github.com/acme/b", QueuedAt: now}, {ID: "a", Repository: "github.com/acme/a", QueuedAt: now.Add(-time.Minute)}},
		errors:  []StatusError{{Code: "z", Message: "late"}, {Code: "a", Message: "first"}},
	}
	service := StatusService{Bindings: bindings, Snapshots: snapshots, Events: events, AppName: "Workbench", Now: func() time.Time { return now }}
	response, err := service.Read(context.Background(), Viewer{Authenticated: true, IdentityID: "uid"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Connection.State != "attention" || response.Connection.Account != "" || response.Installations[0].Account != "alpha" || response.Machines[0].Name != "alpha" || response.PendingRefreshes[0].ID != "a" || response.Errors[0].Code != "a" {
		encoded, _ := json.Marshal(response)
		t.Fatalf("unstable or unscoped status: %s", encoded)
	}
	if response.Machines[0].LastSeenAt == nil || response.Machines[1].State != "offline" {
		t.Fatalf("machine states=%+v", response.Machines)
	}
}

type statusMemory struct {
	delivery *StatusDelivery
	pending  []PendingRefresh
	errors   []StatusError
	err      error
}

func (store statusMemory) IdentityRepositoryEventStatus(context.Context, string) (*StatusDelivery, []PendingRefresh, []StatusError, error) {
	return store.delivery, store.pending, store.errors, store.err
}
