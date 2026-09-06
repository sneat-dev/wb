package githubapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

type snapshotSource struct {
	entries []remotestate.Entry
	calls   int
}

func (source *snapshotSource) List(context.Context) ([]remotestate.Entry, error) {
	source.calls++
	return source.entries, nil
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
	source := &snapshotSource{entries: []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "alex", Machine: "laptop", PublishedAt: laptopPublished, LastSeenAt: laptopPublished.Add(10 * time.Minute), Worktrees: []remotestate.WorktreeState{
			{Repository: "sneat-dev/wb", Task: "dashboard.api", Stream: "dashboard", Branch: "feature/dashboard-api", Lifecycle: "review", OwnerState: "active", Owner: "codex", LastActivityAt: lastActivity, Dir: "/Users/alex/private/dashboard-api", PullRequest: &remotestate.PullRequestState{Number: 77, URL: "https://github.com/sneat-dev/wb/pull/77", State: "open"}},
		}}},
		{Snapshot: remotestate.Snapshot{Login: "alex", Machine: "vm", PublishedAt: vmPublished, Worktrees: []remotestate.WorktreeState{
			{Repository: "sneat-co/sneat-go", Task: "release", Stream: "release", Branch: "feature/release", Lifecycle: "working", OwnerState: "orphaned", Dir: "/home/alex/private/release"},
		}}},
		{Snapshot: remotestate.Snapshot{Login: "other", Machine: "server", PublishedAt: vmPublished.Add(time.Hour), Worktrees: []remotestate.WorktreeState{
			{Repository: "private/secret", Task: "secret", Branch: "secret"},
		}}},
	}}
	model := RemoteStateWorktreeReadModel{
		Source: source, Access: machineAccess{allowed: map[string]bool{"alex/laptop": true, "alex/vm": true}},
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
		t.Fatalf("offline laptop row was not retained with its heartbeat: %+v", second)
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
	source := &snapshotSource{}
	model := RemoteStateWorktreeReadModel{Source: source, Access: machineAccess{}}
	_, err := model.Worktrees(context.Background(), Viewer{}, WorktreeFilter{})
	if !errors.Is(err, ErrPrivateData) || source.calls != 0 {
		t.Fatalf("err = %v, source calls = %d", err, source.calls)
	}
	_, err = (RemoteStateWorktreeReadModel{Source: source}).Worktrees(context.Background(), Viewer{Authenticated: true, Member: true, UserID: "user"}, WorktreeFilter{})
	if !errors.Is(err, ErrNoReadModel) || source.calls != 0 {
		t.Fatalf("missing access err = %v, source calls = %d", err, source.calls)
	}
	failingSource := &snapshotSource{entries: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alex", Machine: "vm"}}}}
	_, err = (RemoteStateWorktreeReadModel{Source: failingSource, Access: machineAccess{err: errors.New("membership unavailable")}}).Worktrees(
		context.Background(), Viewer{Authenticated: true, Member: true, UserID: "user"}, WorktreeFilter{})
	if err == nil || err.Error() != "membership unavailable" || failingSource.calls != 1 {
		t.Fatalf("access failure err = %v, source calls = %d", err, failingSource.calls)
	}
}

func TestWorktreeHandlerFiltersRowsWithoutExposingLocalPaths(t *testing.T) {
	published := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	source := &snapshotSource{entries: []remotestate.Entry{{Snapshot: remotestate.Snapshot{
		Login: "alex", Machine: "vm", PublishedAt: published,
		Worktrees: []remotestate.WorktreeState{{Repository: "sneat-dev/wb", Task: "dashboard.api", Stream: "dashboard", Branch: "feature/dashboard-api", OwnerState: "active", Dir: "/private/must-not-leak", Attention: "/private/must-not-leak"}},
	}}}}
	model := RemoteStateWorktreeReadModel{Source: source, Access: machineAccess{allowed: map[string]bool{"alex/vm": true}}}
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
	for _, forbidden := range []string{"/private/must-not-leak", "projects_root", "head_sha", "login"} {
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
	model := RemoteStateWorktreeReadModel{Source: &snapshotSource{}, Access: machineAccess{}}
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
