package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/hubaddress"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/prwatch"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/sessionmove"
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
	if remoteConfig, loadErr := remotestate.LoadConfig(configPath); loadErr == nil {
		machine = remoteConfig.Machine
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
	targets, transports := cockpitRemotes(configPath, config, logf)
	return cockpitfleet.Options{
		Machine: machine, Version: collectVersion().Version, Hardware: cockpitfleet.LocalHardware(), ProjectsRoot: projectsRoot,
		Sampler: newLocalSampler(projectsRoot, logf, nil), Collectors: local.Collectors(remote), Interval: config.RefreshInterval,
		Remotes: targets, Transports: transports,
		Terminals:    cockpitfleet.NewLocalTerminals(projectsRoot, home),
		PullRequests: pullRequestWatcher(), PullRequestLimit: config.PullRequestLimit, PullRequestHourlyBudget: config.PullRequestHourlyBudget,
		Logf: logf,
	}
}

// cockpitRemotes is the other machines the daemon reads live and the transports
// it reads them over (cockpit-views#req:remote-http-fetch). Every address and
// credential comes from wb.yaml alone: a machine is a key of
// session_move.targets with an http section, whose url is where its
// daemon-hosted hub answers and whose token_file holds the machine credential
// for it. A machine whose http section names no token_file is read only when
// its url is the hub this machine is already enrolled with (remote.provider:
// hub and the same origin as remote.url), and then with remote.token_file; the
// remote section itself names no machine, so the target key is what says which
// machine the hub runs on. With cockpit.remote_http false, no session_move
// section or no http section, nothing is returned and no request is ever made.
// A target named as this machine is dropped by the snapshotter.
func cockpitRemotes(configPath string, config wbconfig.CockpitConfig, logf func(string, ...any)) ([]cockpitfleet.RemoteTarget, []cockpitfleet.RemoteTransport) {
	if !config.RemoteHTTP {
		return nil, nil
	}
	moves, err := sessionmove.LoadConfig(configPath)
	if err != nil {
		var unconfigured *sessionmove.UnconfiguredError
		if !errors.As(err, &unconfigured) {
			logf("cockpit fleet: other machines are not read live: %v", err)
		}
		return nil, nil
	}
	hubURL, hubTokenFile := "", ""
	if remote, loadErr := remotestate.LoadConfig(configPath); loadErr == nil && remote.Provider == "hub" {
		hubURL, hubTokenFile = remote.URL, remote.TokenFile
	}
	var targets []cockpitfleet.RemoteTarget
	for machine, target := range moves.Targets {
		if target.HTTP == nil {
			continue
		}
		route := cockpitfleet.HTTPRoute{URL: target.HTTP.URL, TokenFile: target.HTTP.TokenFile}
		if route.TokenFile == "" {
			if hubURL == "" || !sameOrigin(hubURL, route.URL) {
				logf("cockpit fleet: %s is not read over http: its http section names no token_file and its url is not the hub this machine is enrolled with", machine)
				continue
			}
			route.TokenFile = hubTokenFile
		}
		targets = append(targets, cockpitfleet.RemoteTarget{Machine: machine, HTTP: &route})
	}
	if len(targets) == 0 {
		return nil, nil
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Machine < targets[j].Machine })
	return targets, []cockpitfleet.RemoteTransport{{Name: cockpitfleet.TransportHTTP, Exporter: cockpitfleet.NewHTTPExporter(nil)}}
}

// withoutOwnAddress is targets less any whose http url is this daemon's own
// listener: reading it would show this machine a second time under another
// machine's name. The listener is address, or any loopback name on its port. A
// dropped target is logged by its configured key, never by its url.
func withoutOwnAddress(targets []cockpitfleet.RemoteTarget, address string, logf func(string, ...any)) []cockpitfleet.RemoteTarget {
	_, ownPort, err := net.SplitHostPort(address)
	if err != nil {
		return targets
	}
	kept := make([]cockpitfleet.RemoteTarget, 0, len(targets))
	for _, target := range targets {
		if target.HTTP != nil {
			if parsed, parseErr := url.Parse(hubaddress.Origin(target.HTTP.URL)); parseErr == nil && (parsed.Host == address || (hubaddress.IsLoopbackHost(parsed.Hostname()) && parsed.Port() == ownPort)) {
				logf("cockpit fleet: %s is not read over http: its http url is this daemon's own address", target.Machine)
				continue
			}
		}
		kept = append(kept, target)
	}
	return kept
}

// machineExportSource is where the hub's export route gets this machine's
// envelope: the fleet snapshotter, which is built after the hub is mounted and
// bound here once it exists. It answers with the envelope, prepared once per
// version of the daemon's state and served with gzip and an ETag by the shared
// writer, or with a typed reason: 403 export_refused when this machine does not
// export its metadata (cockpit.anonymous_metadata: false, which no transport
// overrides), 503 warming_up until the snapshotter is bound and its first pass
// has ended, and 503 export_failed when the envelope would not pass its own
// rules.
type machineExportSource struct {
	snapshotter atomic.Pointer[cockpitfleet.Snapshotter]
	refused     atomic.Bool
}

func (source *machineExportSource) serve(writer http.ResponseWriter, request *http.Request, metricsOnly bool) {
	reason := func(status int, code string) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		// The error type cannot fail to marshal.
		body, _ := json.Marshal(map[string]string{"error": code})
		_, _ = writer.Write(append(body, '\n'))
	}
	if source.refused.Load() {
		reason(http.StatusForbidden, cockpitfleet.ErrorExportRefused)
		return
	}
	snapshotter := source.snapshotter.Load()
	if snapshotter == nil {
		reason(http.StatusServiceUnavailable, cockpitfleet.ErrorWarmingUp)
		return
	}
	payload, failure := snapshotter.ExportPayload(metricsOnly)
	if failure != "" {
		reason(http.StatusServiceUnavailable, failure)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	// An export is credentialed and is never to be kept by a client or a proxy.
	cockpit.ServePrivatePayload(writer, request, payload)
}
