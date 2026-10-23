package daemonhost

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func emptyMachineCollectors() cockpitfleet.Collectors {
	return cockpitfleet.Collectors{Repositories: emptyMachine{}, Sessions: emptyMachine{}, Runs: emptyMachine{}, PullRequests: emptyMachine{}}
}

func refreshedSnapshotter(t *testing.T, name string) *cockpitfleet.Snapshotter {
	t.Helper()
	snapshotter := cockpitfleet.New(cockpitfleet.Options{
		Machine: name, Version: "v1.2.3", Collectors: emptyMachineCollectors(), Now: func() time.Time { return exportTestNow },
	})
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return snapshotter
}

func exportTestMount(t *testing.T, address string) (mount *hubMount, get func(target string, headers ...string) *httptest.ResponseRecorder, owner, tokenFile string) {
	t.Helper()
	configPath := memoryHubConfig(t)
	var ticks atomic.Int64
	tuning := &Tuning{Now: func() time.Time { return exportTestNow.Add(time.Duration(ticks.Add(1)) * time.Second) }}
	mount, err := mountHub(context.Background(), configPath, address, narrate.Writer{}, tuning)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	api := mount.handlers()[hub.APIPrefix+"/"]
	get = func(target string, headers ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		for index := 0; index < len(headers); index += 2 {
			request.Header.Set(headers[index], headers[index+1])
		}
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		return recorder
	}
	remote, err := remotestate.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(remote.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	return mount, get, "Bearer " + strings.TrimSpace(string(raw)), remote.TokenFile
}

func fleetBody(snapshotter *cockpitfleet.Snapshotter) []byte {
	recorder := httptest.NewRecorder()
	cockpit.ServePayload(recorder, httptest.NewRequest(http.MethodGet, "/", nil), snapshotter.Payload())
	return recorder.Body.Bytes()
}

func fakeAppDeliveriesServer(t *testing.T, deliveredAt time.Time) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/hook/deliveries", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `[{"id":2,"guid":"wiring-evidence","delivered_at":%q,"redelivery":false,"status_code":200,"event":"push"},{"id":1,"guid":"wiring-guid","delivered_at":%q,"redelivery":false,"status_code":500,"event":"push"}]`,
			deliveredAt.Add(1*time.Minute).UTC().Format(time.RFC3339), deliveredAt.UTC().Format(time.RFC3339))
	})
	mux.HandleFunc("/app/hook/deliveries/1/attempts", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func hubTestConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func memoryHubConfig(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	token := filepath.Join(state, "github.token")
	if err := os.WriteFile(token, []byte("ghp_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return hubTestConfig(t, fmt.Sprintf(memoryHubSection, state, token))
}

func hubStoreDirectoryFromConfig(t *testing.T, configPath string) string {
	t.Helper()
	cfg, found, err := hubconfig.Load(configPath)
	if err != nil || !found {
		t.Fatalf("hubconfig.Load(%s) = %t, %v", configPath, found, err)
	}
	return hubStateDirectory(cfg)
}

func hubConfigWithPath(path string) hubconfig.Config {
	return hubconfig.Config{Store: hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path}}
}

func appHubConfig(t *testing.T, secret string) string {
	t.Helper()
	return appHubConfigWithKey(t, secret, testPrivateKeyPEM)
}

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

func peerAdminTestMount(t *testing.T) (*hubMount, http.Handler) {
	t.Helper()
	configPath := memoryHubConfig(t)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:0", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	handler := daemonruntime.AuthenticatedHandler("owner-token", newPeerAdminHTTPHandler(mount))
	return mount, handler
}

func peerAdminRequest(t *testing.T, handler http.Handler, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func peerRecordFixture(name string) hub.PeerRecord {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	// MachineID must match hub.MachineID("local", name) exactly: that is the
	// key PeerAdminService.Invite/Block/etc. compute and look up by, and a
	// mismatched fixture ID would make every "existing peer" lookup silently
	// miss.
	return hub.PeerRecord{MachineID: hub.MachineID("local", name), Name: name, IdentityID: "local", Trust: hub.PeerTrustActive, CreatedAt: now, TrustChangedAt: now}
}

type emptyMachine struct{}

func (emptyMachine) Repositories(context.Context) ([]discover.Repo, error) { return nil, nil }
func (emptyMachine) Sessions(context.Context) ([]session.View, error)      { return nil, nil }
func (emptyMachine) Runs(context.Context) ([]agents.Result, error)         { return nil, nil }
func (emptyMachine) PullRequests(context.Context) ([]worktrees.RegisteredPullRequestBinding, error) {
	return nil, nil
}

const memoryHubSection = "hub:\n  store:\n    engine: memory\n    path: %s\n  github:\n    token_file: %s\n"

const webhookSecret = "0123456789abcdef0123456789abcdef"

const testPrivateKeyPEM = "-----BEGIN RSA PRIVATE KEY-----\nnot-a-real-key\n-----END RSA PRIVATE KEY-----\n"

const pushPayload = `{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":987,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

const exitUsage = 2

func usageError(message string) error { return &exitError{code: exitUsage, message: message} }

var realTestAppPrivateKeyPEM = generateRealTestAppPrivateKeyPEM()

func generateRealTestAppPrivateKeyPEM() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

var exportTestNow = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

type unresolvableCredentials struct{}

func (unresolvableCredentials) ResolveMachineCredential(context.Context, hub.MachineTokenDigest) (hub.MachineCredentialBinding, error) {
	return hub.MachineCredentialBinding{}, errors.New("no credential")
}

type emptySnapshots struct{}

func (emptySnapshots) StoreLatest(context.Context, hub.StoredMachineSnapshot) (hub.MachineSnapshotStoreResult, error) {
	panic("not used")
}
func (emptySnapshots) ListLatest(context.Context) ([]hub.StoredMachineSnapshot, error) {
	return nil, nil
}

type noEntitlements struct{}

func (noEntitlements) IdentityHasRepositoryEntitlement(context.Context, string, int64, int64) (bool, error) {
	return false, nil
}
