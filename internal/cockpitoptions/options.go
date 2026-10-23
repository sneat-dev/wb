package cockpitoptions

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/prwatch"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotessh"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// pullRequestWatcher is the watcher the fleet snapshotter runs: the daemon's
// own prwatch.Watcher over the lean observation, which skips the reads that
// explain a red head (the cockpit shows only the first failing check's name).
func pullRequestWatcher() *prwatch.Watcher {
	watcher := prwatch.NewWatcher()
	watcher.Observe = prsnapshot.ObserveLean
	return watcher
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
// the observations in a rolling hour, through a prwatch.Watcher. The SSH
// transport of the other machines runs with the daemon's own seams
// (daemonCockpitSSH).
// cockpitFleetOptionsWith is cockpitFleetOptions over the given SSH seams, so a
// test never holds the runner that starts a real ssh.
func Options(request Request, deps Dependencies) cockpitfleet.Options {
	projectsRoot, home, configPath, config, logs, hostname, ssh := request.ProjectsRoot, request.Home, request.ConfigPath, request.Config, request.Logs, deps.Hostname, deps.SSH
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(logs, "wb: "+format+"\n", args...) }
	machine, err := hostname()
	if err != nil || machine == "" {
		machine = "local"
	}
	var remote cockpitfleet.RemoteCollector
	var publisher cockpitfleet.RemotePublisher
	// The login this machine publishes under is what tells its own machines'
	// publications from another login's machine of the same name. The daemon
	// learns it when its periodic publisher resolves it; the snapshotter also
	// learns it from this machine's own publication in the store.
	login := &learnedLogin{}
	if remoteConfig, loadErr := remotestate.LoadConfig(configPath); loadErr == nil {
		machine = remoteConfig.Machine
		if every := remoteConfig.Publish.PublishEvery(); every > 0 {
			publishDeps := deps.Publish
			publishDeps.ConfigPath = configPath
			publisher = remotepublish.New(publishDeps).Periodic(remoteConfig, projectsRoot, logf, login.learn)
		}
		if remoteConfig.Provider != "git" {
			logf("cockpit fleet: other machines are not read from the %s remote provider", remoteConfig.Provider)
		} else if clonePath, pathErr := remotepublish.ClonePath(projectsRoot, remoteConfig); pathErr != nil {
			logf("cockpit fleet: the remote state store cannot be located: %v", pathErr)
		} else {
			remote = cockpitfleet.LocalStateCollector{Reader: gitrepo.New(gitrepo.Options{ClonePath: clonePath, CloneURL: remotepublish.CloneURL(remoteConfig)})}
		}
	}
	local := cockpitfleet.LocalCollectors{
		ProjectsRoot: projectsRoot, Home: home, IndexCachePath: filepath.Join(home, "cockpit-fleet-index.json"),
		CodeIndex: &cockpitfleet.LocalCodeIndex{Reader: lifecyclehooks.NewFreshnessReader(lifecyclehooks.Dispatcher{ConfigPath: configPath})},
	}
	if config.CodeIndexProvider == wbconfig.CodeIndexProviderCodeGrapher {
		local.CodeIndexProvider = cockpitfleet.CodeGrapherProvider{IndexerName: config.CodeIndexIndexer}
	}
	targets, transports, sshRoutes := cockpitRemotes(configPath, config, logf, ssh)
	return cockpitfleet.Options{
		Machine: machine, Version: deps.Version(), Hardware: cockpitfleet.LocalHardware(), ProjectsRoot: projectsRoot,
		LoginSource: login.known,
		Sampler:     newLocalSampler(projectsRoot, logf, nil), Collectors: withHerdrActivity(local.Collectors(remote)), Interval: config.RefreshInterval,
		Remotes: targets, Transports: transports, SSHRoutes: sshRoutes,
		Terminals:    cockpitfleet.NewLocalTerminals(projectsRoot, home),
		Publisher:    publisher,
		PullRequests: pullRequestWatcher(), PullRequestLimit: config.PullRequestLimit, PullRequestHourlyBudget: config.PullRequestHourlyBudget,
		Logf: logf,
	}
}

// learnedLogin holds the login this machine publishes under from the moment
// something resolves it; it is read from memory by the fleet snapshotter.
type learnedLogin struct{ value atomic.Pointer[string] }

func (l *learnedLogin) learn(login string) { l.value.Store(&login) }

// known is the login, or "" while nothing has resolved it.
func (l *learnedLogin) known() string {
	if login := l.value.Load(); login != nil {
		return *login
	}
	return ""
}

