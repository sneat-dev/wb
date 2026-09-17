package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/remotestate"
)

const dqCovMachine = "laptop"

// dqCovHubRecorder records the requests an httptest hub received so a test can
// assert both what crossed the wire and how many attempts were made. The
// recorder is mutex-guarded because an httptest handler runs on its own
// goroutine.
type dqCovHubRecorder struct {
	mu       sync.Mutex
	observed []dqCovHubRequest
}

type dqCovHubRequest struct {
	method string
	uri    string
	auth   string
	body   []byte
}

func (recorder *dqCovHubRecorder) capture(request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.observed = append(recorder.observed, dqCovHubRequest{
		method: request.Method, uri: request.URL.RequestURI(),
		auth: request.Header.Get("Authorization"), body: body,
	})
}

func (recorder *dqCovHubRecorder) requests() []dqCovHubRequest {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]dqCovHubRequest(nil), recorder.observed...)
}

func (recorder *dqCovHubRecorder) count() int { return len(recorder.requests()) }

// dqCovStartHubWith starts a hub on a loopback ephemeral port (127.0.0.1:0),
// points the provider at it, and returns every request the hub observed. The
// server is closed when the test ends.
func dqCovStartHubWith(t *testing.T, options Options, handler http.HandlerFunc) (*Provider, *dqCovHubRecorder) {
	t.Helper()
	recorder := &dqCovHubRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.capture(request)
		handler(writer, request)
	}))
	t.Cleanup(server.Close)

	options.BaseURL = server.URL
	if options.Machine == "" {
		options.Machine = dqCovMachine
	}
	provider, err := New(options)
	if err != nil {
		t.Fatalf("New(%+v) = %v", options, err)
	}
	return provider, recorder
}

func dqCovStartHub(t *testing.T, token string, handler http.HandlerFunc) (*Provider, *dqCovHubRecorder) {
	t.Helper()
	return dqCovStartHubWith(t, Options{Token: token}, handler)
}

// dqCovStatusHandler answers every request with one fixed status and body.
func dqCovStatusHandler(status int, body string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}
}

// dqCovSnapshot is a minimal snapshot that passes the hosted allowlist for the
// configured machine.
func dqCovSnapshot() remotestate.Snapshot {
	return remotestate.Snapshot{
		Login:       "alice",
		Machine:     dqCovMachine,
		PublishedAt: time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC),
	}
}

// dqCovTokenFile writes a credential file under a fresh temp dir.
func dqCovTokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHubProviderNewRejectsMachineRetryDelayAndCredentialDefects(t *testing.T) {
	for name, testCase := range map[string]struct {
		options Options
		want    string
	}{
		"invalid machine":        {Options{BaseURL: "https://hub.example", Machine: "not a machine", Token: "token"}, "remote.machine is invalid"},
		"negative retry delay":   {Options{BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "token", RetryDelays: []time.Duration{-time.Second}}, "retry delay must not be negative"},
		"injected token defect":  {Options{BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "two tokens"}, "must contain one non-empty token"},
		"token with tab":         {Options{BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "laptop\ttoken"}, "must contain one non-empty token"},
		"no credential source":   {Options{BaseURL: "https://hub.example", Machine: dqCovMachine}, "exactly one hub credential source is required"},
		"two credential sources": {Options{BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "token", TokenFile: "/tmp/token"}, "exactly one hub credential source is required"},
	} {
		provider, err := New(testCase.options)
		if provider != nil || err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: New = %v, %v; want a nil provider and an error containing %q", name, provider, err, testCase.want)
		}
	}
}

