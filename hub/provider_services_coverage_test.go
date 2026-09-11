package hub

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

type coverageStateStore struct {
	issueErr      error
	readErr       error
	transitionErr error
	replaceErr    error
	record        InstallationState
}

func (s *coverageStateStore) IssueInstallationState(_ context.Context, _ InstallationStateDigest, state InstallationState) error {
	if s.issueErr == nil {
		s.record = state
	}
	return s.issueErr
}
func (s *coverageStateStore) ReadInstallationState(context.Context, InstallationStateDigest, time.Time) (InstallationState, error) {
	return s.record, s.readErr
}
func (s *coverageStateStore) TransitionInstallationState(context.Context, InstallationStateDigest, InstallationState, time.Time, InstallationStateDigest, InstallationState) error {
	return s.transitionErr
}
func (s *coverageStateStore) ReplaceInstallationState(_ context.Context, _ InstallationStateDigest, _ InstallationState, _ time.Time, next InstallationState) error {
	if s.replaceErr == nil {
		s.record = next
	}
	return s.replaceErr
}

type coverageAppVerifier struct {
	installation VerifiedInstallation
	err          error
}

func (v coverageAppVerifier) VerifyAppInstallation(context.Context, int64) (VerifiedInstallation, error) {
	return v.installation, v.err
}

type coverageIdentityVerifier struct {
	identity    VerifiedGitHubIdentity
	err         error
	exchangeErr error
	token       string
}

func (v coverageIdentityVerifier) ExchangeGitHubOAuthCode(context.Context, string) (string, error) {
	return v.token, v.exchangeErr
}
func (v coverageIdentityVerifier) VerifyGitHubIdentity(context.Context, string) (VerifiedGitHubIdentity, error) {
	return v.identity, v.err
}

type coverageBindingStore struct {
	known       bool
	knownErr    error
	completeErr error
	listErr     error
	bindings    []IdentityInstallationBinding
	completed   IdentityInstallationBinding
}

func (s *coverageBindingStore) IdentityHasInstallation(context.Context, string, int64) (bool, error) {
	return s.known, s.knownErr
}
func (s *coverageBindingStore) CompleteIdentityInstallationBinding(_ context.Context, _ InstallationStateDigest, _ InstallationState, _ time.Time, binding IdentityInstallationBinding) error {
	s.completed = binding
	return s.completeErr
}
func (s *coverageBindingStore) ListIdentityInstallationBindings(context.Context, string) ([]IdentityInstallationBinding, error) {
	return s.bindings, s.listErr
}

type coverageEntitlement struct {
	allowed bool
	err     error
}

func (e coverageEntitlement) IdentityHasRepositoryEntitlement(context.Context, string, int64, int64) (bool, error) {
	return e.allowed, e.err
}

type coverageLifecycle struct{ err error }

func (l coverageLifecycle) ApplyInstallationLifecycle(context.Context, InstallationLifecycleEvent) error {
	return l.err
}

type coverageStatusStore struct {
	delivery *StatusDelivery
	pending  []PendingRefresh
	errors   []StatusError
	err      error
}

type coverageBadCredentialStore struct{}

func (coverageBadCredentialStore) RotateMachineCredential(context.Context, MachineCredentialBinding, MachineTokenDigest) (MachineCredentialBinding, error) {
	return MachineCredentialBinding{}, nil
}

type coverageInvalidSnapshotStore struct{}

func (coverageInvalidSnapshotStore) StoreLatest(context.Context, StoredMachineSnapshot) (MachineSnapshotStoreResult, error) {
	return MachineSnapshotStoreResult{}, nil
}
func (coverageInvalidSnapshotStore) ListLatest(context.Context) ([]StoredMachineSnapshot, error) {
	return nil, nil
}

func (s coverageStatusStore) IdentityRepositoryEventStatus(context.Context, string) (*StatusDelivery, []PendingRefresh, []StatusError, error) {
	return s.delivery, s.pending, s.errors, s.err
}

func coverageInstallation(now time.Time) VerifiedInstallation {
	return VerifiedInstallation{ID: 7, Account: "acme", AccountType: "Organization", RepositorySelection: "selected", Repositories: []VerifiedRepository{{ID: 1, Repository: "github.com/acme/app"}}, State: "installed", ManageURL: "https://github.com/settings/installations/7"}
}

func coverageConfiguredConnection(now time.Time) InstallationConnectionService {
	installation := coverageInstallation(now)
	return InstallationConnectionService{
		States:            &coverageStateStore{},
		AppVerifier:       coverageAppVerifier{installation: installation},
		OAuthVerifier:     coverageIdentityVerifier{token: "access-token", identity: VerifiedGitHubIdentity{UserID: 42, Login: "octocat", Installations: []VerifiedInstallation{installation}}},
		Bindings:          &coverageBindingStore{known: true},
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

func TestCoverageVerifiedGitHubIdentityValidation(t *testing.T) {
	valid := VerifiedGitHubIdentity{UserID: 1, Login: "user", Installations: []VerifiedInstallation{coverageInstallation(time.Time{})}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalidIdentities := []VerifiedGitHubIdentity{
		{},
		{UserID: 1, Login: strings.Repeat("x", 129)},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "bot", RepositorySelection: "selected", State: "installed"}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "User", RepositorySelection: "bad", State: "installed"}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "User", RepositorySelection: "selected", State: "bad"}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "User", RepositorySelection: "selected", State: "installed", ManageURL: "http://bad"}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "User", RepositorySelection: "selected", State: "installed", Repositories: []VerifiedRepository{{ID: 2, Repository: "github.com/acme/z"}, {ID: 1, Repository: "github.com/acme/a"}}}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "User", RepositorySelection: "selected", State: "installed", Repositories: []VerifiedRepository{{ID: 0, Repository: "github.com/acme/a"}}}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "User", RepositorySelection: "selected", State: "installed", Repositories: []VerifiedRepository{{ID: 1, Repository: "bad"}}}}},
		{UserID: 1, Login: "user", Installations: []VerifiedInstallation{{ID: 1, Account: "a", AccountType: "User", RepositorySelection: "selected", State: "installed"}, {ID: 1, Account: "b", AccountType: "User", RepositorySelection: "selected", State: "installed"}}},
	}
	for index, identity := range invalidIdentities {
		if err := identity.Validate(); err == nil {
			t.Fatalf("invalid identity %d accepted: %+v", index, identity)
		}
	}
}

