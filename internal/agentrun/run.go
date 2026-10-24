package agentrun

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

// Run resolves a task type and profile, prepares one execution, and starts its
// detached owner. It returns as soon as the owner is running.
func (s *Service) Run(ctx context.Context, r RunRequest) (agents.Result, error) {
	if strings.TrimSpace(r.Request.Task) == "" {
		return agents.Result{}, agents.NewRequestError("a task is required")
	}
	config, err := s.deps.LoadConfig(s.deps.ConfigPath())
	if err != nil {
		return agents.Result{}, err
	}
	planner := agents.Planner{Config: config, Probe: func(resolved agents.Resolved) error {
		return s.deps.Preflight(ctx, resolved)
	}}
	plan, err := planner.Plan(r.Request)
	if err != nil {
		return agents.Result{}, err
	}
	store, deps, err := s.PrepareDispatch(DispatchRequest{ProjectsRoot: r.ProjectsRoot, Stderr: r.Stderr})
	if err != nil {
		return agents.Result{}, err
	}
	worktree := strings.TrimSpace(r.Request.Worktree)
	if plan.WorktreeMode == agents.ModeCurrent {
		worktree = currentDirectory()
	}
	prepared, err := s.deps.PrepareRun(ctx, agents.DispatchRequest{
		Mode: plan.WorktreeMode, Worktree: worktree, Plan: &plan, Task: r.Request.Task,
		Repository: r.Request.Repository, Base: r.Request.Base, Branch: r.Request.Branch,
		Timeout:        time.Duration(plan.TimeoutMS) * time.Millisecond,
		AllowCanonical: r.Request.AllowCanonical, AllowDirty: r.Request.AllowDirty,
		AgentID: r.AgentID,
	}, deps)
	if err != nil {
		return agents.Result{}, err
	}
	record, err := s.deps.StartRun(prepared, deps)
	if err != nil {
		return store.Render(record), err
	}
	return store.Render(record), nil
}

func currentDirectory() string {
	directory, err := os.Getwd()
	if err != nil {
		return "."
	}
	return directory
}

// Plan resolves a request without starting anything. It never probes a harness,
// so a dry run creates no worktree, writes no record and starts no process.
func (s *Service) Plan(_ context.Context, r PlanRequest) (agents.Plan, error) {
	config, err := s.deps.LoadConfig(s.deps.ConfigPath())
	if err != nil {
		return agents.Plan{}, err
	}
	return agents.Planner{Config: config}.Plan(r.Request)
}

// Profiles reads the machine-readable profile catalog.
func (s *Service) Profiles(_ context.Context, r DiscoveryRequest) (ProfilesCatalog, error) {
	config, err := s.deps.LoadConfig(s.deps.ConfigPath())
	if err != nil {
		return ProfilesCatalog{}, err
	}
	catalog := ProfilesCatalog{
		Providers:      config.ProviderNames(),
		Fallback:       append([]string(nil), config.Fallback...),
		DefaultProfile: config.Defaults.Profile,
	}
	for _, name := range agents.AllHarnesses() {
		adapter, err := agents.HarnessFor(name)
		if err != nil {
			continue
		}
		catalog.Harnesses = append(catalog.Harnesses, adapter.Capabilities())
	}
	for _, name := range config.ProfileNames() {
		resolved, resolveErr := config.Resolve(name)
		entry := ProfileEntry{Name: name, Resolved: resolved, Usable: resolveErr == nil}
		if resolveErr != nil {
			entry.Problem = resolveErr.Error()
		} else if adapter, err := agents.HarnessFor(resolved.Harness); err == nil {
			entry.Capabilities = adapter.Capabilities()
		}
		catalog.Profiles = append(catalog.Profiles, entry)
	}
	return catalog, nil
}

