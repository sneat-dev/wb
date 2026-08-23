package main

import (
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// remoteDeps are the seams tests replace: config location, GitHub login,
// provider construction, and the clock.
type remoteDeps struct {
	configPath string
	login      func() (string, error)
	open       func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error)
	now        func() time.Time
}

func defaultRemoteDeps() remoteDeps {
	return remoteDeps{
		configPath: wbconfig.DefaultPath(),
		login:      discover.AuthUser,
		open:       openRemote,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// openRemote selects the provider named by cfg. It lives here rather than in
// remotestate to keep the provider packages free of an import cycle.
func openRemote(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
	switch cfg.Provider {
	case "git":
		return gitrepo.New(gitrepo.Options{
			ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()),
			CloneURL:  "git@github.com:" + cfg.Repo + ".git",
		}), nil
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

func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Publish this machine's fleet state and read other machines' state",
		Long: `wb remote shares WB fleet state across machines through a store
configured in ~/.config/wb/wb.yaml:

` + remotestate.ConfigSnippet + `

  wb remote publish    scan this machine and publish its snapshot
  wb remote status     cross-machine worklist from the store
  wb remote machines   one line per machine with publish age`,
	}
	cmd.AddCommand(newRemotePublishCmd())
	cmd.AddCommand(newRemoteStatusCmd())
	cmd.AddCommand(newRemoteMachinesCmd())
	return cmd
}
