package main

import (
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// remoteDeps are the seams tests replace: config location, GitHub login,
// provider construction, and the clock.
type remoteDeps struct {
	configPath string
	login      func() (string, error)
	open       func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error)
	now        func() time.Time
	// progressHeartbeat is a test seam. Production always uses the universal
	// ten-second progress contract.
	progressHeartbeat time.Duration
	// stderr is where a publish says what it must say whether or not it shows
	// progress (the one-time note that the snapshot now carries the machine's
	// hardware facts, a publish that had to leave the optional fields out); nil
	// discards.
	stderr io.Writer
}

func defaultRemoteDeps() remoteDeps {
	return remoteDeps{
		configPath:        wbconfig.DefaultPath(),
		login:             discover.AuthUser,
		open:              openRemote,
		now:               func() time.Time { return time.Now().UTC() },
		progressHeartbeat: universalProgressHeartbeat,
		stderr:            os.Stderr,
	}
}

func remoteProgressHeartbeat(deps remoteDeps) time.Duration {
	if deps.progressHeartbeat > 0 {
		return deps.progressHeartbeat
	}
	return universalProgressHeartbeat
}

// remoteStateCloneURL is the state repository's transport URL. It is always on
// GitHub, which is why the clone path below can place a missing mirror at the
// literal host level.

func openRemote(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
	return remotepublish.Open(cfg, projectsRoot, newCLIErrorRuntime().ExitError)
}
func loadRemote(deps remoteDeps, projectsRoot string) (remotestate.Config, remotestate.Provider, error) {
	return remotepublish.Load(deps.configPath, projectsRoot, deps.open, newCLIErrorRuntime().ExitError)
}

func newRemoteCmd(inv *invocation) *cobra.Command {
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
	cmd.AddCommand(newRemotePublishCmd(inv))
	cmd.AddCommand(newRemoteStatusCmd(inv))
	cmd.AddCommand(newRemoteMachinesCmd(inv))
	cmd.AddCommand(newRemoteClaimCmd(inv))
	cmd.AddCommand(newRemoteReleaseCmd(inv))
	cmd.AddCommand(newRemoteClaimsCmd(inv))
	cmd.AddCommand(newRemoteEnrollCmd(inv))
	return cmd
}
