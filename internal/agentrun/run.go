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
// detached owner. It returns as soon as the owner is running, unless the caller
// asked to wait: only a waiting caller can carry out a runtime failover, because
// it is the process that observes the failure.
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
	if plan.FallbackFrom != "" {
		s.logPlanFallback(r, plan)
	}
	store, deps, err := s.PrepareDispatch(DispatchRequest{ProjectsRoot: r.ProjectsRoot, Stderr: r.Stderr})
	if err != nil {
		return agents.Result{}, err
	}
	result, err := s.startOne(ctx, r, plan, deps, retryInfo{})
	if err != nil || r.Wait <= 0 {
		return result, err
	}
	// A waiting run is the only place a runtime failover can happen: start the
	// next configured profile after an infrastructure failure, in a fresh
	// worktree, and record the chain.
	return s.awaitWithFailover(ctx, r, plan, deps, store, result)
}

// startOne admits and starts one execution for a resolved plan.
func (s *Service) startOne(ctx context.Context, r RunRequest, plan agents.Plan, deps agents.DispatchDeps, retry retryInfo) (agents.Result, error) {
	store, err := s.store(r.ProjectsRoot)
	if err != nil {
		return agents.Result{}, err
	}
	prepared, err := s.deps.PrepareRun(ctx, agents.DispatchRequest{
		Mode: plan.WorktreeMode, Worktree: strings.TrimSpace(r.Request.Worktree), Plan: &plan, Task: r.Request.Task,
		Repository: r.Request.Repository, Base: r.Request.Base, Branch: r.Request.Branch,
		Timeout:        time.Duration(plan.TimeoutMS) * time.Millisecond,
		AllowCanonical: r.Request.AllowCanonical, AllowDirty: r.Request.AllowDirty,
		AgentID: r.AgentID, RetryOf: retry.Of, FallbackFrom: retry.From, FallbackReason: retry.Reason,
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

// retryInfo is what a failover execution records about the one it replaces.
type retryInfo struct {
	Of     string
	From   string
	Reason string
}

// awaitWithFailover waits for the first attempt and, while the attempt failed for
// an infrastructure reason without leaving work behind, starts the next profile
// in the chain. It never retries a validation failure, a cancelled run, a
// timeout, or an attempt that produced changes.
func (s *Service) awaitWithFailover(ctx context.Context, r RunRequest, plan agents.Plan, deps agents.DispatchDeps, store agents.Store, first agents.Result) (agents.Result, error) {
	awaiter := Awaiter{Load: store.Load, Render: store.Render, Now: s.deps.Now, Wait: s.deps.Wait}
	current := first
	// An explicit profile or a shared checkout is never swapped silently: the
	// operator chose it, and a retry needs its own worktree.
	if plan.ProfileSource == agents.SourceExplicit || plan.ProfileSource == agents.SourceSynthesized ||
		(plan.WorktreeMode != agents.ModeAuto && plan.WorktreeMode != agents.ModeNew) ||
		!plan.AllowsFallback(agents.FallbackRuntime) {
		return awaiter.Await(ctx, current.AgentID, s.deps.Now().Add(r.Wait))
	}
	for index := 0; ; index++ {
		awaited, err := awaiter.Await(ctx, current.AgentID, s.deps.Now().Add(r.Wait))
		if err != nil {
			return awaited, err
		}
		if !awaited.Terminal || !runtimeRetryable(awaited) || index >= len(plan.FallbackChain) {
			return awaited, nil
		}
		next := plan.FallbackChain[index]
		resolved, err := s.deps.LoadConfig(s.deps.ConfigPath())
		if err != nil {
			return awaited, err
		}
		nextProfile, err := resolved.Resolve(next)
		if err != nil {
			return awaited, err
		}
		if s.deps.Preflight != nil {
			if err := s.deps.Preflight(ctx, nextProfile); err != nil {
				s.logFallback(r, next, awaited, err)
				continue
			}
		}
		nextPlan := plan
		nextPlan.Profile = nextProfile
		nextPlan.ProfileSource = agents.SourceFallback
		nextPlan.FallbackFrom = current.RequestedProfile
		nextPlan.FallbackChain = plan.FallbackChain[index+1:]
		s.logFallback(r, next, awaited, nil)
		// A retry always gets its own worktree, so partial work can never be
		// mistaken for the next profile's output.
		nextRequest := r
		nextRequest.Request.Profile = next
		nextRequest.Request.Harness = ""
		nextRequest.Request.Model = ""
		nextRequest.Request.Variant = ""
		nextRequest.Request.WorktreeMode = agents.ModeAuto
		nextRequest.Request.Worktree = ""
		result, startErr := s.startOne(ctx, nextRequest, nextPlan, deps, retryInfo{
			Of: awaited.AgentID, From: nextPlan.FallbackFrom, Reason: strings.TrimSpace(awaited.Failure),
		})
		if startErr != nil {
			return awaited, startErr
		}
		current = result
	}
}

// runtimeRetryable reports whether a terminal result is an infrastructure
// failure that a different profile could plausibly avoid.
func runtimeRetryable(result agents.Result) bool {
	if result.Status != agents.StatusFailed {
		return false
	}
	switch result.FailureCategory {
	case agents.FailureProvider, agents.FailureCredential, agents.FailureHarnessMiss:
	default:
		return false
	}
	if result.Changes != nil && (result.Changes.FilesChanged > 0 || result.Changes.Commits > 0) {
		return false
	}
	return true
}

// logPlanFallback reports a preflight substitution before the worktree exists.
func (s *Service) logPlanFallback(r RunRequest, plan agents.Plan) {
	if r.Stderr == nil {
		return
	}
	_, _ = fmt.Fprintf(r.Stderr, "wb agent: profile %s is unavailable (%s); using fallback profile %s\n",
		plan.FallbackFrom, plan.FallbackReason, plan.Profile.Profile)
}

func (s *Service) logFallback(r RunRequest, profile string, failed agents.Result, probeErr error) {
	if r.Stderr == nil {
		return
	}
	reason := failed.Failure
	if probeErr != nil {
		reason = probeErr.Error()
	}
	_, _ = fmt.Fprintf(r.Stderr, "wb agent: execution %s on profile %s failed (%s: %s); falling back to profile %s\n",
		failed.AgentID, failed.RequestedProfile, failed.FailureCategory, reason, profile)
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
		// Every name in the closed harness list has an adapter.
		adapter, _ := agents.HarnessFor(name)
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

// leasedWorktree reports whether another execution still holds the writer claim
// on the run's checkout, which makes its artefacts unsafe to remove.
func leasedWorktree(store agents.Store, record agents.Record) bool {
	if strings.TrimSpace(record.WorktreeDir) == "" {
		return false
	}
	live, err := store.LeaseLive(record.WorktreeDir)
	return err == nil && live
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
		case leasedWorktree(store, record):
			entry.Reason = "the worktree is still claimed by a live execution"
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
