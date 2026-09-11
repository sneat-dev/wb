package hub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

const (
	InstallationConnectPath   = APIPrefix + "/github/installations/connect"
	InstallationContinuePath  = APIPrefix + "/github/installations/continue"
	InstallationAuthorizePath = APIPrefix + "/github/installations/authorize"
	InstallationSetupPath     = APIPrefix + "/github/installations/setup"
	InstallationCallbackPath  = APIPrefix + "/github/installations/callback"
	installationStateBytes    = 32
	openerChallengeBytes      = 32
	oauthTokenNonceBytes      = 12
	defaultStateLifetime      = 10 * time.Minute
	defaultChallengeLifetime  = 2 * time.Minute
	maxVerifiedInstallations  = 1000
	maxInstallationRepos      = 10000
)

type installationStateKind string

const (
	installationStateBootstrap      installationStateKind = "bootstrap"
	installationStateSetup          installationStateKind = "setup"
	installationStateOAuth          installationStateKind = "oauth"
	installationStateOAuthExchanged installationStateKind = "oauth_exchanged"
)

type InstallationStateDigest [sha256.Size]byte
type InstallationState struct {
	Kind                       installationStateKind `firestore:"kind"`
	IdentityID                 string                `firestore:"identity_id"`
	InstallationID             int64                 `firestore:"installation_id,omitempty"`
	BrowserContinuationDigest  string                `firestore:"browser_continuation_digest"`
	OpenerChallengeDigest      string                `firestore:"opener_challenge_digest,omitempty"`
	OpenerChallengeExpiresAt   time.Time             `firestore:"opener_challenge_expires_at,omitempty"`
	OpenerAuthorizedAt         time.Time             `firestore:"opener_authorized_at,omitempty"`
	OAuthAccessTokenCiphertext string                `firestore:"oauth_access_token_ciphertext,omitempty"`
	IssuedAt                   time.Time             `firestore:"issued_at"`
	ExpiresAt                  time.Time             `firestore:"expires_at"`
}
type InstallationStateStore interface {
	// IssueInstallationState rejects an existing digest so a random-state
	// collision cannot overwrite another pending authorization.
	IssueInstallationState(context.Context, InstallationStateDigest, InstallationState) error
	// ReadInstallationState returns a pending, unexpired state without consuming
	// it. Store availability errors must remain distinguishable from
	// ErrInvalidInstallationState so callers can safely retry transient failures.
	ReadInstallationState(context.Context, InstallationStateDigest, time.Time) (InstallationState, error)
	// TransitionInstallationState atomically verifies current against the stored
	// pending state, consumes it, and issues next. A replay, expired state,
	// mismatch, or next-digest collision returns ErrInvalidInstallationState.
	TransitionInstallationState(context.Context, InstallationStateDigest, InstallationState, time.Time, InstallationStateDigest, InstallationState) error
	// ReplaceInstallationState atomically verifies current against the stored
	// pending state and replaces it under the same digest. It is used to retain
	// an encrypted exchanged OAuth credential before any fallible post-exchange
	// reads. A replay, expiry, or mismatch returns ErrInvalidInstallationState.
	ReplaceInstallationState(context.Context, InstallationStateDigest, InstallationState, time.Time, InstallationState) error
}

type VerifiedRepository struct {
	ID         int64  `json:"id" firestore:"id"`
	Repository string `json:"repository" firestore:"repository"`
}
type VerifiedInstallation struct {
	ID                  int64                `json:"id" firestore:"id"`
	Account             string               `json:"account" firestore:"account"`
	AccountType         string               `json:"account_type,omitempty" firestore:"account_type,omitempty"`
	RepositorySelection string               `json:"repository_selection" firestore:"repository_selection"`
	Repositories        []VerifiedRepository `json:"repositories" firestore:"repositories"`
	State               string               `json:"state,omitempty" firestore:"state,omitempty"`
	ManageURL           string               `json:"manage_url,omitempty" firestore:"manage_url,omitempty"`
}
type VerifiedGitHubIdentity struct {
	UserID        int64                  `json:"user_id"`
	Login         string                 `json:"login"`
	Installations []VerifiedInstallation `json:"installations"`
}

