package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/web"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// hubTestConfig writes a wb.yaml carrying only a memory-engine hub section and
// returns its path. Memory is the engine the feature names for throwaway runs,
// and it keeps the test free of any durable state outside its temp directory.
func hubTestConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const memoryHubSection = "hub:\n  store:\n    engine: memory\n    path: %s\n  github:\n    token_file: %s\n"

func memoryHubConfig(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	token := filepath.Join(state, "github.token")
	if err := os.WriteFile(token, []byte("ghp_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return hubTestConfig(t, fmt.Sprintf(memoryHubSection, state, token))
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// TestServeDashboardMountsTheHubAndDashboard is the serve-and-fetch journey:
// one loopback listener answers the existing read-only API, the hub API, and
// the embedded bench dashboard, and the hub's own route families are reachable
// rather than 404 or 503.
func TestServeDashboardMountsTheHubAndDashboard(t *testing.T) {
	// The daemon's local transport is a unix socket under the projects root,
	// and the platform caps a socket path at ~104 bytes — well below what
	// t.TempDir() produces on macOS.
	root, err := os.MkdirTemp("/tmp", "wb-hub-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	configPath := memoryHubConfig(t)
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = func() string { return configPath }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	served := make(chan error, 1)
	go func() {
		served <- serveDashboard(command, deps, address, daemon.Store{Path: daemonStatePath(root)}, "owner-token")
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serveDashboard: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveDashboard did not return after its context was cancelled")
		}
	})

	waitForHealth(t, address)

	for path, check := range map[string]func(*testing.T, *http.Response, string){
		"/api/v1/health": func(t *testing.T, response *http.Response, body string) {
			if response.StatusCode != http.StatusOK || !strings.Contains(body, `"ready"`) {
				t.Fatalf("health = %s %q", response.Status, body)
			}
		},
		hub.StatusPath: func(t *testing.T, response *http.Response, body string) {
			// The route family is mounted: anything but 404 or 503 proves the
			// handler and its StatusService are wired. The fixed local viewer
			// makes 200 the expected answer.
			if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusServiceUnavailable {
				t.Fatalf("hub status = %s %q; the route family is not mounted", response.Status, body)
			}
			if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("hub status = %s %q", response.Status, body)
			}
		},
		web.MountPath + "dashboard/": func(t *testing.T, response *http.Response, body string) {
			if response.StatusCode != http.StatusOK {
				t.Fatalf("dashboard = %s %q", response.Status, body)
			}
			// A wb built from a clean clone serves the not-built page; a wb
			// built after `pnpm build` serves the real dashboard. Both are
			// correct, and nothing else is.
			if web.Built() {
				if !strings.Contains(body, `data-dashboard-scope="user"`) {
					t.Fatalf("dashboard body = %q", body)
				}
			} else if !strings.Contains(body, "not built") {
				t.Fatalf("unbuilt dashboard body = %q", body)
			}
		},
	} {
		t.Run(path, func(t *testing.T) {
			response, body := fetch(t, "http://"+address+path)
			check(t, response, body)
		})
	}

	if line := stderr.String(); !strings.Contains(line, "WB hub: engine=memory") || !strings.Contains(line, "dashboard=http://"+address+"/bench/dashboard/") {
		t.Fatalf("start line = %q", line)
	}
}

func waitForHealth(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := daemonHealthy(context.Background(), address); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon on %s never became healthy", address)
}

func fetch(t *testing.T, target string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response, string(body)
}

// TestMountHubEnrolsTheLocalMachineExactlyOnce is the idempotence promise: a
// restart must not mint a second credential, because the daemon would then be
// talking to its own hub with a token the previous run's file no longer holds.
func TestMountHubEnrolsTheLocalMachineExactlyOnce(t *testing.T) {
	ctx := context.Background()
	configPath := memoryHubConfig(t)
	address := "127.0.0.1:8799"

	first, err := mountHub(ctx, configPath, address)
	if err != nil || first == nil {
		t.Fatalf("mountHub = %v, %v", first, err)
	}
	defer func() { _ = first.Close() }()

	cfg, err := remotestate.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("remote section after enrolment: %v", err)
	}
	if cfg.Provider != "hub" || cfg.URL != "http://"+address || cfg.Machine != first.Machine {
		t.Fatalf("remote section = %+v", cfg)
	}
	wantToken := filepath.Join(filepath.Dir(configPath), "credentials", "hub-local-"+first.Machine+".token")
	if cfg.TokenFile != wantToken {
		t.Fatalf("token file = %q, want %q", cfg.TokenFile, wantToken)
	}
	info, err := os.Stat(wantToken)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file = %v, %v; want mode 0600", info, err)
	}
	token, err := os.ReadFile(wantToken)
	if err != nil {
		t.Fatal(err)
	}

	// The pepper is the thing that must survive: regenerating it would make
	// every stored digest unresolvable, so the second mount would silently
	// re-enrol.
	pepper, err := os.ReadFile(filepath.Join(hubStoreDirectoryFromConfig(t, configPath), "pepper"))
	if err != nil {
		t.Fatal(err)
	}

	// A second mount over the SAME store (memory is per-process, so the store
	// is reopened empty) must re-enrol; over a store that still holds the
	// credential it must not. Re-running mountHub here proves the detection
	// itself: the token file and remote section are unchanged, and the only
	// reason a new credential is minted is that the memory store lost it.
	second, err := mountHub(ctx, configPath, address)
	if err != nil || second == nil {
		t.Fatalf("second mountHub = %v, %v", second, err)
	}
	defer func() { _ = second.Close() }()
	if second.Machine != first.Machine {
		t.Fatalf("machine name changed across restarts: %q then %q", first.Machine, second.Machine)
	}
	repeatedPepper, err := os.ReadFile(filepath.Join(hubStoreDirectoryFromConfig(t, configPath), "pepper"))
	if err != nil || !bytes.Equal(pepper, repeatedPepper) {
		t.Fatalf("pepper was regenerated across restarts")
	}
	newToken, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(token, newToken) {
		t.Fatal("the credential was reused even though the memory store lost it; enrolment is not actually verified")
	}
}

