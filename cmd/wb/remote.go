package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/worktrees"
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
}

func defaultRemoteDeps() remoteDeps {
	return remoteDeps{
		configPath:        wbconfig.DefaultPath(),
		login:             discover.AuthUser,
		open:              openRemote,
		now:               func() time.Time { return time.Now().UTC() },
		progressHeartbeat: universalProgressHeartbeat,
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
func remoteStateCloneURL(cfg remotestate.Config) string {
	return "git@github.com:" + cfg.Repo + ".git"
}

// remoteStateClonePath resolves the local mirror of the configured state
// repository. An existing mirror is used where it is — host level first, legacy
// placement second — so the mirror's claims stay readable after a fleet has
// adopted the host level, and a missing one is created at the literal host
// level its URL names.
func remoteStateClonePath(projectsRoot string, cfg remotestate.Config) (string, error) {
	return worktrees.CanonicalRepositoryPathForURL(projectsRoot, cfg.Repo, remoteStateCloneURL(cfg))
}

// openRemote selects the provider named by cfg. It lives here rather than in
// remotestate to keep the provider packages free of an import cycle.
func openRemote(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
	switch cfg.Provider {
	case "git":
		clonePath, err := remoteStateClonePath(projectsRoot, cfg)
		if err != nil {
			return nil, err
		}
		return gitrepo.New(gitrepo.Options{ClonePath: clonePath, CloneURL: remoteStateCloneURL(cfg)}), nil
	case "hub":
		return hub.New(hub.Options{
			BaseURL: cfg.URL, Machine: cfg.Machine, TokenFile: cfg.TokenFile,
		})
	default:
		return nil, &exitError{code: exitUsage, message: "remote.provider " + cfg.Provider + " is not supported"}
	}
}

// loadRemote reads config and opens the provider. Both an unconfigured
// remote section and any other config error map to the usage exit code, so
// the snippet (for the former) or the parse/validation message (for the
// latter) reaches the operator the same way.
func loadRemote(deps remoteDeps, projectsRoot string) (remotestate.Config, remotestate.Provider, error) {
	cfg, err := remotestate.LoadConfig(deps.configPath)
	if err != nil {
		return cfg, nil, &exitError{code: exitUsage, message: err.Error()}
	}
	provider, err := deps.open(cfg, projectsRoot)
	return cfg, provider, err
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
