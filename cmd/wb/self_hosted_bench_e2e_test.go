package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/hub/web"
	"github.com/sneat-dev/wb/internal/daemon"
)

// e2eRepository is the one repository the journey is about. It is spelled the
// way GitHub spells it because the fake API, the machine snapshot and the
// canonical clone's origin all have to agree on it.
const e2eRepository = "acme/app"

// e2eBudget bounds the whole journey. Every wait below is a fraction of it,
// so a failure names the step that stalled rather than timing out the suite.
const e2eBudget = 45 * time.Second

// TestSelfHostedBenchWholeJourney walks spec/features/self-hosted-bench from
// end to end in one process, with nothing mocked between the daemon's front
// door and the canonical clone on disk:
//
//   - `wb daemon serve` mounts the hub and the embedded dashboard on a
//     loopback port, on the memory engine;
//   - the machine enrols itself and publishes an inventory of one repository;
//   - the poller reads a fake GitHub API, sees the default branch move, and
//     enqueues the same event a webhook would have;
//   - the daemon's own event receiver polls its own hub, fast-forwards the
//     canonical clone and acknowledges the event;
//   - the feature worktree is not touched, and /api/v1/health reports the
//     event received and acknowledged.
//
// Only GitHub is fake: a local bare repository stands in for the remote, and
// a fake ssh command makes `git@github.com:acme/app.git` resolve to it so the
// origin check the processor performs is the real one.
func TestSelfHostedBenchWholeJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("the whole-journey end-to-end test starts a daemon and drives git")
	}
	deadline := time.Now().Add(e2eBudget)

	// The daemon's local transport is a unix socket under the projects root,
	// and the platform caps a socket path at ~104 bytes.
	root, err := os.MkdirTemp("/tmp", "wb-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	remote := newFakeGitHubRemote(t)
	canonical := remote.clone(t, filepath.Join(root, e2eRepository))
	worktree := remote.addWorktree(t, canonical, filepath.Join(root, ".worktrees", "app-feature"))
	worktreeHead := gitOutput(t, worktree, "rev-parse", "HEAD")

	api := newFakeGitHubAPI(t, gitOutput(t, canonical, "rev-parse", "HEAD"))

	configPath := filepath.Join(root, "wb.yaml")
	tokenFile := filepath.Join(root, "github.token")
	if err := os.WriteFile(tokenFile, []byte("ghp_journey\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("hub:\n  store:\n    engine: memory\n    path: "+root+"\n  github:\n    token_file: "+tokenFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = func() string { return configPath }
	// A test cannot wait out the 30s configuration floor, and its GitHub has
	// no rate limit to protect.
	deps.hubTuning = &hubTuning{APIBaseURL: api.URL, PollInterval: 150 * time.Millisecond}

	address := freeLoopbackAddress(t)
	console := &journal{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := &cobra.Command{}
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(console)
	served := make(chan error, 1)
	go func() {
		served <- serveDashboard(command, deps, address, daemon.Store{Path: daemonStatePath(root)}, "owner-token", false)
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

	// The dashboard the operator opens. A checkout where `pnpm build` has
	// never run embeds only the placeholder, and the page then says so — the
	// release build asserted in .goreleaser.yml is what guarantees a shipped
	// binary is never in that state.
	response, body := fetch(t, "http://"+address+web.MountPath+"dashboard/")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("dashboard = %s", response.Status)
	}
	if web.Built() {
		if !strings.Contains(body, `data-dashboard-scope="user"`) {
			t.Fatalf("dashboard did not render the user scope: %s", body)
		}
	} else if !strings.Contains(body, "was not built into this wb binary") {
		t.Fatalf("unbuilt dashboard = %s", body)
	}

	machine := publishInventory(t, address, configPath)

	// The first observation of a repository is a baseline: its head was not
	// pushed by anything the daemon missed, so nothing is enqueued for it.
	waitFor(t, deadline, "the poller's first observation", func() bool {
		return console.contains("github.com/" + e2eRepository)
	})

	pushed := remote.push(t, "a commit made on another computer")
	api.setHead(pushed)

	waitFor(t, deadline, "the narrated line naming the repository and the machine", func() bool {
		return console.contains("github.com/"+e2eRepository) && console.contains("queued for "+machine)
	})
	waitFor(t, deadline, "the canonical clone to fast-forward", func() bool {
		return gitOutput(t, canonical, "rev-parse", "HEAD") == pushed
	})
	if head := gitOutput(t, worktree, "rev-parse", "HEAD"); head != worktreeHead {
		t.Fatalf("the feature worktree moved from %s to %s", worktreeHead, head)
	}
	waitFor(t, deadline, "the hub to report the event received and acknowledged", func() bool {
		health, err := daemonHubHealth(context.Background(), address)
		return err == nil && health.LastEventReceived != nil && health.LastEventAcknowledged != nil
	})
	health, err := daemonHubHealth(context.Background(), address)
	if err != nil || health.LastEventReceived.Event != "default_branch_updated" {
		t.Fatalf("hub health = %+v, %v", health, err)
	}
	if health.LastEventAcknowledged.ID != health.LastEventReceived.ID {
		t.Fatalf("the acknowledged event is not the received one: %+v", health)
	}
}

// waitFor polls condition until it holds or the journey's budget runs out,
// naming the step so a failure says which one stalled.
func waitFor(t *testing.T, deadline time.Time, step string, condition func() bool) {
	t.Helper()
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", step)
}

// journal is the daemon's stderr, readable from the test goroutine while the
// daemon writes to it.
type journal struct {
	mu    sync.Mutex
	lines bytes.Buffer
}

func (buffer *journal) Write(payload []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.lines.Write(payload)
}

func (buffer *journal) contains(text string) bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return strings.Contains(buffer.lines.String(), text)
}

// publishInventory enrols nothing new: it publishes the machine snapshot the
// poller polls from, through the hub's own authenticated route with the
// credential the daemon wrote for itself on start. It returns the machine
// name the narration will use.
func publishInventory(t *testing.T, address, configPath string) string {
	t.Helper()
	machine, err := localMachineName(configPath)
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(filepath.Dir(configPath), "credentials", fmt.Sprintf("hub-local-%s.token", machine))
	raw, err := os.ReadFile(tokenPath) //nolint:gosec // written by the daemon under this test's own root.
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(machinesnapshot.Snapshot{
		SchemaVersion: machinesnapshot.SchemaVersion,
		Login:         "local",
		Machine:       machine,
		PublishedAt:   time.Now().UTC(),
		Repositories:  []string{"github.com/" + e2eRepository},
		Worktrees:     []machinesnapshot.Worktree{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+address+machinesnapshot.SnapshotPath, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("publish machine snapshot = %s", response.Status)
	}
	return machine
}

// fakeGitHubAPI serves the two documents the poller reads.
type fakeGitHubAPI struct {
	*httptest.Server
	mu   sync.Mutex
	head string
}

func newFakeGitHubAPI(t *testing.T, head string) *fakeGitHubAPI {
	t.Helper()
	api := &fakeGitHubAPI{head: head}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/"+e2eRepository, func(writer http.ResponseWriter, _ *http.Request) {
		writeJSONFor(writer, map[string]any{"id": 42, "full_name": e2eRepository, "default_branch": "main"})
	})
	mux.HandleFunc("GET /repos/"+e2eRepository+"/commits/main", func(writer http.ResponseWriter, _ *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		writeJSONFor(writer, map[string]any{"sha": api.head})
	})
	api.Server = httptest.NewServer(mux)
	t.Cleanup(api.Close)
	return api
}

func (api *fakeGitHubAPI) setHead(sha string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.head = sha
}

func writeJSONFor(writer http.ResponseWriter, value map[string]any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

// fakeGitHubRemote is a bare repository that answers as
// git@github.com:acme/app.git, so the origin verification and the
// fast-forward the processor performs are both the production ones.
type fakeGitHubRemote struct {
	bare string
	seed string
}

func newFakeGitHubRemote(t *testing.T) *fakeGitHubRemote {
	t.Helper()
	// The remote lives outside the projects root: it is a server, not a
	// checkout wb should ever discover.
	origin, err := os.MkdirTemp("/tmp", "wb-e2e-origin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(origin) })

	remote := &fakeGitHubRemote{bare: filepath.Join(origin, "app.git"), seed: filepath.Join(origin, "seed")}
	git(t, origin, "init", "--bare", "--initial-branch=main", remote.bare)
	git(t, origin, "init", "--initial-branch=main", remote.seed)
	configureGitIdentity(t, remote.seed)
	if err := os.WriteFile(filepath.Join(remote.seed, "README.md"), []byte("bench\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, remote.seed, "add", ".")
	git(t, remote.seed, "commit", "-m", "the commit the clone was made from")
	git(t, remote.seed, "remote", "add", "origin", remote.bare)
	git(t, remote.seed, "push", "origin", "main")

	// GitHub's SSH endpoint, for this test, is a script that serves the bare
	// repository regardless of the host it is asked for. It makes
	// `git@github.com:acme/app.git` a URL git can actually fetch while
	// `git remote get-url` still reports the identity the processor checks.
	script := filepath.Join(origin, "github-ssh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec git-upload-pack "+remote.bare+"\n"), 0o700); err != nil { //nolint:gosec // an executable stub inside this test's own temporary directory.
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", script)
	return remote
}

// clone makes the canonical checkout the daemon will fast-forward.
func (remote *fakeGitHubRemote) clone(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(path), "clone", remote.bare, path)
	configureGitIdentity(t, path)
	git(t, path, "remote", "set-url", "origin", "git@github.com:"+e2eRepository+".git")
	return path
}

// addWorktree is the feature worktree the journey promises stays untouched.
func (remote *fakeGitHubRemote) addWorktree(t *testing.T, canonical, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, canonical, "worktree", "add", "-b", "feature", path)
	return path
}

// push adds one commit to the remote's default branch, the way a push from
// another computer would, and returns its SHA.
func (remote *fakeGitHubRemote) push(t *testing.T, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(remote.seed, "CHANGELOG.md"), []byte(message+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, remote.seed, "add", ".")
	git(t, remote.seed, "commit", "-m", message)
	git(t, remote.seed, "push", "origin", "main")
	return gitOutput(t, remote.seed, "rev-parse", "HEAD")
}

func configureGitIdentity(t *testing.T, path string) {
	t.Helper()
	git(t, path, "config", "user.email", "journey@example.test")
	git(t, path, "config", "user.name", "Bench Journey")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitOutput(t, dir, args...)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
