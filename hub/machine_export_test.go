package hub

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// exportCredentials resolves real bearer tokens: a token's digest names its
// binding, as the credential store does.
type exportCredentials struct {
	pepper   []byte
	bindings map[MachineTokenDigest]MachineCredentialBinding
}

func (store *exportCredentials) ResolveMachineCredential(_ context.Context, digest MachineTokenDigest) (MachineCredentialBinding, error) {
	binding, found := store.bindings[digest]
	if !found {
		return MachineCredentialBinding{}, errors.New("credential not found")
	}
	return binding, nil
}

// issue stores a credential of identity for machine name with scopes and
// returns its token.
func (store *exportCredentials) issue(t *testing.T, token, identity, name string, scopes []MachineScope) string {
	t.Helper()
	digest, err := DigestMachineToken(token, store.pepper)
	if err != nil {
		t.Fatal(err)
	}
	store.bindings[digest] = MachineCredentialBinding{
		MachineID: MachineID(identity, name), MachineName: name, IdentityID: identity,
		IssuedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), Scopes: scopes,
	}
	return token
}

const (
	hostOwner       = "local"
	exportedFull    = `{"schema_version":1,"machine":"vm","fleet":{}}`
	exportedMetrics = `{"schema_version":1,"machine":"vm"}`
)

// exportHub is a daemon-hosted hub's handler with real bearer resolution, the
// owner's machine credential, one of another identity, a peer credential and a
// blocked peer's, and an exporter that counts its calls.
type exportHub struct {
	handler                               http.Handler
	owner, stranger, peer, blocked, wrong string
	exported                              []bool
	unavailable                           bool
}

func newExportHub(t *testing.T) *exportHub {
	t.Helper()
	store := &exportCredentials{pepper: bytes.Repeat([]byte("p"), minimumPepperBytes), bindings: map[MachineTokenDigest]MachineCredentialBinding{}}
	fixture := &exportHub{}
	fixture.owner = store.issue(t, "owner-token", hostOwner, "laptop", cloneEnrollmentScopes())
	fixture.stranger = store.issue(t, "stranger-token", "someone-else", "laptop", cloneEnrollmentScopes())
	fixture.peer = store.issue(t, "peer-token", hostOwner, "peer-node", clonePeerScopes())
	fixture.blocked = store.issue(t, "blocked-token", hostOwner, "blocked-node", cloneEnrollmentScopes())
	fixture.wrong = "no-such-token"
	trust := fakePeerTrustResolver{records: map[string]PeerRecord{
		MachineID(hostOwner, "peer-node"):    {MachineID: MachineID(hostOwner, "peer-node"), Trust: PeerTrustActive},
		MachineID(hostOwner, "blocked-node"): {MachineID: MachineID(hostOwner, "blocked-node"), Trust: PeerTrustBlocked},
	}}
	fixture.handler = NewHandler(HandlerOptions{
		// A viewer that is always authenticated proves the route never consults one.
		ViewerResolver: coverageViewerResolver{viewer: Viewer{Authenticated: true, IdentityID: hostOwner}},
		MachineBearer:  NewPeerAwareBearerResolver(NewMachineBearerResolver(store, store.pepper), trust),
		AllowedOrigin:  "http://127.0.0.1:8766",
		MachineExport: &MachineExport{OwnerIdentityID: hostOwner, Export: func(metricsOnly bool) ([]byte, bool) {
			fixture.exported = append(fixture.exported, metricsOnly)
			if fixture.unavailable {
				return nil, false
			}
			if metricsOnly {
				return []byte(exportedMetrics), true
			}
			return []byte(exportedFull), true
		}},
	})
	return fixture
}

func (fixture *exportHub) get(t *testing.T, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return coverageRequest(t, fixture.handler, http.MethodGet, target, "", headers)
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// TestMachineExportRouteServesOnlyTheHostOwnersMachineCredential proves the
// credential half of cockpit-views#ac:hub-export-route-requires-a-machine-bearer:
// only a machine credential with machine_snapshot:read of the host owner's
// identity receives the envelope, and the metrics-only form is asked for by its
// query alone.
func TestMachineExportRouteServesOnlyTheHostOwnersMachineCredential(t *testing.T) {
	fixture := newExportHub(t)
	full := fixture.get(t, MachineExportPath, bearer(fixture.owner))
	if full.Code != http.StatusOK || full.Body.String() != exportedFull {
		t.Fatalf("the owner's export = %d %s", full.Code, full.Body.String())
	}
	if full.Header().Get("Content-Type") != "application/json" || full.Header().Get("Cache-Control") != "no-store" || full.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", full.Header())
	}
	metrics := fixture.get(t, MachineExportPath+"?metrics_only=1", bearer(fixture.owner))
	if metrics.Code != http.StatusOK || metrics.Body.String() != exportedMetrics || strings.Contains(metrics.Body.String(), "fleet") {
		t.Fatalf("the owner's metrics-only export = %d %s", metrics.Code, metrics.Body.String())
	}
	if len(fixture.exported) != 2 || fixture.exported[0] || !fixture.exported[1] {
		t.Fatalf("exports = %v, want a full one and a metrics-only one", fixture.exported)
	}
	// A parameter the route does not know chooses nothing: there is no way to ask
	// for another machine.
	other := fixture.get(t, MachineExportPath+"?machine=desktop", bearer(fixture.owner))
	if other.Code != http.StatusOK || other.Body.String() != exportedFull {
		t.Errorf("a request naming another machine = %d %s, want this machine's export", other.Code, other.Body.String())
	}
	for _, query := range []string{"?metrics_only=0", "?metrics_only=true", "?metrics_only=1&metrics_only=1", "?metrics_only="} {
		if refused := fixture.get(t, MachineExportPath+query, bearer(fixture.owner)); refused.Code != http.StatusBadRequest || coverageErrorCode(t, refused) != "invalid_metrics_only" {
			t.Errorf("%s = %d %s, want 400", query, refused.Code, refused.Body.String())
		}
	}
}