func TestCoverageInstallationConnectionFailureBranches(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	validViewer := Viewer{Authenticated: true, IdentityID: "uid"}
	service := coverageConfiguredConnection(now)
	if _, err := service.Begin(context.Background(), Viewer{}, InstallationConnectRequest{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthenticated begin=%v", err)
	}
	misconfigured := service
	misconfigured.StateSecret = nil
	if _, err := misconfigured.Begin(context.Background(), validViewer, InstallationConnectRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("misconfigured begin=%v", err)
	}
	if _, err := service.Begin(context.Background(), validViewer, InstallationConnectRequest{InstallationID: -1}); err == nil {
		t.Fatal("negative installation accepted")
	}
	bindingStore := service.Bindings.(*coverageBindingStore)
	bindingStore.known = false
	if _, err := service.Begin(context.Background(), validViewer, InstallationConnectRequest{InstallationID: 7}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown installation=%v", err)
	}
	bindingStore.knownErr = errors.New("lookup")
	if _, err := service.Begin(context.Background(), validViewer, InstallationConnectRequest{InstallationID: 7}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lookup failure=%v", err)
	}
	bindingStore.knownErr, bindingStore.known = nil, true
	service.Random = strings.NewReader(strings.Repeat("q", installationStateBytes*2))
	if response, err := service.Begin(context.Background(), validViewer, InstallationConnectRequest{InstallationID: 7}); err != nil || !strings.Contains(response.ConnectURL, InstallationContinuePath) {
		t.Fatalf("reconnect=%+v err=%v", response, err)
	}

	service.Random = bytes.NewReader(nil)
	if _, err := service.Begin(context.Background(), validViewer, InstallationConnectRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("random failure=%v", err)
	}
	service.Random = strings.NewReader(strings.Repeat("r", installationStateBytes))
	service.States = &coverageStateStore{issueErr: errors.New("write")}
	if _, err := service.Begin(context.Background(), validViewer, InstallationConnectRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("state write failure=%v", err)
	}
	if _, err := misconfigured.Continue(context.Background(), "state", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("misconfigured continuation=%v", err)
	}
	service = coverageConfiguredConnection(now)
	bootstrapState := signInstallationState([]byte("bootstrap"), service.StateSecret)
	bootstrapRecord := InstallationState{Kind: installationStateBootstrap, IdentityID: "uid", InstallationID: 7, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	stateStore := service.States.(*coverageStateStore)
	stateStore.record = bootstrapRecord
	service.Random = bytes.NewReader(nil)
	if _, err := service.Continue(context.Background(), bootstrapState, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("continuation nonce failure=%v", err)
	}
	service = coverageConfiguredConnection(now)
	stateStore = service.States.(*coverageStateStore)
	stateStore.record = bootstrapRecord
	page, err := service.Continue(context.Background(), bootstrapState, "")
	if err != nil || page.openerChallenge == "" || page.RedirectURL != "" {
		t.Fatalf("opener challenge page=%+v err=%v", page, err)
	}
	if err := service.AuthorizeOpener(context.Background(), validViewer, InstallationOpenerAuthorizationRequest{State: bootstrapState, Challenge: page.openerChallenge}); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Continue(context.Background(), bootstrapState, page.openerChallenge); err != nil || !strings.Contains(result.RedirectURL, "client_id=client") {
		t.Fatalf("reconnect continuation=%+v err=%v", result, err)
	}

	continuation := "browser-continuation"
	continuationDigest := browserContinuationDigest(continuation)
	if _, err := misconfigured.CompleteSetup(context.Background(), "state", 7, continuation); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("misconfigured setup=%v", err)
	}
	if _, err := service.CompleteSetup(context.Background(), "state", 0, continuation); err == nil {
		t.Fatal("zero setup installation accepted")
	}
	service = coverageConfiguredConnection(now)
	validState, setupRecord, err := service.issueState(context.Background(), installationStateSetup, "uid", 0, continuationDigest)
	if err != nil {
		t.Fatal(err)
	}
	stateStore = service.States.(*coverageStateStore)
	stateStore.record = setupRecord
	service.AppVerifier = coverageAppVerifier{err: errors.New("verify")}
	if _, err := service.CompleteSetup(context.Background(), validState, 7, continuation); err == nil {
		t.Fatal("app verification failure accepted")
	}
	service.AppVerifier = coverageAppVerifier{installation: VerifiedInstallation{ID: 8}}
	stateStore.record = setupRecord
	if _, err := service.CompleteSetup(context.Background(), validState, 7, continuation); err == nil {
		t.Fatal("mismatched installation accepted")
	}
	service.AppVerifier = coverageAppVerifier{installation: coverageInstallation(now)}
	stateStore.record = setupRecord
	service.Random = bytes.NewReader(nil)
	if _, err := service.CompleteSetup(context.Background(), validState, 7, continuation); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("OAuth state random failure=%v", err)
	}
	stateStore.transitionErr = errors.New("oauth state")
	service.Random = strings.NewReader(strings.Repeat("o", installationStateBytes))
	if _, err := service.CompleteSetup(context.Background(), validState, 7, continuation); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oauth state failure=%v", err)
	}
	stateStore.transitionErr = ErrInvalidInstallationState
	service.Random = strings.NewReader(strings.Repeat("p", installationStateBytes))
	if _, err := service.CompleteSetup(context.Background(), validState, 7, continuation); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("invalid setup transition=%v", err)
	}

	if _, err := misconfigured.CompleteOAuth(context.Background(), "state", "code", continuation, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("misconfigured oauth=%v", err)
	}
	service = coverageConfiguredConnection(now)
	oauthState, oauthRecord, err := service.issueState(context.Background(), installationStateOAuth, "uid", 7, continuationDigest)
	if err != nil {
		t.Fatal(err)
	}
	stateStore = service.States.(*coverageStateStore)
	for _, code := range []string{"", "bad code", strings.Repeat("x", 2049)} {
		stateStore.record = oauthRecord
		if _, err := service.CompleteOAuth(context.Background(), oauthState, code, continuation, ""); err == nil {
			t.Fatalf("invalid code accepted: %q", code)
		}
	}
	service.OAuthVerifier = coverageIdentityVerifier{exchangeErr: ErrUnavailable}
	stateStore.record = oauthRecord
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "code", continuation, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("transient OAuth exchange=%v", err)
	}
	service.OAuthVerifier = coverageIdentityVerifier{exchangeErr: errors.New("invalid code")}
	stateStore.record = oauthRecord
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "code", continuation, ""); err == nil {
		t.Fatal("invalid OAuth exchange accepted")
	}
	service.OAuthVerifier = coverageIdentityVerifier{token: "bad token"}
	stateStore.record = oauthRecord
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "code", continuation, ""); err == nil {
		t.Fatal("invalid OAuth token accepted")
	}
	service.OAuthVerifier = coverageIdentityVerifier{token: "access-token"}
	service.Random = bytes.NewReader(nil)
	stateStore.record = oauthRecord
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "code", continuation, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("OAuth credential seal failure=%v", err)
	}
	service = coverageConfiguredConnection(now)
	stateStore = service.States.(*coverageStateStore)
	stateStore.record = oauthRecord
	staged, err := service.CompleteOAuth(context.Background(), oauthState, "code", continuation, "stale-credential")
	if err != nil || staged.oauthCredential == "" {
		t.Fatalf("stage OAuth=%+v err=%v", staged, err)
	}
	stateStore.record = oauthRecord
	malformedCredential := oauthCredentialStateDigest(oauthState) + ".bad"
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, malformedCredential); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("invalid staged credential=%v", err)
	}
	invalidExchanged := oauthRecord
	invalidExchanged.Kind = installationStateOAuthExchanged
	invalidExchanged.OAuthAccessTokenCiphertext = malformedCredential
	stateStore.record = invalidExchanged
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, ""); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("invalid stored OAuth credential=%v", err)
	}
	stateStore.record = oauthRecord
	stateStore.replaceErr = errors.New("replace")
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, staged.oauthCredential); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("OAuth stage replacement failure=%v", err)
	}
	stateStore.replaceErr = ErrInvalidInstallationState
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, staged.oauthCredential); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("invalid OAuth stage replacement=%v", err)
	}
	stateStore.replaceErr = nil
	stateStore.record = oauthRecord
	service.OAuthVerifier = coverageIdentityVerifier{token: "access-token", err: errors.New("oauth")}
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, staged.oauthCredential); err == nil {
		t.Fatal("OAuth verifier failure accepted")
	}
	stateStore.record = oauthRecord
	service.OAuthVerifier = coverageIdentityVerifier{token: "access-token", identity: VerifiedGitHubIdentity{UserID: 42, Login: "octocat", Installations: []VerifiedInstallation{}}}
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, staged.oauthCredential); err == nil {
		t.Fatal("missing installation accepted")
	}
	service.OAuthVerifier = coverageIdentityVerifier{token: "access-token", identity: VerifiedGitHubIdentity{UserID: 42, Login: "octocat", Installations: []VerifiedInstallation{coverageInstallation(now)}}}
	stateStore.record = oauthRecord
	bindingStore = service.Bindings.(*coverageBindingStore)
	bindingStore.completeErr = errors.New("replace")
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, staged.oauthCredential); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("binding replacement failure=%v", err)
	}
	bindingStore.completeErr = ErrInvalidInstallationState
	stateStore.record = oauthRecord
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation, staged.oauthCredential); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("invalid OAuth completion=%v", err)
	}

	if service.now().IsZero() {
		t.Fatal("configured now returned zero")
	}
	service.Now = nil
	if service.now().IsZero() {
		t.Fatal("default now returned zero")
	}
}