// TestHubProviderNewCopiesMutableOptionsAndNormalisesValues proves the
// constructor does not alias caller-owned memory and that the endpoint and
// injected token are trimmed before use.
func TestHubProviderNewCopiesMutableOptionsAndNormalisesValues(t *testing.T) {
	delays := []time.Duration{time.Second}
	provider, err := New(Options{
		BaseURL: " https://hub.example/ ", Machine: dqCovMachine, Token: "  injected  ", RetryDelays: delays,
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.baseURL != "https://hub.example" {
		t.Fatalf("baseURL = %q, want the trimmed endpoint without a trailing slash", provider.baseURL)
	}
	if provider.token != "injected" {
		t.Fatalf("token = %q, want the trimmed token", provider.token)
	}
	delays[0] = time.Hour
	if len(provider.retryDelays) != 1 || provider.retryDelays[0] != time.Second {
		t.Fatalf("retryDelays = %v, want an independent copy of [1s]", provider.retryDelays)
	}
	if provider.client == nil || provider.client.Timeout != 30*time.Second {
		t.Fatalf("default client = %+v, want the 30s-timeout client", provider.client)
	}
	if provider.sleep == nil {
		t.Fatal("default sleep function is nil")
	}
}

func TestHubProviderPublishRejectsMismatchedMachineAndInvalidSnapshotWithoutARequest(t *testing.T) {
	provider, recorder := dqCovStartHub(t, "token", func(http.ResponseWriter, *http.Request) {
		t.Error("a locally rejected snapshot must not reach the hub")
	})

	mismatched := dqCovSnapshot()
	mismatched.Machine = "desktop"
	if _, err := provider.Publish(context.Background(), mismatched); err == nil || !strings.Contains(err.Error(), "does not match configured machine") {
		t.Fatalf("Publish(mismatched machine) = %v, want a machine mismatch error", err)
	}

	invalid := dqCovSnapshot()
	invalid.Login = ""
	if _, err := provider.Publish(context.Background(), invalid); !errors.Is(err, machinesnapshot.ErrInvalidSnapshot) {
		t.Fatalf("Publish(invalid snapshot) = %v, want ErrInvalidSnapshot", err)
	}

	if recorder.count() != 0 {
		t.Fatalf("hub received %d requests for locally rejected snapshots", recorder.count())
	}
}

func TestHubProviderPublishReportsHubAndReceiptFailures(t *testing.T) {
	for name, testCase := range map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"http failure": {
			dqCovStatusHandler(http.StatusInternalServerError, `{}`),
			"publish hosted snapshot",
		},
		"receipt names another machine": {
			dqCovStatusHandler(http.StatusOK, `{"login":"alice","machine":"desktop","published_at":"2026-09-06T14:00:00Z","received_at":"2026-09-06T14:00:01Z"}`),
			"receipt for a different publisher",
		},
		"receipt names another publish time": {
			dqCovStatusHandler(http.StatusOK, `{"login":"alice","machine":"laptop","published_at":"2026-09-07T14:00:00Z","received_at":"2026-09-07T14:00:01Z"}`),
			"receipt for a different publisher",
		},
		"receipt has no received time": {
			dqCovStatusHandler(http.StatusOK, `{"login":"alice","machine":"laptop","published_at":"2026-09-06T14:00:00Z"}`),
			"receipt for a different publisher",
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider, recorder := dqCovStartHub(t, "token", testCase.handler)
			result, err := provider.Publish(context.Background(), dqCovSnapshot())
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Publish = %+v, %v; want an error containing %q", result, err, testCase.want)
			}
			if result.Location != "" {
				t.Fatalf("failed Publish reported a location: %+v", result)
			}
			if recorder.count() != 1 {
				t.Fatalf("publish attempts = %d, want 1", recorder.count())
			}
		})
	}
}

func TestHubProviderListFlagsInvalidHostedRecords(t *testing.T) {
	provider, recorder := dqCovStartHub(t, "token", dqCovStatusHandler(http.StatusOK, `{"snapshots":[
		{"snapshot":{"schema_version":1,"login":"alice","machine":"laptop","published_at":"2026-09-06T14:00:00Z"},"received_at":"2026-09-06T14:00:01Z"},
		{"snapshot":{"schema_version":2,"login":"bob","machine":"old","published_at":"2026-09-06T14:00:00Z"},"received_at":"2026-09-06T14:00:01Z"},
		{"snapshot":{"schema_version":1,"login":"carol","machine":"new","published_at":"2026-09-06T14:00:00Z"}}
	]}`))

	entries, err := provider.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v, want 3", entries)
	}
	// SortPublished orders by "<login>/<machine>".
	if entries[0].Error != "" || entries[0].Snapshot.Key() != "alice/laptop" {
		t.Fatalf("valid entry = %+v, want it accepted without an error", entries[0])
	}
	if entries[1].Error != "invalid hosted snapshot response" || entries[1].Snapshot.Key() != "bob/old" {
		t.Fatalf("unsupported-schema entry = %+v, want it flagged while keeping the identity", entries[1])
	}
	if entries[2].Error != "invalid hosted snapshot response" || entries[2].Snapshot.Key() != "carol/new" {
		t.Fatalf("missing-received-at entry = %+v, want it flagged while keeping the identity", entries[2])
	}
	if entries[1].Snapshot.ProjectsRoot != "" || entries[1].Snapshot.Repositories != nil {
		t.Fatalf("flagged entry leaked local-only state: %+v", entries[1])
	}
	if recorder.count() != 1 {
		t.Fatalf("list requests = %d, want 1", recorder.count())
	}
}

