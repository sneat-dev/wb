package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// newCockpitServer builds the Cockpit server one daemon run serves on
// address. It keeps the default session store, which is in memory: every
// owner session ends when the daemon restarts (cockpit#req:owner-session).
func newCockpitServer(address string, config wbconfig.CockpitConfig) *cockpit.Server {
	return cockpit.New(cockpit.Options{CanonicalHost: cockpit.CanonicalHost(address), Config: config})
}

// registerCockpitFleet builds the fleet snapshotter for one daemon run and
// registers its routes on server, which must not have taken its mounts yet.
// The caller starts the snapshotter with the daemon's lifetime context.
func registerCockpitFleet(server *cockpit.Server, options cockpitfleet.Options) *cockpitfleet.Snapshotter {
	snapshotter := cockpitfleet.New(options)
	cockpitfleet.Register(server, snapshotter)
	return snapshotter
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
	return cockpitfleet.Options{
		Machine: machine, Version: collectVersion().Version, ProjectsRoot: projectsRoot, Collectors: local.Collectors(remote), Interval: config.RefreshInterval,
		Logf: logf,
	}
}
