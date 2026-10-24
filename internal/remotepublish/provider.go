package remotepublish

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func CloneURL(cfg remotestate.Config) string {
	return "git@github.com:" + cfg.Repo + ".git"
}

// ClonePath is where the configured git state repository is cloned:
// <projects-root>/.wb/remote-store/<owner>/<name>, inside WB's private state
// directory, where no fleet tool mistakes it for a canonical source clone
// (sneat-dev/wb#901).
func ClonePath(projectsRoot string, cfg remotestate.Config) (string, error) {
	root, err := storeRoot(projectsRoot, cfg)
	if err != nil {
		return "", err
	}
	return wbhome.RemoteStoreClonePath(root, cfg.RepoOwner(), cfg.RepoName()), nil
}

// LegacyClonePaths are where releases before sneat-dev/wb#901 cloned the
// store — at canonical-clone locations, host level first — and so where the
// one-time migration looks for a clone to move into ClonePath.
func LegacyClonePaths(projectsRoot string, cfg remotestate.Config) ([]string, error) {
	root, err := storeRoot(projectsRoot, cfg)
	if err != nil {
		return nil, err
	}
	owner, name := cfg.RepoOwner(), cfg.RepoName()
	return []string{
		repopath.FromCloneURL(CloneURL(cfg), owner, name).Path(root),
		repopath.Address{Org: owner, Repo: name}.Path(root),
	}, nil
}

// ActiveClonePath is the clone that currently is the store on this machine:
// ClonePath once it holds a clone (or when no legacy clone exists either),
// otherwise the legacy clone a not-yet-migrated machine still uses. Readers
// that never migrate (the cockpit fleet view, the hook guard's exemption)
// resolve through it so they keep working until the next remote operation
// moves the clone.
func ActiveClonePath(projectsRoot string, cfg remotestate.Config) (string, error) {
	clonePath, err := ClonePath(projectsRoot, cfg)
	if err != nil {
		return "", err
	}
	legacy, _ := LegacyClonePaths(projectsRoot, cfg)
	if isClone(clonePath) {
		return clonePath, nil
	}
	for _, path := range legacy {
		if isClone(path) {
			return path, nil
		}
	}
	return clonePath, nil
}

// GitOptions are the gitrepo options for the configured git store: its clone
// path, origin, and the legacy locations to migrate from.
func GitOptions(projectsRoot string, cfg remotestate.Config) (gitrepo.Options, error) {
	clonePath, err := ClonePath(projectsRoot, cfg)
	if err != nil {
		return gitrepo.Options{}, err
	}
	legacy, _ := LegacyClonePaths(projectsRoot, cfg)
	return gitrepo.Options{ClonePath: clonePath, CloneURL: CloneURL(cfg), LegacyClonePaths: legacy, RemoteStore: true}, nil
}

// storeRoot validates the projects root and the configured owner/name, so no
// crafted remote.repo can place the clone outside the state directory.
func storeRoot(projectsRoot string, cfg remotestate.Config) (string, error) {
	root, err := worktrees.AbsoluteProjectsRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
		return "", fmt.Errorf("projects root %s is not a directory", root)
	}
	if !repopath.SafeSegment(cfg.RepoOwner(), false) || !repopath.SafeSegment(cfg.RepoName(), true) {
		return "", fmt.Errorf("remote.repo %q is not an <owner>/<name> repository", cfg.Repo)
	}
	return root, nil
}

func isClone(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

// openRemote selects the provider named by cfg. It lives here rather than in
// remotestate to keep the provider packages free of an import cycle.
func Open(cfg remotestate.Config, projectsRoot string, exitFactory func(int, string) error) (remotestate.Provider, error) {
	switch cfg.Provider {
	case "git":
		options, err := GitOptions(projectsRoot, cfg)
		if err != nil {
			return nil, err
		}
		return gitrepo.New(options), nil
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
