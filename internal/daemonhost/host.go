// Package daemonhost composes the native daemon's listeners, queue and mounted services.
package daemonhost

import (
	"io"
	"net"

	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// Request is one current invocation of the native host.
type Request struct {
	ProjectsRoot, Listen, LifecycleState string
	Quiet, ManagedStart                  bool
}

// Dependencies supplies the actual runtime and fleet composition. FleetOptions
// is required and must construct the requested machine's real collectors.
type Dependencies struct {
	Runtime      daemonruntime.Dependencies
	Listen       func(string, string) (net.Listener, error)
	Tuning       *Tuning
	FleetOptions func(string, string, string, wbconfig.CockpitConfig, io.Writer, func() (string, error)) cockpitfleet.Options
}

type hostEffects struct {
	loadState     func(daemon.Store) (daemon.State, bool, error)
	saveState     func(daemon.Store, daemon.State) error
	rawPolicyPath func() (string, error)
	operationsDir func(string) (string, error)
}

type Host struct {
	deps    Dependencies
	effects hostEffects
}

func New(deps Dependencies) *Host {
	return &Host{deps: deps, effects: hostEffects{
		loadState:     daemon.Store.Load,
		saveState:     daemon.Store.Save,
		rawPolicyPath: daemon.RawExecutionPolicyPath,
		operationsDir: daemon.OperationsDir,
	}}
}

type daemonDependencies struct {
	daemonruntime.Dependencies
	listen    func(string, string) (net.Listener, error)
	hubTuning *Tuning
}

func (host *Host) runtimeDependencies() daemonDependencies {
	return daemonDependencies{Dependencies: host.deps.Runtime, listen: host.deps.Listen, hubTuning: host.deps.Tuning}
}