func TestHubProviderStatusSurfacesListFailure(t *testing.T) {
	provider, recorder := dqCovStartHub(t, "token", dqCovStatusHandler(http.StatusInternalServerError, `{"error":"boom"}`))

	status, err := provider.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list hosted snapshots") {
		t.Fatalf("Status = %+v, %v; want the list failure to surface", status, err)
	}
	if status.Machines != nil || status.Claims != nil {
		t.Fatalf("Status returned a partial snapshot with an error: %+v", status)
	}
	if recorder.count() != 1 {
		t.Fatalf("status requests = %d, want 1", recorder.count())
	}
}

func TestHubProviderClaimReleaseClaimsReportUnsupported(t *testing.T) {
	provider, recorder := dqCovStartHub(t, "token", func(http.ResponseWriter, *http.Request) {
		t.Error("claim operations must not reach the hub")
	})
	ctx := context.Background()

	if outcome, err := provider.Claim(ctx, remotestate.Claim{Task: "t-1"}, remotestate.ClaimTakeOverStale, "alice/laptop"); !errors.Is(err, ErrClaimsUnsupported) || outcome.Kind != "" {
		t.Fatalf("Claim = %+v, %v; want the unsupported-claims error", outcome, err)
	}
	if outcome, err := provider.Release(ctx, "t-1", "alice", dqCovMachine, true); !errors.Is(err, ErrClaimsUnsupported) || outcome.Kind != "" {
		t.Fatalf("Release = %+v, %v; want the unsupported-claims error", outcome, err)
	}
	if claims, err := provider.Claims(ctx); !errors.Is(err, ErrClaimsUnsupported) || claims != nil {
		t.Fatalf("Claims = %+v, %v; want nil and the unsupported-claims error", claims, err)
	}
	if recorder.count() != 0 {
		t.Fatalf("hub received %d requests for unsupported claim operations", recorder.count())
	}
}

func TestHubProviderPollRepositoryEventsRejectsInvalidArgumentsWithoutARequest(t *testing.T) {
	provider, recorder := dqCovStartHub(t, "token", func(http.ResponseWriter, *http.Request) {
		t.Error("an invalid poll must not reach the hub")
	})

	for name, testCase := range map[string]struct {
		cursor string
		limit  int
		wait   time.Duration
		want   string
	}{
		"unsafe cursor":  {cursor: "bad\ncursor", limit: 1, want: "cursor"},
		"zero limit":     {cursor: "before", limit: 0, want: "limit must be between 1 and 100"},
		"over max limit": {cursor: "before", limit: repositoryevent.MaxLimit + 1, want: "limit must be between 1 and 100"},
		"negative wait":  {cursor: "before", limit: 1, wait: -time.Second, want: "wait must be between 0 and 30 seconds"},
		"wait over max":  {cursor: "before", limit: 1, wait: (repositoryevent.MaxWaitSeconds + 1) * time.Second, want: "wait must be between 0 and 30 seconds"},
	} {
		t.Run(name, func(t *testing.T) {
			response, err := provider.PollRepositoryEvents(context.Background(), testCase.cursor, testCase.limit, testCase.wait)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("PollRepositoryEvents(%q, %d, %v) = %+v, %v; want an error containing %q",
					testCase.cursor, testCase.limit, testCase.wait, response, err, testCase.want)
			}
			if len(response.Events) != 0 {
				t.Fatalf("rejected poll returned events: %+v", response)
			}
		})
	}
	if recorder.count() != 0 {
		t.Fatalf("hub received %d requests for rejected polls", recorder.count())
	}
}