func TestCoverageInstallationStateAndConfigurationHelpers(t *testing.T) {
	now := time.Now().UTC()
	continuation := "browser-continuation"
	continuationDigest := browserContinuationDigest(continuation)
	service := coverageConfiguredConnection(now)
	validState := signInstallationState([]byte("raw"), service.StateSecret)
	invalidStates := []string{strings.Repeat("x", 257), "missing", ".signature", "raw.", "raw.bad*"}
	for _, state := range invalidStates {
		if verifyInstallationState(state, service.StateSecret) {
			t.Fatalf("invalid state accepted: %q", state)
		}
	}
	if !verifyInstallationState(validState, service.StateSecret) {
		t.Fatal("valid state rejected")
	}
	randomService := coverageConfiguredConnection(now)
	randomService.Random = nil
	if continuation, digest, err := randomService.newBrowserContinuation(); err != nil || continuation == "" || digest == "" {
		t.Fatalf("default browser continuation=%q digest=%q err=%v", continuation, digest, err)
	}
	if _, _, err := randomService.issueState(context.Background(), installationStateSetup, "uid", 0, browserContinuationDigest("browser")); err != nil {
		t.Fatalf("default cryptographic random source failed: %v", err)
	}
	stateStore := service.States.(*coverageStateStore)
	invalidRecords := []InstallationState{
		{Kind: installationStateSetup, BrowserContinuationDigest: continuationDigest, ExpiresAt: now.Add(time.Minute), IssuedAt: now},
		{Kind: installationStateOAuth, IdentityID: "uid", BrowserContinuationDigest: continuationDigest, ExpiresAt: now.Add(time.Minute), IssuedAt: now, InstallationID: 0},
		{Kind: installationStateSetup, IdentityID: "uid", BrowserContinuationDigest: continuationDigest, ExpiresAt: now.Add(-time.Minute), IssuedAt: now.Add(-time.Hour)},
		{Kind: installationStateSetup, IdentityID: "uid", BrowserContinuationDigest: continuationDigest, ExpiresAt: now.Add(time.Minute), IssuedAt: now.Add(time.Minute)},
	}
	for _, record := range invalidRecords {
		stateStore.record = record
		if _, _, err := service.readState(context.Background(), validState, record.Kind, continuation); err == nil {
			t.Fatalf("invalid record accepted: %+v", record)
		}
	}
	stateStore.record = InstallationState{Kind: installationStateSetup, IdentityID: "uid", BrowserContinuationDigest: continuationDigest, ExpiresAt: now.Add(time.Minute), IssuedAt: now, InstallationID: 7}
	if _, _, err := service.readOAuthState(context.Background(), validState, continuation); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("non-OAuth state accepted=%v", err)
	}
	stateStore.readErr = errors.New("consume")
	stateStore.record = InstallationState{Kind: installationStateSetup, IdentityID: "uid", BrowserContinuationDigest: continuationDigest, ExpiresAt: now.Add(time.Minute), IssuedAt: now}
	if _, _, err := service.readState(context.Background(), validState, installationStateSetup, continuation); !errors.Is(err, ErrUnavailable) {
		t.Fatal("consume failure accepted")
	}
	cryptoService := coverageConfiguredConnection(now)
	cryptoService.Random = nil
	sealed, err := cryptoService.sealOAuthAccessToken("token", validState)
	if err != nil {
		t.Fatalf("seal OAuth token=%v", err)
	}
	if token, err := cryptoService.openOAuthAccessToken(sealed, validState); err != nil || token != "token" {
		t.Fatalf("open OAuth token=%q err=%v", token, err)
	}
	if _, err := cryptoService.openOAuthAccessToken("bad*", validState); err == nil {
		t.Fatal("invalid OAuth credential encoding accepted")
	}
	if _, err := cryptoService.openOAuthAccessToken(oauthCredentialStateDigest(validState)+".bad*", validState); err == nil {
		t.Fatal("invalid OAuth credential ciphertext accepted")
	}
	cryptoService.Random = strings.NewReader(strings.Repeat("n", oauthTokenNonceBytes))
	empty, err := cryptoService.sealOAuthAccessToken("", validState)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cryptoService.openOAuthAccessToken(empty, validState); err == nil {
		t.Fatal("empty OAuth credential accepted")
	}

	badConfigurations := []func(*InstallationConnectionService){
		func(s *InstallationConnectionService) { s.States = nil },
		func(s *InstallationConnectionService) { s.AppVerifier = nil },
		func(s *InstallationConnectionService) { s.OAuthVerifier = nil },
		func(s *InstallationConnectionService) { s.Bindings = nil },
		func(s *InstallationConnectionService) { s.StateSecret = []byte("short") },
		func(s *InstallationConnectionService) { s.InstallURL = "http://bad" },
		func(s *InstallationConnectionService) { s.OAuthAuthorizeURL = "bad" },
		func(s *InstallationConnectionService) { s.OAuthCallbackURL = "https://user@example.com/path" },
		func(s *InstallationConnectionService) { s.SuccessURL = "" },
		func(s *InstallationConnectionService) { s.OAuthClientID = "" },
	}
	for index, mutate := range badConfigurations {
		candidate := coverageConfiguredConnection(now)
		mutate(&candidate)
		if candidate.validateConfiguration() == nil {
			t.Fatalf("bad configuration %d accepted", index)
		}
	}
}

