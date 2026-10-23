package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/daemonview"
)

// webhookSecret is 32 bytes, the floor hub.MinimumWebhookSecretBytes sets.
const webhookSecret = "0123456789abcdef0123456789abcdef"

const testPrivateKeyPEM = "-----BEGIN RSA PRIVATE KEY-----\nnot-a-real-key\n-----END RSA PRIVATE KEY-----\n"

// appHubConfig writes a hub section with a complete GitHub App block and
// returns the configuration path. The private key is the package's fixed
// placeholder, which parses as nothing: every test using it never signs
// anything with it (the App verifier route those tests exercise 503s before
// it would need to).
func appHubConfig(t *testing.T, secret string) string {
	t.Helper()
	return appHubConfigWithKey(t, secret, testPrivateKeyPEM)
}

// appHubConfigWithKey is appHubConfig with the private key file's contents as
// a parameter, for a test that must actually sign a JWT with it (the
// missed-webhook redelivery sweep) and so needs a real RSA key rather than
// the package's placeholder.
func appHubConfigWithKey(t *testing.T, secret, privateKeyPEM string) string {
	t.Helper()
	state := t.TempDir()
	token := filepath.Join(state, "github.token")
	key := filepath.Join(state, "app.pem")
	secretFile := filepath.Join(state, "webhook.secret")
	for path, content := range map[string]string{
		token:      "ghp_test\n",
		key:        privateKeyPEM,
		secretFile: secret + "\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return hubTestConfig(t, fmt.Sprintf(memoryHubSection, state, token)+
		"    app:\n      app_id: 1234\n      private_key_file: "+key+
		"\n      webhook_secret_file: "+secretFile+
		"\n      public_url: https://bench.example.test\n")
}

// TestWebhookModeVerifiesDeliveriesWithTheOperatorsSecret is the whole point
// of the mode: with an App configured the webhook route stops answering
// "webhook not configured", accepts a delivery signed with the secret file's
// contents, and refuses one that is not.

// TestWithoutAnAppEverythingWebhookIsOff is the default journey: no secret,
// no installation service, nothing covered, and a start line that says so.

// TestWebhookModeRefusesUnusableSecretFiles keeps a half-configured App from
// starting a daemon that would silently reject every delivery. Each message
// names the file and never its contents.

// stateDirectoryOf finds the directory appHubConfig put the secret files in,
// by reading the token path back out of the configuration it wrote.

// TestLocalEntitlementsBelongToTheOneLocalIdentity pins the judgement the
// loopback hub makes: its single identity is entitled to everything its own
// App and token reach, and no other identity exists to ask about.

// TestInstallationStateSecretIsDerivedNotReused keeps the state signing key
// in its own domain, so a signature from one can never be replayed as the
// other.

// TestConnectRoutesAnswer503WithoutAnOAuthVerifier is the honest limit of
// self-hosted webhook mode: the browser half of connecting an installation
// cannot complete on a loopback listener, and the route says so rather than
// failing some other way.

// TestPollIntervalPrefersTheTestOverride keeps the configuration floor for
// the operator and out of the way of a test's fake GitHub.

// TestDaemonStatusReportsWebhookMode is the status half: an operator asks
// whether GitHub is expected to reach them, and where.
func TestDaemonStatusReportsWebhookMode(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.HubConfigPath = func() string { return appHubConfig(t, webhookSecret) }
	statusResult, statusErr := newDaemonController(deps, root).Status(context.Background())
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	status := statusResult.Hub
	if !status.Webhook || status.WebhookPublicURL != "https://bench.example.test" {
		t.Fatalf("hub status = %+v", status)
	}

	var out bytes.Buffer
	if err := daemonview.Result(&out, "text", daemonResult{Action: "status", Hub: status}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hub_webhook=true", "hub_webhook_public_url=https://bench.example.test"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status text %q does not contain %q", out.String(), want)
		}
	}

	deps.HubConfigPath = func() string { return memoryHubConfig(t) }
	pollingResult, pollingErr := newDaemonController(deps, root).Status(context.Background())
	if pollingErr != nil {
		t.Fatal(pollingErr)
	}
	polling := pollingResult.Hub
	if polling.Webhook || polling.WebhookPublicURL != "" {
		t.Fatalf("polling-only hub status = %+v", polling)
	}
	out.Reset()
	if err := daemonview.Result(&out, "text", daemonResult{Action: "status", Hub: polling}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hub_webhook=false") || strings.Contains(out.String(), "hub_webhook_public_url") {
		t.Fatalf("status text = %q", out.String())
	}
}
