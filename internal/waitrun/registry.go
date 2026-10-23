package waitrun

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"time"
)

type Registry struct {
	EnsureRoot func(string) (string, error)
	Register   func(string, waitregistry.Record) (func(), error)
	List       func(string, waitregistry.Options) ([]waitregistry.Record, error)
	Prune      func(string, waitregistry.Options) (int, error)
	Now        func() time.Time
	PID        func() int
	Identity   func() (worktrees.AgentIdentity, bool)
}

func DefaultRegistry() Registry {
	return Registry{EnsureRoot: wbhome.EnsureRoot, Register: waitregistry.Register, List: waitregistry.List, Prune: waitregistry.Prune, Now: time.Now, PID: os.Getpid, Identity: worktrees.RegisteredIdentity}
}

type Registration struct {
	ProjectsRoot, Kind string
	Targets            []Reference
	Until              string
	Slice              time.Duration
}

// RegisterWait retains best-effort metadata and a nonnil release on every path.
func (registry Registry) RegisterWait(request Registration) func() {
	home, err := registry.EnsureRoot(request.ProjectsRoot)
	if err != nil {
		return func() {}
	}
	selectors := make([]string, 0, len(request.Targets))
	for _, target := range request.Targets {
		selectors = append(selectors, target.Selector)
	}
	record := waitregistry.Record{ID: fmt.Sprintf("%d-%d", registry.PID(), registry.Now().UnixNano()), PID: registry.PID(), Kind: request.Kind, Targets: selectors, Until: request.Until, StartedAt: registry.Now().UTC(), Deadline: registry.Now().UTC().Add(request.Slice), ResumeArgs: append([]string{"wb", "wait", request.Kind}, append(selectors, "--until", request.Until)...)}
	if identity, ok := registry.Identity(); ok {
		record.WBSessionID = identity.WBSessionID
	}
	release, err := registry.Register(home, record)
	if err != nil {
		return func() {}
	}
	return release
}

type ListRequest struct {
	ProjectsRoot string
	Prune        bool
}
type ListResult struct {
	Records []waitregistry.Record
	Removed int
	Pruned  bool
}

func (registry Registry) Inspect(request ListRequest) (ListResult, error) {
	home, err := registry.EnsureRoot(request.ProjectsRoot)
	if err != nil {
		return ListResult{}, err
	}
	if request.Prune {
		removed, err := registry.Prune(home, waitregistry.Options{})
		return ListResult{Removed: removed, Pruned: true}, err
	}
	records, err := registry.List(home, waitregistry.Options{})
	return ListResult{Records: records}, err
}
