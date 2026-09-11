package hub

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

type memoryStates struct {
	records map[InstallationStateDigest]InstallationState
}

func (s *memoryStates) IssueInstallationState(_ context.Context, d InstallationStateDigest, state InstallationState) error {
	if s.records == nil {
		s.records = map[InstallationStateDigest]InstallationState{}
	}
	if _, exists := s.records[d]; exists {
		return errors.New("duplicate")
	}
	s.records[d] = state
	return nil
}
func (s *memoryStates) ReadInstallationState(_ context.Context, d InstallationStateDigest, now time.Time) (InstallationState, error) {
	state, ok := s.records[d]
	if !ok || !state.ExpiresAt.After(now) {
		return InstallationState{}, ErrInvalidInstallationState
	}
	return state, nil
}
func (s *memoryStates) TransitionInstallationState(_ context.Context, currentDigest InstallationStateDigest, current InstallationState, now time.Time, nextDigest InstallationStateDigest, next InstallationState) error {
	stored, err := s.ReadInstallationState(context.Background(), currentDigest, now)
	if err != nil || stored != current {
		return ErrInvalidInstallationState
	}
	if _, exists := s.records[nextDigest]; exists {
		return ErrInvalidInstallationState
	}
	delete(s.records, currentDigest)
	s.records[nextDigest] = next
	return nil
}
func (s *memoryStates) ReplaceInstallationState(_ context.Context, digest InstallationStateDigest, current InstallationState, now time.Time, next InstallationState) error {
	stored, err := s.ReadInstallationState(context.Background(), digest, now)
	if err != nil || stored != current {
		return ErrInvalidInstallationState
	}
	s.records[digest] = next
	return nil
}

type fakeAppVerifier struct {
	installation VerifiedInstallation
	err          error
}

func (f fakeAppVerifier) VerifyAppInstallation(context.Context, int64) (VerifiedInstallation, error) {
	return f.installation, f.err
}

type fakeIdentityVerifier struct {
	identity    VerifiedGitHubIdentity
	err         error
	exchangeErr error
}

func (f fakeIdentityVerifier) ExchangeGitHubOAuthCode(_ context.Context, code string) (string, error) {
	return "token-" + code, f.exchangeErr
}
func (f fakeIdentityVerifier) VerifyGitHubIdentity(context.Context, string) (VerifiedGitHubIdentity, error) {
	return f.identity, f.err
}

type singleUseIdentityVerifier struct {
	identity      VerifiedGitHubIdentity
	verifyErr     error
	exchangeCalls int
}

func (f *singleUseIdentityVerifier) ExchangeGitHubOAuthCode(context.Context, string) (string, error) {
	f.exchangeCalls++
	if f.exchangeCalls > 1 {
		return "", errors.New("OAuth code reused")
	}
	return "single-use-access-token", nil
}

func (f *singleUseIdentityVerifier) VerifyGitHubIdentity(context.Context, string) (VerifiedGitHubIdentity, error) {
	return f.identity, f.verifyErr
}

type memoryBindings struct {
	known  map[int64]bool
	stored []IdentityInstallationBinding
	states *memoryStates
	err    error
}

func (s *memoryBindings) IdentityHasInstallation(_ context.Context, _ string, id int64) (bool, error) {
	return s.known[id], nil
}
func (s *memoryBindings) CompleteIdentityInstallationBinding(_ context.Context, digest InstallationStateDigest, expected InstallationState, now time.Time, binding IdentityInstallationBinding) error {
	if s.err != nil {
		return s.err
	}
	stored, err := s.states.ReadInstallationState(context.Background(), digest, now)
	if err != nil || stored != expected || expected.IdentityID != binding.IdentityID || expected.InstallationID != binding.Installation.ID {
		return ErrInvalidInstallationState
	}
	for index, existing := range s.stored {
		if existing.IdentityID == binding.IdentityID && existing.GitHubUserID != binding.GitHubUserID {
			return ErrUnauthorized
		}
		if existing.Installation.ID == binding.Installation.ID {
			s.stored[index] = binding
			delete(s.states.records, digest)
			return nil
		}
	}
	s.stored = append(s.stored, binding)
	delete(s.states.records, digest)
	return nil
}
func (s *memoryBindings) ListIdentityInstallationBindings(context.Context, string) ([]IdentityInstallationBinding, error) {
	return append([]IdentityInstallationBinding(nil), s.stored...), nil
}

