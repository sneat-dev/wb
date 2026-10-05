package daemonhost

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

func TestWebhookModeVerifiesDeliveriesWithTheOperatorsSecret(t *testing.T) {
	t.Parallel()
	var console bytes.Buffer
	configPath := appHubConfig(t, webhookSecret)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8801", narrate.Writer{Out: &console}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })

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

func TestWithoutAnAppEverythingWebhookIsOff(t *testing.T) {
	t.Parallel()
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
	t.Cleanup(func() { _ = mount.Close() })
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

func TestWebhookModeRefusesUnusableSecretFiles(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
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

func TestLocalEntitlementsBelongToTheOneLocalIdentity(t *testing.T) {
	t.Parallel()
	entitlements := localRepositoryEntitlements{identityID: localIdentityID}
	allowed, err := entitlements.IdentityHasRepositoryEntitlement(context.Background(), localIdentityID, 123, 987)
	if err != nil || !allowed {
		t.Fatalf("local identity = %t, %v", allowed, err)
	}
	if allowed, err := entitlements.IdentityHasRepositoryEntitlement(context.Background(), "someone-else", 123, 987); allowed || err == nil {
		t.Fatalf("another identity = %t, %v", allowed, err)
	}
}

func TestInstallationStateSecretIsDerivedNotReused(t *testing.T) {
	t.Parallel()
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

func TestConnectRoutesAnswer503WithoutAnOAuthVerifier(t *testing.T) {
	t.Parallel()
	configPath := appHubConfig(t, webhookSecret)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8804", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	if mount.Webhook.Installations() == nil {
		t.Fatal("webhook mode must wire the installation service")
	}
	// The route is the owner's alone; the limit is what the owner is told.
	mount.authorizeOwnerWith(func(*http.Request) bool { return true })
	handler := mount.handlers()[hub.APIPrefix+"/"]
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, hub.InstallationConnectPath, strings.NewReader("{}"))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "installation_connection_unavailable") {
		t.Fatalf("connect = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPollIntervalPrefersTheTestOverride(t *testing.T) {
	t.Parallel()
	configPath := memoryHubConfig(t)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8805", narrate.Writer{}, &Tuning{PollInterval: 5 * time.Millisecond})
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	if mount.Interval != 5*time.Millisecond {
		t.Fatalf("interval = %s", mount.Interval)
	}
}