func (identity VerifiedGitHubIdentity) Validate() error {
	if identity.UserID <= 0 || strings.TrimSpace(identity.Login) == "" || len(identity.Login) > 128 || len(identity.Installations) > maxVerifiedInstallations {
		return errors.New("invalid verified GitHub identity")
	}
	seen := make(map[int64]bool, len(identity.Installations))
	for _, installation := range identity.Installations {
		if installation.ID <= 0 || seen[installation.ID] || strings.TrimSpace(installation.Account) == "" || len(installation.Account) > 256 ||
			(!strings.EqualFold(installation.AccountType, "user") && !strings.EqualFold(installation.AccountType, "organization")) ||
			(installation.RepositorySelection != "all" && installation.RepositorySelection != "selected") ||
			(installation.State != "installed" && installation.State != "suspended" && installation.State != "revoked") ||
			(installation.ManageURL != "" && !validHTTPSURL(installation.ManageURL)) || len(installation.Repositories) > maxInstallationRepos ||
			!slices.IsSortedFunc(installation.Repositories, func(a, b VerifiedRepository) int { return strings.Compare(a.Repository, b.Repository) }) {
			return errors.New("invalid verified GitHub installation")
		}
		seen[installation.ID] = true
		previous := ""
		ids := make(map[int64]bool, len(installation.Repositories))
		for _, repository := range installation.Repositories {
			if repository.ID <= 0 || ids[repository.ID] || repository.Repository == previous || repositoryevent.ValidateRepository(repository.Repository) != nil {
				return errors.New("invalid verified GitHub repository")
			}
			ids[repository.ID] = true
			previous = repository.Repository
		}
	}
	return nil
}

type AppInstallationVerifier interface {
	VerifyAppInstallation(context.Context, int64) (VerifiedInstallation, error)
}
type GitHubIdentityVerifier interface {
	ExchangeGitHubOAuthCode(context.Context, string) (string, error)
	VerifyGitHubIdentity(context.Context, string) (VerifiedGitHubIdentity, error)
}
type IdentityInstallationBinding struct {
	IdentityID   string               `firestore:"identity_id"`
	GitHubUserID int64                `firestore:"github_user_id"`
	GitHubLogin  string               `firestore:"github_login"`
	Installation VerifiedInstallation `firestore:"installation"`
	VerifiedAt   time.Time            `firestore:"verified_at"`
}

type InstallationBindingStore interface {
	IdentityHasInstallation(context.Context, string, int64) (bool, error)
	// CompleteIdentityInstallationBinding atomically verifies and consumes the
	// pending OAuth state and upserts only the explicitly selected installation.
	// It must reject a different GitHub user when the Workbench identity already
	// has bindings. A replay or state mismatch returns ErrInvalidInstallationState.
	CompleteIdentityInstallationBinding(context.Context, InstallationStateDigest, InstallationState, time.Time, IdentityInstallationBinding) error
	ListIdentityInstallationBindings(context.Context, string) ([]IdentityInstallationBinding, error)
}