func TestInstallationFlowRequiresExactOAuthUserAccess(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	installation := VerifiedInstallation{ID: 7, Account: "acme", AccountType: "Organization", RepositorySelection: "selected", Repositories: []VerifiedRepository{{ID: 99, Repository: "github.com/acme/app"}}, State: "installed"}
	states := &memoryStates{}
	bindings := &memoryBindings{known: map[int64]bool{7: true}, states: states}
	other := VerifiedInstallation{ID: 8, Account: "other", AccountType: "User", RepositorySelection: "selected", Repositories: []VerifiedRepository{{ID: 100, Repository: "github.com/other/private"}}, State: "installed"}
	service := InstallationConnectionService{States: states, AppVerifier: fakeAppVerifier{installation: installation}, OAuthVerifier: fakeIdentityVerifier{identity: VerifiedGitHubIdentity{UserID: 42, Login: "octocat", Installations: []VerifiedInstallation{other, installation}}}, Bindings: bindings, InstallURL: "https://github.com/apps/workbench/installations/new", OAuthAuthorizeURL: "https://github.com/login/oauth/authorize", OAuthClientID: "client", OAuthCallbackURL: "https://wb-github-app.sneat.dev/v0/workbench/github/installations/callback", SuccessURL: "https://sneat.work/bench", StateSecret: []byte(strings.Repeat("s", 32)), Random: installationFlowRandom(), Now: func() time.Time { return now }}
	connect, err := service.Begin(context.Background(), Viewer{Authenticated: true, IdentityID: "firebase-uid"}, InstallationConnectRequest{})
	if err != nil {
		t.Fatal(err)
	}
	continueURL, _ := url.Parse(connect.ConnectURL)
	continuation := authorizeAndContinue(t, &service, continueURL.Query().Get("state"), Viewer{Authenticated: true, IdentityID: "firebase-uid"})
	installURL, _ := url.Parse(continuation.RedirectURL)
	setupState := installURL.Query().Get("state")
	if setupState == "" || strings.Contains(connect.ConnectURL, "firebase-uid") {
		t.Fatalf("unsafe install URL %q", connect.ConnectURL)
	}
	oauthRedirect, err := service.CompleteSetup(context.Background(), setupState, 7, continuation.browserContinuation)
	if err != nil {
		t.Fatal(err)
	}
	oauthURL, _ := url.Parse(oauthRedirect.RedirectURL)
	oauthState := oauthURL.Query().Get("state")
	if oauthState == "" || oauthState == setupState || oauthURL.Query().Get("client_id") != "client" {
		t.Fatalf("invalid OAuth redirect %q", oauthRedirect.RedirectURL)
	}
	staged, err := service.CompleteOAuth(context.Background(), oauthState, "single-use-code", continuation.browserContinuation, "")
	if err != nil || staged.oauthCredential == "" {
		t.Fatalf("stage OAuth exchange=%+v err=%v", staged, err)
	}
	done, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation.browserContinuation, staged.oauthCredential)
	if err != nil {
		t.Fatal(err)
	}
	if done.RedirectURL != service.SuccessURL || len(bindings.stored) != 1 || bindings.stored[0].IdentityID != "firebase-uid" || bindings.stored[0].Installation.ID != 7 {
		t.Fatalf("binding=%+v redirect=%+v", bindings.stored, done)
	}
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "replay", continuation.browserContinuation, staged.oauthCredential); err == nil {
		t.Fatal("replayed OAuth state accepted")
	}
}

func TestInstallationOAuthRejectsForwardedInstallWithoutUserAccess(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	states := &memoryStates{}
	bindings := &memoryBindings{states: states}
	installation := VerifiedInstallation{ID: 7, Account: "acme", AccountType: "Organization", RepositorySelection: "selected", Repositories: []VerifiedRepository{}, State: "installed"}
	service := InstallationConnectionService{States: states, AppVerifier: fakeAppVerifier{installation: installation}, OAuthVerifier: fakeIdentityVerifier{identity: VerifiedGitHubIdentity{UserID: 42, Login: "different-user", Installations: []VerifiedInstallation{}}}, Bindings: bindings, InstallURL: "https://github.com/apps/workbench/installations/new", OAuthAuthorizeURL: "https://github.com/login/oauth/authorize", OAuthClientID: "client", OAuthCallbackURL: "https://wb-github-app.sneat.dev/v0/workbench/github/installations/callback", SuccessURL: "https://sneat.work/bench", StateSecret: []byte(strings.Repeat("s", 32)), Random: installationFlowRandom(), Now: func() time.Time { return now }}
	connect, _ := service.Begin(context.Background(), Viewer{Authenticated: true, IdentityID: "firebase-uid"}, InstallationConnectRequest{})
	u, _ := url.Parse(connect.ConnectURL)
	continuation := authorizeAndContinue(t, &service, u.Query().Get("state"), Viewer{Authenticated: true, IdentityID: "firebase-uid"})
	u, _ = url.Parse(continuation.RedirectURL)
	redirect, _ := service.CompleteSetup(context.Background(), u.Query().Get("state"), 7, continuation.browserContinuation)
	u, _ = url.Parse(redirect.RedirectURL)
	staged, _ := service.CompleteOAuth(context.Background(), u.Query().Get("state"), "code", continuation.browserContinuation, "")
	if _, err := service.CompleteOAuth(context.Background(), u.Query().Get("state"), "", continuation.browserContinuation, staged.oauthCredential); err == nil {
		t.Fatal("forwarded installation granted entitlement")
	}
	if len(bindings.stored) != 0 {
		t.Fatalf("unexpected binding %+v", bindings.stored)
	}
}

