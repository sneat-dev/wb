package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

// dqCovSnapshotStore is a SnapshotStore with scripted read/write outcomes.
type dqCovSnapshotStore struct {
	list     []machinesnapshot.StoredSnapshot
	listErr  error
	storeErr error
	result   machinesnapshot.StoreResult
	calls    int
}

func (store *dqCovSnapshotStore) StoreLatest(context.Context, machinesnapshot.StoredSnapshot) (machinesnapshot.StoreResult, error) {
	store.calls++
	return store.result, store.storeErr
}

func (store *dqCovSnapshotStore) ListLatest(context.Context) ([]machinesnapshot.StoredSnapshot, error) {
	return store.list, store.listErr
}

func TestDQCovMachineSnapshotPublishValidatesIdentityAndStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	snapshot := validHostedSnapshot()
	receivedAt := time.Date(2026, 9, 6, 14, 0, 1, 0, time.UTC)
	validResult := machinesnapshot.StoreResult{
		Updated: true,
		Current: machinesnapshot.StoredSnapshot{Snapshot: snapshot, ReceivedAt: receivedAt, Digest: "digest"},
	}

	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{result: validResult}}).
		Publish(ctx, MachinePublisher{Login: " ", Machine: "laptop"}, snapshot); !errors.Is(err, ErrPublisherIdentity) {
		t.Errorf("blank login err = %v", err)
	}
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{result: validResult}}).
		Publish(ctx, MachinePublisher{Login: "alice", Machine: ""}, snapshot); !errors.Is(err, ErrPublisherIdentity) {
		t.Errorf("blank machine err = %v", err)
	}
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{result: validResult}}).
		Publish(ctx, MachinePublisher{Login: "alice", Machine: "vm"}, snapshot); !errors.Is(err, ErrPublisherMismatch) {
		t.Errorf("identity mismatch err = %v", err)
	}
	invalidSnapshotInput := validHostedSnapshot()
	invalidSnapshotInput.SchemaVersion = 0
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{result: validResult}}).
		Publish(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}, invalidSnapshotInput); !errors.Is(err, machinesnapshot.ErrInvalidSnapshot) {
		t.Errorf("invalid snapshot err = %v", err)
	}
	if _, err := (MachineSnapshotService{}).
		Publish(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}, snapshot); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("missing store err = %v", err)
	}
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{storeErr: errors.New("disk full")}}).
		Publish(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}, snapshot); err == nil || !strings.Contains(err.Error(), "store hosted machine snapshot") {
		t.Errorf("store failure err = %v", err)
	}
	invalidRecord := validResult
	invalidRecord.Current.Snapshot.Login = "bob"
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{result: invalidRecord}}).
		Publish(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}, snapshot); err == nil || !strings.Contains(err.Error(), "invalid publisher record") {
		t.Errorf("invalid stored record err = %v", err)
	}

	service := MachineSnapshotService{Store: &dqCovSnapshotStore{result: validResult}, Now: func() time.Time { return receivedAt }}
	receipt, err := service.Publish(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Login != "alice" || receipt.Machine != "laptop" || !receipt.Updated || !receipt.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestDQCovMachineSnapshotPublishFailsClosedWhenSnapshotCannotBeEncoded(t *testing.T) {
	t.Parallel()
	// A year outside RFC 3339's range passes Snapshot.Validate but cannot be
	// encoded for durable storage, so Publish must fail before touching the
	// store rather than persisting an unverifiable record.
	snapshot := validHostedSnapshot()
	snapshot.PublishedAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	snapshot.Worktrees = nil
	snapshot.Repositories = nil
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("probe snapshot should pass validation: %v", err)
	}
	store := &dqCovSnapshotStore{}
	_, err := (MachineSnapshotService{Store: store}).Publish(
		context.Background(), MachinePublisher{Login: "alice", Machine: "laptop"}, snapshot)
	if err == nil || !strings.Contains(err.Error(), "encode validated hosted snapshot") {
		t.Fatalf("err = %v, want encode refusal", err)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want none after an encode failure", store.calls)
	}
}