func TestHubProviderPollRepositoryEventsSurfacesHubFailures(t *testing.T) {
	validEvent := func(id string) string {
		return `{"version":1,"id":"` + id + `","repository":"github.com/acme/app","ref":"refs/heads/main","reason":"default_branch_updated"}`
	}
	for name, testCase := range map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"http failure": {
			dqCovStatusHandler(http.StatusInternalServerError, `{}`),
			"poll repository events",
		},
		"response version": {
			dqCovStatusHandler(http.StatusOK, `{"version":2,"cursor":"before","next_cursor":"after","events":[]}`),
			"version must be 1",
		},
		"response cursor": {
			dqCovStatusHandler(http.StatusOK, `{"version":1,"cursor":"other","next_cursor":"after","events":[]}`),
			"cursor does not match",
		},
		"more events than requested": {
			dqCovStatusHandler(http.StatusOK, `{"version":1,"cursor":"before","next_cursor":"after","events":[`+validEvent("delivery-1:default")+`,`+validEvent("delivery-2:default")+`]}`),
			"more repository events than requested",
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider, recorder := dqCovStartHub(t, "token", testCase.handler)
			response, err := provider.PollRepositoryEvents(context.Background(), "before", 1, 0)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("PollRepositoryEvents = %+v, %v; want an error containing %q", response, err, testCase.want)
			}
			if len(response.Events) != 0 {
				t.Fatalf("failed poll returned events: %+v", response)
			}
			if recorder.count() != 1 {
				t.Fatalf("poll requests = %d, want 1", recorder.count())
			}
		})
	}
}

func TestHubProviderAckRepositoryEventsSurfacesHubFailures(t *testing.T) {
	valid := repositoryevent.AckRequest{
		Version:  repositoryevent.ContractVersion,
		Cursor:   "after",
		EventIDs: []string{"delivery-1:default"},
	}

	t.Run("invalid request", func(t *testing.T) {
		provider, recorder := dqCovStartHub(t, "token", func(http.ResponseWriter, *http.Request) {
			t.Error("an invalid acknowledgement must not reach the hub")
		})
		invalid := valid
		invalid.Version = repositoryevent.ContractVersion + 1
		response, err := provider.AckRepositoryEvents(context.Background(), invalid)
		if err == nil || !strings.Contains(err.Error(), "acknowledgement version must be 1") {
			t.Fatalf("AckRepositoryEvents = %+v, %v; want a version error", response, err)
		}
		if response.Version != 0 || response.Cursor != "" {
			t.Fatalf("rejected acknowledgement returned a response: %+v", response)
		}
		if recorder.count() != 0 {
			t.Fatalf("hub received %d requests for an invalid acknowledgement", recorder.count())
		}
	})

	for name, testCase := range map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"http failure": {
			dqCovStatusHandler(http.StatusInternalServerError, `{}`),
			"acknowledge repository events",
		},
		"cursor mismatch": {
			dqCovStatusHandler(http.StatusOK, `{"version":1,"cursor":"other"}`),
			"different cursor",
		},
		"version mismatch": {
			dqCovStatusHandler(http.StatusOK, `{"version":2,"cursor":"after"}`),
			"different cursor",
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider, recorder := dqCovStartHub(t, "token", testCase.handler)
			response, err := provider.AckRepositoryEvents(context.Background(), valid)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("AckRepositoryEvents = %+v, %v; want an error containing %q", response, err, testCase.want)
			}
			if recorder.count() != 1 {
				t.Fatalf("acknowledgement requests = %d, want 1", recorder.count())
			}
		})
	}
}

