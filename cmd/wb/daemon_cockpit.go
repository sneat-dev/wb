package main

import (
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpitoptions"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"io"
)

func cockpitFleetOptions(projectsRoot, home, configPath string, config wbconfig.CockpitConfig, logs io.Writer, hostname func() (string, error)) cockpitfleet.Options {
	deps := cockpitoptions.DefaultDependencies(configPath, newCLIErrorRuntime().ExitError)
	deps.Hostname = hostname
	return cockpitoptions.Options(cockpitoptions.Request{ProjectsRoot: projectsRoot, Home: home, ConfigPath: configPath, Config: config, Logs: logs}, deps)
}