func TestCoverageInstallationOpenerChallengeFailures(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	viewer := Viewer{Authenticated: true, IdentityID: "uid"}
	service := coverageConfiguredConnection(now)
	state := signInstallationState([]byte("opener-state"), service.StateSecret)
	base := InstallationState{Kind: installationStateBootstrap, IdentityID: viewer.IdentityID, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute)}
	store := service.States.(*coverageStateStore)

	for _, replaceErr := range []error{errors.New("store"), ErrInvalidInstallationState} {
		store.record = base
		store.replaceErr = replaceErr
		service.Random = strings.NewReader(strings.Repeat("c", openerChallengeBytes))
		_, err := service.Continue(context.Background(), state, "")
		if err == nil || errors.Is(err, ErrInvalidInstallationState) != errors.Is(replaceErr, ErrInvalidInstallationState) {
			t.Fatalf("challenge replacement %v returned %v", replaceErr, err)
		}
	}
	store.replaceErr = nil
	challenge := "opener-challenge"
	challenged := base
	challenged.OpenerChallengeDigest = openerChallengeDigest(challenge)
	challenged.OpenerChallengeExpiresAt = now.Add(time.Minute)
	store.record = challenged
	if page, err := service.Continue(context.Background(), state, challenge); err != nil || page.openerChallenge != challenge || page.RedirectURL != "" {
		t.Fatalf("pending challenge page=%+v err=%v", page, err)
	}
	expired := challenged
	expired.OpenerChallengeExpiresAt = now.Add(-time.Second)
	store.record = expired
	if _, err := service.Continue(context.Background(), state, challenge); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("expired challenge continuation=%v", err)
	}
	futureAuthorization := challenged
	futureAuthorization.OpenerAuthorizedAt = now.Add(time.Second)
	store.record = futureAuthorization
	if _, err := service.Continue(context.Background(), state, challenge); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("future challenge authorization=%v", err)
	}
	authorized := challenged
	authorized.OpenerAuthorizedAt = now
	store.record = authorized
	service.Random = bytes.NewReader(nil)
	if _, err := service.Continue(context.Background(), state, challenge); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("browser continuation randomness=%v", err)
	}
	service.Random = strings.NewReader(strings.Repeat("b", installationStateBytes))
	if _, err := service.Continue(context.Background(), state, challenge); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("post-authorization state randomness=%v", err)
	}
	for _, transitionErr := range []error{errors.New("store"), ErrInvalidInstallationState} {
		store.record = authorized
		store.transitionErr = transitionErr
		service.Random = strings.NewReader(strings.Repeat("b", installationStateBytes) + strings.Repeat("s", installationStateBytes))
		_, err := service.Continue(context.Background(), state, challenge)
		if err == nil || errors.Is(err, ErrInvalidInstallationState) != errors.Is(transitionErr, ErrInvalidInstallationState) {
			t.Fatalf("authorized transition %v returned %v", transitionErr, err)
		}
	}
	store.transitionErr = nil

	if err := service.AuthorizeOpener(context.Background(), Viewer{}, InstallationOpenerAuthorizationRequest{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthenticated opener=%v", err)
	}
	misconfigured := service
	misconfigured.StateSecret = nil
	if err := misconfigured.AuthorizeOpener(context.Background(), viewer, InstallationOpenerAuthorizationRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("misconfigured opener authorization=%v", err)
	}
	for _, invalidChallenge := range []string{"", strings.Repeat("x", 257)} {
		if err := service.AuthorizeOpener(context.Background(), viewer, InstallationOpenerAuthorizationRequest{State: state, Challenge: invalidChallenge}); !errors.Is(err, ErrInvalidInstallationState) {
			t.Fatalf("invalid opener challenge=%v", err)
		}
	}
	if err := service.AuthorizeOpener(context.Background(), viewer, InstallationOpenerAuthorizationRequest{State: "wrong", Challenge: challenge}); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("mismatched opener state=%v", err)
	}
	store.record = challenged
	if err := service.AuthorizeOpener(context.Background(), Viewer{Authenticated: true, IdentityID: "other"}, InstallationOpenerAuthorizationRequest{State: state, Challenge: challenge}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("different opener identity=%v", err)
	}
	store.record = challenged
	if err := service.AuthorizeOpener(context.Background(), viewer, InstallationOpenerAuthorizationRequest{State: state, Challenge: "wrong"}); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("mismatched opener challenge=%v", err)
	}
	store.record = expired
	if err := service.AuthorizeOpener(context.Background(), viewer, InstallationOpenerAuthorizationRequest{State: state, Challenge: challenge}); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("expired opener authorization=%v", err)
	}
	for _, replaceErr := range []error{errors.New("store"), ErrInvalidInstallationState} {
		store.record = challenged
		store.replaceErr = replaceErr
		err := service.AuthorizeOpener(context.Background(), viewer, InstallationOpenerAuthorizationRequest{State: state, Challenge: challenge})
		if err == nil || errors.Is(err, ErrInvalidInstallationState) != errors.Is(replaceErr, ErrInvalidInstallationState) {
			t.Fatalf("opener authorization replacement %v returned %v", replaceErr, err)
		}
	}
	service.Random = nil
	if generated, digest, err := service.newOpenerChallenge(); err != nil || generated == "" || digest == "" {
		t.Fatalf("default opener challenge=%q digest=%q err=%v", generated, digest, err)
	}
}

