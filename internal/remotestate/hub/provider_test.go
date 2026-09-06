package hub

import (
	"context"
	"encoding/json"
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
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer rotated-token" {
			t.Fatalf("request = %s, authorization %q", request.Method, request.Header.Get("Authorization"))
		}
		return response(http.StatusOK, `{"snapshots":[{"snapshot":{"schema_version":1,"login":"alice","machine":"laptop","published_at":"2026-09-06T14:00:00Z","worktrees":[{"task":"dashboard","repository":"acme/widgets","branch":"feature/dashboard"}]},"received_at":"2026-09-06T14:00:01Z","digest":"abc"}]}`), nil
	})}
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: "laptop", TokenFile: tokenFile, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := provider.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Snapshot.Worktrees[0].Repository != "acme/widgets" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Snapshot.ProjectsRoot != "" || entries[0].Snapshot.Worktrees[0].Dir != "" {
		t.Fatalf("entry exposed local paths: %+v", entries[0])
	}
}

func TestProviderRejectsInsecureOrAmbiguousConfiguration(t *testing.T) {
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

func TestProviderDoesNotRetryAuthenticationFailureOrLeakCredential(t *testing.T) {
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
