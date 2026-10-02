package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/remotestate"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestProviderPublishesPrivacySafeSnapshotAndRetriesGatewayFailure(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	attempts := 0
	var published machinesnapshot.Snapshot
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if request.URL.String() != "https://hub.example/v0/workbench/machines/snapshot" {
			t.Fatalf("URL = %s", request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer injected-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		if attempts == 1 {
			return response(http.StatusBadGateway, `{}`), nil
		}
		if err := json.NewDecoder(request.Body).Decode(&published); err != nil {
			t.Fatal(err)
		}
		return response(http.StatusOK, `{"login":"alice","machine":"laptop","published_at":"2026-09-06T14:00:00Z","received_at":"2026-09-06T14:00:01Z","updated":true}`), nil
	})}
	provider, err := New(Options{
		BaseURL: "https://hub.example", Machine: "laptop", Token: "injected-token", Client: client,
		RetryDelays: []time.Duration{0}, Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Publish(context.Background(), remotestate.Snapshot{
		Login: "alice", Machine: "laptop", PublishedAt: at,
		ProjectsRoot:      "/Users/alice/private",
		KnownRepositories: []string{"Acme/Widgets", "acme/widgets"},
		Repositories:      []remotestate.RepositoryState{{Path: "/Users/alice/private/acme/widgets", Unpushed: []string{"private subject"}}},
		Worktrees: []remotestate.WorktreeState{{
			Task: "dashboard", Repository: "acme/widgets", Branch: "feature/dashboard",
			Dir: "/Users/alice/private/.worktrees/dashboard", HeadSHA: "secret-sha",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || result.Location != "https://hub.example/v0/workbench/machines/snapshot" {
		t.Fatalf("attempts/location = %d, %q", attempts, result.Location)
	}
	raw, _ := json.Marshal(published)
	for _, forbidden := range []string{"/Users/alice", "private subject", "secret-sha", "projects_root"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("published body contains %q: %s", forbidden, raw)
		}
	}
	if len(published.Repositories) != 1 || published.Repositories[0] != "github.com/acme/widgets" {
		t.Fatalf("published repositories = %+v", published.Repositories)
	}
}

func TestProviderPollsAndAcknowledgesRepositoryEventsWithStrictBounds(t *testing.T) {
	t.Parallel()
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.RequestURI() {
		case repositoryevent.EventsPath + "?cursor=before&limit=1&wait_seconds=25":
			return response(http.StatusOK, `{"version":1,"cursor":"before","next_cursor":"after","events":[{"version":1,"id":"delivery-1:default","repository":"github.com/acme/app","ref":"refs/heads/main","reason":"default_branch_updated"}]}`), nil
		case repositoryevent.AckPath:
			return response(http.StatusOK, `{"version":1,"cursor":"after"}`), nil
		default:
			t.Fatalf("request URI = %q", request.URL.RequestURI())
			return nil, nil
		}
	})}
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: "laptop", Token: "token", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	poll, err := provider.PollRepositoryEvents(context.Background(), "before", 1, 25*time.Second)
	if err != nil || len(poll.Events) != 1 {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	ack := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: poll.NextCursor, EventIDs: []string{poll.Events[0].ID}}
	if _, err := provider.AckRepositoryEvents(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
	if _, err := provider.PollRepositoryEvents(context.Background(), "", repositoryevent.MaxLimit+1, 0); err == nil || requests != 2 {
		t.Fatalf("unbounded request error=%v requests=%d", err, requests)
	}
}

func TestProviderUsesTokenFileAndListsSafeEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer rotated-token" {
			t.Fatalf("request = %s, authorization %q", request.Method, request.Header.Get("Authorization"))
		}
		return response(http.StatusOK, `{"snapshots":[{"snapshot":{"schema_version":1,"login":"alice","machine":"laptop","published_at":"2026-09-06T14:00:00Z","worktrees":[{"task":"dashboard","repository":"acme/widgets","branch":"feature/dashboard"}]},"received_at":"2026-09-06T14:00:01Z","digest":"abc"}]}`), nil
	})}
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: "laptop", TokenFile: tokenFile, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	status, err := provider.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entries := status.Machines
	if requests != 1 || len(status.Claims) != 0 {
		t.Fatalf("requests=%d claims=%+v, want one request and no hosted claims", requests, status.Claims)
	}
	if len(entries) != 1 || entries[0].Snapshot.Worktrees[0].Repository != "acme/widgets" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Snapshot.ProjectsRoot != "" || entries[0].Snapshot.Worktrees[0].Dir != "" {
		t.Fatalf("entry exposed local paths: %+v", entries[0])
	}
}

