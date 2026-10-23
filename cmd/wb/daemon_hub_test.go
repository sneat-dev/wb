package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonhost"
	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/web"
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

// lockedBuffer is a buffer several goroutines may write to.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
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
	pinDaemonHome(t, root)
	projectsRoot := root

	configPath := memoryHubConfig(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	deps.HubConfigPath = func() string { return configPath }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	// The daemon writes its log from several goroutines; the buffer is locked.
	var stdout bytes.Buffer
	stderr := &lockedBuffer{}
	command.SetOut(&stdout)
	command.SetErr(stderr)

	served := make(chan error, 1)
	go func() {
		served <- newDaemonHost(deps).Serve(command.Context(), daemonhost.Request{ProjectsRoot: projectsRoot, Listen: address, Quiet: false, ManagedStart: false}, command.OutOrStdout(), command.ErrOrStderr())
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
		// The dashboard's own data feed. These routes exist on the read API
		// that shares the loopback listener with the hub, so a 200 with the
		// documented shape is what proves the two handlers were composed
		// rather than one of them silently owning the whole prefix.
		githubapp.APIPrefix + "/dashboard": func(t *testing.T, response *http.Response, body string) {
			if response.StatusCode != http.StatusOK {
				t.Fatalf("dashboard read API = %s %q", response.Status, body)
			}
			var payload struct {
				Summary map[string]int `json:"summary"`
				Fleet   *struct {
					Machines []json.RawMessage `json:"machines"`
				} `json:"fleet"`
			}
			if err := json.Unmarshal([]byte(body), &payload); err != nil {
				t.Fatalf("dashboard payload = %q: %v", body, err)
			}
			for _, field := range []string{"repositories", "open_pulls", "merged_pulls", "open_issues", "releases"} {
				if _, ok := payload.Summary[field]; !ok {
					t.Fatalf("dashboard summary is missing %q in %q", field, body)
				}
			}
			if payload.Fleet == nil {
				t.Fatalf("dashboard has no fleet projection: %q", body)
			}
		},
		githubapp.APIPrefix + "/worktrees": func(t *testing.T, response *http.Response, body string) {
			if response.StatusCode != http.StatusOK {
				t.Fatalf("worktrees read API = %s %q", response.Status, body)
			}
			if !strings.Contains(body, `"rows"`) {
				t.Fatalf("worktrees payload = %q", body)
			}
		},
	} {
		t.Run(path, func(t *testing.T) {
			response, body := fetch(t, "http://"+address+path)
			check(t, response, body)
		})
	}

	// The served hub refuses a write from an anonymous local caller and takes
	// one with the owner's machine credential
	// (self-hosted-bench#ac:hub-writes-need-a-credential), over the real listener.
	const coverage = `{"repository":"sneat-dev/wb","statements":10,"covered":9}`
	if status, body := postToServedHub(t, address, hub.CoveragePath, coverage, ""); status != http.StatusUnauthorized || !strings.Contains(body, "coverage_unauthorized") {
		t.Fatalf("anonymous coverage write = %d %q, want 401", status, body)
	}
	if response, body := fetch(t, "http://"+address+hub.CoveragePath); response.StatusCode != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("coverage after a refused write = %s %q, want an empty list", response.Status, body)
	}
	if status, body := postToServedHub(t, address, hub.CoveragePath, coverage, localHubBearer(t, configPath)); status != http.StatusCreated {
		t.Fatalf("coverage write with the owner's machine bearer = %d %q, want 201", status, body)
	}

	if line := stderr.String(); !strings.Contains(line, "WB hub: engine=memory") || !strings.Contains(line, "dashboard=http://"+address+"/workbench/dashboard/") {
		t.Fatalf("start line = %q", line)
	}
}

func waitForHealth(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := daemonruntime.DefaultDependencies(usageError).Health(context.Background(), address); err == nil {
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

// TestEnsureLocalEnrollmentSkipsAnAlreadyResolvableCredential is the
// restart-with-a-durable-store case the memory engine cannot express: the
// credential resolves, so nothing is written.

// TestDaemonStatusReportsTheMountedHub covers both the text and JSON shapes
// `wb daemon status` gained.
func TestDaemonStatusReportsTheMountedHub(t *testing.T) {
	root := daemonTestRoot(t)
	projectsRoot := root

	configPath := memoryHubConfig(t)
	deps := daemonTestDependencies(t, root)
	deps.HubConfigPath = func() string { return configPath }

	run := func(t *testing.T, args ...string) string {
		t.Helper()
		command := daemonCommandForTest("status", &invocation{projectsRoot: projectsRoot}, deps)
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
	deps.HubConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	if text := run(t); !strings.Contains(text, "hub_mounted=false") || strings.Contains(text, "hub_engine") {
		t.Fatalf("status without a hub section = %q", text)
	}

	// A hub section that will not load is reported as absent rather than
	// failing status, which is when an operator needs it most.
	broken := hubTestConfig(t, "hub:\n  store:\n    engine: postgres\n")
	deps.HubConfigPath = func() string { return broken }
	if text := run(t); !strings.Contains(text, "hub_mounted=false") {
		t.Fatalf("status with an invalid hub section = %q", text)
	}
}

// TestMountHubDoesNothingWithoutAHubSection is the contract for every
// operator who does not self-host.

// TestHubPepperRefusesATruncatedFile keeps a corrupted pepper from silently
// weakening every digest derived from it.

// TestLocalMachineNamePrefersTheConfiguredName keeps one machine name across a
// later move to the hosted instance.

// TestFixedViewerResolverAlwaysReturnsTheLocalIdentity is what replaces
// sign-in on a listener only this machine can reach.

// hubStoreDirectoryFromConfig resolves the directory the pepper lives in, the
// same way buildHubMount does.