// TestMachineExportRouteRefusesEveryOtherCaller proves the refusals of the same
// AC: another identity gets 403; no bearer, an unknown one, a peer credential
// (peer:session alone), a blocked machine, a session cookie and a second
// Authorization header get 401; none of them reaches the exporter or receives a
// byte of the envelope.
func TestMachineExportRouteRefusesEveryOtherCaller(t *testing.T) {
	fixture := newExportHub(t)
	for name, test := range map[string]struct {
		headers map[string]string
		status  int
		code    string
	}{
		"another identity":     {bearer(fixture.stranger), http.StatusForbidden, "not_the_host_owner"},
		"no credential":        {nil, http.StatusUnauthorized, "machine_bearer_unavailable"},
		"an unknown bearer":    {bearer(fixture.wrong), http.StatusUnauthorized, "machine_bearer_unavailable"},
		"a peer credential":    {bearer(fixture.peer), http.StatusUnauthorized, "machine_bearer_unavailable"},
		"a blocked machine":    {bearer(fixture.blocked), http.StatusUnauthorized, "machine_bearer_unavailable"},
		"a session cookie":     {map[string]string{"Cookie": "wb_cockpit_session=owner; __Secure-wb_github_oauth=owner"}, http.StatusUnauthorized, "machine_bearer_unavailable"},
		"a basic credential":   {map[string]string{"Authorization": "Basic " + fixture.owner}, http.StatusUnauthorized, "machine_bearer_unavailable"},
		"the token in a query": {nil, http.StatusUnauthorized, "machine_bearer_unavailable"},
	} {
		for _, target := range []string{MachineExportPath, MachineExportPath + "?metrics_only=1"} {
			if name == "the token in a query" {
				target = MachineExportPath + "?access_token=" + fixture.owner
			}
			response := fixture.get(t, target, test.headers)
			if response.Code != test.status || coverageErrorCode(t, response) != test.code {
				t.Errorf("%s: %s = %d %s, want %d %s", name, target, response.Code, response.Body.String(), test.status, test.code)
			}
			if strings.Contains(response.Body.String(), "schema_version") || response.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s: the refusal carries the envelope or may be stored: %s %v", name, response.Body.String(), response.Header())
			}
		}
	}
	if len(fixture.exported) != 0 {
		t.Fatalf("a refused request reached the exporter %d times", len(fixture.exported))
	}
	// Only GET exists: nothing is written through this route.
	if post := coverageRequest(t, fixture.handler, http.MethodPost, MachineExportPath, "{}", bearer(fixture.owner)); post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", post.Code)
	}
}

// TestMachineExportRouteAnswers503WhenTheEnvelopeCannotBeBuilt is a daemon whose
// state is not ready: the owner is told so, with no body of an envelope.
func TestMachineExportRouteAnswers503WhenTheEnvelopeCannotBeBuilt(t *testing.T) {
	fixture := newExportHub(t)
	fixture.unavailable = true
	response := fixture.get(t, MachineExportPath, bearer(fixture.owner))
	if response.Code != http.StatusServiceUnavailable || coverageErrorCode(t, response) != "machine_export_unavailable" {
		t.Fatalf("an unavailable export = %d %s", response.Code, response.Body.String())
	}
}

// TestMachineExportRouteDoesNotExistOnTheHostedService proves the rest of the
// AC: a handler built as the hosted multi-identity service builds it (no
// MachineExport), or with an incomplete one, serves no such route even to a
// valid machine credential; the path is outside the Cockpit API; and the scope
// it needs is one of the existing enrollment scopes, so no scope was added.
func TestMachineExportRouteDoesNotExistOnTheHostedService(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: hostOwner, Scopes: cloneEnrollmentScopes()}
	for name, export := range map[string]*MachineExport{
		"the hosted service": nil,
		"no owner identity":  {Export: func(bool) ([]byte, bool) { return []byte(exportedFull), true }},
		"a blank owner":      {OwnerIdentityID: " ", Export: func(bool) ([]byte, bool) { return []byte(exportedFull), true }},
		"no exporter":        {OwnerIdentityID: hostOwner},
	} {
		handler := NewHandler(HandlerOptions{MachineBearer: coverageMachineResolver{machine: machine}, MachineExport: export})
		response := coverageRequest(t, handler, http.MethodGet, MachineExportPath, "", bearer("any"))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "schema_version") {
			t.Errorf("%s: the route = %d %s, want 404", name, response.Code, response.Body.String())
		}
	}
	if MachineExportPath != "/v0/workbench/machines/export" || strings.HasPrefix(MachineExportPath, "/api/v1/cockpit/") {
		t.Errorf("the route is %s", MachineExportPath)
	}
	want := []MachineScope{ScopeSnapshotPublish, ScopeSnapshotRead, ScopeEventsPoll, ScopeEventsAck}
	if !scopeSetsEqual(enrollmentScopes, want) || !scopeSetsEqual(peerScopes, []MachineScope{ScopePeerSession}) {
		t.Errorf("the scope sets changed: %v and %v", enrollmentScopes, peerScopes)
	}
}
