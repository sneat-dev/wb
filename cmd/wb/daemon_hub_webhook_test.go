package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

// webhookSecret is 32 bytes, the floor hub.MinimumWebhookSecretBytes sets.
const webhookSecret = "0123456789abcdef0123456789abcdef"

const testPrivateKeyPEM = "-----BEGIN RSA PRIVATE KEY-----\nnot-a-real-key\n-----END RSA PRIVATE KEY-----\n"

const pushPayload = `{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":987,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`

// appHubConfig writes a hub section with a complete GitHub App block and
// returns the configuration path.
func appHubConfig(t *testing.T, secret string) string {
	t.Helper()
	state := t.TempDir()
	token := filepath.Join(state, "github.token")
	key := filepath.Join(state, "app.pem")
	secretFile := filepath.Join(state, "webhook.secret")
	for path, content := range map[string]string{
		token:      "ghp_test\n",
		key:        testPrivateKeyPEM,
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
func TestWebhookModeVerifiesDeliveriesWithTheOperatorsSecret(t *testing.T) {
	var console bytes.Buffer
	configPath := appHubConfig(t, webhookSecret)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8801", narrate.Writer{Out: &console}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()

	handler := mount.handlers()[hub.APIPrefix+"/"]
	if handler == nil {
		t.Fatal("the hub API is not mounted")
	}

	unsigned := httptest.NewRecorder()
	handler.ServeHTTP(unsigned, webhookRequest(t, "delivery-unsigned", "sha256=deadbeef"))
	if unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery = %d", unsigned.Code)
	}
	if !strings.Contains(console.String(), "rejected: bad signature") {
		t.Fatalf("narration = %q", console.String())
	}

	signed := httptest.NewRecorder()
	handler.ServeHTTP(signed, webhookRequest(t, "delivery-signed", signature(webhookSecret, pushPayload)))
	if signed.Code != http.StatusAccepted {
		t.Fatalf("signed delivery = %d (%s)", signed.Code, signed.Body.String())
	}
	// No machine has published an inventory here, so the hub has nowhere to
	// route the push — but it verified, translated and narrated it, which is
	// everything the secret wiring is responsible for.
	if !strings.Contains(console.String(), "github.com/acme/app") {
		t.Fatalf("narration = %q", console.String())
	}
	if strings.Contains(console.String(), webhookSecret) {
		t.Fatal("the webhook secret reached the console")
	}
	if suffix := mount.Webhook.StartSuffix(); suffix != " webhook=on public_url=https://bench.example.test" {
		t.Fatalf("start suffix = %q", suffix)
	}
	if !strings.Contains(mount.StartLine(), "webhook=on public_url=https://bench.example.test") {
		t.Fatalf("start line = %q", mount.StartLine())
	}
}

func webhookRequest(t *testing.T, delivery, signature string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, hub.WebhookPath, strings.NewReader(pushPayload))
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-GitHub-Delivery", delivery)
	request.Header.Set("X-Hub-Signature-256", signature)
	return request
}

func signature(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestWithoutAnAppEverythingWebhookIsOff is the default journey: no secret,
// no installation service, nothing covered, and a start line that says so.
func TestWithoutAnAppEverythingWebhookIsOff(t *testing.T) {
	var absent *webhookMode
	if absent.WebhookSecret() != nil || absent.Installations() != nil {
		t.Fatal("a hub with no App must offer no webhook wiring")
	}
	if absent.StartSuffix() != " webhook=off" {
		t.Fatalf("start suffix = %q", absent.StartSuffix())
	}

	mount, err := mountHub(context.Background(), memoryHubConfig(t), "127.0.0.1:8802", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()
	if mount.Webhook != nil {
		t.Fatal("a hub with no hub.github.app must not be in webhook mode")
	}
	if !strings.HasSuffix(mount.StartLine(), " webhook=off") {
		t.Fatalf("start line = %q", mount.StartLine())
	}

	var console bytes.Buffer
	quiet := hub.NewHandler(hub.HandlerOptions{Narrate: narrate.Writer{Out: &console}.Write})
	recorder := httptest.NewRecorder()
	quiet.ServeHTTP(recorder, webhookRequest(t, "delivery", signature(webhookSecret, pushPayload)))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(console.String(), "webhook not configured") {
		t.Fatalf("unconfigured webhook = %d, %q", recorder.Code, console.String())
	}
}

// TestWebhookModeRefusesUnusableSecretFiles keeps a half-configured App from
// starting a daemon that would silently reject every delivery. Each message
// names the file and never its contents.
func TestWebhookModeRefusesUnusableSecretFiles(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mutate  func(t *testing.T, state string)
		wantSub string
	}{
		{
			name:    "the private key file is missing",
			mutate:  func(t *testing.T, state string) { remove(t, filepath.Join(state, "app.pem")) },
			wantSub: "private key file",
		},
		{
			name:    "the private key file is empty",
			mutate:  func(t *testing.T, state string) { write(t, filepath.Join(state, "app.pem"), "\n  \n") },
			wantSub: "is empty",
		},
		{
			name:    "the webhook secret file is missing",
			mutate:  func(t *testing.T, state string) { remove(t, filepath.Join(state, "webhook.secret")) },
			wantSub: "webhook secret file",
		},
		{
			name:    "the webhook secret is too short",
			mutate:  func(t *testing.T, state string) { write(t, filepath.Join(state, "webhook.secret"), "short\n") },
			wantSub: "fewer than 32 bytes",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configPath := appHubConfig(t, webhookSecret)
			state := stateDirectoryOf(t, configPath)
			testCase.mutate(t, state)
			_, err := mountHub(context.Background(), configPath, "127.0.0.1:8803", narrate.Writer{}, nil)
			if err == nil || !strings.Contains(err.Error(), testCase.wantSub) {
				t.Fatalf("mountHub = %v, want an error naming %q", err, testCase.wantSub)
			}
			if err != nil && strings.Contains(err.Error(), webhookSecret) {
				t.Fatal("the error carried the secret")
			}
		})
	}
}

