// Package agentrun composes the existing agent authorities for CLI and remote callers.
package agentrun

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
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

// RunRequest is one high-level execution: the caller names the work and the
// policy, and the planner resolves the rest.
type RunRequest struct {
	ProjectsRoot string
	Stderr       io.Writer
	Request      agents.RunRequest
	// AgentID is normally empty; an experiment pre-allocates it.
	AgentID string
	// Wait makes the service wait for the attempt and, when the task type
	// allows it, carry out a runtime failover. Zero returns as soon as the
	// owner starts.
	Wait time.Duration
}

// PlanRequest is one dry-run resolution.
type PlanRequest struct {
	ProjectsRoot string
	Request      agents.RunRequest
}

// CompareRequest is one experiment: the same task across several profiles.
type CompareRequest struct {
	ProjectsRoot string
	Stderr       io.Writer
	Task         string
	TaskType     string
	Complexity   string
	Effort       string
	Profiles     []string
	Repeat       int
	Concurrency  int
	Repository   string
	Base         string
	// Timeout bounds each attempt; empty means each profile's own bound.
	Timeout    time.Duration
	TimeoutSet bool
}

// ExperimentRequest looks up one durable experiment.
type ExperimentRequest struct{ ProjectsRoot, ExperimentID string }

// DiscoveryRequest is one catalog read.
type DiscoveryRequest struct{ ProjectsRoot string }

// DiffRequest reads one run's diff.
type DiffRequest struct {
	ProjectsRoot, AgentID string
	Patch                 bool
}

// ReviewRequest appends one acceptance decision.
type ReviewRequest struct {
	ProjectsRoot, AgentID string
	Status                string
	Evaluator             string
	Note                  string
}

// PruneRequest plans or applies retention.
type PruneRequest struct {
	ProjectsRoot string
	OlderThan    time.Duration
	Apply        bool
}

// PruneEntry is one candidate in a retention plan.
type PruneEntry struct {
	AgentID string `json:"agent_id"`
	State   string `json:"state"`
	AgeMS   int64  `json:"age_ms"`
	Action  string `json:"action"`
	Reason  string `json:"reason,omitempty"`
	Dir     string `json:"dir,omitempty"`
}

// PrunePlan is the durable, reviewable output of a retention run.
type PrunePlan struct {
	OlderThanMS int64        `json:"older_than_ms"`
	Applied     bool         `json:"applied"`
	Entries     []PruneEntry `json:"entries"`
	Removed     int          `json:"removed"`
}

// ProfilesCatalog is the machine-readable profile discovery document.
type ProfilesCatalog struct {
	Profiles       []ProfileEntry               `json:"profiles"`
	Providers      []string                     `json:"providers"`
	Fallback       []string                     `json:"fallback,omitempty"`
	DefaultProfile string                       `json:"default_profile,omitempty"`
	Harnesses      []agents.HarnessCapabilities `json:"harnesses"`
}

// ProfileEntry is one configured profile plus what it resolves to.
type ProfileEntry struct {
	Name         string                     `json:"name"`
	Resolved     agents.Resolved            `json:"resolved"`
	Capabilities agents.HarnessCapabilities `json:"capabilities"`
	Usable       bool                       `json:"usable"`
	Problem      string                     `json:"problem,omitempty"`
}

// TaskTypesCatalog is the machine-readable task-type discovery document.
type TaskTypesCatalog struct {
	TaskTypes []TaskTypeEntry `json:"task_types"`
	Default   string          `json:"default_task_type"`
}

// TaskTypeEntry is one task type with its resolved policy.
type TaskTypeEntry struct {
	Name        string                             `json:"name"`
	Policy      agents.TaskType                    `json:"policy"`
	Builtin     bool                               `json:"builtin"`
	Validations map[string][]agents.ValidationStep `json:"validations,omitempty"`
}

// Operations is every concrete effect shared by command adapters and protocol
// handling. The first six are the remote protocol's surface; the rest are
// local-only.
type Operations struct {
	Dispatch   func(context.Context, DispatchRequest) (agents.Result, error)
	Status     func(context.Context, LookupRequest) (agents.Result, error)
	Await      func(context.Context, AwaitRequest) (agents.Result, error)
	List       func(context.Context, ListRequest) ([]agents.Result, error)
	Logs       func(context.Context, LogsRequest) (string, error)
	Stop       func(context.Context, LookupRequest) (agents.Record, error)
	Run        func(context.Context, RunRequest) (agents.Result, error)
	Plan       func(context.Context, PlanRequest) (agents.Plan, error)
	Compare    func(context.Context, CompareRequest) (agents.ExperimentReport, error)
	Experiment func(context.Context, ExperimentRequest) (agents.ExperimentReport, error)
	Profiles   func(context.Context, DiscoveryRequest) (ProfilesCatalog, error)
	TaskTypes  func(context.Context, DiscoveryRequest) (TaskTypesCatalog, error)
	Diff       func(context.Context, DiffRequest) (string, error)
	Review     func(context.Context, ReviewRequest) (agents.ReviewRecord, error)
	Prune      func(context.Context, PruneRequest) (PrunePlan, error)
}