func TestHubProviderReadsTheCredentialFileForEveryRequest(t *testing.T) {
	recorder := &dqCovHubRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.capture(request)
		_, _ = io.WriteString(writer, `{"snapshots":[]}`)
	}))
	t.Cleanup(server.Close)

	tokenPath := dqCovTokenFile(t, "file-token\n")
	provider, err := New(Options{BaseURL: server.URL, Machine: dqCovMachine, TokenFile: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A rotated credential file must be picked up without rebuilding the provider.
	if err := os.WriteFile(tokenPath, []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Status(context.Background()); err != nil {
		t.Fatal(err)
	}

	requests := recorder.requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].auth != "Bearer file-token" || requests[1].auth != "Bearer rotated" {
		t.Fatalf("authorization headers = %q, %q; want the token file re-read per request", requests[0].auth, requests[1].auth)
	}
	for index, request := range requests {
		if request.method != http.MethodGet || request.uri != machinesnapshot.SnapshotPath {
			t.Fatalf("request %d = %s %s, want GET %s", index, request.method, request.uri, machinesnapshot.SnapshotPath)
		}
	}
}

func TestHubProviderDoJSONRejectsUnencodableInput(t *testing.T) {
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	var output any
	if err := provider.doJSON(context.Background(), http.MethodPost, machinesnapshot.SnapshotPath, make(chan int), &output); err == nil || !strings.Contains(err.Error(), "encode request") {
		t.Fatalf("doJSON(unencodable) = %v, want an encode request error", err)
	}
}

func TestHubProviderDoJSONReportsCredentialFailureBeforeRequesting(t *testing.T) {
	provider, err := New(Options{
		BaseURL: "https://hub.example", Machine: dqCovMachine,
		TokenFile: filepath.Join(t.TempDir(), "absent.token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output any
	if err := provider.doJSON(context.Background(), http.MethodGet, machinesnapshot.SnapshotPath, nil, &output); err == nil || !strings.Contains(err.Error(), "hub credential file does not exist") {
		t.Fatalf("doJSON(absent credential) = %v, want a credential error", err)
	}
}

func TestHubProviderDoJSONRejectsAnInvalidRequestMethod(t *testing.T) {
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	var output any
	if err := provider.doJSON(context.Background(), "BAD METHOD", machinesnapshot.SnapshotPath, nil, &output); err == nil || !strings.Contains(err.Error(), "invalid method") {
		t.Fatalf("doJSON(invalid method) = %v, want a request construction error", err)
	}
}

// dqCovFailingBody fails reads, closes, or neither, so doJSON's response-body
// error handling can be driven independently of a real socket.
type dqCovFailingBody struct {
	readErr  error
	closeErr error
}

func (body *dqCovFailingBody) Read([]byte) (int, error) {
	if body.readErr != nil {
		return 0, body.readErr
	}
	return 0, io.EOF
}

func (body *dqCovFailingBody) Close() error { return body.closeErr }

func TestHubProviderDoJSONReportsBodyReadAndCloseFailures(t *testing.T) {
	readErr := errors.New("socket read failed")
	closeErr := errors.New("socket close failed")
	for name, testCase := range map[string]struct {
		body *dqCovFailingBody
		want string
		err  error
	}{
		"read":  {&dqCovFailingBody{readErr: readErr}, "read successful hub response", readErr},
		"close": {&dqCovFailingBody{closeErr: closeErr}, "close successful hub response", closeErr},
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: testCase.body, Header: make(http.Header)}, nil
			})}
			provider, err := New(Options{BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "token", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			var output any
			err = provider.doJSON(context.Background(), http.MethodGet, machinesnapshot.SnapshotPath, nil, &output)
			if err == nil || !strings.Contains(err.Error(), testCase.want) || !errors.Is(err, testCase.err) {
				t.Fatalf("doJSON = %v, want an error containing %q wrapping %v", err, testCase.want, testCase.err)
			}
		})
	}
}

func TestHubProviderDoJSONReportsDecodeFailureAndTrailingData(t *testing.T) {
	for name, testCase := range map[string]struct {
		body string
		want string
	}{
		"decode":   {`not json`, "decode successful hub response"},
		"trailing": {`{"snapshots":[]} }`, "trailing data"},
	} {
		t.Run(name, func(t *testing.T) {
			provider, recorder := dqCovStartHub(t, "token", dqCovStatusHandler(http.StatusOK, testCase.body))
			var output machinesnapshot.ListResponse
			err := provider.doJSON(context.Background(), http.MethodGet, machinesnapshot.SnapshotPath, nil, &output)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("doJSON = %v, want an error containing %q", err, testCase.want)
			}
			if recorder.count() != 1 {
				t.Fatalf("requests = %d, want 1", recorder.count())
			}
		})
	}
}

