package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	operatorCoverageBody = `{"repository":"sneat-dev/wb","statements":10,"covered":9}`
	operatorMetricBody   = `{"repository":"sneat-dev/wb","metric_type":"commits_per_day","value":3}`
	operatorOwnerHeader  = "X-Test-Owner-Session"
)

// operatorBearer resolves the machine a test names in its Authorization
// header, the way a daemon-hosted hub resolves a real token to its binding.
type operatorBearer map[string]Machine

func (bearers operatorBearer) ResolveMachineBearer(request *http.Request) (Machine, error) {
	machine, known := bearers[request.Header.Get("Authorization")]
	if !known {
		return Machine{}, errors.New("unknown bearer")
	}
	return machine, nil
}

// daemonHostedHub is a hub wired the way the daemon wires one: a fixed viewer
// that answers for every caller, a machine bearer resolver, and the operator
// write gate. owner is the session seam; nil leaves it unconfigured.
func daemonHostedHub(owner func(*http.Request) bool) (http.Handler, *firestoreMemoryBackend, *memoryStates) {
	backend := newFirestoreMemoryBackend()
	coverage := NewRepositoryCoverageStore(backend)
	installations := coverageValidInstallationService(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	handler := NewHandler(HandlerOptions{
		ViewerResolver: coverageViewerResolver{viewer: Viewer{Authenticated: true, IdentityID: "local"}},
		MachineBearer: operatorBearer{
			"Bearer owner-machine": {ID: "m-1", Name: "laptop", IdentityID: "local", Scopes: []MachineScope{ScopeSnapshotRead}},
			"Bearer other-machine": {ID: "m-2", Name: "guest", IdentityID: "someone-else", Scopes: []MachineScope{ScopeSnapshotRead}},
			"Bearer peer-machine":  {ID: "m-3", Name: "peer", IdentityID: "local", Scopes: clonePeerScopes()},
		},
		Coverage:       coverage,
		Metrics:        NewRepositoryMetricsStore(backend, coverage),
		Installations:  installations,
		OperatorWrites: &OperatorWrites{OwnerIdentityID: "local", Owner: owner},
	})
	return handler, backend, installations.States.(*memoryStates)
}

func ownerSessionHeader(request *http.Request) bool {
	return request.Header.Get(operatorOwnerHeader) == "live"
}

// storedWrites counts what the write routes have put in the store.
func storedWrites(t *testing.T, handler http.Handler, states *memoryStates) int {
	t.Helper()
	count := len(states.records)
	for _, path := range []string{CoveragePath, MetricsPath + "?type=commits_per_day"} {
		response := coverageRequest(t, handler, http.MethodGet, path, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
		}
		count += strings.Count(response.Body.String(), `"repository"`)
	}
	return count
}

var operatorWriteRoutes = []struct {
	name, path, body, refusal string
	headers                   map[string]string
}{
	{"coverage", CoveragePath, operatorCoverageBody, "coverage_unauthorized", nil},
	{"metrics", MetricsPath, operatorMetricBody, "metrics_unauthorized", nil},
	{"installation connect", InstallationConnectPath, `{}`, "viewer_unauthorized", nil},
	{"installation authorize", InstallationAuthorizePath, `{"state":"s","challenge":"c"}`, "viewer_unauthorized", map[string]string{"Origin": installationOpenerOrigin}},
}

// TestDaemonHostedHubRefusesAWriteWithoutACredential proves
// self-hosted-bench#ac:hub-writes-need-a-credential's refusals: on a
// daemon-hosted hub, whose viewer answers for every caller, a request with no
// credential, with a machine bearer of another identity, with a peer
// credential, or with an unknown bearer is refused with 401 on every write
// route, and nothing reaches the store. Reads stay open to the local reader.
func TestDaemonHostedHubRefusesAWriteWithoutACredential(t *testing.T) {
	t.Parallel()
	handler, _, states := daemonHostedHub(ownerSessionHeader)
	callers := map[string]map[string]string{
		"anonymous":            {},
		"another identity":     {"Authorization": "Bearer other-machine"},
		"a peer credential":    {"Authorization": "Bearer peer-machine"},
		"an unknown bearer":    {"Authorization": "Bearer nobody"},
		"a dead owner session": {operatorOwnerHeader: "expired"},
	}
	for caller, credentials := range callers {
		for _, route := range operatorWriteRoutes {
			headers := map[string]string{}
			for name, value := range route.headers {
				headers[name] = value
			}
			for name, value := range credentials {
				headers[name] = value
			}
			response := coverageRequest(t, handler, http.MethodPost, route.path, route.body, headers)
			if response.Code != http.StatusUnauthorized || coverageErrorCode(t, response) != route.refusal {
				t.Errorf("%s as %s = %d %s, want 401 %s", route.name, caller, response.Code, response.Body.String(), route.refusal)
			}
		}
	}
	if written := storedWrites(t, handler, states); written != 0 {
		t.Fatalf("refused writes left %d records in the store", written)
	}
}

// TestDaemonHostedHubFailsClosedWithNoOwnerCheck proves the gate does not open
// when the daemon configured no session seam: only a machine bearer writes.
func TestDaemonHostedHubFailsClosedWithNoOwnerCheck(t *testing.T) {
	t.Parallel()
	handler, _, states := daemonHostedHub(nil)
	for _, route := range operatorWriteRoutes {
		headers := map[string]string{operatorOwnerHeader: "live"}
		for name, value := range route.headers {
			headers[name] = value
		}
		if response := coverageRequest(t, handler, http.MethodPost, route.path, route.body, headers); response.Code != http.StatusUnauthorized {
			t.Errorf("%s with no owner check = %d %s, want 401", route.name, response.Code, response.Body.String())
		}
	}
	if written := storedWrites(t, handler, states); written != 0 {
		t.Fatalf("refused writes left %d records in the store", written)
	}
	// An owner identity that was never named matches no machine either.
	unnamed := NewHandler(HandlerOptions{
		MachineBearer:  operatorBearer{"Bearer blank": {ID: "m", Name: "n", IdentityID: "local"}},
		Coverage:       NewRepositoryCoverageStore(newFirestoreMemoryBackend()),
		OperatorWrites: &OperatorWrites{},
	})
	if response := coverageRequest(t, unnamed, http.MethodPost, CoveragePath, operatorCoverageBody, map[string]string{"Authorization": "Bearer blank"}); response.Code != http.StatusUnauthorized {
		t.Fatalf("write with no owner identity configured = %d, want 401", response.Code)
	}
}

// TestDaemonHostedHubAcceptsTheOwnersCredential proves the other half: the
// owner's machine bearer and the owner's session each write, and the write
// lands.
func TestDaemonHostedHubAcceptsTheOwnersCredential(t *testing.T) {
	t.Parallel()
	for name, credential := range map[string]map[string]string{
		"machine bearer": {"Authorization": "Bearer owner-machine"},
		"owner session":  {operatorOwnerHeader: "live"},
	} {
		handler, _, states := daemonHostedHub(ownerSessionHeader)
		if response := coverageRequest(t, handler, http.MethodPost, CoveragePath, operatorCoverageBody, credential); response.Code != http.StatusCreated {
			t.Fatalf("coverage by %s = %d %s", name, response.Code, response.Body.String())
		}
		if response := coverageRequest(t, handler, http.MethodPost, MetricsPath, operatorMetricBody, credential); response.Code != http.StatusCreated {
			t.Fatalf("metric by %s = %d %s", name, response.Code, response.Body.String())
		}
		if response := coverageRequest(t, handler, http.MethodPost, InstallationConnectPath, `{}`, credential); response.Code != http.StatusOK {
			t.Fatalf("connect by %s = %d %s", name, response.Code, response.Body.String())
		}
		// Past the gate the authorize route applies its own rules: an unknown
		// state is a 400, which only a request that was let through can reach.
		headers := map[string]string{"Origin": installationOpenerOrigin}
		for key, value := range credential {
			headers[key] = value
		}
		if response := coverageRequest(t, handler, http.MethodPost, InstallationAuthorizePath, `{"state":"s","challenge":"c"}`, headers); response.Code != http.StatusBadRequest {
			t.Fatalf("authorize by %s = %d %s", name, response.Code, response.Body.String())
		}
		if written := storedWrites(t, handler, states); written != 3 {
			t.Fatalf("%s wrote %d records, want coverage, metric and connection state", name, written)
		}
	}
}

// TestHostedHubWritesAreDecidedByItsOwnViewerAndBearer proves the hosted
// deployment, which sets no operator gate, is unchanged: its viewer alone
// begins a connection and writes coverage and metrics, with no machine bearer
// and no owner session anywhere in the request.
func TestHostedHubWritesAreDecidedByItsOwnViewerAndBearer(t *testing.T) {
	t.Parallel()
	backend := newFirestoreMemoryBackend()
	coverage := NewRepositoryCoverageStore(backend)
	signedIn := func(request *http.Request) (Viewer, error) {
		if request.Header.Get("Authorization") != "Bearer firebase" {
			return Viewer{}, errors.New("signed out")
		}
		return Viewer{Authenticated: true, IdentityID: "uid"}, nil
	}
	handler := NewHandler(HandlerOptions{
		ViewerResolver: testViewerResolver{resolve: signedIn},
		MachineBearer:  operatorBearer{},
		Coverage:       coverage,
		Metrics:        NewRepositoryMetricsStore(backend, coverage),
		Installations:  coverageValidInstallationService(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)),
	})
	viewer := map[string]string{"Authorization": "Bearer firebase"}
	for path, want := range map[string]int{CoveragePath: http.StatusCreated, MetricsPath: http.StatusCreated, InstallationConnectPath: http.StatusOK} {
		body := map[string]string{CoveragePath: operatorCoverageBody, MetricsPath: operatorMetricBody, InstallationConnectPath: `{}`}[path]
		if response := coverageRequest(t, handler, http.MethodPost, path, body, viewer); response.Code != want {
			t.Errorf("hosted POST %s by its viewer = %d %s, want %d", path, response.Code, response.Body.String(), want)
		}
		if response := coverageRequest(t, handler, http.MethodPost, path, body, nil); response.Code != http.StatusUnauthorized {
			t.Errorf("hosted POST %s signed out = %d, want 401", path, response.Code)
		}
	}
}

