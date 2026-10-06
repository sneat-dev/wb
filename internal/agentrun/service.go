// Package agentrun composes the existing agent authorities for CLI and remote callers.
package agentrun

import (
	"context"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"os"
	"time"
)

type LookupRequest struct{ ProjectsRoot, AgentID string }
type ListRequest struct{ ProjectsRoot string }
type DispatchRequest struct {
	ProjectsRoot string
	Stderr       io.Writer
	Request      agents.DispatchRequest
}
type AwaitRequest struct {
	ProjectsRoot, AgentID string
	WaitTimeout           time.Duration
}
type LogsRequest struct {
	ProjectsRoot, AgentID string
	Tail                  int
	Raw                   bool
}

// Operations is the six concrete effects shared by command adapters and protocol handling.
type Operations struct {
	Dispatch func(context.Context, DispatchRequest) (agents.Result, error)
	Status   func(context.Context, LookupRequest) (agents.Result, error)
	Await    func(context.Context, AwaitRequest) (agents.Result, error)
	List     func(context.Context, ListRequest) ([]agents.Result, error)
	Logs     func(context.Context, LogsRequest) (string, error)
	Stop     func(context.Context, LookupRequest) (agents.Record, error)
}

type Dependencies struct {
	Root, EnsureRoot func(string) (string, error)
	ConfigPath       func() string
	LoadConfig       func(string) (agents.Config, error)
	Dispatch         func(context.Context, agents.DispatchRequest, agents.DispatchDeps) (agents.Record, error)
	Stop             func(agents.Store, string, agents.OwnerDeps) (agents.Record, error)
	SpawnOwner       func(string, func() (string, error)) (int, error)
	Executable       func() (string, error)
	Now              func() time.Time
	Wait             func(context.Context, time.Duration) error
	BeforeCreate     func(string, []string) error
	AfterCreate      func(string, io.Writer, string, []worktrees.CreateResult)
	Transcripts      TranscriptReader
}

type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }

// DefaultDependencies supplies real operations; composition binds the two creation callbacks.
func DefaultDependencies() Dependencies {
	return Dependencies{Root: wbhome.Root, EnsureRoot: wbhome.EnsureRoot, ConfigPath: wbconfig.DefaultPath, LoadConfig: agents.LoadConfigFile, Dispatch: agents.Dispatch, Stop: agents.StopRun, SpawnOwner: agents.SpawnOwner, Executable: os.Executable, Now: time.Now, Wait: wait, Transcripts: DefaultTranscriptReader()}
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (s *Service) Operations() Operations {
	return Operations{Dispatch: s.Dispatch, Status: s.Status, Await: s.Await, List: s.List, Logs: s.Logs, Stop: s.Stop}
}
func (s *Service) store(root string) (agents.Store, error) {
	home, err := s.deps.Root(root)
	if err != nil {
		return agents.Store{}, err
	}
	return agents.NewStore(home), nil
}
func (s *Service) load(request LookupRequest) (agents.Store, agents.Record, error) {
	store, err := s.store(request.ProjectsRoot)
	if err != nil {
		return agents.Store{}, agents.Record{}, err
	}
	record, err := store.Load(request.AgentID)
	return store, record, err
}
func (s *Service) PrepareDispatch(r DispatchRequest) (agents.Store, agents.DispatchDeps, error) {
	home, err := s.deps.EnsureRoot(r.ProjectsRoot)
	if err != nil {
		return agents.Store{}, agents.DispatchDeps{}, err
	}
	store := agents.NewStore(home)
	deps := agents.DispatchDeps{ProjectsRoot: r.ProjectsRoot, Home: home, ConfigPath: s.deps.ConfigPath(), LoadConfig: func() (agents.Config, error) { return s.deps.LoadConfig(s.deps.ConfigPath()) }, Now: s.deps.Now,
		BeforeCreate: func(repos []string) error { return s.deps.BeforeCreate(r.ProjectsRoot, repos) }, AfterCreate: func(_ []string, results []worktrees.CreateResult) {
			s.deps.AfterCreate(r.ProjectsRoot, r.Stderr, r.Request.Base, results)
		}, SpawnOwner: func(id string) (int, error) { return s.deps.SpawnOwner(store.Dir(id), s.deps.Executable) }}
	return store, deps, nil
}
func (s *Service) Dispatch(ctx context.Context, r DispatchRequest) (agents.Result, error) {
	store, deps, err := s.PrepareDispatch(r)
	if err != nil {
		return agents.Result{}, err
	}
	record, err := s.deps.Dispatch(ctx, r.Request, deps)
	if err != nil {
		return agents.Result{}, err
	}
	return store.Render(record), nil
}
func (s *Service) Status(_ context.Context, r LookupRequest) (agents.Result, error) {
	store, record, err := s.load(r)
	if err != nil {
		return agents.Result{}, err
	}
	return store.Render(record), nil
}
func (s *Service) Await(ctx context.Context, r AwaitRequest) (agents.Result, error) {
	store, err := s.store(r.ProjectsRoot)
	if err != nil {
		return agents.Result{}, err
	}
	deadline := time.Time{}
	if r.WaitTimeout > 0 {
		deadline = s.deps.Now().Add(r.WaitTimeout)
	}
	return (Awaiter{Load: store.Load, Render: store.Render, Now: s.deps.Now, Wait: s.deps.Wait}).Await(ctx, r.AgentID, deadline)
}
func (s *Service) List(_ context.Context, r ListRequest) ([]agents.Result, error) {
	store, err := s.store(r.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	records, err := store.List()
	if err != nil {
		return nil, err
	}
	results := make([]agents.Result, 0, len(records))
	for _, record := range records {
		results = append(results, store.Render(record))
	}
	return results, nil
}
func (s *Service) Logs(_ context.Context, r LogsRequest) (string, error) {
	_, record, err := s.load(LookupRequest{r.ProjectsRoot, r.AgentID})
	if err != nil {
		return "", err
	}
	return s.deps.Transcripts.Read(record, r.Tail, r.Raw)
}
func (s *Service) Stop(_ context.Context, r LookupRequest) (agents.Record, error) {
	store, err := s.store(r.ProjectsRoot)
	if err != nil {
		return agents.Record{}, err
	}
	return s.deps.Stop(store, r.AgentID, agents.DefaultOwnerDeps())
}
