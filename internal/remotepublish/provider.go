package remotepublish

import (
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func CloneURL(cfg remotestate.Config) string {
	return "git@github.com:" + cfg.Repo + ".git"
}

// ClonePath resolves the local mirror of the configured state
// repository. An existing mirror is used where it is — host level first, legacy
// placement second — so the mirror's claims stay readable after a fleet has
// adopted the host level, and a missing one is created at the literal host
// level its URL names.
func ClonePath(projectsRoot string, cfg remotestate.Config) (string, error) {
	return worktrees.CanonicalRepositoryPathForURL(projectsRoot, cfg.Repo, CloneURL(cfg))
}

// openRemote selects the provider named by cfg. It lives here rather than in
// remotestate to keep the provider packages free of an import cycle.
func Open(cfg remotestate.Config, projectsRoot string, exitFactory func(int, string) error) (remotestate.Provider, error) {
	switch cfg.Provider {
	case "git":
		clonePath, err := ClonePath(projectsRoot, cfg)
		if err != nil {
			return nil, err
		}
		return gitrepo.New(gitrepo.Options{ClonePath: clonePath, CloneURL: CloneURL(cfg)}), nil
	case "hub":
		return hub.New(hub.Options{
			BaseURL: cfg.URL, Machine: cfg.Machine, TokenFile: cfg.TokenFile,
		})
	default:
		return nil, exitFactory(2, "remote.provider "+cfg.Provider+" is not supported")
	}
}

// loadRemote reads config and opens the provider. Both an unconfigured
// remote section and any other config error map to the usage exit code, so
// the snippet (for the former) or the parse/validation message (for the
// latter) reaches the operator the same way.
func Load(configPath, projectsRoot string, open func(remotestate.Config, string) (remotestate.Provider, error), exitFactory func(int, string) error) (remotestate.Config, remotestate.Provider, error) {
	cfg, err := remotestate.LoadConfig(configPath)
	if err != nil {
		return cfg, nil, exitFactory(2, err.Error())
	}
	provider, err := open(cfg, projectsRoot)
	return cfg, provider, err
}
