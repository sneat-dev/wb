package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/hubconfig"
)

// coverageTTL is how long the poller's "an App already covers this" answer is
// reused before the installation bindings are read again. It is short enough
// that connecting an installation takes effect within a tick or two, and long
// enough that a tick over a large inventory reads the bindings once.
const coverageTTL = 30 * time.Second

// coverageTimeout bounds one bindings read. The poller asks on its own
// goroutine and a store that does not answer must not hold up a tick: an
// unanswered question means "not covered", which polls a repository that may
// not have needed it rather than missing a push that did.
const coverageTimeout = 5 * time.Second

// webhookMode is everything hub.github.app adds to the mounted hub: the
// secret every delivery is verified against, the installation service the
// connect routes need, and the coverage rule that stops the poller asking
// GitHub about repositories the App already pushes.
//
// A nil *webhookMode is the default journey — no App, no public URL — so
// every accessor below is safe on it and the caller needs no branch.
type webhookMode struct {
	secret        []byte
	installations *hub.InstallationConnectionService
	coverage      *appCoverage
	publicURL     string
}

// newWebhookMode reads the App's two secret files and builds the wiring. It
// returns (nil, nil) when no App is configured, and an error naming the file
// — never its contents — when one cannot be used.
func newWebhookMode(cfg hubconfig.Config, states hub.InstallationStateStore, bindings hub.InstallationBindingStore, pepper []byte) (*webhookMode, error) {
	app := cfg.GitHub.App
	if app == nil {
		return nil, nil
	}
	privateKey, err := os.ReadFile(app.PrivateKeyFile) //nolint:gosec // operator-owned private key path from their own configuration.
	if err != nil {
		return nil, fmt.Errorf("read hub GitHub App private key file: %w", err)
	}
	if len(bytes.TrimSpace(privateKey)) == 0 {
		return nil, fmt.Errorf("hub GitHub App private key file %s is empty", app.PrivateKeyFile)
	}
	rawSecret, err := os.ReadFile(app.WebhookSecretFile) //nolint:gosec // operator-owned webhook secret path from their own configuration.
	if err != nil {
		return nil, fmt.Errorf("read hub GitHub App webhook secret file: %w", err)
	}
	// The file is written by a human or a password manager, so a trailing
	// newline is expected; the secret GitHub signs with is what remains.
	secret := bytes.TrimSpace(rawSecret)
	if len(secret) < hub.MinimumWebhookSecretBytes {
		return nil, fmt.Errorf("hub GitHub App webhook secret file %s holds fewer than %d bytes; GitHub deliveries could not be verified against it", app.WebhookSecretFile, hub.MinimumWebhookSecretBytes)
	}
	return &webhookMode{
		secret: secret,
		// The connect, setup and callback routes additionally need an OAuth
		// verifier, which a loopback hub has no way to complete: GitHub
		// cannot redirect a browser to a client secret this operator has not
		// registered. The service is wired with everything it does have, and
		// validateConfiguration turns the missing half into a plain 503 on
		// those routes rather than a panic. Webhook delivery — the whole of
		// this mode — needs none of it.
		installations: &hub.InstallationConnectionService{
			States:      states,
			AppVerifier: hub.GitHubAppInstallationVerifier{Client: &http.Client{Timeout: 30 * time.Second}, AppID: app.AppID, PrivateKeyPEM: privateKey},
			Bindings:    bindings,
			StateSecret: installationStateSecret(pepper),
		},
		coverage:  newAppCoverage(bindings),
		publicURL: app.PublicURL,
	}, nil
}

// WebhookSecret is what hub.HandlerOptions verifies deliveries with. Nil
// without an App, which is what makes the webhook route answer
// "rejected: webhook not configured".
func (mode *webhookMode) WebhookSecret() []byte {
	if mode == nil {
		return nil
	}
	return mode.secret
}

// Installations is the connection service, or nil without an App.
func (mode *webhookMode) Installations() *hub.InstallationConnectionService {
	if mode == nil {
		return nil
	}
	return mode.installations
}