// stateDirectoryOf finds the directory appHubConfig put the secret files in,
// by reading the token path back out of the configuration it wrote.
func stateDirectoryOf(t *testing.T, configPath string) string {
	t.Helper()
	raw, err := os.ReadFile(configPath) //nolint:gosec // this test wrote it.
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if _, value, found := strings.Cut(strings.TrimSpace(line), "token_file:"); found {
			return filepath.Dir(strings.TrimSpace(value))
		}
	}
	t.Fatalf("no token_file in %s", raw)
	return ""
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// TestLocalEntitlementsBelongToTheOneLocalIdentity pins the judgement the
// loopback hub makes: its single identity is entitled to everything its own
// App and token reach, and no other identity exists to ask about.
func TestLocalEntitlementsBelongToTheOneLocalIdentity(t *testing.T) {
	entitlements := localRepositoryEntitlements{identityID: localIdentityID}
	allowed, err := entitlements.IdentityHasRepositoryEntitlement(context.Background(), localIdentityID, 123, 987)
	if err != nil || !allowed {
		t.Fatalf("local identity = %t, %v", allowed, err)
	}
	if allowed, err := entitlements.IdentityHasRepositoryEntitlement(context.Background(), "someone-else", 123, 987); allowed || err == nil {
		t.Fatalf("another identity = %t, %v", allowed, err)
	}
}

// TestInstallationStateSecretIsDerivedNotReused keeps the state signing key
// in its own domain, so a signature from one can never be replayed as the
// other.
func TestInstallationStateSecretIsDerivedNotReused(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, hubPepperBytes)
	secret := installationStateSecret(pepper)
	if len(secret) < 32 {
		t.Fatalf("state secret is %d bytes", len(secret))
	}
	if bytes.Equal(secret, pepper) {
		t.Fatal("the state secret is the machine pepper")
	}
	if !bytes.Equal(secret, installationStateSecret(pepper)) {
		t.Fatal("the derivation is not stable across restarts")
	}
}

// TestConnectRoutesAnswer503WithoutAnOAuthVerifier is the honest limit of
// self-hosted webhook mode: the browser half of connecting an installation
// cannot complete on a loopback listener, and the route says so rather than
// failing some other way.
func TestConnectRoutesAnswer503WithoutAnOAuthVerifier(t *testing.T) {
	configPath := appHubConfig(t, webhookSecret)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8804", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()
	if mount.Webhook.Installations() == nil {
		t.Fatal("webhook mode must wire the installation service")
	}
	handler := mount.handlers()[hub.APIPrefix+"/"]
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, hub.InstallationConnectPath, strings.NewReader("{}"))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "installation_connection_unavailable") {
		t.Fatalf("connect = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestPollIntervalPrefersTheTestOverride keeps the configuration floor for
// the operator and out of the way of a test's fake GitHub.
func TestPollIntervalPrefersTheTestOverride(t *testing.T) {
	configPath := memoryHubConfig(t)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8805", narrate.Writer{}, &hubTuning{PollInterval: 5 * time.Millisecond})
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()
	if mount.Interval != 5*time.Millisecond {
		t.Fatalf("interval = %s", mount.Interval)
	}
}

// TestDaemonStatusReportsWebhookMode is the status half: an operator asks
// whether GitHub is expected to reach them, and where.
func TestDaemonStatusReportsWebhookMode(t *testing.T) {
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = func() string { return appHubConfig(t, webhookSecret) }
	status := newDaemonController(deps, root).hubStatus(context.Background(), "")
	if !status.Webhook || status.WebhookPublicURL != "https://bench.example.test" {
		t.Fatalf("hub status = %+v", status)
	}

	var out bytes.Buffer
	if err := writeDaemonResult(&out, "text", daemonResult{Action: "status", Hub: status}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hub_webhook=true", "hub_webhook_public_url=https://bench.example.test"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status text %q does not contain %q", out.String(), want)
		}
	}

	deps.hubConfigPath = func() string { return memoryHubConfig(t) }
	polling := newDaemonController(deps, root).hubStatus(context.Background(), "")
	if polling.Webhook || polling.WebhookPublicURL != "" {
		t.Fatalf("polling-only hub status = %+v", polling)
	}
	out.Reset()
	if err := writeDaemonResult(&out, "text", daemonResult{Action: "status", Hub: polling}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hub_webhook=false") || strings.Contains(out.String(), "hub_webhook_public_url") {
		t.Fatalf("status text = %q", out.String())
	}
}