type Dependencies struct {
	Root, EnsureRoot func(string) (string, error)
	ConfigPath       func() string
	LoadConfig       func(string) (agents.Config, error)
	Dispatch         func(context.Context, agents.DispatchRequest, agents.DispatchDeps) (agents.Record, error)
	PrepareRun       func(context.Context, agents.DispatchRequest, agents.DispatchDeps) (*agents.PreparedRun, error)
	StartRun         func(*agents.PreparedRun, agents.DispatchDeps) (agents.Record, error)
	Stop             func(agents.Store, string, agents.OwnerDeps) (agents.Record, error)
	SpawnOwner       func(string, func() (string, error), []*os.File) (int, error)
	Executable       func() (string, error)
	Now              func() time.Time
	Wait             func(context.Context, time.Duration) error
	BeforeCreate     func(string, []string) error
	AfterCreate      func(string, io.Writer, string, []worktrees.CreateResult)
	Transcripts      TranscriptReader
	Preflight        func(context.Context, agents.Resolved) error
	DetectVersion    func(context.Context, string) string
	Diff             func(context.Context, agents.Record, bool, agents.TargetDeps) (string, error)
	NewAgentID       func() (string, error)
	NewExperimentID  func() string
}

type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }

// DefaultDependencies supplies real operations; composition binds the two creation callbacks.
func DefaultDependencies() Dependencies {
	preflightDeps := agents.DefaultPreflightDeps()
	return Dependencies{
		Root: wbhome.Root, EnsureRoot: wbhome.EnsureRoot, ConfigPath: wbconfig.DefaultPath,
		LoadConfig: agents.LoadConfigFile, Dispatch: agents.Dispatch,
		PrepareRun: agents.PrepareRun, StartRun: agents.StartRun,
		Stop: agents.StopRun, SpawnOwner: agents.SpawnOwner, Executable: os.Executable,
		Now: time.Now, Wait: wait, Transcripts: DefaultTranscriptReader(),
		Preflight: func(ctx context.Context, resolved agents.Resolved) error {
			return agents.Preflight(ctx, resolved, preflightDeps)
		},
		DetectVersion: func(ctx context.Context, harness string) string {
			return agents.DetectVersion(ctx, harness, preflightDeps)
		},
		Diff:            agents.RenderDiff,
		NewAgentID:      agents.NewID,
		NewExperimentID: agents.NewExperimentID,
	}
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
	return Operations{
		Dispatch: s.Dispatch, Status: s.Status, Await: s.Await, List: s.List,
		Logs: s.Logs, Stop: s.Stop, Run: s.Run, Plan: s.Plan, Compare: s.Compare,
		Experiment: s.Experiment, Profiles: s.Profiles, TaskTypes: s.TaskTypes,
		Diff: s.Diff, Review: s.Review, Prune: s.Prune,
	}
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

// PrepareDispatch assembles the agent-side seams for one dispatch or run.
func (s *Service) PrepareDispatch(r DispatchRequest) (agents.Store, agents.DispatchDeps, error) {
	home, err := s.deps.EnsureRoot(r.ProjectsRoot)
	if err != nil {
		return agents.Store{}, agents.DispatchDeps{}, err
	}
	store := agents.NewStore(home)
	deps := agents.DispatchDeps{
		ProjectsRoot: r.ProjectsRoot, Home: home, ConfigPath: s.deps.ConfigPath(),
		LoadConfig: func() (agents.Config, error) { return s.deps.LoadConfig(s.deps.ConfigPath()) },
		Now:        s.deps.Now,
		BeforeCreate: func(repos []string) error {
			return s.deps.BeforeCreate(r.ProjectsRoot, repos)
		},
		AfterCreate: func(_ []string, results []worktrees.CreateResult) {
			s.deps.AfterCreate(r.ProjectsRoot, r.Stderr, r.Request.Base, results)
		},
		SpawnOwner: func(id string, extraFiles []*os.File) (int, error) {
			return s.deps.SpawnOwner(store.Dir(id), s.deps.Executable, extraFiles)
		},
		Preflight:     s.deps.Preflight,
		DetectVersion: s.deps.DetectVersion,
	}
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