// pathLeakingBackend fails every write with the kind of text a store engine
// produces: a file path and free text a caller must never be shown.
type pathLeakingBackend struct{ *firestoreMemoryBackend }

const leakedStoreText = "open /Users/alex/.wb/hub/store.db: permission denied"

func (pathLeakingBackend) Set(context.Context, string, string, any) error {
	return errors.New(leakedStoreText)
}

// TestHubWriteErrorsAreClosedCodes proves
// self-hosted-bench#ac:hub-write-errors-are-closed-codes: a record the store
// refuses and a store that cannot write answer with a fixed code, and the body
// carries nothing else, in particular none of the store's own text.
func TestHubWriteErrorsAreClosedCodes(t *testing.T) {
	t.Parallel()
	working := newFirestoreMemoryBackend()
	failing := pathLeakingBackend{newFirestoreMemoryBackend()}
	cases := []struct {
		name, path, body string
		handler          http.Handler
		status           int
		code             string
	}{
		{"coverage with no repository", CoveragePath, `{"statements":1}`, NewHandler(HandlerOptions{Coverage: NewRepositoryCoverageStore(working)}), http.StatusBadRequest, "invalid_coverage_record"},
		{"metric with no repository", MetricsPath, `{"metric_type":"commits_per_day"}`, NewHandler(HandlerOptions{Metrics: NewRepositoryMetricsStore(working, nil)}), http.StatusBadRequest, "invalid_metric_record"},
		{"metric with no type", MetricsPath, `{"repository":"sneat-dev/wb"}`, NewHandler(HandlerOptions{Metrics: NewRepositoryMetricsStore(working, nil)}), http.StatusBadRequest, "invalid_metric_record"},
		{"coverage the store cannot write", CoveragePath, operatorCoverageBody, NewHandler(HandlerOptions{Coverage: NewRepositoryCoverageStore(failing)}), http.StatusServiceUnavailable, "coverage_failed"},
		{"metric the store cannot write", MetricsPath, operatorMetricBody, NewHandler(HandlerOptions{Metrics: NewRepositoryMetricsStore(failing, nil)}), http.StatusServiceUnavailable, "metrics_failed"},
	}
	for _, test := range cases {
		response := coverageRequest(t, test.handler, http.MethodPost, test.path, test.body, nil)
		want := `{"error":"` + test.code + `"}`
		if got := strings.TrimSpace(response.Body.String()); response.Code != test.status || got != want {
			t.Errorf("%s = %d %s, want %d %s", test.name, response.Code, got, test.status, want)
		}
	}
}

// TestHubOwnerSeamIsAPlainRequestPredicate keeps the seam what the daemon
// fills it with: any func(*http.Request) bool, asked once per write and only
// when no machine bearer already answered.
func TestHubOwnerSeamIsAPlainRequestPredicate(t *testing.T) {
	t.Parallel()
	asked := 0
	handler, _, _ := daemonHostedHub(func(*http.Request) bool { asked++; return true })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, CoveragePath, strings.NewReader(operatorCoverageBody))
	request.Header.Set("Authorization", "Bearer owner-machine")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || asked != 0 {
		t.Fatalf("bearer write = %d, owner check asked %d times", recorder.Code, asked)
	}
	if response := coverageRequest(t, handler, http.MethodPost, CoveragePath, operatorCoverageBody, nil); response.Code != http.StatusCreated || asked != 1 {
		t.Fatalf("session write = %d, owner check asked %d times", response.Code, asked)
	}
}
