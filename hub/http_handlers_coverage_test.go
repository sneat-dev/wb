package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

type coverageViewerResolver struct {
	viewer Viewer
	err    error
}

func (r coverageViewerResolver) Viewer(*http.Request) (Viewer, error) { return r.viewer, r.err }

type coverageMachineResolver struct {
	machine Machine
	err     error
}

func (r coverageMachineResolver) ResolveMachineBearer(*http.Request) (Machine, error) {
	return r.machine, r.err
}

type coverageProjection struct{ err error }

func (p coverageProjection) ProcessProjection(context.Context, WebhookDelivery, string) error {
	return p.err
}

type coverageEventStore struct {
	pollResponse repositoryevent.PollResponse
	pollErr      error
	ackResponse  repositoryevent.AckResponse
	ackErr       error
	enqueueErr   error
}

func (s *coverageEventStore) EnqueueForMachines(_ context.Context, _ repositoryevent.Event, machines []Machine) (EnqueueResult, error) {
	if s.enqueueErr != nil {
		return EnqueueResult{}, s.enqueueErr
	}
	return EnqueueResult{Enqueued: len(machines)}, nil
}
func (s *coverageEventStore) Poll(context.Context, Machine, string, int) (repositoryevent.PollResponse, error) {
	return s.pollResponse, s.pollErr
}
func (s *coverageEventStore) Acknowledge(context.Context, Machine, repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
	return s.ackResponse, s.ackErr
}

type coverageFailingSnapshotStore struct{ err error }

func (s coverageFailingSnapshotStore) StoreLatest(context.Context, StoredMachineSnapshot) (MachineSnapshotStoreResult, error) {
	return MachineSnapshotStoreResult{}, s.err
}
func (s coverageFailingSnapshotStore) ListLatest(context.Context) ([]StoredMachineSnapshot, error) {
	return nil, s.err
}

func coverageValidInstallationService(now time.Time) *InstallationConnectionService {
	installation := VerifiedInstallation{ID: 7, Account: "acme", AccountType: "Organization", RepositorySelection: "selected", Repositories: []VerifiedRepository{}, State: "installed"}
	states := &memoryStates{}
	return &InstallationConnectionService{
		States:            states,
		AppVerifier:       fakeAppVerifier{installation: installation},
		OAuthVerifier:     fakeIdentityVerifier{identity: VerifiedGitHubIdentity{UserID: 42, Login: "octocat", Installations: []VerifiedInstallation{installation}}},
		Bindings:          &memoryBindings{known: map[int64]bool{7: true}, states: states},
		InstallURL:        "https://github.com/apps/workbench/installations/new",
		OAuthAuthorizeURL: "https://github.com/login/oauth/authorize",
		OAuthClientID:     "client",
		OAuthCallbackURL:  "https://wb-github-app.sneat.dev/v0/workbench/github/installations/callback",
		SuccessURL:        "https://sneat.work/bench/dashboard/github/",
		StateSecret:       []byte(strings.Repeat("s", 32)),
		Random:            installationFlowRandom(),
		Now:               func() time.Time { return now },
	}
}

func coverageRequest(t *testing.T, handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func coverageErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var value map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return value["error"]
}

