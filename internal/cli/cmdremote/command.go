package cmdremote

import (
	"context"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/spf13/cobra"
)

type Operations struct {
	Claim     func(remoterun.ClaimRequest) (remoterun.ClaimResult, error)
	Release   func(remoterun.ReleaseRequest) (remoterun.ReleaseResult, error)
	Machines  func(string, time.Duration) ([]remoterun.MachineRow, error)
	Claims    func(string, time.Duration) ([]remoterun.ClaimRow, error)
	Status    func(remoterun.StatusRequest, remoterun.StatusProgress) (remoterun.StatusResult, error)
	Enroll    func(context.Context, remoterun.EnrollRequest) (remoterun.EnrollResult, error)
	Publish   func(remotepublish.Request, remotepublish.Progress, io.Writer) (remotepublish.Result, error)
	Heartbeat time.Duration
}

func New(runtime shared.Runtime, operations Operations) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Publish this machine's fleet state and read other machines' state",
		Long: `wb remote shares WB fleet state across machines through a store
configured in ~/.config/wb/wb.yaml:

` + remotestate.ConfigSnippet + `

For the authenticated outbound HTTPS hub:

` + remotestate.HubConfigSnippet + `

  wb remote publish    scan this machine and publish its snapshot
  wb remote enroll     securely install a hosted-hub machine credential
  wb remote status     cross-machine worklist from the store
  wb remote machines   one line per machine with publish age
  wb remote claim      claim a task, or refresh your own claim on it
  wb remote release    release this machine's remote claim on a task
  wb remote claims     list every claim in the store, with staleness`,
	}
	cmd.AddCommand(newPublish(runtime, operations))
	cmd.AddCommand(newStatus(runtime, operations))
	cmd.AddCommand(newMachines(runtime, operations))
	cmd.AddCommand(newClaim(runtime, operations))
	cmd.AddCommand(newRelease(runtime, operations))
	cmd.AddCommand(newClaims(runtime, operations))
	cmd.AddCommand(newEnroll(runtime, operations))
	return cmd
}
