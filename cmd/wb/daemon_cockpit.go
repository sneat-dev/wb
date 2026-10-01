package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/prwatch"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/remotestate/periodic"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// newCockpitServer builds the Cockpit server one daemon run serves on
// address. It keeps the default session store, which is in memory: every
// owner session ends when the daemon restarts (cockpit#req:owner-session).
func newCockpitServer(address string, config wbconfig.CockpitConfig) *cockpit.Server {
	return cockpit.New(cockpit.Options{CanonicalHost: cockpit.CanonicalHost(address), Config: config})
}

// pullRequestWatcher is the watcher the fleet snapshotter runs: the daemon's
// own prwatch.Watcher over the lean observation, which skips the reads that
// explain a red head (the cockpit shows only the first failing check's name).
func pullRequestWatcher() *prwatch.Watcher {
	watcher := prwatch.NewWatcher()
	watcher.Observe = prsnapshot.ObserveLean
	return watcher
}

// registerCockpitFleet builds the fleet snapshotter for one daemon run and
// registers its routes on server, which must not have taken its mounts yet.
// The caller starts the snapshotter with the daemon's lifetime context.
func registerCockpitFleet(server *cockpit.Server, options cockpitfleet.Options) *cockpitfleet.Snapshotter {
	snapshotter := cockpitfleet.New(options)
	cockpitfleet.Register(server, snapshotter)
	return snapshotter
}

// newLocalSampler is this machine's metrics sampler: the platform's own reader of
// the projects root's disk, memory, load and CPU, on the real clock, ticking every
// machinemetrics.Interval unless a test supplies tick.
func newLocalSampler(projectsRoot string, logf func(string, ...any), tick func(time.Duration) (<-chan time.Time, func())) *machinemetrics.Sampler {
	return machinemetrics.New(machinemetrics.Options{Source: machinemetrics.NewSource(projectsRoot), Tick: tick, Logf: logf})
}