// TaskTypes reads the machine-readable task-type catalog.
func (s *Service) TaskTypes(_ context.Context, r DiscoveryRequest) (TaskTypesCatalog, error) {
	config, err := s.deps.LoadConfig(s.deps.ConfigPath())
	if err != nil {
		return TaskTypesCatalog{}, err
	}
	catalog := TaskTypesCatalog{Default: defaultTaskType(config)}
	for _, name := range config.TaskTypesInOrder() {
		policy := config.TaskTypes[name]
		entry := TaskTypeEntry{Name: name, Policy: policy, Builtin: !config.ConfiguredTaskTypes[name]}
		if len(policy.Validate) > 0 {
			entry.Validations = map[string][]agents.ValidationStep{}
			for _, suite := range policy.Validate {
				entry.Validations[suite] = config.Validations[suite]
			}
		}
		catalog.TaskTypes = append(catalog.TaskTypes, entry)
	}
	return catalog, nil
}

func defaultTaskType(config agents.Config) string {
	if config.Defaults.TaskType != "" {
		return config.Defaults.TaskType
	}
	return "coding"
}

// Experiment re-reads one durable experiment and joins it with its member
// records.
func (s *Service) Experiment(_ context.Context, r ExperimentRequest) (agents.ExperimentReport, error) {
	store, err := s.store(r.ProjectsRoot)
	if err != nil {
		return agents.ExperimentReport{}, err
	}
	experiment, err := store.LoadExperiment(r.ExperimentID)
	if err != nil {
		return agents.ExperimentReport{}, err
	}
	return store.BuildReport(experiment), nil
}

// Diff renders one run's diff.
func (s *Service) Diff(ctx context.Context, r DiffRequest) (string, error) {
	_, record, err := s.load(LookupRequest{r.ProjectsRoot, r.AgentID})
	if err != nil {
		return "", err
	}
	return s.deps.Diff(ctx, record, r.Patch, agents.DefaultTargetDeps())
}

// Review appends one acceptance decision to a terminal execution.
func (s *Service) Review(_ context.Context, r ReviewRequest) (agents.ReviewRecord, error) {
	store, record, err := s.load(LookupRequest{r.ProjectsRoot, r.AgentID})
	if err != nil {
		return agents.ReviewRecord{}, err
	}
	result := store.Render(record)
	if !result.Terminal {
		return agents.ReviewRecord{}, agents.NewRequestError(fmt.Sprintf("run %s is still %s; review it after it finishes", r.AgentID, result.Status))
	}
	evaluator := strings.TrimSpace(r.Evaluator)
	if evaluator == "" {
		evaluator = "operator"
	}
	return store.AppendReview(r.AgentID, agents.ReviewEntry{
		Status: r.Status, Evaluator: evaluator, Note: strings.TrimSpace(r.Note), At: s.deps.Now().UTC(),
	})
}

// Prune plans or applies retention. It never touches a running, leased,
// unaccepted-required or accepted run.
func (s *Service) Prune(_ context.Context, r PruneRequest) (PrunePlan, error) {
	if r.OlderThan <= 0 {
		return PrunePlan{}, agents.NewRequestError("--older-than must be positive")
	}
	store, err := s.store(r.ProjectsRoot)
	if err != nil {
		return PrunePlan{}, err
	}
	records, err := store.List()
	if err != nil {
		return PrunePlan{}, err
	}
	now := s.deps.Now()
	plan := PrunePlan{OlderThanMS: r.OlderThan.Milliseconds(), Applied: r.Apply}
	for _, record := range records {
		result := store.Render(record)
		age := now.Sub(record.StartedAt)
		entry := PruneEntry{AgentID: record.AgentID, State: string(result.Status), AgeMS: age.Milliseconds(), Dir: store.Dir(record.AgentID), Action: "keep"}
		switch {
		case !result.Terminal:
			entry.Reason = "the execution has not finished"
		case age < r.OlderThan:
			entry.Reason = "younger than the retention bound"
		case result.Review.Status == agents.ReviewAccepted:
			entry.Reason = "accepted work is never pruned automatically"
		case result.AcceptanceRequired && !result.Review.Reviewed():
			entry.Reason = "acceptance is required and the run has not been reviewed"
		default:
			entry.Action = "remove"
			if r.Apply {
				if err := os.RemoveAll(store.Dir(record.AgentID)); err != nil {
					entry.Action = "keep"
					entry.Reason = fmt.Sprintf("remove failed: %v", err)
					break
				}
				plan.Removed++
			}
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}