func TestCoverageMachineServiceFailuresAndSorting(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	viewer := Viewer{Authenticated: true, IdentityID: "uid"}
	service := MachineEnrollmentService{Store: &credentialMemory{}, Pepper: bytes.Repeat([]byte("p"), minimumPepperBytes), Now: func() time.Time { return now }}
	if _, err := service.Enroll(context.Background(), viewer, MachineEnrollmentRequest{Name: "laptop"}); err != nil {
		t.Fatal(err)
	}
	badStore := coverageBadCredentialStore{}
	service.Random = bytes.NewReader(bytes.Repeat([]byte("r"), machineTokenBytes))
	service.Store = badStore
	if _, err := service.Enroll(context.Background(), viewer, MachineEnrollmentRequest{Name: "laptop"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid stored enrollment=%v", err)
	}

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Add("Authorization", "Bearer token")
	request.Header.Add("Authorization", "Bearer other")
	if _, err := NewMachineBearerResolver(&credentialMemory{}, bytes.Repeat([]byte("p"), minimumPepperBytes)).ResolveMachineBearer(request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("multiple authorizations=%v", err)
	}
	request = httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	if _, err := NewMachineBearerResolver(nil, bytes.Repeat([]byte("p"), minimumPepperBytes)).ResolveMachineBearer(request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("nil credential store=%v", err)
	}
	if _, err := NewMachineBearerResolver(&credentialMemory{}, []byte("short")).ResolveMachineBearer(request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("short bearer pepper=%v", err)
	}
	pepper := bytes.Repeat([]byte("p"), minimumPepperBytes)
	digest, err := DigestMachineToken("token", pepper)
	if err != nil {
		t.Fatal(err)
	}
	invalidCredential := &credentialMemory{digest: digest, binding: MachineCredentialBinding{}}
	if _, err := NewMachineBearerResolver(invalidCredential, pepper).ResolveMachineBearer(request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid stored bearer=%v", err)
	}

	snapshotService := MachineSnapshotService{Store: &snapshotStoreMemory{}, Now: func() time.Time { return now }}
	for _, machine := range []Machine{{}, {ID: "id", Name: "m", IdentityID: "uid", Scopes: []MachineScope{ScopeSnapshotRead}}} {
		if _, err := snapshotService.Publish(context.Background(), machine, validSnapshot(now)); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("invalid publish machine=%+v err=%v", machine, err)
		}
	}
	snapshot := validSnapshot(now)
	snapshot.Machine = "other"
	if _, err := snapshotService.Publish(context.Background(), validMachine(), snapshot); err == nil {
		t.Fatal("mismatched snapshot machine accepted")
	}
	snapshot = validSnapshot(now)
	snapshot.Login = "bad login"
	if _, err := snapshotService.Publish(context.Background(), validMachine(), snapshot); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	snapshot = validSnapshot(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := snapshotService.Publish(context.Background(), validMachine(), snapshot); err == nil {
		t.Fatal("JSON-unencodable timestamp accepted")
	}
	snapshotService.Store = coverageInvalidSnapshotStore{}
	if _, err := snapshotService.Publish(context.Background(), validMachine(), validSnapshot(now)); err == nil {
		t.Fatal("invalid store response accepted")
	}

	if _, err := snapshotService.List(context.Background(), Machine{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid list machine=%v", err)
	}
	listStore := &snapshotStoreMemory{records: []StoredMachineSnapshot{{IdentityID: "firebase-uid", MachineID: "bad"}}}
	snapshotService.Store = listStore
	if _, err := snapshotService.List(context.Background(), validMachine()); err == nil {
		t.Fatal("invalid stored snapshot accepted")
	}
	validA := validSnapshot(now)
	validA.Machine = "alpha"
	validB := validSnapshot(now)
	validB.Machine = "beta"
	listStore.records = []StoredMachineSnapshot{
		{IdentityID: "firebase-uid", MachineID: "b", Snapshot: validB, ReceivedAt: now, Digest: "b"},
		{IdentityID: "firebase-uid", MachineID: "a", Snapshot: validA, ReceivedAt: now, Digest: "a"},
		{IdentityID: "firebase-uid", MachineID: "a2", Snapshot: validA, ReceivedAt: now, Digest: "a2"},
	}
	listed, err := snapshotService.List(context.Background(), validMachine())
	if err != nil || listed.Snapshots[0].Snapshot.Machine != "alpha" || listed.Snapshots[2].Snapshot.Machine != "beta" {
		t.Fatalf("sorted snapshots=%+v err=%v", listed, err)
	}
	listStore.records = []StoredMachineSnapshot{
		{IdentityID: "firebase-uid", MachineID: "a", Snapshot: validA, ReceivedAt: now, Digest: "a"},
		{IdentityID: "firebase-uid", MachineID: "b", Snapshot: validB, ReceivedAt: now, Digest: "b"},
	}
	if _, err := snapshotService.List(context.Background(), validMachine()); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageRepositoryEventBranches(t *testing.T) {
	now := time.Now().UTC()
	validRecord := StoredMachineSnapshot{IdentityID: "uid", MachineID: "machine", Snapshot: validSnapshot(now), ReceivedAt: now, Digest: "digest"}
	push := WebhookDelivery{ID: "delivery", Event: "push", Payload: []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":7,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`)}
	for _, service := range []RepositoryEventService{
		{},
		{Snapshots: snapshotMemory{validRecord}, Entitlements: coverageEntitlement{allowed: true}},
		{Snapshots: snapshotMemory{validRecord}, Store: &coverageEventStore{}},
	} {
		if _, err := service.EnqueueWebhook(context.Background(), push); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("missing dependency enqueue=%v", err)
		}
	}
	service := RepositoryEventService{Snapshots: coverageFailingSnapshotStore{err: errors.New("list")}, Entitlements: coverageEntitlement{allowed: true}, Store: &coverageEventStore{}}
	if _, err := service.EnqueueWebhook(context.Background(), push); err == nil {
		t.Fatal("snapshot list failure accepted")
	}
	service.Snapshots = snapshotMemory{{IdentityID: "uid", MachineID: "machine"}}
	if _, err := service.EnqueueWebhook(context.Background(), push); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	other := validRecord
	other.Snapshot.Repositories = []string{"github.com/other/repo"}
	service.Snapshots = snapshotMemory{other}
	if result, err := service.EnqueueWebhook(context.Background(), push); err != nil || result.Enqueued != 0 {
		t.Fatalf("unmatched snapshot result=%+v err=%v", result, err)
	}
	conflict := validRecord
	conflict.IdentityID = "other"
	conflict.MachineID = validRecord.MachineID
	service.Snapshots = snapshotMemory{validRecord, conflict}
	if _, err := service.EnqueueWebhook(context.Background(), push); err == nil {
		t.Fatal("machine identity conflict accepted")
	}
	badIdentity := validRecord
	badIdentity.IdentityID = ""
	service.Snapshots = snapshotMemory{badIdentity}
	if _, err := service.EnqueueWebhook(context.Background(), push); err == nil {
		t.Fatal("empty snapshot identity accepted")
	}
	service.Snapshots = snapshotMemory{validRecord}
	service.Entitlements = coverageEntitlement{err: errors.New("entitlement")}
	if _, err := service.EnqueueWebhook(context.Background(), push); err == nil {
		t.Fatal("entitlement failure accepted")
	}

	machine := validMachine()
	for _, invalid := range []RepositoryEventService{{}, {Store: &coverageEventStore{}}} {
		if _, err := invalid.Poll(context.Background(), Machine{}, "", 1, 0); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("unauthorized poll=%v", err)
		}
		if _, err := invalid.Acknowledge(context.Background(), Machine{}, repositoryevent.AckRequest{}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("unauthorized ack=%v", err)
		}
	}
	store := &coverageEventStore{pollResponse: repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "", NextCursor: "next", Events: []repositoryevent.Event{}}}
	service = RepositoryEventService{Store: store}
	for _, request := range []struct {
		cursor string
		limit  int
		wait   time.Duration
	}{{"bad\n", 1, 0}, {"", 0, 0}, {"", repositoryevent.MaxLimit + 1, 0}, {"", 1, -1}, {"", 1, (repositoryevent.MaxWaitSeconds + 1) * time.Second}} {
		if _, err := service.Poll(context.Background(), machine, request.cursor, request.limit, request.wait); err == nil {
			t.Fatalf("invalid poll accepted: %+v", request)
		}
	}
	store.pollResponse = repositoryevent.PollResponse{}
	if _, err := service.Poll(context.Background(), machine, "", 1, 0); err == nil {
		t.Fatal("invalid store poll accepted")
	}
	store.pollErr = errors.New("poll")
	if _, err := service.Poll(context.Background(), machine, "", 1, 0); err == nil {
		t.Fatal("poll store failure accepted")
	}
	store.pollErr = nil
	store.pollResponse = repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "", NextCursor: "next", Events: []repositoryevent.Event{}}
	service.PollInterval = time.Hour
	service.Sleep = func(context.Context, time.Duration) error { return errors.New("sleep") }
	if _, err := service.Poll(context.Background(), machine, "", 1, time.Second); err == nil {
		t.Fatal("sleep failure accepted")
	}
	service.Sleep = func(context.Context, time.Duration) error { return nil }
	service.PollInterval = time.Second
	if response, err := service.Poll(context.Background(), machine, "", 1, time.Nanosecond); err != nil || response.NextCursor != "next" {
		t.Fatalf("deadline poll=%+v err=%v", response, err)
	}
	clockStart := time.Now()
	clockCalls := 0
	service.Now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return clockStart
		}
		return clockStart.Add(time.Second)
	}
	if response, err := service.Poll(context.Background(), machine, "", 1, time.Second); err != nil || response.NextCursor != "next" {
		t.Fatalf("deterministic deadline poll=%+v err=%v", response, err)
	}
	clockCalls = 0
	service.PollInterval = 2 * time.Second
	service.Now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return clockStart
		}
		return clockStart.Add(500 * time.Millisecond)
	}
	service.Sleep = func(_ context.Context, delay time.Duration) error {
		if delay != 500*time.Millisecond {
			t.Fatalf("poll delay=%v", delay)
		}
		return errors.New("stop")
	}
	if _, err := service.Poll(context.Background(), machine, "", 1, time.Second); err == nil {
		t.Fatal("deterministic bounded delay did not stop")
	}

	request := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: "next", EventIDs: []string{"event"}}
	if _, err := service.Acknowledge(context.Background(), machine, repositoryevent.AckRequest{}); err == nil {
		t.Fatal("invalid ack accepted")
	}
	store.ackErr = errors.New("ack")
	if _, err := service.Acknowledge(context.Background(), machine, request); err == nil {
		t.Fatal("ack store failure accepted")
	}
	store.ackErr = nil
	store.ackResponse = repositoryevent.AckResponse{Version: 0, Cursor: request.Cursor}
	if _, err := service.Acknowledge(context.Background(), machine, request); err == nil {
		t.Fatal("invalid ack response accepted")
	}
	store.ackResponse = repositoryevent.AckResponse{Version: repositoryevent.ContractVersion, Cursor: request.Cursor}
	if response, err := service.Acknowledge(context.Background(), machine, request); err != nil || response.Cursor != request.Cursor {
		t.Fatalf("ack=%+v err=%v", response, err)
	}

	unsupported := []WebhookDelivery{
		{ID: "bad id", Event: "push", Payload: push.Payload},
		{ID: "delivery", Event: "unknown", Payload: []byte(`{}`)},
		{ID: "delivery", Event: "push", Payload: []byte(`{`)},
		{ID: "delivery", Event: "push", Payload: []byte(`{"repository":{"id":0}}`)},
		{ID: "delivery", Event: "repository", Payload: []byte(`{`)},
		{ID: "delivery", Event: "repository", Payload: []byte(`{"action":"edited"}`)},
		{ID: "delivery", Event: "repository", Payload: []byte(`{"action":"renamed","repository":{"id":0}}`)},
		{ID: "delivery", Event: "repository", Payload: []byte(`{"action":"renamed","repository":{"id":1,"full_name":"bad","default_branch":"main"},"changes":{"repository":{"name":{"from":"old"}}},"installation":{"id":123}}`)},
		{ID: "delivery", Event: "repository", Payload: []byte(`{"action":"renamed","repository":{"id":1,"full_name":"acme/new","default_branch":"main"},"changes":{"repository":{"name":{"from":"bad/name"}}},"installation":{"id":123}}`)},
	}
	for index, delivery := range unsupported {
		_, _, _ = translateWebhook(delivery)
		if index == 0 {
			if _, _, err := translateWebhook(delivery); err == nil {
				t.Fatal("invalid delivery ID accepted")
			}
		}
	}
	for index, delivery := range []WebhookDelivery{
		{ID: "bad id", Event: "installation", Payload: []byte(`{}`)},
		{ID: "delivery", Event: "installation", Payload: []byte(`{`)},
		{ID: "delivery", Event: "installation", Payload: []byte(`{"action":"created","installation":{"id":7}}`)},
		{ID: "delivery", Event: "installation", Payload: []byte(`{"action":"suspend","installation":{"id":0}}`)},
		{ID: "delivery", Event: "installation_repositories", Payload: []byte(`{"action":"removed","installation":{"id":7},"repositories_removed":[]}`)},
		{ID: "delivery", Event: "membership", Payload: []byte(`{"action":"removed","installation":{"id":7},"member":{"id":0}}`)},
		{ID: "delivery", Event: "organization", Payload: []byte(`{"action":"member_removed","installation":{"id":7}}`)},
	} {
		supported, _, err := translateInstallationLifecycle(delivery)
		if index != 2 {
			if !supported || err == nil {
				t.Fatalf("invalid lifecycle %d supported=%v err=%v", index, supported, err)
			}
		} else if supported || err != nil {
			t.Fatalf("ignored lifecycle supported=%v err=%v", supported, err)
		}
	}
	lifecycleDelivery := WebhookDelivery{ID: "delivery-lifecycle", Event: "installation", Payload: []byte(`{"action":"deleted","installation":{"id":7}}`)}
	if _, err := (RepositoryEventService{}).EnqueueWebhook(context.Background(), WebhookDelivery{ID: "bad id", Event: "installation", Payload: []byte(`{}`)}); err == nil {
		t.Fatal("invalid lifecycle delivery accepted")
	}
	if _, err := (RepositoryEventService{}).EnqueueWebhook(context.Background(), lifecycleDelivery); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing lifecycle store=%v", err)
	}
	if _, err := (RepositoryEventService{Lifecycle: coverageLifecycle{err: errors.New("store")}}).EnqueueWebhook(context.Background(), lifecycleDelivery); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lifecycle store failure=%v", err)
	}
	invalidPush := WebhookDelivery{ID: "delivery-invalid-event", Event: "push", Payload: []byte(`{"ref":"refs/heads/main","after":"bad","repository":{"id":7,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`)}
	if _, _, err := translateWebhook(invalidPush); err == nil {
		t.Fatal("invalid translated push event accepted")
	}
	if canonicalRepository(" github.com/acme/app ") != "github.com/acme/app" || canonicalRepository("Acme/App") != "github.com/acme/app" {
		t.Fatal("canonical repository helper failed")
	}
	if err := decodeWebhook([]byte(`{`), &map[string]any{}); err == nil {
		t.Fatal("bad webhook JSON accepted")
	}
	if err := sleepContext(context.Background(), 0); err != nil {
		t.Fatalf("zero sleep=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleep=%v", err)
	}
}