// TestEnsureLocalEnrollmentSkipsAnAlreadyResolvableCredential is the
// restart-with-a-durable-store case the memory engine cannot express: the
// credential resolves, so nothing is written.
func TestEnsureLocalEnrollmentSkipsAnAlreadyResolvableCredential(t *testing.T) {
	ctx := context.Background()
	configPath := memoryHubConfig(t)
	address := "127.0.0.1:8798"
	pepper := []byte(strings.Repeat("p", 32))

	backend, closer, err := hubstore.Open(ctx, hubconfig.Store{Engine: hubconfig.EngineMemory})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	credentials, resolver, _ := hub.NewMachineStores(backend)
	enrollment := &hub.MachineEnrollmentService{Store: credentials, Pepper: pepper}
	viewer := hub.Viewer{Authenticated: true, IdentityID: localIdentityID, DisplayName: "laptop"}

	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, "laptop", pepper, address); err != nil {
		t.Fatalf("first enrolment: %v", err)
	}
	cfg, loadErr := remotestate.LoadConfig(configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	token, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}

	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, "laptop", pepper, address); err != nil {
		t.Fatalf("second enrolment: %v", err)
	}
	repeated, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(token, repeated) {
		t.Fatal("a resolvable credential was rotated on restart")
	}

	// A pepper change is the one thing that must force a fresh credential:
	// the stored digest can no longer be reproduced from the token on disk.
	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, "laptop", []byte(strings.Repeat("q", 32)), address); err != nil {
		t.Fatalf("enrolment after a pepper change: %v", err)
	}
	rotated, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(token, rotated) {
		t.Fatal("an unresolvable credential was kept")
	}
}

// TestDaemonStatusReportsTheMountedHub covers both the text and JSON shapes
// `wb daemon status` gained.
func TestDaemonStatusReportsTheMountedHub(t *testing.T) {
	root := t.TempDir()
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	configPath := memoryHubConfig(t)
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = func() string { return configPath }

	run := func(t *testing.T, args ...string) string {
		t.Helper()
		command := newDaemonStatusCmd(deps)
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}

	text := run(t)
	for _, want := range []string{"hub_mounted=true", "hub_engine=memory", "hub_store="} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text %q does not contain %q", text, want)
		}
	}

	var result daemonResult
	if err := json.Unmarshal([]byte(run(t, "--json")), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Hub.Mounted || result.Hub.Engine != "memory" || result.Hub.Store == "" {
		t.Fatalf("hub status = %+v", result.Hub)
	}

	// Without a hub section nothing is claimed, and the text stays the shape
	// it was plus one honest false.
	deps.hubConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	if text := run(t); !strings.Contains(text, "hub_mounted=false") || strings.Contains(text, "hub_engine") {
		t.Fatalf("status without a hub section = %q", text)
	}

	// A hub section that will not load is reported as absent rather than
	// failing status, which is when an operator needs it most.
	broken := hubTestConfig(t, "hub:\n  store:\n    engine: postgres\n")
	deps.hubConfigPath = func() string { return broken }
	if text := run(t); !strings.Contains(text, "hub_mounted=false") {
		t.Fatalf("status with an invalid hub section = %q", text)
	}
}