func TestInstallationCallbacksRequireInitiatingBrowserAndPreserveTransientRetries(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	viewer := Viewer{Authenticated: true, IdentityID: "firebase-uid"}
	installation := VerifiedInstallation{ID: 7, Account: "acme", AccountType: "Organization", RepositorySelection: "selected", Repositories: []VerifiedRepository{{ID: 99, Repository: "github.com/acme/app"}}, State: "installed"}
	states := &memoryStates{}
	bindings := &memoryBindings{states: states}
	service := InstallationConnectionService{
		States: states, AppVerifier: fakeAppVerifier{installation: installation},
		OAuthVerifier: fakeIdentityVerifier{identity: VerifiedGitHubIdentity{UserID: 42, Login: "octocat", Installations: []VerifiedInstallation{installation}}},
		Bindings:      bindings, InstallURL: "https://github.com/apps/workbench/installations/new", OAuthAuthorizeURL: "https://github.com/login/oauth/authorize",
		OAuthClientID: "client", OAuthCallbackURL: "https://wb-github-app.sneat.dev/v0/workbench/github/installations/callback", SuccessURL: "https://sneat.work/bench",
		StateSecret: []byte(strings.Repeat("s", 32)), Random: installationFlowRandom(), Now: func() time.Time { return now },
	}
	connect, err := service.Begin(context.Background(), viewer, InstallationConnectRequest{})
	if err != nil {
		t.Fatal(err)
	}
	continueURL, _ := url.Parse(connect.ConnectURL)
	continuation := authorizeAndContinue(t, &service, continueURL.Query().Get("state"), viewer)
	installURL, _ := url.Parse(continuation.RedirectURL)
	setupState := installURL.Query().Get("state")
	if _, err := service.CompleteSetup(context.Background(), setupState, 7, "forwarded-browser"); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("forwarded browser error=%v", err)
	}
	service.AppVerifier = fakeAppVerifier{err: ErrUnavailable}
	if _, err := service.CompleteSetup(context.Background(), setupState, 7, continuation.browserContinuation); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("transient setup error=%v", err)
	}
	service.AppVerifier = fakeAppVerifier{installation: installation}
	redirect, err := service.CompleteSetup(context.Background(), setupState, 7, continuation.browserContinuation)
	if err != nil {
		t.Fatal(err)
	}
	oauthURL, _ := url.Parse(redirect.RedirectURL)
	oauthState := oauthURL.Query().Get("state")
	singleUse := &singleUseIdentityVerifier{identity: VerifiedGitHubIdentity{UserID: 42, Login: "octocat", Installations: []VerifiedInstallation{installation}}, verifyErr: ErrUnavailable}
	service.OAuthVerifier = singleUse
	staged, err := service.CompleteOAuth(context.Background(), oauthState, "code", continuation.browserContinuation, "")
	if err != nil || staged.oauthCredential == "" {
		t.Fatalf("stage single-use OAuth exchange=%+v err=%v", staged, err)
	}
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation.browserContinuation, staged.oauthCredential); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("transient OAuth error=%v", err)
	}
	singleUse.verifyErr = nil
	bindings.err = errors.New("store unavailable")
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation.browserContinuation, staged.oauthCredential); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("transient store error=%v", err)
	}
	bindings.err = nil
	if _, err := service.CompleteOAuth(context.Background(), oauthState, "", continuation.browserContinuation, staged.oauthCredential); err != nil {
		t.Fatal(err)
	}
	if len(bindings.stored) != 1 || bindings.stored[0].Installation.ID != 7 {
		t.Fatalf("stored bindings=%+v", bindings.stored)
	}
	if singleUse.exchangeCalls != 1 {
		t.Fatalf("OAuth code exchanges=%d", singleUse.exchangeCalls)
	}
}

func installationFlowRandom() *strings.Reader {
	return strings.NewReader(strings.Repeat("a", 32) + strings.Repeat("b", 32) + strings.Repeat("c", 32) + strings.Repeat("d", 32) + strings.Repeat("e", 32) + strings.Repeat("f", 32))
}

func authorizeAndContinue(t *testing.T, service *InstallationConnectionService, state string, viewer Viewer) InstallationContinuation {
	t.Helper()
	page, err := service.Continue(context.Background(), state, "")
	if err != nil || page.openerChallenge == "" || page.RedirectURL != "" {
		t.Fatalf("opener page=%+v err=%v", page, err)
	}
	if err := service.AuthorizeOpener(context.Background(), viewer, InstallationOpenerAuthorizationRequest{State: state, Challenge: page.openerChallenge}); err != nil {
		t.Fatal(err)
	}
	continuation, err := service.Continue(context.Background(), state, page.openerChallenge)
	if err != nil || continuation.RedirectURL == "" {
		t.Fatalf("authorized continuation=%+v err=%v", continuation, err)
	}
	return continuation
}