func TestDQCovMachineSnapshotListValidatesPublisherAndRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	valid := validHostedSnapshot()

	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{}}).
		List(ctx, MachinePublisher{Login: "", Machine: "laptop"}); !errors.Is(err, ErrPublisherIdentity) {
		t.Errorf("blank login err = %v", err)
	}
	if _, err := (MachineSnapshotService{}).
		List(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("missing store err = %v", err)
	}
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{listErr: errors.New("list down")}}).
		List(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}); err == nil || !strings.Contains(err.Error(), "list hosted machine snapshots") {
		t.Errorf("list failure err = %v", err)
	}

	invalidStored := machinesnapshot.StoredSnapshot{
		Snapshot: machinesnapshot.Snapshot{Login: "alice", Machine: "laptop"}, ReceivedAt: at, Digest: "d",
	}
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{list: []machinesnapshot.StoredSnapshot{invalidStored}}}).
		List(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}); err == nil || !strings.Contains(err.Error(), "stored hosted machine snapshot is invalid") {
		t.Errorf("invalid stored snapshot err = %v", err)
	}

	missingEvidence := machinesnapshot.StoredSnapshot{Snapshot: valid}
	if _, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{list: []machinesnapshot.StoredSnapshot{missingEvidence}}}).
		List(ctx, MachinePublisher{Login: "alice", Machine: "laptop"}); err == nil || !strings.Contains(err.Error(), "missing receipt evidence") {
		t.Errorf("missing receipt evidence err = %v", err)
	}

	listed, err := (MachineSnapshotService{Store: &dqCovSnapshotStore{list: []machinesnapshot.StoredSnapshot{
		{Snapshot: valid, ReceivedAt: at, Digest: "d"},
	}}}).List(ctx, MachinePublisher{Login: "alice", Machine: "laptop"})
	if err != nil || len(listed.Snapshots) != 1 || listed.Snapshots[0].Snapshot.Machine != "laptop" {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
}