// TestHubProviderDoJSONGivesUpOnPersistentTransportFailure proves a transport
// error is retried for every configured delay and then reported as a failed
// hub request, not retried forever.
func TestHubProviderDoJSONGivesUpOnPersistentTransportFailure(t *testing.T) {
	transportErr := errors.New("dial tcp: connection refused")
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, transportErr
	})}
	provider, err := New(Options{
		BaseURL: "https://hub.example", Machine: dqCovMachine, Token: "token", Client: client,
		RetryDelays: []time.Duration{0, 0},
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var output any
	err = provider.doJSON(context.Background(), http.MethodGet, machinesnapshot.SnapshotPath, nil, &output)
	if err == nil || !strings.Contains(err.Error(), "hub request failed") || !errors.Is(err, transportErr) {
		t.Fatalf("doJSON = %v, want a wrapped transport failure", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want the initial attempt plus both retries", attempts)
	}
}

func TestHubProviderDoJSONStopsRetryingWhenSleepFails(t *testing.T) {
	sleepErr := errors.New("retry wait was cancelled")
	provider, recorder := dqCovStartHubWith(t, Options{
		Token:       "token",
		RetryDelays: []time.Duration{time.Millisecond},
		Sleep:       func(context.Context, time.Duration) error { return sleepErr },
	}, dqCovStatusHandler(http.StatusServiceUnavailable, `{}`))

	var output any
	err := provider.doJSON(context.Background(), http.MethodGet, machinesnapshot.SnapshotPath, nil, &output)
	if !errors.Is(err, sleepErr) {
		t.Fatalf("doJSON = %v, want the sleep error %v", err, sleepErr)
	}
	if recorder.count() != 1 {
		t.Fatalf("attempts = %d, want 1 before the failed retry wait", recorder.count())
	}
}

func TestHubProviderCredentialResolvesATokenFile(t *testing.T) {
	provider, err := New(Options{
		BaseURL: "https://hub.example", Machine: dqCovMachine,
		TokenFile: dqCovTokenFile(t, "  rotated-token\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := provider.credential()
	if err != nil || token != "rotated-token" {
		t.Fatalf("credential() = %q, %v; want rotated-token", token, err)
	}
}

func TestHubProviderCredentialRejectsUnusableTokenFiles(t *testing.T) {
	dir := t.TempDir()
	oversized := filepath.Join(dir, "oversized.token")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("a"), maxTokenBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, testCase := range map[string]struct {
		tokenFile string
		want      string
	}{
		"absent":            {filepath.Join(dir, "absent.token"), "hub credential file does not exist"},
		"unopenable path":   {filepath.Join(dir, "tok\x00en"), "open hub credential file"},
		"oversized":         {oversized, "hub credential file is too large"},
		"two tokens":        {dqCovTokenFile(t, "first second\n"), "must contain one non-empty token"},
		"blank contents":    {dqCovTokenFile(t, "   \n"), "must contain one non-empty token"},
		"directory instead": {dir, "hub credential file"},
	} {
		t.Run(name, func(t *testing.T) {
			provider, err := New(Options{BaseURL: "https://hub.example", Machine: dqCovMachine, TokenFile: testCase.tokenFile})
			if err != nil {
				t.Fatal(err)
			}
			token, err := provider.credential()
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("credential() = %q, %v; want an error containing %q", token, err, testCase.want)
			}
			if token != "" {
				t.Fatalf("credential() returned %q alongside an error", token)
			}
		})
	}
}

// TestHubProviderCredentialRejectsAnUnreadableTokenFile covers the
// permission-denied branch, which only exists on platforms and users that
// enforce unix read permission bits.
func TestHubProviderCredentialRejectsAnUnreadableTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not enforce unix read permission bits, so the unreadable-file branch is unreachable there")
	}
	path := dqCovTokenFile(t, "token\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	// os.Chmod is a no-op for a privileged user, which makes the branch
	// unreachable in that environment rather than broken.
	if file, openErr := os.Open(path); openErr == nil {
		_ = file.Close()
		t.Skip("the current user can still read a mode-000 file (elevated privileges), so the unreadable-file branch is unreachable")
	}
	provider, err := New(Options{BaseURL: "https://hub.example", Machine: dqCovMachine, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	token, err := provider.credential()
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("credential() = %q, %v; want an unreadable-file error", token, err)
	}
	if token != "" {
		t.Fatalf("credential() returned %q alongside an error", token)
	}
}

func TestSleepContextWaitsForTheDelayAndHonoursCancellation(t *testing.T) {
	start := time.Now()
	if err := sleepContext(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("sleepContext(delay) = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("sleepContext returned after %v, want it to wait for the delay", elapsed)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepContext(cancelled) = %v, want context.Canceled", err)
	}

	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	if err := sleepContext(expired, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sleepContext(expired) = %v, want context.DeadlineExceeded", err)
	}
}
