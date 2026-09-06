package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

type memoryMachineSnapshotStore struct {
	mu      sync.Mutex
	records map[string]machinesnapshot.StoredSnapshot
	writes  int
}

func (store *memoryMachineSnapshotStore) StoreLatest(_ context.Context, candidate machinesnapshot.StoredSnapshot) (machinesnapshot.StoreResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.records == nil {
		store.records = make(map[string]machinesnapshot.StoredSnapshot)
	}
	current, exists := store.records[candidate.Snapshot.Key()]
	var currentPointer *machinesnapshot.StoredSnapshot
	if exists {
		currentPointer = &current
	}
	result, err := machinesnapshot.ResolveLatest(currentPointer, candidate)
	if err != nil {
		return result, err
	}
	if result.Updated {
		store.records[candidate.Snapshot.Key()] = result.Current
		store.writes++
	}
	return result, nil
}

func (store *memoryMachineSnapshotStore) ListLatest(context.Context) ([]machinesnapshot.StoredSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]machinesnapshot.StoredSnapshot, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result, nil
}

type publisherResolverFunc func(*http.Request) (MachinePublisher, error)

func (fn publisherResolverFunc) Publisher(request *http.Request) (MachinePublisher, error) {
	return fn(request)
}

func TestMachineSnapshotHTTPBindsIdentityAndStoresIdempotently(t *testing.T) {
	receivedAt := time.Date(2026, 9, 6, 14, 0, 1, 0, time.UTC)
	store := &memoryMachineSnapshotStore{}
	service := &MachineSnapshotService{Store: store, Now: func() time.Time { return receivedAt }}
	handler := NewHandler(HandlerOptions{
		MachineSnapshots: service,
		PublisherResolver: publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
			return MachinePublisher{Login: "alice", Machine: "laptop"}, nil
		}),
	})
	snapshot := validHostedSnapshot()
	body, _ := json.Marshal(snapshot)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, bytes.NewReader(body)))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}
	var firstReceipt machinesnapshot.Receipt
	if err := json.Unmarshal(first.Body.Bytes(), &firstReceipt); err != nil {
		t.Fatal(err)
	}
	if !firstReceipt.Updated || !firstReceipt.ReceivedAt.Equal(receivedAt) || store.writes != 1 {
		t.Fatalf("first receipt/writes = %+v / %d", firstReceipt, store.writes)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, bytes.NewReader(body)))
	var secondReceipt machinesnapshot.Receipt
	if err := json.Unmarshal(second.Body.Bytes(), &secondReceipt); err != nil {
		t.Fatal(err)
	}
	if second.Code != http.StatusOK || secondReceipt.Updated || store.writes != 1 || !secondReceipt.ReceivedAt.Equal(firstReceipt.ReceivedAt) {
		t.Fatalf("duplicate status/receipt/writes = %d / %+v / %d", second.Code, secondReceipt, store.writes)
	}
}

func TestMachineSnapshotHTTPRejectsIdentityMismatchAndUnknownLocalFields(t *testing.T) {
	store := &memoryMachineSnapshotStore{}
	handler := NewHandler(HandlerOptions{
		MachineSnapshots: &MachineSnapshotService{Store: store},
		PublisherResolver: publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
			return MachinePublisher{Login: "alice", Machine: "laptop"}, nil
		}),
	})
	mismatch := validHostedSnapshot()
	mismatch.Machine = "vm"
	body, _ := json.Marshal(mismatch)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, bytes.NewReader(body)))
	if response.Code != http.StatusForbidden || store.writes != 0 {
		t.Fatalf("mismatch status/writes = %d/%d: %s", response.Code, store.writes, response.Body.String())
	}

	unsafeBody := strings.TrimSuffix(string(marshalHostedJSON(t, validHostedSnapshot())), "}") + `,"projects_root":"/Users/alice/private"}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, strings.NewReader(unsafeBody)))
	if response.Code != http.StatusBadRequest || store.writes != 0 {
		t.Fatalf("unsafe status/writes = %d/%d: %s", response.Code, store.writes, response.Body.String())
	}
}

func TestMachineSnapshotHTTPFailsClosedAndBoundsPayload(t *testing.T) {
	service := &MachineSnapshotService{Store: &memoryMachineSnapshotStore{}}
	withoutAuth := NewHandler(HandlerOptions{MachineSnapshots: service})
	response := httptest.NewRecorder()
	withoutAuth.ServeHTTP(response, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, strings.NewReader(`{}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing resolver status = %d", response.Code)
	}

	withAuth := NewHandler(HandlerOptions{
		MachineSnapshots: service,
		PublisherResolver: publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
			return MachinePublisher{Login: "alice", Machine: "laptop"}, nil
		}),
	})
	response = httptest.NewRecorder()
	oversized := validHostedSnapshot()
	oversized.Worktrees[0].AttentionReason = strings.Repeat("x", maxMachineSnapshotBodyBytes+1)
	withAuth.ServeHTTP(response, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, bytes.NewReader(marshalHostedJSON(t, oversized))))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d: %s", response.Code, response.Body.String())
	}
}

func TestMachineSnapshotListIsLoginScopedAndFeedsWorktreeReadModel(t *testing.T) {
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	store := &memoryMachineSnapshotStore{records: map[string]machinesnapshot.StoredSnapshot{}}
	alice := validHostedSnapshot()
	bob := validHostedSnapshot()
	bob.Login, bob.Machine = "bob", "vm"
	store.records[alice.Key()] = machinesnapshot.StoredSnapshot{Snapshot: alice, ReceivedAt: at, Digest: "a"}
	store.records[bob.Key()] = machinesnapshot.StoredSnapshot{Snapshot: bob, ReceivedAt: at, Digest: "b"}
	service := MachineSnapshotService{Store: store}
	listed, err := service.List(context.Background(), MachinePublisher{Login: "alice", Machine: "laptop"})
	if err != nil || len(listed.Snapshots) != 1 || listed.Snapshots[0].Snapshot.Login != "alice" {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
	entries, err := (StoredSnapshotSource{Store: store}).List(context.Background())
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Snapshot.ProjectsRoot != "" || entry.Snapshot.Worktrees[0].Dir != "" || entry.Snapshot.Worktrees[0].HeadSHA != "" {
			t.Fatalf("source exposed local state: %+v", entry)
		}
	}
}

func validHostedSnapshot() machinesnapshot.Snapshot {
	return machinesnapshot.Snapshot{
		SchemaVersion: machinesnapshot.SchemaVersion, Login: "alice", Machine: "laptop",
		PublishedAt: time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC),
		Worktrees: []machinesnapshot.Worktree{{
			Task: "dashboard", Stream: "fleet", Repository: "sneat-dev/wb", Branch: "feature/dashboard",
			Lifecycle: "review", OwnerState: "active", Owner: "worker-1",
			PullRequest: &machinesnapshot.PullRequest{Number: 437, URL: "https://github.com/sneat-dev/wb/pull/437", State: "open"},
		}},
	}
}

func marshalHostedJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