func TestDQCovMachineSnapshotsHandlerListFailuresAndSuccess(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	valid := validHostedSnapshot()
	validPublisher := publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
		return MachinePublisher{Login: "alice", Machine: "laptop"}, nil
	})

	withoutResolver := NewHandler(HandlerOptions{MachineSnapshots: &MachineSnapshotService{Store: &dqCovSnapshotStore{}}})
	response := httptest.NewRecorder()
	withoutResolver.ServeHTTP(response, httptest.NewRequest(http.MethodGet, machinesnapshot.SnapshotPath, nil))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "publisher_unavailable") {
		t.Fatalf("missing resolver = %d %s", response.Code, response.Body.String())
	}

	failingResolver := NewHandler(HandlerOptions{
		MachineSnapshots:  &MachineSnapshotService{Store: &dqCovSnapshotStore{}},
		PublisherResolver: publisherResolverFunc(func(*http.Request) (MachinePublisher, error) { return MachinePublisher{}, errors.New("no credential") }),
	})
	response = httptest.NewRecorder()
	failingResolver.ServeHTTP(response, httptest.NewRequest(http.MethodGet, machinesnapshot.SnapshotPath, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("resolver failure = %d", response.Code)
	}

	withoutStore := NewHandler(HandlerOptions{PublisherResolver: validPublisher})
	response = httptest.NewRecorder()
	withoutStore.ServeHTTP(response, httptest.NewRequest(http.MethodGet, machinesnapshot.SnapshotPath, nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "snapshot_store_not_configured") {
		t.Fatalf("missing store = %d %s", response.Code, response.Body.String())
	}

	blankIdentity := NewHandler(HandlerOptions{
		MachineSnapshots: &MachineSnapshotService{Store: &dqCovSnapshotStore{}},
		PublisherResolver: publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
			return MachinePublisher{}, nil
		}),
	})
	response = httptest.NewRecorder()
	blankIdentity.ServeHTTP(response, httptest.NewRequest(http.MethodGet, machinesnapshot.SnapshotPath, nil))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "publisher_unavailable") {
		t.Fatalf("blank identity = %d %s", response.Code, response.Body.String())
	}

	failingList := NewHandler(HandlerOptions{
		MachineSnapshots:  &MachineSnapshotService{Store: &dqCovSnapshotStore{listErr: errors.New("list down")}},
		PublisherResolver: validPublisher,
	})
	response = httptest.NewRecorder()
	failingList.ServeHTTP(response, httptest.NewRequest(http.MethodGet, machinesnapshot.SnapshotPath, nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "snapshot_store_error") {
		t.Fatalf("list failure = %d %s", response.Code, response.Body.String())
	}

	success := NewHandler(HandlerOptions{
		MachineSnapshots: &MachineSnapshotService{Store: &dqCovSnapshotStore{list: []machinesnapshot.StoredSnapshot{
			{Snapshot: valid, ReceivedAt: at, Digest: "d"},
		}}},
		PublisherResolver: validPublisher,
	})
	response = httptest.NewRecorder()
	success.ServeHTTP(response, httptest.NewRequest(http.MethodGet, machinesnapshot.SnapshotPath, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", response.Code, response.Body.String())
	}
	var listed machinesnapshot.ListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Snapshots) != 1 || listed.Snapshots[0].Snapshot.Login != "alice" {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestDQCovPublishMachineSnapshotMapsEveryOutcome(t *testing.T) {
	t.Parallel()
	valid := validHostedSnapshot()
	body, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	validPublisher := publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
		return MachinePublisher{Login: "alice", Machine: "laptop"}, nil
	})
	receivedAt := time.Date(2026, 9, 6, 14, 0, 1, 0, time.UTC)

	unconfigured := NewHandler(HandlerOptions{PublisherResolver: validPublisher})
	if response := dqCovPostSnapshot(t, unconfigured, string(body)); response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "snapshot_store_not_configured") {
		t.Fatalf("missing store = %d %s", response.Code, response.Body.String())
	}

	store := &dqCovSnapshotStore{result: machinesnapshot.StoreResult{
		Updated: true,
		Current: machinesnapshot.StoredSnapshot{Snapshot: valid, ReceivedAt: receivedAt, Digest: "d"},
	}}
	handler := NewHandler(HandlerOptions{
		MachineSnapshots:  &MachineSnapshotService{Store: store, Now: func() time.Time { return receivedAt }},
		PublisherResolver: validPublisher,
	})

	if response := dqCovPostSnapshot(t, handler, "{}x"); response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "invalid_snapshot_payload") {
		t.Fatalf("trailing token = %d %s", response.Code, response.Body.String())
	}
	if response := dqCovPostSnapshot(t, handler, string(body)); response.Code != http.StatusOK {
		t.Fatalf("success = %d %s", response.Code, response.Body.String())
	}

	blankIdentity := NewHandler(HandlerOptions{
		MachineSnapshots: &MachineSnapshotService{Store: store},
		PublisherResolver: publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
			return MachinePublisher{}, nil
		}),
	})
	if response := dqCovPostSnapshot(t, blankIdentity, string(body)); response.Code != http.StatusUnauthorized ||
		!strings.Contains(response.Body.String(), "publisher_unavailable") {
		t.Fatalf("blank publisher = %d %s", response.Code, response.Body.String())
	}

	invalidSnapshot := valid
	invalidSnapshot.SchemaVersion = 0
	invalidBody, err := json.Marshal(invalidSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if response := dqCovPostSnapshot(t, handler, string(invalidBody)); response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "invalid_snapshot_payload") {
		t.Fatalf("invalid snapshot = %d %s", response.Code, response.Body.String())
	}

	for name, storeErr := range map[string]error{
		"stale":        machinesnapshot.ErrStaleSnapshot,
		"conflict":     machinesnapshot.ErrSnapshotConflict,
		"unclassified": errors.New("unexpected storage failure"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			failing := NewHandler(HandlerOptions{
				MachineSnapshots:  &MachineSnapshotService{Store: &dqCovSnapshotStore{storeErr: storeErr}},
				PublisherResolver: validPublisher,
			})
			response := dqCovPostSnapshot(t, failing, string(body))
			wantStatus := http.StatusInternalServerError
			wantCode := "snapshot_store_error"
			if errors.Is(storeErr, machinesnapshot.ErrStaleSnapshot) || errors.Is(storeErr, machinesnapshot.ErrSnapshotConflict) {
				wantStatus, wantCode = http.StatusConflict, "stale_snapshot"
			}
			if response.Code != wantStatus || !strings.Contains(response.Body.String(), wantCode) {
				t.Fatalf("status/body = %d %s, want %d %s", response.Code, response.Body.String(), wantStatus, wantCode)
			}
		})
	}

	missingStore := NewHandler(HandlerOptions{
		MachineSnapshots:  &MachineSnapshotService{},
		PublisherResolver: validPublisher,
	})
	if response := dqCovPostSnapshot(t, missingStore, string(body)); response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "snapshot_store_not_configured") {
		t.Fatalf("nil snapshot store = %d %s", response.Code, response.Body.String())
	}
}

func dqCovPostSnapshot(t *testing.T, handler http.Handler, payload string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, strings.NewReader(payload)))
	return response
}

func TestDQCovPublishMachineSnapshotMismatchIsForbidden(t *testing.T) {
	t.Parallel()
	mismatch := validHostedSnapshot()
	mismatch.Machine = "vm"
	body, err := json.Marshal(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(HandlerOptions{
		MachineSnapshots: &MachineSnapshotService{Store: &dqCovSnapshotStore{}},
		PublisherResolver: publisherResolverFunc(func(*http.Request) (MachinePublisher, error) {
			return MachinePublisher{Login: "alice", Machine: "laptop"}, nil
		}),
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, machinesnapshot.SnapshotPath, bytes.NewReader(body)))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "publisher_identity_mismatch") {
		t.Fatalf("mismatch = %d %s", response.Code, response.Body.String())
	}
}