func TestProviderRejectsInsecureOrAmbiguousConfiguration(t *testing.T) {
	t.Parallel()
	for name, options := range map[string]Options{
		"http":               {BaseURL: "http://hub.example", Machine: "laptop", Token: "token"},
		"missing credential": {BaseURL: "https://hub.example", Machine: "laptop"},
		"two credentials":    {BaseURL: "https://hub.example", Machine: "laptop", Token: "token", TokenFile: "/tmp/token"},
		"URL credential":     {BaseURL: "https://user:secret@hub.example", Machine: "laptop", Token: "token"},
	} {
		if _, err := New(options); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestProviderRejectsOversizedOrTrailingSuccessfulResponse(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"oversized": strings.Repeat(" ", maxResponseBytes) + "{}",
		"trailing":  `{"snapshots":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, body), nil
			})}
			provider, err := New(Options{BaseURL: "https://hub.example", Machine: "laptop", Token: "token", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.List(context.Background()); err == nil {
				t.Fatal("invalid successful response was accepted")
			}
		})
	}
}

func TestProviderDoesNotRetryAuthenticationFailureOrLeakCredential(t *testing.T) {
	t.Parallel()
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return response(http.StatusUnauthorized, `{"error":"no"}`), nil
	})}
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: "laptop", Token: "do-not-leak", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.List(context.Background())
	if err == nil || attempts != 1 || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("attempts/error = %d, %v", attempts, err)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// TestNewAcceptsALoopbackHTTPHub is the constructor half of the relaxation:
// the in-process provider a self-hosted daemon configures talks plain HTTP to
// its own listener, and nothing else.
func TestNewAcceptsALoopbackHTTPHub(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "hub.token")
	if err := os.WriteFile(tokenFile, []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, accepted := range []string{"http://127.0.0.1:8766", "http://localhost:8766", "https://wb-github-app.sneat.dev"} {
		provider, err := New(Options{BaseURL: accepted, Machine: "laptop", TokenFile: tokenFile})
		if err != nil || provider == nil {
			t.Fatalf("New(%q) = %v, %v", accepted, provider, err)
		}
		if provider.baseURL != strings.TrimSuffix(accepted, "/") {
			t.Fatalf("baseURL = %q, want %q", provider.baseURL, accepted)
		}
	}
	for _, rejected := range []string{"http://bench.example", "http://10.0.0.1:8766"} {
		if _, err := New(Options{BaseURL: rejected, Machine: "laptop", TokenFile: tokenFile}); !errors.Is(err, remotestate.ErrHubURL) {
			t.Fatalf("New(%q) = %v, want ErrHubURL", rejected, err)
		}
	}
}

// TestAnOlderHubRefusingTheOptionalFieldsIsRetriedOnceWithoutThem is the old-hub
// half of cockpit-views#ac:remote-snapshot-carries-optional-agents-and-metrics:
// the fake hub decodes with unknown fields refused into the previous snapshot
// model, so it answers 400 to the new fields exactly as a deployed hub does.
func TestAnOlderHubRefusingTheOptionalFieldsIsRetriedOnceWithoutThem(t *testing.T) {
	t.Parallel()
	type previousModel struct {
		SchemaVersion int                        `json:"schema_version"`
		Login         string                     `json:"login"`
		Machine       string                     `json:"machine"`
		PublishedAt   time.Time                  `json:"published_at"`
		LastSeenAt    time.Time                  `json:"last_seen_at,omitempty"`
		RemoteStore   string                     `json:"remote_store,omitempty"`
		Repositories  []string                   `json:"repositories"`
		Worktrees     []machinesnapshot.Worktree `json:"worktrees"`
	}
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var old previousModel
		if err := decoder.Decode(&old); err != nil {
			return response(http.StatusBadRequest, `{"error":"invalid_snapshot_payload"}`), nil
		}
		return response(http.StatusOK, `{"login":"alice","machine":"laptop","published_at":"2026-10-01T09:00:00Z","received_at":"2026-10-01T09:00:01Z","updated":true}`), nil
	})}
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: "laptop", Token: "t", Client: client, RetryDelays: []time.Duration{}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	snapshot := remotestate.Snapshot{Login: "alice", Machine: "laptop", PublishedAt: at, OS: "linux", CPUCount: 4,
		Agents: []remotestate.AgentState{{Kind: "run", State: "running"}}}

	// Without the helper the provider's refusal is a typed status.
	_, err = provider.Publish(context.Background(), snapshot)
	var status remotestate.StatusCoder
	if !errors.As(err, &status) || status.HTTPStatus() != http.StatusBadRequest || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v, want a 400 status error", err)
	}
	requests = 0
	_, diagnostic, err := remotestate.PublishWithFallback(context.Background(), provider, snapshot, at)
	if err != nil || !errors.Is(diagnostic, remotestate.ErrOptionalFieldsDropped) || requests != 2 {
		t.Fatalf("diagnostic=%v err=%v requests=%d", diagnostic, err, requests)
	}
}

func TestTheHubProviderRemembersAnOlderHubsRefusalForADay(t *testing.T) {
	t.Parallel()
	requests, sawOptional := 0, 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		body, _ := io.ReadAll(request.Body)
		if strings.Contains(string(body), `"os"`) {
			sawOptional++
			return response(http.StatusBadRequest, `{}`), nil
		}
		return response(http.StatusOK, `{"login":"alice","machine":"laptop","published_at":"2026-10-01T09:00:00Z","received_at":"2026-10-01T09:00:01Z","updated":true}`), nil
	})}
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: "laptop", Token: "t", Client: client, RetryDelays: []time.Duration{}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	snapshot := remotestate.Snapshot{Login: "alice", Machine: "laptop", PublishedAt: at, OS: "linux"}
	publish := func(now time.Time) {
		t.Helper()
		if _, diagnostic, err := remotestate.PublishWithFallback(context.Background(), provider, snapshot, now); err != nil || !errors.Is(diagnostic, remotestate.ErrOptionalFieldsDropped) {
			t.Fatalf("publish at %s: %v %v", now, diagnostic, err)
		}
	}
	publish(at)
	if requests != 2 {
		t.Fatalf("first publish made %d requests", requests)
	}
	publish(at.Add(12 * time.Hour))
	if requests != 3 || sawOptional != 1 {
		t.Fatalf("a remembered refusal still sent the optional fields: %d requests, %d refused", requests, sawOptional)
	}
	publish(at.Add(25 * time.Hour)) // the full payload is tried again after a day
	if sawOptional != 2 {
		t.Fatalf("the refusal outlived its day: %d", sawOptional)
	}
	// A new provider (a daemon restart) remembers nothing.
	fresh, _ := New(Options{BaseURL: "https://hub.example", Machine: "laptop", Token: "t", Client: client, RetryDelays: []time.Duration{}})
	if fresh.OptionalFieldsRefused(at) {
		t.Fatal("a new provider remembers a refusal")
	}
}
