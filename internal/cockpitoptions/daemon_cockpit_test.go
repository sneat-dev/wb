package cockpitoptions

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/prwatch"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCockpitFleetOptionsReadThisMachineAndTheConfiguredRemote(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	host := func() (string, error) { return "the-host", nil }
	config := wbconfig.DefaultCockpitConfig()
	config.RefreshInterval = 90 * time.Second
	var logs bytes.Buffer

	bare := testOptions(root, home, filepath.Join(t.TempDir(), "absent.yaml"), config, &logs, host, SSH{})
	if bare.Machine != "the-host" || bare.Collectors.Remote != nil || bare.Interval != 90*time.Second || bare.Collectors.Repositories == nil || bare.Collectors.CodeIndex == nil {
		t.Errorf("options with no remote section = %+v", bare)
	}
	if hardware := bare.Hardware; hardware.OS != runtime.GOOS || hardware.Arch != runtime.GOARCH || hardware.CPUCount != runtime.NumCPU() {
		t.Errorf("hardware = %+v, want this machine's", hardware)
	}
	if bare.Sampler == nil {
		t.Error("this machine has no metrics sampler")
	}
	if bare.Collectors.Activity == nil {
		t.Error("this machine does not read herdr for agent activity")
	}
	if terminals, ok := bare.Terminals.(*cockpitfleet.LocalTerminals); !ok || terminals.ProjectsRoot != root || terminals.Home != home {
		t.Errorf("the throughput source = %+v, want this machine's terminal records", bare.Terminals)
	}
	bare.Logf("refresh failed: %v", "boom")
	if got := logs.String(); got != "wb: refresh failed: boom\n" {
		t.Errorf("log = %q", got)
	}
	nameless := testOptions(root, home, filepath.Join(t.TempDir(), "absent.yaml"), config, &logs, func() (string, error) { return "", io.EOF }, SSH{})
	if nameless.Machine != "local" {
		t.Errorf("machine without a host name = %q, want local", nameless.Machine)
	}
	remote := testOptions(root, home, cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-1\n")(), config, &logs, host, SSH{})
	if remote.Machine != "laptop-1" || remote.Collectors.Remote == nil {
		t.Errorf("options with a remote section = machine %q, remote %v", remote.Machine, remote.Collectors.Remote)
	}

	// A hub provider and an unlocatable store know no other machines, and the
	// daemon's log says so, once, while the options are built.
	logs.Reset()
	hub := testOptions(root, home, cockpitConfigFile(t, "remote:\n  provider: hub\n  url: https://hub.example\n  token_file: /tmp/token\n  machine: laptop-2\n")(), config, &logs, host, SSH{})
	if hub.Machine != "laptop-2" || hub.Collectors.Remote != nil || !strings.Contains(logs.String(), "not read from the hub remote provider") {
		t.Errorf("options with a hub provider = machine %q, remote %v, log %q", hub.Machine, hub.Collectors.Remote, logs.String())
	}
	logs.Reset()
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unlocatable := testOptions(file, home, cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-3\n")(), config, &logs, host, SSH{})
	if unlocatable.Collectors.Remote != nil || !strings.Contains(logs.String(), "cannot be located") {
		t.Errorf("options with an unlocatable store = remote %v, log %q", unlocatable.Collectors.Remote, logs.String())
	}
}

func TestCockpitFleetOptionsConfigureTheCodeIndexProviderOnlyWhenNamed(t *testing.T) {
	t.Parallel()
	host := func() (string, error) { return "the-host", nil }
	absent := filepath.Join(t.TempDir(), "absent.yaml")
	none := testOptions(t.TempDir(), t.TempDir(), absent, wbconfig.DefaultCockpitConfig(), io.Discard, host, SSH{})
	if none.Collectors.CodeIndexProvider != nil {
		t.Errorf("a provider with none configured = %+v", none.Collectors.CodeIndexProvider)
	}
	config := wbconfig.DefaultCockpitConfig()
	config.CodeIndexProvider, config.CodeIndexIndexer = wbconfig.CodeIndexProviderCodeGrapher, "code-graph"
	named := testOptions(t.TempDir(), t.TempDir(), absent, config, io.Discard, host, SSH{})
	provider := named.Collectors.CodeIndexProvider
	if provider == nil || provider.Name() != "codegrapher" || provider.Indexer() != "code-graph" {
		t.Errorf("the configured provider = %+v", provider)
	}
}

func TestCockpitFleetOptionsObservePullRequestsThroughTheWatcherWithTheConfiguredLimit(t *testing.T) {
	t.Parallel()
	config := wbconfig.DefaultCockpitConfig()
	config.PullRequestLimit, config.PullRequestHourlyBudget = 7, 55
	options := testOptions(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "absent.yaml"), config, io.Discard, func() (string, error) { return "h", nil }, SSH{})
	watcher, ok := options.PullRequests.(*prwatch.Watcher)
	if !ok || options.PullRequestLimit != 7 || options.PullRequestHourlyBudget != 55 {
		t.Fatalf("pull request observer = %T limit %d budget %d, want a *prwatch.Watcher, 7 and 55", options.PullRequests, options.PullRequestLimit, options.PullRequestHourlyBudget)
	}
	// The observation is the lean one, shown by what a red head costs: the
	// reads of any open pull request and not one run of `gh` to explain it.
	var reads, executions atomic.Int64
	watcher.Reader = &githubobserver.Reader{
		Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
			reads.Add(1)
			body := "{}"
			switch {
			case strings.HasSuffix(request.Endpoint, "/pulls/5"):
				body = `{"number":5,"state":"open","head":{"sha":"abc"},"base":{"ref":"main"}}`
			case strings.Contains(request.Endpoint, "/check-runs"):
				body = `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"failure","app":{"id":1,"slug":"gh"}}]}`
			case strings.Contains(request.Endpoint, "/actions/runs"):
				body = `{"total_count":0,"workflow_runs":[]}`
			case strings.Contains(request.Endpoint, "/status"):
				body = `{"state":"success","statuses":[]}`
			case strings.HasSuffix(request.Endpoint, "/branches/main"):
				body = `{"protected":false,"protection":{}}`
			case strings.Contains(request.Endpoint, "/rules/"):
				body = `[]`
			}
			return githubobserver.Response{Body: []byte(body), StatusCode: 200}, nil
		},
		Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
			executions.Add(1)
			return githubobserver.CommandResponse{ExitCode: 1}
		},
	}
	outcome, err := watcher.Evaluate(context.Background(), worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/app", PullRequest: 5})
	snapshot := outcome.Snapshot
	if err != nil || snapshot.Err != nil || len(snapshot.Failed) != 1 || reads.Load() != prsnapshot.ReadsPerObservation || executions.Load() != 0 || len(snapshot.Failures) != 0 {
		t.Errorf("a red head cost %d reads and %d executions: %+v", reads.Load(), executions.Load(), snapshot)
	}
}