func TestCoverageStatusFailuresAndTieSorts(t *testing.T) {
	now := time.Now().UTC()
	validViewer := Viewer{Authenticated: true, IdentityID: "uid"}
	if _, err := (StatusService{}).Read(context.Background(), Viewer{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized status=%v", err)
	}
	if _, err := (StatusService{}).Read(context.Background(), validViewer); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable status=%v", err)
	}
	base := StatusService{Bindings: &coverageBindingStore{}, Snapshots: &snapshotStoreMemory{}, Events: coverageStatusStore{}, Now: func() time.Time { return now }}
	bindingFailure := base
	bindingFailure.Bindings = &coverageBindingStore{listErr: errors.New("bindings")}
	if _, err := bindingFailure.Read(context.Background(), validViewer); err == nil {
		t.Fatal("binding list failure accepted")
	}
	snapshotFailure := base
	snapshotFailure.Snapshots = coverageFailingSnapshotStore{err: errors.New("snapshots")}
	if _, err := snapshotFailure.Read(context.Background(), validViewer); err == nil {
		t.Fatal("snapshot list failure accepted")
	}
	eventFailure := base
	eventFailure.Events = coverageStatusStore{err: errors.New("events")}
	if _, err := eventFailure.Read(context.Background(), validViewer); err == nil {
		t.Fatal("event status failure accepted")
	}
	invalidBinding := base
	invalidBinding.Bindings = &coverageBindingStore{bindings: []IdentityInstallationBinding{{IdentityID: "other", Installation: coverageInstallation(now)}}}
	if _, err := invalidBinding.Read(context.Background(), validViewer); err == nil {
		t.Fatal("invalid binding accepted")
	}
	invalidSnapshot := base
	invalidSnapshot.Snapshots = &snapshotStoreMemory{records: []StoredMachineSnapshot{{IdentityID: "uid", MachineID: "bad"}}}
	if _, err := invalidSnapshot.Read(context.Background(), validViewer); err == nil {
		t.Fatal("invalid status snapshot accepted")
	}

	first := coverageInstallation(now)
	first.State = ""
	first.Account = "same"
	first.ID = 20
	second := first
	second.ID = 10
	second.State = "suspended"
	bindings := &coverageBindingStore{bindings: []IdentityInstallationBinding{{IdentityID: "uid", Installation: first}, {IdentityID: "uid", Installation: second}}}
	snapA := validSnapshot(now)
	snapA.Machine = "same"
	snapB := snapA
	pendingAt := now
	status := StatusService{
		Bindings: bindings,
		Snapshots: &snapshotStoreMemory{records: []StoredMachineSnapshot{
			{IdentityID: "uid", MachineID: "z", Snapshot: snapA, ReceivedAt: now, Digest: "z"},
			{IdentityID: "uid", MachineID: "a", Snapshot: snapB, ReceivedAt: now, Digest: "a"},
		}},
		Events: coverageStatusStore{
			pending: []PendingRefresh{{ID: "z", QueuedAt: pendingAt}, {ID: "a", QueuedAt: pendingAt}},
			errors:  []StatusError{{Code: "same", InstallationID: "z", Message: "z"}, {Code: "same", InstallationID: "a", Message: "z"}, {Code: "same", InstallationID: "a", Message: "a"}},
		},
		Now: func() time.Time { return now },
	}
	response, err := status.Read(context.Background(), validViewer)
	if err != nil {
		t.Fatal(err)
	}
	if response.Connection.State != "attention" || response.Connection.Account != "same" || response.Installations[0].ID != "10" || response.Machines[0].ID != "a" || response.PendingRefreshes[0].ID != "a" || response.Errors[0].Message != "a" {
		t.Fatalf("tie sorting/status failed: %+v", response)
	}
}

func TestCoverageScopeValidationBranches(t *testing.T) {
	if hasScope(nil, ScopeEventsAck) || validScopes(nil) {
		t.Fatal("empty scopes accepted")
	}
	missing := cloneEnrollmentScopes()
	missing[len(missing)-1] = ScopeSnapshotRead
	if validScopes(missing) {
		t.Fatal("duplicate/missing scope accepted")
	}
}
