package githubapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

type readSnapshotStore struct {
	records []machinesnapshot.StoredSnapshot
	calls   int
	err     error
}

func (store *readSnapshotStore) StoreLatest(context.Context, machinesnapshot.StoredSnapshot) (machinesnapshot.StoreResult, error) {
	return machinesnapshot.StoreResult{}, errors.New("unexpected write")
}

func (store *readSnapshotStore) ListLatest(context.Context) ([]machinesnapshot.StoredSnapshot, error) {
	store.calls++
	return store.records, store.err
}

type machineAccess struct {
	allowed map[string]bool
	err     error
}

func (access machineAccess) CanViewMachine(_ context.Context, _ Viewer, login, machine string) (bool, error) {
	return access.allowed[login+"/"+machine], access.err
}

type fixedViewer struct{ viewer Viewer }

func (resolver fixedViewer) Viewer(*http.Request) (Viewer, error) { return resolver.viewer, nil }

func TestRemoteStateWorktreeReadModelFiltersAuthorizedMachines(t *testing.T) {
	laptopPublished := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	vmPublished := laptopPublished.Add(time.Hour)
	lastActivity := laptopPublished.Add(-time.Minute)
	store := &readSnapshotStore{records: []machinesnapshot.StoredSnapshot{
		storedMachine("alex", "laptop", laptopPublished, laptopPublished.Add(10*time.Minute), []machinesnapshot.Worktree{{
			Repository: "sneat-dev/wb", Task: "dashboard.api", Stream: "dashboard", Branch: "feature/dashboard-api",
			Lifecycle: "review", OwnerState: "active", Owner: "codex", LastActivityAt: lastActivity,
			PullRequest: &machinesnapshot.PullRequest{Number: 77, URL: "https://github.com/sneat-dev/wb/pull/77", State: "open"},
		}}),
		storedMachine("alex", "vm", vmPublished, vmPublished, []machinesnapshot.Worktree{{
			Repository: "sneat-co/sneat-go", Task: "release", Stream: "release", Branch: "feature/release",
			Lifecycle: "working", OwnerState: "orphaned",
		}}),
		storedMachine("other", "server", vmPublished.Add(time.Hour), vmPublished.Add(time.Hour), []machinesnapshot.Worktree{{
			Repository: "private/secret", Task: "secret", Branch: "secret",
		}}),
	}}
	model := RemoteStateWorktreeReadModel{
		Store: store, Access: machineAccess{allowed: map[string]bool{"alex/laptop": true, "alex/vm": true}},
		Now: func() time.Time { return vmPublished.Add(time.Hour) }, StaleAfter: 90 * time.Minute,
	}
	viewer := Viewer{Authenticated: true, Member: true, UserID: "firebase-user"}

	access, err := model.Worktrees(context.Background(), viewer, WorktreeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if access.Visibility != VisibilityPrivate || len(access.Value.Rows) != 2 {
		t.Fatalf("access = %+v, want two private rows", access)
	}
	if !access.Value.GeneratedAt.Equal(vmPublished) {
		t.Fatalf("generated_at = %v, want %v", access.Value.GeneratedAt, vmPublished)
	}
	first := access.Value.Rows[0]
	if first.Repository != "sneat-co/sneat-go" || first.Machine != "vm" || first.Status != "attention" || !first.NeedsAttention {
		t.Fatalf("first row = %+v", first)
	}
	if first.MachineStale || !first.MachineSeenAt.Equal(vmPublished) {
		t.Fatalf("fresh VM row = %+v", first)
	}
	second := access.Value.Rows[1]
	if second.PullRequest != 77 || second.PullRequestURL == "" || second.Stream != "dashboard" || second.LastActivityAt == nil || !second.LastActivityAt.Equal(lastActivity) {
		t.Fatalf("second row = %+v", second)
	}
	if !second.MachineStale || !second.MachineSeenAt.Equal(laptopPublished.Add(10*time.Minute)) {
		t.Fatalf("offline laptop row was not retained with its server receipt: %+v", second)
	}

	needsAttention := true
	cases := []struct {
		name   string
		filter WorktreeFilter
		want   string
	}{
		{name: "machine", filter: WorktreeFilter{Machine: "laptop"}, want: "dashboard.api"},
		{name: "repository", filter: WorktreeFilter{Repository: "sneat-co/sneat-go"}, want: "release"},
		{name: "lifecycle status", filter: WorktreeFilter{Status: "review"}, want: "dashboard.api"},
		{name: "owner status", filter: WorktreeFilter{Status: "orphaned"}, want: "release"},
		{name: "stream", filter: WorktreeFilter{Stream: "dashboard"}, want: "dashboard.api"},
		{name: "task", filter: WorktreeFilter{Task: "release"}, want: "release"},
		{name: "attention", filter: WorktreeFilter{NeedsAttention: &needsAttention}, want: "release"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			filtered, err := model.Worktrees(context.Background(), viewer, test.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(filtered.Value.Rows) != 1 || filtered.Value.Rows[0].Task != test.want {
				t.Fatalf("rows = %+v, want task %q", filtered.Value.Rows, test.want)
			}
		})
	}
}

func TestRemoteStateWorktreeReadModelFailsClosedBeforeReadingSnapshots(t *testing.T) {
	store := &readSnapshotStore{}
	model := RemoteStateWorktreeReadModel{Store: store, Access: machineAccess{}}
	_, err := model.Worktrees(context.Background(), Viewer{}, WorktreeFilter{})
	if !errors.Is(err, ErrPrivateData) || store.calls != 0 {
		t.Fatalf("err = %v, store calls = %d", err, store.calls)
	}
	_, err = (RemoteStateWorktreeReadModel{Store: store}).Worktrees(context.Background(), Viewer{Authenticated: true, Member: true, UserID: "user"}, WorktreeFilter{})
	if !errors.Is(err, ErrNoReadModel) || store.calls != 0 {
		t.Fatalf("missing access err = %v, store calls = %d", err, store.calls)
	}
	now := time.Now().UTC()
	failingStore := &readSnapshotStore{records: []machinesnapshot.StoredSnapshot{storedMachine("alex", "vm", now, now, nil)}}
	_, err = (RemoteStateWorktreeReadModel{Store: failingStore, Access: machineAccess{err: errors.New("membership unavailable")}}).Worktrees(
		context.Background(), Viewer{Authenticated: true, Member: true, UserID: "user"}, WorktreeFilter{})
	if err == nil || err.Error() != "membership unavailable" || failingStore.calls != 1 {
		t.Fatalf("access failure err = %v, store calls = %d", err, failingStore.calls)
	}
}

func TestWorktreeHandlerFiltersRowsWithoutLocalPathFields(t *testing.T) {
	published := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := &readSnapshotStore{records: []machinesnapshot.StoredSnapshot{storedMachine("alex", "vm", published, published, []machinesnapshot.Worktree{{
		Repository: "sneat-dev/wb", Task: "dashboard.api", Stream: "dashboard", Branch: "feature/dashboard-api",
		OwnerState: "active", NeedsAttention: true, AttentionReason: machinesnapshot.AttentionReviewRequired,
	}})}}
	model := RemoteStateWorktreeReadModel{Store: store, Access: machineAccess{allowed: map[string]bool{"alex/vm": true}}}
	handler := NewHandler(HandlerOptions{
		Service:        Service{Worktrees: model},
		ViewerResolver: fixedViewer{viewer: Viewer{Authenticated: true, Member: true, UserID: "user"}},
	})
	request := httptest.NewRequest(http.MethodGet, APIPrefix+"/worktrees?machine=vm&repository=sneat-dev%2Fwb&status=attention&stream=dashboard&task=dashboard.api&needs_attention=true", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{"projects_root", "head_sha", "login", "digest"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{`"repository":"sneat-dev/wb"`, `"machine":"vm"`, `"task":"dashboard.api"`} {
		if !strings.Contains(body, required) {
			t.Fatalf("response omitted %s: %s", required, body)
		}
	}
}

func TestWorktreeHandlerRejectsInvalidAttentionFilterAndAnonymousViewer(t *testing.T) {
	model := RemoteStateWorktreeReadModel{Store: &readSnapshotStore{}, Access: machineAccess{}}
	handler := NewHandler(HandlerOptions{Service: Service{Worktrees: model}})

	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, APIPrefix+"/worktrees?needs_attention=sometimes", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid filter status = %d, want %d", invalid.Code, http.StatusBadRequest)
	}
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, APIPrefix+"/worktrees", nil))
	if anonymous.Code != http.StatusNotFound {
		t.Fatalf("anonymous status = %d, want %d", anonymous.Code, http.StatusNotFound)
	}
}

func storedMachine(login, machine string, publishedAt, receivedAt time.Time, worktrees []machinesnapshot.Worktree) machinesnapshot.StoredSnapshot {
	if worktrees == nil {
		worktrees = []machinesnapshot.Worktree{}
	}
	return machinesnapshot.StoredSnapshot{
		Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion, Login: login, Machine: machine,
			PublishedAt: publishedAt, Worktrees: worktrees,
		},
		ReceivedAt: receivedAt, Digest: login + "-" + machine,
	}
}