// cockpitRemotes is the other machines the daemon reads live, the transports it
// reads them over (cockpit-views#req:remote-http-fetch, #req:remote-ssh-fetch)
// and every configured machine's SSH route. Every address and credential comes
// from wb.yaml alone: a machine is a key of session_move.targets.
//
// HTTP: a machine with an http section, whose url is where its daemon-hosted hub
// answers and whose token_file holds the machine credential for it. A machine
// whose http section names no token_file is read only when its url is the hub
// this machine is already enrolled with (remote.provider: hub and the same
// origin as remote.url), and then with remote.token_file; the remote section
// itself names no machine, so the target key is what says which machine the hub
// runs on. cockpit.remote_http: false turns it off.
//
// SSH: a machine with an ssh section (host, user, wb_path, which
// sessionmove.LoadConfig has validated). cockpit.remote_ssh: false turns it off:
// no machine then has an SSH route to read and no SSH transport exists, so no
// ssh process is ever started. HTTP is asked first and SSH after it.
//
// The routes returned third are every ssh section, whatever cockpit.remote_ssh
// says: they are what an owner's "Copy command" entries are built from, and
// nothing is run with them.
//
// With no session_move section nothing is returned, no request is ever made and
// no process is ever started. A target named as this machine is dropped by the
// snapshotter.
func cockpitRemotes(configPath string, config wbconfig.CockpitConfig, logf func(string, ...any), ssh SSH) ([]cockpitfleet.RemoteTarget, []cockpitfleet.RemoteTransport, map[string]cockpitfleet.SSHRoute) {
	moves, err := sessionmove.LoadConfig(configPath)
	if err != nil {
		var unconfigured *sessionmove.UnconfiguredError
		if !errors.As(err, &unconfigured) {
			logf("cockpit fleet: other machines are not read live: %v", err)
		}
		return nil, nil, nil
	}
	hubURL, hubTokenFile := "", ""
	if remote, loadErr := remotestate.LoadConfig(configPath); loadErr == nil && remote.Provider == "hub" {
		hubURL, hubTokenFile = remote.URL, remote.TokenFile
	}
	var targets []cockpitfleet.RemoteTarget
	routes := map[string]cockpitfleet.SSHRoute{}
	overHTTP, overSSH := false, false
	for machine, configured := range moves.Targets {
		target := cockpitfleet.RemoteTarget{Machine: machine}
		if configured.HTTP != nil && config.RemoteHTTP {
			route := cockpitfleet.HTTPRoute{URL: configured.HTTP.URL, TokenFile: configured.HTTP.TokenFile}
			switch {
			case route.TokenFile != "":
				target.HTTP = &route
			case hubURL != "" && remotestate.SameOrigin(hubURL, route.URL):
				route.TokenFile = hubTokenFile
				target.HTTP = &route
			default:
				logf("cockpit fleet: %s is not read over http: its http section names no token_file and its url is not the hub this machine is enrolled with", machine)
			}
		}
		if configured.SSH != nil {
			route := cockpitfleet.SSHRoute{Host: configured.SSH.Host, User: configured.SSH.User, WBPath: configured.SSH.WBPath}
			routes[machine] = route
			if config.RemoteSSH {
				target.SSH = &route
			}
		}
		if target.HTTP == nil && target.SSH == nil {
			continue
		}
		overHTTP, overSSH = overHTTP || target.HTTP != nil, overSSH || target.SSH != nil
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Machine < targets[j].Machine })
	var transports []cockpitfleet.RemoteTransport
	if overHTTP {
		transports = append(transports, cockpitfleet.RemoteTransport{Name: cockpitfleet.TransportHTTP, Exporter: cockpitfleet.NewHTTPExporter(nil)})
	}
	if overSSH {
		transports = append(transports, cockpitfleet.RemoteTransport{Name: cockpitfleet.TransportSSH, Exporter: cockpitfleet.NewSSHExporter(ssh.Find, ssh.Runner, nil, logf)})
	}
	return targets, transports, routes
}

// SSH is what the SSH transport runs with: what finds the local ssh
// executable and the runner of it.
type SSH struct {
	Find   func() (string, error)
	Runner remotessh.Runner
}

// daemonCockpitSSH is the daemon's: the system's own ssh, or the one on the PATH
// when no other process could have replaced it (remotessh.ResolveTrusted), and
// the runner that gives ssh an allow-listed environment and kills the whole
// process group of one that overruns its time.
func defaultSSH() SSH {
	return SSH{Runner: remotessh.GroupRunner{}, Find: func() (string, error) {
		// A home directory that cannot be named is not checked against.
		home, _ := os.UserHomeDir()
		return remotessh.ResolveTrusted(exec.LookPath, remotessh.SystemExecutable, home)
	}}
}

// withHerdrActivity adds the herdr read of agent activity to the local
// collectors. herdr is optional: a machine without it reports no activity.
func withHerdrActivity(collectors cockpitfleet.Collectors) cockpitfleet.Collectors {
	collectors.Activity = cockpitfleet.DefaultHerdrActivity()
	return collectors
}

// Request binds the daemon's current local roots and configuration.
type Request struct {
	ProjectsRoot, Home, ConfigPath string
	Config                         wbconfig.CockpitConfig
	Logs                           io.Writer
}
type Dependencies struct {
	Hostname func() (string, error)
	Version  func() string
	Publish  remotepublish.Dependencies
	SSH      SSH
}

func DefaultDependencies(configPath string, exitFactory func(int, string) error) Dependencies {
	return Dependencies{Hostname: os.Hostname, Version: func() string { return buildinfo.Snapshot().Version }, Publish: remotepublish.DefaultDependencies(configPath, exitFactory), SSH: defaultSSH()}
}
