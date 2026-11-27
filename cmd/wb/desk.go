package main

import (
	"crypto/rand"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/cmddesk"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/deskrun"
	"github.com/sneat-dev/wb/internal/nodeidentity"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// deskMachineName names this machine as the daemon's fleet document does:
// remote.machine, else the hostname, else "local".
func deskMachineName(configPath string, hostname func() (string, error)) string {
	if config, err := remotestate.LoadConfig(configPath); err == nil && config.Machine != "" {
		return config.Machine
	}
	if host, err := hostname(); err == nil && host != "" {
		return host
	}
	return "local"
}

func defaultDeskDependencies() deskrun.Dependencies {
	return deskrun.DefaultPlatform(deskrun.Dependencies{
		ConfigPath:  wbconfig.DefaultPath,
		NewClient:   deskrun.NewDeskClient,
		MachineName: func(configPath string) string { return deskMachineName(configPath, os.Hostname) },
		MachineID:   func(projectsRoot string) (string, error) { return nodeidentity.Load(projectsRoot, rand.Reader) },
		OpenBrowser: openBrowser,
		Restart:     remoterun.RestartDaemonAfterEnroll,
		Sleep:       deskrun.Sleep,
		Now:         time.Now,
		Random:      rand.Reader,
		Export:      deskrun.ExportFromDaemon,
		WBVersion:   buildinfo.Version(),
	})
}

func newDeskCmd(inv *invocation) *cobra.Command {
	service := deskrun.New(defaultDeskDependencies(), newCLIErrorRuntime().ExitError)
	return cmddesk.New(newCLIRuntime(inv), cmddesk.Operations{
		Connect: service.Connect, Disconnect: service.Disconnect, Status: service.Status, Push: service.Push,
		ExportTaskKey: service.ExportTaskKey, IsTerminal: console.IsTerminal, Now: time.Now,
	})
}