// Covered is the poller's skip rule. Without an App nothing is covered, so
// polling is the only ingester and every repository is asked about.
func (mode *webhookMode) Covered(repository string) bool {
	if mode == nil || mode.coverage == nil {
		return false
	}
	return mode.coverage.Covered(repository)
}

// StartSuffix is what the daemon's start line says about webhook mode, so an
// operator running `wb daemon serve` in a terminal can see at a glance
// whether GitHub is expected to reach them and at which URL.
func (mode *webhookMode) StartSuffix() string {
	if mode == nil {
		return " webhook=off"
	}
	return " webhook=on public_url=" + mode.publicURL
}

// installationStateSecret derives the installation-state signing key from the
// machine pepper. The two are kept in separate domains so a state signature
// can never be replayed as a machine token digest or the other way round,
// while the operator still has exactly one secret file to keep.
func installationStateSecret(pepper []byte) []byte {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("wb/hub/installation-state"))
	return mac.Sum(nil)
}

// localRepositoryEntitlements entitles the one identity a loopback hub has.
//
// The hosted instance resolves entitlements from the installation bindings a
// GitHub user authorised, because it serves many identities and must never
// route one account's push to another's machine. A self-hosted hub has one
// identity, one machine and one operator, who registered the App, holds its
// webhook secret and owns the token the poller uses: a delivery that verifies
// against that secret is by construction about a repository they chose to
// install it on. The poller makes the same judgement for the same reason.
type localRepositoryEntitlements struct{ identityID string }

func (entitlements localRepositoryEntitlements) IdentityHasRepositoryEntitlement(_ context.Context, identityID string, _, _ int64) (bool, error) {
	if identityID != entitlements.identityID {
		return false, errors.New("a loopback hub has exactly one identity")
	}
	return true, nil
}

// appCoverage answers "does the operator's GitHub App already deliver
// webhooks for this repository?" from the installation bindings the local
// identity holds. The answer is cached for coverageTTL because the poller
// asks once per repository per tick and the bindings change only when an
// installation does.
type appCoverage struct {
	bindings hub.InstallationBindingStore
	identity string
	ttl      time.Duration
	now      func() time.Time

	mu        sync.Mutex
	refreshed time.Time
	covered   map[string]bool
}

func newAppCoverage(bindings hub.InstallationBindingStore) *appCoverage {
	return &appCoverage{bindings: bindings, identity: localIdentityID, ttl: coverageTTL, now: time.Now}
}

// Covered reports whether any binding of the local identity lists repository.
// A store that cannot be read answers "not covered": polling a repository the
// App also delivers costs one request and deduplicates in the event store,
// while skipping one it does not deliver loses the push entirely.
func (coverage *appCoverage) Covered(repository string) bool {
	coverage.mu.Lock()
	defer coverage.mu.Unlock()
	now := coverage.now()
	if coverage.covered == nil || now.Sub(coverage.refreshed) >= coverage.ttl {
		coverage.covered = coverage.read()
		coverage.refreshed = now
	}
	return coverage.covered[canonicalRepository(repository)]
}

func (coverage *appCoverage) read() map[string]bool {
	covered := make(map[string]bool)
	if coverage.bindings == nil {
		return covered
	}
	ctx, cancel := context.WithTimeout(context.Background(), coverageTimeout)
	defer cancel()
	bindings, err := coverage.bindings.ListIdentityInstallationBindings(ctx, coverage.identity)
	if err != nil {
		return covered
	}
	for _, binding := range bindings {
		for _, repository := range binding.Installation.Repositories {
			covered[canonicalRepository(repository.Repository)] = true
		}
	}
	return covered
}

// canonicalRepository is the one spelling coverage is keyed by. A binding
// stores `owner/name`; a snapshot and the poller speak `github.com/owner/name`.
func canonicalRepository(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(value, "github.com/") {
		return value
	}
	return "github.com/" + value
}
