package main

import (
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/internal/cli/cmdinstall"
	"github.com/sneat-dev/wb/internal/wbupdate"
	"github.com/spf13/cobra"
)

func newSelfUpdateCmd() *cobra.Command {
	return cmdinstall.NewSelfUpdate(newCLIErrorRuntime(), wbupdate.Config(collectVersion().Version), maintenanceUpdateService().AfterUpdate)
}
func maintenanceUpdateService() wbupdate.Service {
	return wbupdate.Service{RunChild: wbupdate.RunChild, HandoffTimeout: daemonHandoffTimeout(daemonruntime.DefaultLifecycleBounds)}
}

// The operation reads these actual lifecycle bounds when the verified handoff starts.
func daemonHandoffTimeout(bounds func() daemonruntime.LifecycleBounds) func() time.Duration {
	return func() time.Duration {
		current := bounds()
		return current.Stop + current.SupervisorRestart + wbupdate.HandoffMargin
	}
}