type InstallationConnectRequest struct {
	InstallationID int64 `json:"installation_id,omitempty"`
}
type InstallationConnectResponse struct {
	ConnectURL string    `json:"connect_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type InstallationContinuation struct {
	RedirectURL         string
	ExpiresAt           time.Time
	browserContinuation string
	openerChallenge     string
	inert               bool
}
type InstallationRedirect struct {
	RedirectURL     string
	ExpiresAt       time.Time
	oauthCredential string
}

type InstallationConnectionService struct {
	States            InstallationStateStore
	AppVerifier       AppInstallationVerifier
	OAuthVerifier     GitHubIdentityVerifier
	Bindings          InstallationBindingStore
	InstallURL        string
	OAuthAuthorizeURL string
	OAuthClientID     string
	OAuthCallbackURL  string
	SuccessURL        string
	StateSecret       []byte
	StateLifetime     time.Duration
	Random            io.Reader
	Now               func() time.Time
}

func (service InstallationConnectionService) Begin(ctx context.Context, viewer Viewer, request InstallationConnectRequest) (InstallationConnectResponse, error) {
	if !viewer.Authenticated || strings.TrimSpace(viewer.IdentityID) == "" {
		return InstallationConnectResponse{}, ErrUnauthorized
	}
	if service.validateConfiguration() != nil {
		return InstallationConnectResponse{}, ErrUnavailable
	}
	if request.InstallationID < 0 {
		return InstallationConnectResponse{}, errors.New("invalid installation ID")
	}
	installationID := int64(0)
	if request.InstallationID > 0 {
		known, err := service.Bindings.IdentityHasInstallation(ctx, viewer.IdentityID, request.InstallationID)
		if err != nil {
			return InstallationConnectResponse{}, ErrUnavailable
		}
		if !known {
			return InstallationConnectResponse{}, ErrUnauthorized
		}
		installationID = request.InstallationID
	}
	state, record, err := service.issueState(ctx, installationStateBootstrap, viewer.IdentityID, installationID, "")
	if err != nil {
		return InstallationConnectResponse{}, err
	}
	parsed := service.continuationURL()
	query := parsed.Query()
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return InstallationConnectResponse{ConnectURL: parsed.String(), ExpiresAt: record.ExpiresAt}, nil
}

type InstallationOpenerAuthorizationRequest struct {
	State     string `json:"state"`
	Challenge string `json:"challenge"`
}

func (service InstallationConnectionService) Continue(ctx context.Context, state, openerChallenge string) (InstallationContinuation, error) {
	if service.validateConfiguration() != nil {
		return InstallationContinuation{}, ErrUnavailable
	}
	digest, record, err := service.readState(ctx, state, installationStateBootstrap, "")
	if err != nil {
		return InstallationContinuation{}, err
	}
	now := service.now()
	if record.OpenerChallengeDigest == "" {
		challenge, challengeDigest, challengeErr := service.newOpenerChallenge()
		if challengeErr != nil {
			return InstallationContinuation{}, challengeErr
		}
		next := record
		next.OpenerChallengeDigest = challengeDigest
		next.OpenerChallengeExpiresAt = now.Add(defaultChallengeLifetime)
		if next.OpenerChallengeExpiresAt.After(record.ExpiresAt) {
			next.OpenerChallengeExpiresAt = record.ExpiresAt
		}
		if replaceErr := service.States.ReplaceInstallationState(ctx, digest, record, now, next); replaceErr != nil {
			if errors.Is(replaceErr, ErrInvalidInstallationState) || errors.Is(replaceErr, ErrUnauthorized) {
				return InstallationContinuation{}, replaceErr
			}
			return InstallationContinuation{}, ErrUnavailable
		}
		return InstallationContinuation{ExpiresAt: next.OpenerChallengeExpiresAt, openerChallenge: challenge}, nil
	}
	if openerChallenge == "" || !hmac.Equal([]byte(record.OpenerChallengeDigest), []byte(openerChallengeDigest(openerChallenge))) {
		return InstallationContinuation{ExpiresAt: record.ExpiresAt, inert: true}, nil
	}
	if !record.OpenerChallengeExpiresAt.After(now) || record.OpenerChallengeExpiresAt.After(record.ExpiresAt) {
		return InstallationContinuation{}, ErrInvalidInstallationState
	}
	if record.OpenerAuthorizedAt.IsZero() {
		return InstallationContinuation{ExpiresAt: record.OpenerChallengeExpiresAt, openerChallenge: openerChallenge}, nil
	}
	if record.OpenerAuthorizedAt.After(now) || record.OpenerAuthorizedAt.After(record.OpenerChallengeExpiresAt) {
		return InstallationContinuation{}, ErrInvalidInstallationState
	}
	continuation, continuationDigest, err := service.newBrowserContinuation()
	if err != nil {
		return InstallationContinuation{}, err
	}
	kind := installationStateSetup
	endpoint := service.InstallURL
	if record.InstallationID > 0 {
		kind = installationStateOAuth
		endpoint = service.OAuthAuthorizeURL
	}
	nextState, nextRecord, err := service.newState(kind, record.IdentityID, record.InstallationID, continuationDigest)
	if err != nil {
		return InstallationContinuation{}, err
	}
	nextDigest := InstallationStateDigest(sha256.Sum256([]byte(nextState)))
	if err := service.States.TransitionInstallationState(ctx, digest, record, service.now(), nextDigest, nextRecord); err != nil {
		if errors.Is(err, ErrInvalidInstallationState) || errors.Is(err, ErrUnauthorized) {
			return InstallationContinuation{}, err
		}
		return InstallationContinuation{}, ErrUnavailable
	}
	parsed, _ := url.Parse(endpoint)
	query := parsed.Query()
	query.Set("state", nextState)
	if kind == installationStateOAuth {
		query.Set("client_id", service.OAuthClientID)
		query.Set("redirect_uri", service.OAuthCallbackURL)
	}
	parsed.RawQuery = query.Encode()
	return InstallationContinuation{RedirectURL: parsed.String(), ExpiresAt: nextRecord.ExpiresAt, browserContinuation: continuation}, nil
}

func (service InstallationConnectionService) AuthorizeOpener(ctx context.Context, viewer Viewer, request InstallationOpenerAuthorizationRequest) error {
	if !viewer.Authenticated || strings.TrimSpace(viewer.IdentityID) == "" {
		return ErrUnauthorized
	}
	if service.validateConfiguration() != nil {
		return ErrUnavailable
	}
	if request.Challenge == "" || len(request.Challenge) > 256 {
		return ErrInvalidInstallationState
	}
	digest, record, err := service.readState(ctx, request.State, installationStateBootstrap, "")
	if err != nil {
		return err
	}
	now := service.now()
	if record.IdentityID != viewer.IdentityID {
		return ErrUnauthorized
	}
	if record.OpenerChallengeDigest == "" || !hmac.Equal([]byte(record.OpenerChallengeDigest), []byte(openerChallengeDigest(request.Challenge))) || !record.OpenerChallengeExpiresAt.After(now) || !record.OpenerAuthorizedAt.IsZero() {
		return ErrInvalidInstallationState
	}
	next := record
	next.OpenerAuthorizedAt = now
	if err := service.States.ReplaceInstallationState(ctx, digest, record, now, next); err != nil {
		if errors.Is(err, ErrInvalidInstallationState) || errors.Is(err, ErrUnauthorized) {
			return err
		}
		return ErrUnavailable
	}
	return nil
}

func (service InstallationConnectionService) CompleteSetup(ctx context.Context, state string, installationID int64, browserContinuation string) (InstallationRedirect, error) {
	if service.validateConfiguration() != nil {
		return InstallationRedirect{}, ErrUnavailable
	}
	if installationID <= 0 {
		return InstallationRedirect{}, errors.New("invalid setup callback")
	}
	digest, record, err := service.readState(ctx, state, installationStateSetup, browserContinuation)
	if err != nil {
		return InstallationRedirect{}, err
	}
	verified, err := service.AppVerifier.VerifyAppInstallation(ctx, installationID)
	if errors.Is(err, ErrUnavailable) {
		return InstallationRedirect{}, ErrUnavailable
	}
	if err != nil || verified.ID != installationID || (VerifiedGitHubIdentity{UserID: 1, Login: "validation", Installations: []VerifiedInstallation{verified}}).Validate() != nil {
		return InstallationRedirect{}, errors.New("invalid setup callback")
	}
	oauthState, oauthRecord, err := service.newState(installationStateOAuth, record.IdentityID, installationID, record.BrowserContinuationDigest)
	if err != nil {
		return InstallationRedirect{}, err
	}
	oauthDigest := InstallationStateDigest(sha256.Sum256([]byte(oauthState)))
	if err := service.States.TransitionInstallationState(ctx, digest, record, service.now(), oauthDigest, oauthRecord); err != nil {
		if errors.Is(err, ErrInvalidInstallationState) || errors.Is(err, ErrUnauthorized) {
			return InstallationRedirect{}, err
		}
		return InstallationRedirect{}, ErrUnavailable
	}
	endpoint, _ := url.Parse(service.OAuthAuthorizeURL)
	query := endpoint.Query()
	query.Set("client_id", service.OAuthClientID)
	query.Set("redirect_uri", service.OAuthCallbackURL)
	query.Set("state", oauthState)
	endpoint.RawQuery = query.Encode()
	return InstallationRedirect{RedirectURL: endpoint.String()}, nil
}

func (service InstallationConnectionService) CompleteOAuth(ctx context.Context, state, code, browserContinuation, oauthCredential string) (InstallationRedirect, error) {
	if service.validateConfiguration() != nil {
		return InstallationRedirect{}, ErrUnavailable
	}
	digest, record, err := service.readOAuthState(ctx, state, browserContinuation)
	if err != nil {
		return InstallationRedirect{}, err
	}
	if record.Kind == installationStateOAuth {
		if oauthCredential != "" && !oauthCredentialMatchesState(oauthCredential, state) {
			oauthCredential = ""
		}
		if oauthCredential == "" {
			if code == "" || len(code) > 2048 || strings.ContainsAny(code, " \t\r\n") {
				return InstallationRedirect{}, errors.New("invalid OAuth callback")
			}
			token, exchangeErr := service.OAuthVerifier.ExchangeGitHubOAuthCode(ctx, code)
			if errors.Is(exchangeErr, ErrUnavailable) {
				return InstallationRedirect{}, ErrUnavailable
			}
			if exchangeErr != nil || token == "" || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
				return InstallationRedirect{}, errors.New("invalid OAuth callback")
			}
			ciphertext, sealErr := service.sealOAuthAccessToken(token, state)
			if sealErr != nil {
				return InstallationRedirect{}, sealErr
			}
			callback, _ := url.Parse(service.OAuthCallbackURL)
			query := callback.Query()
			query.Set("state", state)
			callback.RawQuery = query.Encode()
			return InstallationRedirect{RedirectURL: callback.String(), ExpiresAt: record.ExpiresAt, oauthCredential: ciphertext}, nil
		}
		if _, openErr := service.openOAuthAccessToken(oauthCredential, state); openErr != nil {
			return InstallationRedirect{}, ErrInvalidInstallationState
		}
		next := record
		next.Kind = installationStateOAuthExchanged
		next.OAuthAccessTokenCiphertext = oauthCredential
		if replaceErr := service.States.ReplaceInstallationState(ctx, digest, record, service.now(), next); replaceErr != nil {
			if errors.Is(replaceErr, ErrInvalidInstallationState) || errors.Is(replaceErr, ErrUnauthorized) {
				return InstallationRedirect{}, replaceErr
			}
			return InstallationRedirect{}, ErrUnavailable
		}
		record = next
	}
	token, err := service.openOAuthAccessToken(record.OAuthAccessTokenCiphertext, state)
	if err != nil {
		return InstallationRedirect{}, ErrInvalidInstallationState
	}
	identity, err := service.OAuthVerifier.VerifyGitHubIdentity(ctx, token)
	if errors.Is(err, ErrUnavailable) {
		return InstallationRedirect{}, ErrUnavailable
	}
	if err != nil || identity.Validate() != nil {
		return InstallationRedirect{}, errors.New("invalid OAuth callback")
	}
	index := slices.IndexFunc(identity.Installations, func(value VerifiedInstallation) bool { return value.ID == record.InstallationID })
	if index < 0 {
		return InstallationRedirect{}, errors.New("OAuth user cannot access installation")
	}
	binding := IdentityInstallationBinding{
		IdentityID:   record.IdentityID,
		GitHubUserID: identity.UserID,
		GitHubLogin:  identity.Login,
		Installation: identity.Installations[index],
		VerifiedAt:   service.now(),
	}
	if err := service.Bindings.CompleteIdentityInstallationBinding(ctx, digest, record, service.now(), binding); err != nil {
		if errors.Is(err, ErrInvalidInstallationState) || errors.Is(err, ErrUnauthorized) {
			return InstallationRedirect{}, err
		}
		return InstallationRedirect{}, ErrUnavailable
	}
	return InstallationRedirect{RedirectURL: service.SuccessURL}, nil
}

func (service InstallationConnectionService) issueState(ctx context.Context, kind installationStateKind, identityID string, installationID int64, browserContinuationDigest string) (string, InstallationState, error) {
	state, record, err := service.newState(kind, identityID, installationID, browserContinuationDigest)
	if err != nil {
		return "", InstallationState{}, err
	}
	digest := InstallationStateDigest(sha256.Sum256([]byte(state)))
	if err := service.States.IssueInstallationState(ctx, digest, record); err != nil {
		return "", InstallationState{}, ErrUnavailable
	}
	return state, record, nil
}

func (service InstallationConnectionService) newState(kind installationStateKind, identityID string, installationID int64, browserContinuationDigest string) (string, InstallationState, error) {
	random := service.Random
	if random == nil {
		random = rand.Reader
	}
	raw := make([]byte, installationStateBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", InstallationState{}, ErrUnavailable
	}
	state := signInstallationState(raw, service.StateSecret)
	now := service.now()
	lifetime := service.StateLifetime
	if lifetime <= 0 {
		lifetime = defaultStateLifetime
	}
	record := InstallationState{Kind: kind, IdentityID: identityID, InstallationID: installationID, BrowserContinuationDigest: browserContinuationDigest, IssuedAt: now, ExpiresAt: now.Add(lifetime)}
	return state, record, nil
}
func (service InstallationConnectionService) newBrowserContinuation() (string, string, error) {
	random := service.Random
	if random == nil {
		random = rand.Reader
	}
	raw := make([]byte, installationStateBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", "", ErrUnavailable
	}
	continuation := base64.RawURLEncoding.EncodeToString(raw)
	return continuation, browserContinuationDigest(continuation), nil
}
func (service InstallationConnectionService) newOpenerChallenge() (string, string, error) {
	random := service.Random
	if random == nil {
		random = rand.Reader
	}
	raw := make([]byte, openerChallengeBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", "", ErrUnavailable
	}
	challenge := base64.RawURLEncoding.EncodeToString(raw)
	return challenge, openerChallengeDigest(challenge), nil
}
func (service InstallationConnectionService) readState(ctx context.Context, state string, kind installationStateKind, browserContinuation string) (InstallationStateDigest, InstallationState, error) {
	digest, record, err := service.readStoredState(ctx, state)
	if err != nil {
		return InstallationStateDigest{}, InstallationState{}, err
	}
	if record.Kind != kind || (kind == installationStateBootstrap && record.BrowserContinuationDigest != "") || (kind != installationStateBootstrap && (record.BrowserContinuationDigest == "" || !hmac.Equal([]byte(record.BrowserContinuationDigest), []byte(browserContinuationDigest(browserContinuation))))) || (kind == installationStateOAuth && record.InstallationID <= 0) {
		return InstallationStateDigest{}, InstallationState{}, ErrInvalidInstallationState
	}
	return digest, record, nil
}

func (service InstallationConnectionService) readOAuthState(ctx context.Context, state, browserContinuation string) (InstallationStateDigest, InstallationState, error) {
	digest, record, err := service.readStoredState(ctx, state)
	if err != nil {
		return InstallationStateDigest{}, InstallationState{}, err
	}
	validKind := record.Kind == installationStateOAuth && record.OAuthAccessTokenCiphertext == "" || record.Kind == installationStateOAuthExchanged && record.OAuthAccessTokenCiphertext != ""
	if !validKind || record.InstallationID <= 0 || record.BrowserContinuationDigest == "" || !hmac.Equal([]byte(record.BrowserContinuationDigest), []byte(browserContinuationDigest(browserContinuation))) {
		return InstallationStateDigest{}, InstallationState{}, ErrInvalidInstallationState
	}
	return digest, record, nil
}

func (service InstallationConnectionService) readStoredState(ctx context.Context, state string) (InstallationStateDigest, InstallationState, error) {
	if !verifyInstallationState(state, service.StateSecret) {
		return InstallationStateDigest{}, InstallationState{}, ErrInvalidInstallationState
	}
	now := service.now()
	digest := InstallationStateDigest(sha256.Sum256([]byte(state)))
	record, err := service.States.ReadInstallationState(ctx, digest, now)
	if errors.Is(err, ErrInvalidInstallationState) {
		return InstallationStateDigest{}, InstallationState{}, err
	}
	if err != nil {
		return InstallationStateDigest{}, InstallationState{}, ErrUnavailable
	}
	if record.IdentityID == "" || !record.ExpiresAt.After(now) || record.IssuedAt.After(now) {
		return InstallationStateDigest{}, InstallationState{}, ErrInvalidInstallationState
	}
	return digest, record, nil
}

func (service InstallationConnectionService) sealOAuthAccessToken(token, state string) (string, error) {
	block, _ := aes.NewCipher(service.oauthTokenKey())
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, oauthTokenNonceBytes)
	random := service.Random
	if random == nil {
		random = rand.Reader
	}
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", ErrUnavailable
	}
	sealed := gcm.Seal(nonce, nonce, []byte(token), []byte(state))
	return oauthCredentialStateDigest(state) + "." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (service InstallationConnectionService) openOAuthAccessToken(value, state string) (string, error) {
	prefix, encoded, found := strings.Cut(value, ".")
	if !found || !hmac.Equal([]byte(prefix), []byte(oauthCredentialStateDigest(state))) {
		return "", errors.New("invalid OAuth credential state")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(service.oauthTokenKey())
	gcm, _ := cipher.NewGCM(block)
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("invalid OAuth credential")
	}
	plaintext, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(state))
	if err != nil || len(plaintext) == 0 {
		return "", errors.New("invalid OAuth credential")
	}
	return string(plaintext), nil
}

func oauthCredentialMatchesState(value, state string) bool {
	prefix, _, found := strings.Cut(value, ".")
	return found && hmac.Equal([]byte(prefix), []byte(oauthCredentialStateDigest(state)))
}

func oauthCredentialStateDigest(state string) string {
	digest := sha256.Sum256([]byte(state))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (service InstallationConnectionService) oauthTokenKey() []byte {
	digest := sha256.Sum256(append(append([]byte(nil), service.StateSecret...), []byte("workbench-github-oauth-token")...))
	return digest[:]
}

func (service InstallationConnectionService) continuationURL() *url.URL {
	callback, _ := url.Parse(service.OAuthCallbackURL)
	return &url.URL{Scheme: callback.Scheme, Host: callback.Host, Path: InstallationContinuePath}
}

func browserContinuationDigest(continuation string) string {
	digest := sha256.Sum256([]byte(continuation))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
func openerChallengeDigest(challenge string) string {
	digest := sha256.Sum256([]byte(challenge))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
func (service InstallationConnectionService) validateConfiguration() error {
	if service.States == nil || service.AppVerifier == nil || service.OAuthVerifier == nil || service.Bindings == nil || len(service.StateSecret) < 32 || !validHTTPSURL(service.InstallURL) || !validHTTPSURL(service.OAuthAuthorizeURL) || !validHTTPSURL(service.OAuthCallbackURL) || !validHTTPSURL(service.SuccessURL) || service.OAuthClientID == "" {
		return ErrUnavailable
	}
	return nil
}
func (service InstallationConnectionService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}
func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}
func signInstallationState(raw, secret []byte) string {
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func verifyInstallationState(state string, secret []byte) bool {
	if len(state) > 256 {
		return false
	}
	raw, signature, found := strings.Cut(state, ".")
	if !found || raw == "" || signature == "" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(raw))
	return hmac.Equal(decoded, mac.Sum(nil))
}
