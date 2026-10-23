// Package remotepublish owns manual and periodic local fleet publication.
package remotepublish

import (
	"context"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type Dependencies struct {
	ConfigPath     string
	Login          func() (string, error)
	Open           func(remotestate.Config, string) (remotestate.Provider, error)
	Now            func() time.Time
	Version        func() string
	Hardware       func() cockpitfleet.Hardware
	ReadRepository func(string) (gitops.RepoStatus, gitops.TrackingState, error)
	Select         func(reposelection.Request) ([]reposelection.Target, error)
	ListWorktrees  func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error)
	Fingerprint    func(string) (string, error)
	ExitError      func(int, string) error
}

func DefaultDependencies(configPath string, exitFactory func(int, string) error) Dependencies {
	return Dependencies{ConfigPath: configPath, Login: discover.AuthUser, Open: func(cfg remotestate.Config, root string) (remotestate.Provider, error) {
		return Open(cfg, root, exitFactory)
	}, Now: func() time.Time { return time.Now().UTC() }, Version: func() string { return buildinfo.Snapshot().Version }, Hardware: cockpitfleet.LocalHardware, ReadRepository: readRepositoryWithGit, Select: reposelection.Select, ListWorktrees: worktrees.List, Fingerprint: cockpitfleet.Fingerprint, ExitError: exitFactory}
}

type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }

type Request struct {
	ProjectsRoot, Filter string
	Parallel             int
	DryRun               bool
}
type Result struct {
	DryRun   bool
	Snapshot remotestate.Snapshot
	Report   Report
}
type Progress struct {
	Start              func(int)
	RepositoryComplete func(string, error)
	Phase              func(string)
	Worktree           func(worktrees.ListProgress)
	Finish             func(string)
	Fail               func(error)
}

func (p Progress) start(n int) {
	if p.Start != nil {
		p.Start(n)
	}
}
func (p Progress) repositoryComplete(name string, err error) {
	if p.RepositoryComplete != nil {
		p.RepositoryComplete(name, err)
	}
}
func (p Progress) phase(s string) {
	if p.Phase != nil {
		p.Phase(s)
	}
}
func (p Progress) worktree(e worktrees.ListProgress) {
	if p.Worktree != nil {
		p.Worktree(e)
	}
}
func (p Progress) finish(s string) {
	if p.Finish != nil {
		p.Finish(s)
	}
}
func (p Progress) fail(err error) {
	if p.Fail != nil {
		p.Fail(err)
	}
}