func TestCoverageHTTPHappyPathsAndCORS(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	viewer := Viewer{Authenticated: true, IdentityID: "uid", DisplayName: "Alex"}
	machine := validMachine()
	credentialStore := &credentialMemory{}
	enrollment := &MachineEnrollmentService{Store: credentialStore, Pepper: bytes.Repeat([]byte("p"), minimumPepperBytes), Random: bytes.NewReader(bytes.Repeat([]byte("r"), machineTokenBytes)), Now: func() time.Time { return now }}
	snapshotStore := &snapshotStoreMemory{}
	snapshots := &MachineSnapshotService{Store: snapshotStore, Now: func() time.Time { return now.Add(time.Minute) }}
	eventStore := &coverageEventStore{
		pollResponse: repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "", NextCursor: "next", Events: []repositoryevent.Event{}},
		ackResponse:  repositoryevent.AckResponse{Version: repositoryevent.ContractVersion, Cursor: "next"},
	}
	lifecycle := &lifecycleMemory{}
	repositoryEvents := &RepositoryEventService{Snapshots: &snapshotStoreMemory{}, Entitlements: entitlementMemory{}, Lifecycle: lifecycle, Store: eventStore}
	bindings := &memoryBindings{}
	status := &StatusService{Bindings: bindings, Snapshots: snapshotStore, Events: statusMemory{}, Now: func() time.Time { return now }}
	installations := coverageValidInstallationService(now)
	options := HandlerOptions{
		ViewerResolver: coverageViewerResolver{viewer: viewer}, MachineBearer: coverageMachineResolver{machine: machine},
		Enrollment: enrollment, Snapshots: snapshots, Installations: installations, RepositoryEvents: repositoryEvents,
		Status: status, WebhookSecret: bytes.Repeat([]byte("w"), MinimumWebhookSecretBytes), AllowedOrigin: "https://sneat.work",
	}
	handler := NewHandler(options)

	response := coverageRequest(t, handler, http.MethodPost, MachineEnrollmentPath, `{"name":"laptop"}`, map[string]string{"Origin": "https://sneat.work"})
	if response.Code != http.StatusOK || response.Header().Get("Access-Control-Allow-Origin") != "https://sneat.work" || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("enroll status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	snapshot := validSnapshot(now)
	payload, _ := json.Marshal(snapshot)
	response = coverageRequest(t, handler, http.MethodPost, machinesnapshot.SnapshotPath, string(payload), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", response.Code, response.Body.String())
	}
	response = coverageRequest(t, handler, http.MethodGet, machinesnapshot.SnapshotPath, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	response = coverageRequest(t, handler, http.MethodGet, StatusPath, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status status=%d body=%s", response.Code, response.Body.String())
	}
	response = coverageRequest(t, handler, http.MethodPost, InstallationConnectPath, `{}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", response.Code, response.Body.String())
	}
	var connect InstallationConnectResponse
	_ = json.Unmarshal(response.Body.Bytes(), &connect)
	if len(response.Result().Cookies()) != 0 {
		t.Fatalf("cross-site connect response set cookies=%+v", response.Result().Cookies())
	}
	continueURL := httptest.NewRequest(http.MethodGet, connect.ConnectURL, nil).URL
	response = coverageRequest(t, handler, http.MethodGet, continueURL.RequestURI(), "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `const opener=window.opener;if(!opener)return`) || !strings.Contains(response.Body.String(), `event.origin!=="`+installationOpenerOrigin+`"||event.source!==opener`) || !strings.Contains(response.Body.String(), `postMessage`) {
		t.Fatalf("top-level continuation status=%d body=%s", response.Code, response.Body.String())
	}
	var openerChallenge *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == openerChallengeCookie {
			openerChallenge = cookie
		}
	}
	if openerChallenge == nil || !openerChallenge.HttpOnly || !openerChallenge.Secure || openerChallenge.SameSite != http.SameSiteLaxMode || !strings.Contains(response.Body.String(), openerChallenge.Value) {
		t.Fatalf("opener challenge cookie=%+v", openerChallenge)
	}
	forwarded := coverageRequest(t, handler, http.MethodGet, continueURL.RequestURI(), "", nil)
	if forwarded.Code != http.StatusOK || strings.Contains(forwarded.Body.String(), "postMessage") || !strings.Contains(forwarded.Body.String(), "start the GitHub connection again") {
		t.Fatalf("forwarded continuation status=%d body=%s", forwarded.Code, forwarded.Body.String())
	}
	authorizationBody, _ := json.Marshal(InstallationOpenerAuthorizationRequest{State: continueURL.Query().Get("state"), Challenge: openerChallenge.Value})
	response = coverageRequest(t, handler, http.MethodPost, InstallationAuthorizePath, string(authorizationBody), map[string]string{"Origin": installationOpenerOrigin})
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != installationOpenerOrigin {
		t.Fatalf("opener authorization status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if replay := coverageRequest(t, handler, http.MethodPost, InstallationAuthorizePath, string(authorizationBody), map[string]string{"Origin": installationOpenerOrigin}); replay.Code != http.StatusBadRequest {
		t.Fatalf("opener authorization replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	openerCookieHeader := openerChallenge.Name + "=" + openerChallenge.Value
	response = coverageRequest(t, handler, http.MethodGet, continueURL.RequestURI(), "", map[string]string{"Cookie": openerCookieHeader})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("authorized continuation status=%d body=%s", response.Code, response.Body.String())
	}
	if replay := coverageRequest(t, handler, http.MethodGet, continueURL.RequestURI(), "", map[string]string{"Cookie": openerCookieHeader}); replay.Code != http.StatusBadRequest {
		t.Fatalf("completed continuation replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var continuation *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == installationContinuationCookie {
			continuation = cookie
		}
	}
	if continuation == nil || !continuation.HttpOnly || !continuation.Secure || continuation.SameSite != http.SameSiteLaxMode || continuation.Path != installationContinuationPath {
		t.Fatalf("continuation cookie=%+v", continuation)
	}
	cookieHeader := continuation.Name + "=" + continuation.Value
	setupURL := InstallationSetupPath + "?installation_id=7&state=" + connectStateFromURL(t, response.Header().Get("Location"))
	response = coverageRequest(t, handler, http.MethodGet, setupURL, "", map[string]string{"Cookie": cookieHeader})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("setup status=%d body=%s", response.Code, response.Body.String())
	}
	oauthState := connectStateFromURL(t, response.Header().Get("Location"))
	response = coverageRequest(t, handler, http.MethodGet, InstallationCallbackPath+"?state="+oauthState+"&code=code", "", map[string]string{"Cookie": cookieHeader})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("OAuth exchange status=%d body=%s", response.Code, response.Body.String())
	}
	var oauthCredential *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == oauthCredentialCookie {
			oauthCredential = cookie
		}
	}
	if oauthCredential == nil || !oauthCredential.HttpOnly || !oauthCredential.Secure || oauthCredential.SameSite != http.SameSiteLaxMode {
		t.Fatalf("OAuth credential cookie=%+v", oauthCredential)
	}
	cookieHeader += "; " + oauthCredential.Name + "=" + oauthCredential.Value
	response = coverageRequest(t, handler, http.MethodGet, httptest.NewRequest(http.MethodGet, response.Header().Get("Location"), nil).URL.RequestURI(), "", map[string]string{"Cookie": cookieHeader})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("OAuth completion status=%d body=%s", response.Code, response.Body.String())
	}
	cleared := response.Result().Cookies()
	if len(cleared) != 2 || cleared[0].Name != installationContinuationCookie || cleared[0].MaxAge >= 0 || cleared[1].Name != oauthCredentialCookie || cleared[1].MaxAge >= 0 {
		t.Fatalf("cleared continuation cookies=%+v", cleared)
	}

	response = coverageRequest(t, handler, http.MethodGet, repositoryevent.EventsPath+"?wait_seconds=0", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", response.Code, response.Body.String())
	}
	ack := `{"version":1,"cursor":"next","event_ids":["event-1"]}`
	response = coverageRequest(t, handler, http.MethodPost, repositoryevent.AckPath, ack, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", response.Code, response.Body.String())
	}

	webhookPayload := []byte(`{"ref":"refs/heads/feature","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":7,"full_name":"Acme/App","default_branch":"main"},"installation":{"id":123}}`)
	mac := hmac.New(sha256.New, options.WebhookSecret)
	_, _ = mac.Write(webhookPayload)
	response = coverageRequest(t, handler, http.MethodPost, WebhookPath, string(webhookPayload), map[string]string{
		"X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil)), "X-GitHub-Delivery": "delivery", "X-GitHub-Event": "push",
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("webhook status=%d body=%s", response.Code, response.Body.String())
	}
	for _, lifecycleWebhook := range []struct {
		event, delivery string
		payload         []byte
		action          InstallationLifecycleAction
	}{
		{"github_app_authorization", "authorization-revoked", []byte(`{"action":"revoked","sender":{"id":42}}`), GitHubUserAuthorizationRevoked},
		{"member", "collaborator-removed", []byte(`{"action":"removed","installation":{"id":7},"repository":{"id":99},"member":{"id":42}}`), RepositoryUserAccessRemoved},
	} {
		mac = hmac.New(sha256.New, options.WebhookSecret)
		_, _ = mac.Write(lifecycleWebhook.payload)
		response = coverageRequest(t, handler, http.MethodPost, WebhookPath, string(lifecycleWebhook.payload), map[string]string{
			"X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil)), "X-GitHub-Delivery": lifecycleWebhook.delivery, "X-GitHub-Event": lifecycleWebhook.event,
		})
		last := lifecycle.events[len(lifecycle.events)-1]
		if response.Code != http.StatusAccepted || last.Action != lifecycleWebhook.action || last.GitHubUserID != 42 {
			t.Fatalf("%s webhook status=%d events=%+v body=%s", lifecycleWebhook.event, response.Code, lifecycle.events, response.Body.String())
		}
	}

	response = coverageRequest(t, handler, http.MethodOptions, StatusPath, "", map[string]string{"Origin": "https://sneat.work"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("cors preflight status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodOptions, InstallationAuthorizePath, "", map[string]string{"Origin": "https://evil.example"})
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign preflight status=%d", response.Code)
	}
}

func connectStateFromURL(t *testing.T, value string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, value, nil)
	state := request.URL.Query().Get("state")
	if state == "" {
		t.Fatalf("missing state in %q", value)
	}
	return state
}

func TestCoverageHTTPAuthenticationAndUnavailableBranches(t *testing.T) {
	paths := []struct{ method, path, body, code string }{
		{http.MethodGet, StatusPath, "", "viewer_unauthorized"},
		{http.MethodPost, MachineEnrollmentPath, `{}`, "viewer_unauthorized"},
		{http.MethodPost, machinesnapshot.SnapshotPath, `{}`, "machine_bearer_unavailable"},
		{http.MethodGet, machinesnapshot.SnapshotPath, "", "machine_bearer_unavailable"},
		{http.MethodPost, InstallationConnectPath, `{}`, "viewer_unauthorized"},
		{http.MethodPost, InstallationAuthorizePath, `{}`, "viewer_unauthorized"},
		{http.MethodGet, repositoryevent.EventsPath, "", "machine_bearer_unavailable"},
		{http.MethodPost, repositoryevent.AckPath, `{}`, "machine_bearer_unavailable"},
	}
	for _, test := range paths {
		response := coverageRequest(t, NewHandler(HandlerOptions{}), test.method, test.path, test.body, nil)
		if response.Code != http.StatusUnauthorized || coverageErrorCode(t, response) != test.code {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}

	viewer := coverageViewerResolver{viewer: Viewer{Authenticated: true, IdentityID: "uid"}}
	machine := coverageMachineResolver{machine: validMachine()}
	nilServices := NewHandler(HandlerOptions{ViewerResolver: viewer, MachineBearer: machine})
	for _, test := range []struct{ method, path, body, code string }{
		{http.MethodGet, StatusPath, "", "github_status_unavailable"},
		{http.MethodPost, MachineEnrollmentPath, `{}`, "machine_enrollment_unavailable"},
		{http.MethodPost, machinesnapshot.SnapshotPath, `{}`, "machine_snapshot_unavailable"},
		{http.MethodGet, machinesnapshot.SnapshotPath, "", "machine_snapshot_unavailable"},
		{http.MethodPost, InstallationConnectPath, `{}`, "installation_connection_unavailable"},
		{http.MethodGet, InstallationContinuePath, "", "installation_connection_unavailable"},
		{http.MethodPost, InstallationAuthorizePath, `{}`, "installation_connection_unavailable"},
		{http.MethodGet, InstallationSetupPath + "?installation_id=7", "", "installation_connection_unavailable"},
		{http.MethodGet, InstallationCallbackPath, "", "installation_connection_unavailable"},
		{http.MethodGet, repositoryevent.EventsPath, "", "repository_event_poll_failed"},
		{http.MethodPost, repositoryevent.AckPath, `{}`, "repository_event_ack_failed"},
		{http.MethodPost, WebhookPath, `{}`, "webhook_unavailable"},
	} {
		response := coverageRequest(t, nilServices, test.method, test.path, test.body, nil)
		if response.Code != http.StatusServiceUnavailable || coverageErrorCode(t, response) != test.code {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}

	badViewer := NewHandler(HandlerOptions{ViewerResolver: coverageViewerResolver{err: errors.New("auth")}})
	if got := coverageRequest(t, badViewer, http.MethodGet, StatusPath, "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("viewer resolver error status=%d", got)
	}
	badMachine := NewHandler(HandlerOptions{MachineBearer: coverageMachineResolver{err: errors.New("auth")}})
	if got := coverageRequest(t, badMachine, http.MethodGet, machinesnapshot.SnapshotPath, "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("machine resolver error status=%d", got)
	}
	callbackHandler := NewHandler(HandlerOptions{Installations: coverageValidInstallationService(time.Now().UTC())})
	for _, target := range []string{InstallationContinuePath, InstallationSetupPath + "?installation_id=7", InstallationCallbackPath + "?code=code"} {
		if got := coverageRequest(t, callbackHandler, http.MethodGet, target, "", nil).Code; got != http.StatusBadRequest {
			t.Fatalf("callback without continuation %s status=%d", target, got)
		}
	}
}

func TestCoverageHTTPValidationAndServiceFailures(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	viewer := coverageViewerResolver{viewer: Viewer{Authenticated: true, IdentityID: "uid"}}
	machine := coverageMachineResolver{machine: validMachine()}

	enrollmentUnavailable := &MachineEnrollmentService{}
	handler := NewHandler(HandlerOptions{ViewerResolver: viewer, MachineBearer: machine, Enrollment: enrollmentUnavailable})
	for _, body := range []string{`{"unknown":true}`, `{} {}`} {
		response := coverageRequest(t, handler, http.MethodPost, MachineEnrollmentPath, body, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("bad enrollment JSON status=%d body=%s", response.Code, response.Body.String())
		}
	}
	response := coverageRequest(t, handler, http.MethodPost, MachineEnrollmentPath, `{"name":"laptop"}`, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable enrollment status=%d body=%s", response.Code, response.Body.String())
	}
	validEnrollment := &MachineEnrollmentService{Store: &credentialMemory{}, Pepper: bytes.Repeat([]byte("p"), minimumPepperBytes), Random: bytes.NewReader(bytes.Repeat([]byte("r"), machineTokenBytes))}
	handler = NewHandler(HandlerOptions{ViewerResolver: viewer, Enrollment: validEnrollment})
	response = coverageRequest(t, handler, http.MethodPost, MachineEnrollmentPath, `{"name":"bad name"}`, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid enrollment service input status=%d", response.Code)
	}

	badSnapshotService := &MachineSnapshotService{Store: coverageFailingSnapshotStore{err: ErrUnavailable}}
	handler = NewHandler(HandlerOptions{MachineBearer: machine, Snapshots: badSnapshotService})
	response = coverageRequest(t, handler, http.MethodPost, machinesnapshot.SnapshotPath, `{"unknown":true}`, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid snapshot status=%d", response.Code)
	}
	validPayload, _ := json.Marshal(validSnapshot(now))
	response = coverageRequest(t, handler, http.MethodPost, machinesnapshot.SnapshotPath, string(validPayload), nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("snapshot store failure status=%d", response.Code)
	}
	mismatched := validSnapshot(now)
	mismatched.Machine = "other"
	mismatchedPayload, _ := json.Marshal(mismatched)
	response = coverageRequest(t, handler, http.MethodPost, machinesnapshot.SnapshotPath, string(mismatchedPayload), nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("snapshot validation failure status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodGet, machinesnapshot.SnapshotPath, "", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("snapshot list failure status=%d", response.Code)
	}

	installations := coverageValidInstallationService(now)
	unavailableCallbacks := NewHandler(HandlerOptions{ViewerResolver: viewer, Installations: &InstallationConnectionService{}})
	for _, target := range []string{InstallationContinuePath, InstallationSetupPath + "?installation_id=7", InstallationCallbackPath + "?code=code"} {
		response = coverageRequest(t, unavailableCallbacks, http.MethodGet, target, "", nil)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("unavailable callback %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	handler = NewHandler(HandlerOptions{ViewerResolver: viewer, Installations: installations})
	response = coverageRequest(t, handler, http.MethodPost, InstallationConnectPath, `{"unknown":1}`, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad connect JSON status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodPost, InstallationConnectPath, `{"installation_id":99}`, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forbidden reconnect status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodPost, InstallationConnectPath, `{"installation_id":-1}`, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid connect service input status=%d", response.Code)
	}
	if response = coverageRequest(t, handler, http.MethodPost, InstallationAuthorizePath, `{"unknown":true}`, map[string]string{"Origin": installationOpenerOrigin}); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid opener authorization JSON status=%d", response.Code)
	}
	connectResponse := coverageRequest(t, handler, http.MethodPost, InstallationConnectPath, `{}`, nil)
	var connect InstallationConnectResponse
	_ = json.Unmarshal(connectResponse.Body.Bytes(), &connect)
	state := connectStateFromURL(t, connect.ConnectURL)
	page := coverageRequest(t, handler, http.MethodGet, httptest.NewRequest(http.MethodGet, connect.ConnectURL, nil).URL.RequestURI(), "", nil)
	var opener *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == openerChallengeCookie {
			opener = cookie
		}
	}
	if opener == nil {
		t.Fatal("missing opener challenge fixture")
	}
	authorizeBody, _ := json.Marshal(InstallationOpenerAuthorizationRequest{State: state, Challenge: opener.Value})
	if response = coverageRequest(t, handler, http.MethodPost, InstallationAuthorizePath, string(authorizeBody), map[string]string{"Origin": "https://evil.example"}); response.Code != http.StatusForbidden {
		t.Fatalf("wrong opener origin status=%d body=%s", response.Code, response.Body.String())
	}
	wrongViewerHandler := NewHandler(HandlerOptions{ViewerResolver: coverageViewerResolver{viewer: Viewer{Authenticated: true, IdentityID: "other"}}, Installations: installations})
	if response = coverageRequest(t, wrongViewerHandler, http.MethodPost, InstallationAuthorizePath, string(authorizeBody), map[string]string{"Origin": installationOpenerOrigin}); response.Code != http.StatusForbidden {
		t.Fatalf("wrong opener identity status=%d body=%s", response.Code, response.Body.String())
	}
	unavailableAuthorization := NewHandler(HandlerOptions{ViewerResolver: viewer, Installations: &InstallationConnectionService{}})
	if response = coverageRequest(t, unavailableAuthorization, http.MethodPost, InstallationAuthorizePath, string(authorizeBody), map[string]string{"Origin": installationOpenerOrigin}); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable opener authorization status=%d body=%s", response.Code, response.Body.String())
	}
	response = coverageRequest(t, handler, http.MethodGet, InstallationSetupPath+"?installation_id=bad", "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad setup id status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodGet, InstallationSetupPath+"?installation_id=7&state=bad", "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad setup state status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodGet, InstallationCallbackPath+"?state=bad&code=bad", "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad callback status=%d", response.Code)
	}

	eventStore := &coverageEventStore{pollErr: errors.New("poll"), ackErr: errors.New("ack")}
	events := &RepositoryEventService{Store: eventStore}
	handler = NewHandler(HandlerOptions{MachineBearer: machine, RepositoryEvents: events})
	for _, target := range []string{repositoryevent.EventsPath + "?limit=0", repositoryevent.EventsPath + "?wait_seconds=31"} {
		response = coverageRequest(t, handler, http.MethodGet, target, "", nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid poll %s status=%d", target, response.Code)
		}
	}
	response = coverageRequest(t, handler, http.MethodGet, repositoryevent.EventsPath+"?wait_seconds=0", "", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("poll failure status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodPost, repositoryevent.AckPath, `{}`, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid ack status=%d", response.Code)
	}
	response = coverageRequest(t, handler, http.MethodPost, repositoryevent.AckPath, `{"unknown":true}`, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed ack status=%d", response.Code)
	}
	validAck := `{"version":1,"cursor":"next","event_ids":["event-1"]}`
	response = coverageRequest(t, handler, http.MethodPost, repositoryevent.AckPath, validAck, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("ack failure status=%d", response.Code)
	}

	status := &StatusService{Bindings: &memoryBindings{}, Snapshots: coverageFailingSnapshotStore{err: errors.New("status")}, Events: statusMemory{}}
	handler = NewHandler(HandlerOptions{ViewerResolver: viewer, Status: status})
	if got := coverageRequest(t, handler, http.MethodGet, StatusPath, "", nil).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("status service failure=%d", got)
	}
}

func TestCoverageHTTPWebhookFailuresAndHelpers(t *testing.T) {
	secret := bytes.Repeat([]byte("w"), MinimumWebhookSecretBytes)
	store := &coverageEventStore{}
	events := &RepositoryEventService{Snapshots: snapshotMemory{}, Entitlements: entitlementMemory{}, Store: store}
	handler := NewHandler(HandlerOptions{RepositoryEvents: events, WebhookSecret: secret})
	weakHandler := NewHandler(HandlerOptions{RepositoryEvents: events, WebhookSecret: []byte("short")})
	if response := coverageRequest(t, weakHandler, http.MethodPost, WebhookPath, `{}`, nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("weak webhook secret status=%d", response.Code)
	}
	payload := `{"repository":{"full_name":"Acme/App"}}`
	for _, signature := range []string{"", "sha1=bad", "sha256=zz", "sha256=00"} {
		response := coverageRequest(t, handler, http.MethodPost, WebhookPath, payload, map[string]string{"X-Hub-Signature-256": signature})
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("signature %q status=%d", signature, response.Code)
		}
	}
	tooLarge := strings.Repeat("x", maxWebhookBodyBytes+1)
	response := coverageRequest(t, handler, http.MethodPost, WebhookPath, tooLarge, map[string]string{"X-Hub-Signature-256": "sha256=00"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("large webhook status=%d", response.Code)
	}

	signed := func(body string) string {
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	projectionFailure := NewHandler(HandlerOptions{RepositoryEvents: events, WebhookSecret: secret, Projection: coverageProjection{err: errors.New("projection")}})
	response = coverageRequest(t, projectionFailure, http.MethodPost, WebhookPath, payload, map[string]string{"X-Hub-Signature-256": signed(payload), "X-GitHub-Delivery": "delivery", "X-GitHub-Event": "unknown"})
	if response.Code != http.StatusServiceUnavailable || coverageErrorCode(t, response) != "webhook_projection_failed" {
		t.Fatalf("projection failure status=%d body=%s", response.Code, response.Body.String())
	}
	store.enqueueErr = errors.New("enqueue")
	enqueueFailure := NewHandler(HandlerOptions{RepositoryEvents: events, WebhookSecret: secret})
	response = coverageRequest(t, enqueueFailure, http.MethodPost, WebhookPath, payload, map[string]string{"X-Hub-Signature-256": signed(payload), "X-GitHub-Delivery": "delivery", "X-GitHub-Event": "push"})
	if response.Code != http.StatusServiceUnavailable || coverageErrorCode(t, response) != "webhook_event_enqueue_failed" {
		t.Fatalf("enqueue failure status=%d body=%s", response.Code, response.Body.String())
	}

	for _, test := range []struct {
		raw                string
		fallback, min, max int
		want               int
		wantErr            bool
	}{
		{"", 5, 1, 10, 5, false}, {"7", 5, 1, 10, 7, false}, {"bad", 5, 1, 10, 0, true}, {"0", 5, 1, 10, 0, true}, {"11", 5, 1, 10, 0, true},
	} {
		got, err := boundedInt(test.raw, test.fallback, test.min, test.max)
		if got != test.want || (err != nil) != test.wantErr {
			t.Fatalf("boundedInt(%q)=%d,%v", test.raw, got, err)
		}
	}
}
