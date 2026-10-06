// Package streamrun binds existing stream engines to WB's native operations.
// It accepts plain requests and never constructs CLI commands.
package streamrun

import (
	"context"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Config binds private root identity resolution and the verified hook executable.
type Config struct {
	Machine    func(string) (string, error)
	Login      func() (string, error)
	Executable func() string
}

// Creation carries the already prepared task provenance for a native operation.
type Creation struct {
	ProjectsRoot    string
	Base            string
	WorkLog         worktrees.WorkLogOptions
	SessionRequired bool
}

// SyncRequest carries one parsed reconciliation request.
type SyncRequest struct {
	ProjectsRoot, Name, Base string
	Libraries                []streamsync.Library
	Verify, AllowMidReview   bool
	PushTrigger              streamsync.PushTrigger
	PushReason               string
	Timeout                  time.Duration
}

// Service keeps immutable bindings; every operation builds fresh native state.
type Service struct {
	config     Config
	open       func(string) (*streams.Store, error)
	start      func(*streams.Engine, context.Context, streams.StartOptions, []string) (streams.StartResult, error)
	join       func(*streams.Engine, context.Context, streams.JoinOptions) (streams.StartResult, error)
	status     func(*streams.Engine, context.Context, string) (streams.Status, error)
	end        func(*streams.Engine, context.Context, streams.EndOptions) (streams.EndResult, error)
	graph      func(string, []string) ([]string, bool, error)
	registered func() string
	sync       func(context.Context, streamsync.Options) (streamsync.Result, error)
}

// New binds production methods without retaining execution-specific options.
func New(config Config) *Service {
	return &Service{config: config, open: streams.Open, start: (*streams.Engine).Start, join: (*streams.Engine).Join, status: (*streams.Engine).Status, end: (*streams.Engine).End, graph: proposedTransitiveConsumers, registered: SessionIdentity}
}
func SessionIdentity() string {
	identity, ok := worktrees.RegisteredIdentity()
	if !ok {
		return ""
	}
	return identity.WBSessionID
}
func (service *Service) Identity(root string) (login, machine string) {
	resolved, err := service.config.Machine(root)
	if err != nil {
		return "", ""
	}
	machine = resolved
	if resolved, err := service.config.Login(); err == nil {
		login = resolved
	}
	return login, machine
}
func (service *Service) engine(request Creation, store *streams.Store) *streams.Engine {
	login, machine := service.Identity(request.ProjectsRoot)
	return &streams.Engine{Store: store, Git: streams.ExecGit{}, GitHub: streams.ExecGitHub{}, Worktrees: &streamWorktrees{projectsRoot: request.ProjectsRoot, workLog: request.WorkLog, sessionMode: request.SessionRequired, base: request.Base, create: worktrees.Create, cleanup: worktrees.Cleanup}, ProjectsRoot: request.ProjectsRoot, HooksCheck: streams.InstalledHooksChecker(service.config.Executable(), request.ProjectsRoot), Login: login, Machine: machine, Session: service.registered()}
}

// Start preserves graph reporting after successful native creation.
func (service *Service) Start(ctx context.Context, request Creation, options streams.StartOptions) (streams.StartResult, error) {
	store, err := service.open(request.ProjectsRoot)
	if err != nil {
		return streams.StartResult{}, err
	}
	engine := service.engine(request, store)
	transitive, proposed, err := service.graph(request.ProjectsRoot, options.Repositories)
	if err != nil {
		return streams.StartResult{}, err
	}
	result, err := service.start(engine, ctx, options, transitive)
	if err != nil {
		return result, err
	}
	if !proposed {
		result.Reported = append(result.Reported, streams.PreflightFinding{Check: "transitive-membership", Status: streams.PreflightUnknown, Detail: "no dependency graph evidence on this machine; run `wb deps graph --fleet --format json` so a transitive consumer left out of the stream is named"})
	}
	return result, nil
}
func (service *Service) Join(ctx context.Context, request Creation, options streams.JoinOptions) (streams.StartResult, error) {
	store, err := service.open(request.ProjectsRoot)
	if err != nil {
		return streams.StartResult{}, err
	}
	return service.join(service.engine(request, store), ctx, options)
}
func (service *Service) List(_ context.Context, root string) ([]streams.Stream, []streams.Unreadable, error) {
	store, err := service.open(root)
	if err != nil {
		return nil, nil, err
	}
	engine := service.engine(Creation{ProjectsRoot: root}, store)
	return engine.Store.List()
}
func (service *Service) Status(ctx context.Context, root, name string) (streams.Status, error) {
	store, err := service.open(root)
	if err != nil {
		return streams.Status{}, err
	}
	return service.status(service.engine(Creation{ProjectsRoot: root}, store), ctx, name)
}
func (service *Service) End(ctx context.Context, root string, options streams.EndOptions) (streams.EndResult, error) {
	store, err := service.open(root)
	if err != nil {
		return streams.EndResult{}, err
	}
	return service.end(service.engine(Creation{ProjectsRoot: root}, store), ctx, options)
}
func (service *Service) Delete(root, name string) error {
	store, err := service.open(root)
	if err != nil {
		return err
	}
	return store.Delete(name)
}
