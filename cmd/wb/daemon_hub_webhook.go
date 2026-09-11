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
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/hubconfig"
)

// webhookMode is everything hub.github.app adds to the mounted hub: the
// secret every delivery is verified against, the installation service the
// connect routes need.
//
// A nil *webhookMode is the default journey — no App, no public URL — so
// every accessor below is safe on it and the caller needs no branch.
type webhookMode struct {
	secret        []byte
	installations *hub.InstallationConnectionService
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