// cockpitFleetOptions is what the fleet snapshotter reads on this machine: the
// projects root and WB home through the local collectors, and other machines
// from the local copy of the git remote-state store wb.yaml configures, when it
// does and the copy exists. The snapshot never fetches that copy, so a machine
// with no remote section, an unlocatable store or a hub provider knows no other
// machines; the last two are logged once, here. Code-index freshness is read
// from the lifecycle-hook receipts that wb.yaml's hooks section configures. The
// receipt stream and queue are located by lifecyclehooks.NewFreshnessReader,
// which resolves them with the same defaulting function the lifecycle-hook
// worker and every `wb hooks lifecycle` command use (Dispatcher.defaults, as in
// DefaultDispatcher), so this daemon's own XDG_STATE_HOME and home are honoured
// exactly as the worker's are. The machine is named after the
// remote section's machine, else after hostname's answer ("local" when it has
// none). cockpit.refresh_interval, when set, is the refresh interval, and
// cockpit.code_index_provider, when set, names the provider that reports the
// statistics of each checkout's index (the snapshotter alone asks it).
// cockpit.pull_request_limit, when set, bounds the pull requests the
// snapshotter observes on GitHub in one pass and cockpit.pull_request_hourly_budget
// the observations in a rolling hour, through a prwatch.Watcher.
func cockpitFleetOptions(projectsRoot, home, configPath string, config wbconfig.CockpitConfig, logs io.Writer, hostname func() (string, error)) cockpitfleet.Options {
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(logs, "wb: "+format+"\n", args...) }
	machine, err := hostname()
	if err != nil || machine == "" {
		machine = "local"
	}
	var remote cockpitfleet.RemoteCollector
	var publisher cockpitfleet.RemotePublisher
	if remoteConfig, loadErr := remotestate.LoadConfig(configPath); loadErr == nil {
		machine = remoteConfig.Machine
		if every := remoteConfig.Publish.PublishEvery(); every > 0 {
			deps := defaultRemoteDeps()
			deps.configPath = configPath
			publisher = newPeriodicPublisher(deps, remoteConfig, projectsRoot, logf)
		}
		if remoteConfig.Provider != "git" {
			logf("cockpit fleet: other machines are not read from the %s remote provider", remoteConfig.Provider)
		} else if clonePath, pathErr := remoteStateClonePath(projectsRoot, remoteConfig); pathErr != nil {
			logf("cockpit fleet: the remote state store cannot be located: %v", pathErr)
		} else {
			remote = cockpitfleet.LocalStateCollector{Reader: gitrepo.New(gitrepo.Options{ClonePath: clonePath, CloneURL: remoteStateCloneURL(remoteConfig)})}
		}
	}
	local := cockpitfleet.LocalCollectors{
		ProjectsRoot: projectsRoot, Home: home, IndexCachePath: filepath.Join(home, "cockpit-fleet-index.json"),
		CodeIndex: &cockpitfleet.LocalCodeIndex{Reader: lifecyclehooks.NewFreshnessReader(lifecyclehooks.Dispatcher{ConfigPath: configPath})},
	}
	if config.CodeIndexProvider == wbconfig.CodeIndexProviderCodeGrapher {
		local.CodeIndexProvider = cockpitfleet.CodeGrapherProvider{IndexerName: config.CodeIndexIndexer}
	}
	return cockpitfleet.Options{
		Machine: machine, Version: collectVersion().Version, Hardware: cockpitfleet.LocalHardware(), ProjectsRoot: projectsRoot,
		Sampler: newLocalSampler(projectsRoot, logf, nil), Collectors: withHerdrActivity(local.Collectors(remote)), Interval: config.RefreshInterval,
		Terminals:    cockpitfleet.NewLocalTerminals(projectsRoot, home),
		Publisher:    publisher,
		PullRequests: pullRequestWatcher(), PullRequestLimit: config.PullRequestLimit, PullRequestHourlyBudget: config.PullRequestHourlyBudget,
		Logf: logf,
	}
}

// withHerdrActivity adds the herdr read of agent activity to the local
// collectors. herdr is optional: a machine without it reports no activity.
func withHerdrActivity(collectors cockpitfleet.Collectors) cockpitfleet.Collectors {
	collectors.Activity = cockpitfleet.DefaultHerdrActivity()
	return collectors
}

// periodicScanWorkers bounds the repositories one periodic publish scans at
// once; the daemon shares the machine with the work it reports on, so it reads
// gently where `wb remote publish` reads eight at a time.
const periodicScanWorkers = 2

// newPeriodicPublisher is the daemon's periodic remote publisher for a machine
// whose remote section sets publish.interval (cockpit-views#req:periodic-
// remote-publish): the same collector, identity and provider as `wb remote
// publish`, wrapped in the interval, change and backoff policy of package
// periodic. The GitHub login keying this machine's entry is resolved on the
// first attempt that needs it and kept.
func newPeriodicPublisher(deps remoteDeps, cfg remotestate.Config, projectsRoot string, logf func(string, ...any)) *periodic.Publisher {
	var login string
	return periodic.New(periodic.Options{
		Every: cfg.Publish.PublishEvery(), Agents: cfg.Publish.Agents, Metrics: cfg.Publish.Metrics, Logf: logf, Now: deps.now,
		Collect: func(ctx context.Context, now time.Time) (remotestate.Snapshot, error) {
			if login == "" {
				found, err := deps.login()
				if err != nil || found == "" {
					return remotestate.Snapshot{}, errors.New("the GitHub login keying this machine's entry is unavailable (gh auth status)")
				}
				login = found
			}
			return collectSnapshot(ctx, projectsRoot, "", periodicScanWorkers, publishIdentity(cfg, login, now), cfg.Publish.Unpushed, nil)
		},
		Open: func() (remotestate.Provider, error) { return deps.open(cfg, projectsRoot) },
	})
}
