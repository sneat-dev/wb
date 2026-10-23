// Package remoterun coordinates remote-store commands without Cobra.
package remoterun

import (
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type Dependencies struct {
	ConfigPath func() string
	Login      func() (string, error)
	Open       func(remotestate.Config, string) (remotestate.Provider, error)
	Now        func() time.Time
	ExitError  func(int, string) error
}
type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }
func DefaultDependencies(exitFactory func(int, string) error) Dependencies {
	return Dependencies{ConfigPath: wbconfig.DefaultPath, Login: discover.AuthUser, Open: func(cfg remotestate.Config, root string) (remotestate.Provider, error) {
		return remotepublish.Open(cfg, root, exitFactory)
	}, Now: func() time.Time { return time.Now().UTC() }, ExitError: exitFactory}
}
func loadRemote(deps Dependencies, root string) (remotestate.Config, remotestate.Provider, error) {
	return remotepublish.Load(deps.ConfigPath(), root, deps.Open, deps.ExitError)
}
func (s *Service) Load(root string) (remotestate.Config, remotestate.Provider, error) {
	return loadRemote(s.deps, root)
}

type ClaimRequest struct {
	ProjectsRoot, Task, Note string
	TakeOver, Force, JSON    bool
	Stale                    time.Duration
}
type ClaimResult struct {
	Outcome remotestate.ClaimOutcome
	Text    string
}

func (s *Service) Claim(req ClaimRequest) (ClaimResult, error) {
	return runRemoteClaim(s.deps, req.ProjectsRoot, req.Task, req.Note, req.TakeOver, req.Force, req.JSON, req.Stale)
}

type ReleaseRequest struct {
	ProjectsRoot, Task string
	Force              bool
}
type ReleaseResult struct {
	Outcome remotestate.ReleaseOutcome
	Text    string
}

func (s *Service) Release(req ReleaseRequest) (ReleaseResult, error) {
	return runRemoteRelease(s.deps, req.ProjectsRoot, req.Task, req.Force)
}
