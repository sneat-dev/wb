package main

import (
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmdinstall"
	"github.com/sneat-dev/wb/internal/wbupdate"
	"github.com/spf13/cobra"
)

func newSelfUpdateCmd() *cobra.Command {
	return cmdinstall.NewSelfUpdate(newCLIErrorRuntime(), wbupdate.Config(collectVersion().Version), maintenanceUpdateService().AfterUpdate)
}
func maintenanceUpdateService() wbupdate.Service {
	return wbupdate.Service{RunChild: wbupdate.RunChild, HandoffTimeout: selfUpdateDaemonHandoffTimeout}
}

// The operation reads these actual lifecycle bounds when the verified handoff starts.
func selfUpdateDaemonHandoffTimeout() time.Duration {
	return daemonStopTimeout + daemonSupervisorRestartTimeout + wbupdate.HandoffMargin
}