// TestMountHubDoesNothingWithoutAHubSection is the contract for every
// operator who does not self-host.
func TestMountHubDoesNothingWithoutAHubSection(t *testing.T) {
	mount, err := mountHub(context.Background(), filepath.Join(t.TempDir(), "absent.yaml"), "127.0.0.1:8797")
	if err != nil || mount != nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	if mount.handlers() != nil || mount.StartLine() != "" || mount.Close() != nil {
		t.Fatal("a nil mount is not inert")
	}
}

func TestMountHubSurfacesAnInvalidSection(t *testing.T) {
	if _, err := mountHub(context.Background(), hubTestConfig(t, "hub:\n  store:\n    engine: postgres\n"), "127.0.0.1:8796"); err == nil {
		t.Fatal("an invalid hub section was mounted")
	}
	// An engine that cannot open is a mount failure, not a silent no-op.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mountHub(context.Background(), hubTestConfig(t, "hub:\n  store:\n    engine: ingitdb\n    path: "+blocked+"\n"), "127.0.0.1:8796"); err == nil {
		t.Fatal("an unopenable store was mounted")
	}
}

// TestHubPepperRefusesATruncatedFile keeps a corrupted pepper from silently
// weakening every digest derived from it.
func TestHubPepperRefusesATruncatedFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "pepper"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hubPepper(directory); err == nil {
		t.Fatal("a truncated pepper was accepted")
	}
	if _, err := hubPepper(""); err == nil {
		t.Fatal("an unresolvable hub state directory was accepted")
	}
	// A regular file where the directory should be is the portable way to make
	// both the read and the MkdirAll fail.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hubPepper(filepath.Join(file, "nested")); err == nil {
		t.Fatal("an unusable hub state directory was accepted")
	}
}

// TestLocalMachineNamePrefersTheConfiguredName keeps one machine name across a
// later move to the hosted instance.
func TestLocalMachineNamePrefersTheConfiguredName(t *testing.T) {
	configured := hubTestConfig(t, "remote:\n  provider: git\n  repo: sneat-dev/wb\n  machine: workhorse\n")
	if name, err := localMachineName(configured); err != nil || name != "workhorse" {
		t.Fatalf("localMachineName = %q, %v", name, err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname on this machine")
	}
	host, _, _ = strings.Cut(host, ".")
	if name, err := localMachineName(filepath.Join(t.TempDir(), "absent.yaml")); err != nil || name != host {
		t.Fatalf("localMachineName = %q, %v; want the hostname %q", name, err, host)
	}
}

func TestHubStateDirectoryFollowsTheConfiguredPath(t *testing.T) {
	if got := hubStateDirectory(hubConfigWithPath("/var/bench")); got != "/var/bench" {
		t.Fatalf("hubStateDirectory = %q", got)
	}
}

// TestFixedViewerResolverAlwaysReturnsTheLocalIdentity is what replaces
// sign-in on a listener only this machine can reach.
func TestFixedViewerResolverAlwaysReturnsTheLocalIdentity(t *testing.T) {
	resolver := fixedViewerResolver{viewer: hub.Viewer{Authenticated: true, IdentityID: localIdentityID, DisplayName: "laptop"}}
	viewer, err := resolver.Viewer(nil)
	if err != nil || !viewer.Authenticated || viewer.IdentityID != localIdentityID {
		t.Fatalf("viewer = %+v, %v", viewer, err)
	}
}

func TestEnsureLocalEnrollmentSurfacesAnEnrolmentFailure(t *testing.T) {
	// A service without a pepper cannot mint a credential, and the failure
	// must name the machine rather than leaving remote: half written.
	err := ensureLocalEnrollment(context.Background(), &hub.MachineEnrollmentService{}, unresolvableCredentials{},
		hub.Viewer{Authenticated: true, IdentityID: localIdentityID}, hubTestConfig(t, ""), "laptop", []byte(strings.Repeat("p", 32)), "127.0.0.1:8795")
	if err == nil || !strings.Contains(err.Error(), "laptop") {
		t.Fatalf("ensureLocalEnrollment = %v", err)
	}
}

type unresolvableCredentials struct{}

func (unresolvableCredentials) ResolveMachineCredential(context.Context, hub.MachineTokenDigest) (hub.MachineCredentialBinding, error) {
	return hub.MachineCredentialBinding{}, errors.New("no credential")
}

// hubStoreDirectoryFromConfig resolves the directory the pepper lives in, the
// same way buildHubMount does.
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
