package agentrun

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

// compareSlack bounds how long the command waits beyond an attempt's own
// timeout before giving up on it.
const compareSlack = time.Minute

// Compare runs the same task across several profiles from one pinned base
// commit. Every worktree is created before any worker starts, so a base that
// moves mid-experiment produces a reported `base_mismatch` attempt instead of a
// silently unfair comparison.
func (s *Service) Compare(ctx context.Context, r CompareRequest) (agents.ExperimentReport, error) {
	if strings.TrimSpace(r.Task) == "" {
		return agents.ExperimentReport{}, agents.NewRequestError("a task is required")
	}
	if len(r.Profiles) == 0 {
		return agents.ExperimentReport{}, agents.NewRequestError("--profiles requires at least one profile")
	}
	if r.Repeat < 1 {
		return agents.ExperimentReport{}, agents.NewRequestError("--repeat must be at least 1")
	}
	if r.Concurrency < 1 {
		return agents.ExperimentReport{}, agents.NewRequestError("--concurrency must be at least 1")
	}
	config, err := s.deps.LoadConfig(s.deps.ConfigPath())
	if err != nil {
		return agents.ExperimentReport{}, err
	}
	store, deps, err := s.PrepareDispatch(DispatchRequest{ProjectsRoot: r.ProjectsRoot, Stderr: r.Stderr})
	if err != nil {
		return agents.ExperimentReport{}, err
	}
	experimentID := s.deps.NewExperimentID()
	experiment := agents.Experiment{
		ExperimentID: experimentID, TaskType: r.TaskType, Complexity: r.Complexity,
		Base: r.Base, Profiles: append([]string(nil), r.Profiles...), Repeat: r.Repeat,
		Concurrency: r.Concurrency, CreatedAt: s.deps.Now().UTC(),
	}
	experiment.TaskSHA256, experiment.TaskBytes = agents.TaskIdentity(r.Task)
	plans := make(map[string]agents.Plan, len(r.Profiles))
	for _, profile := range r.Profiles {
		// Fallback is deliberately disabled inside a comparison: swapping a
		// profile for another one would invalidate the experiment.
		planner := agents.Planner{Config: config}
		plan, err := planner.Plan(agents.RunRequest{
			Task: r.Task, TaskType: r.TaskType, Complexity: r.Complexity, Effort: r.Effort,
			Profile: profile, Timeout: r.Timeout, TimeoutSet: r.TimeoutSet,
		})
		if err != nil {
			return agents.ExperimentReport{}, err
		}
		plans[profile] = plan
	}
	// A mandatory sandbox admits only one contained execution at a time. Refuse
	// a concurrent comparison before any worktree exists; a serial comparison
	// still validates every plan's snapshot and supported harness.
	if config.Execution != nil {
		if r.Concurrency > 1 {
			return agents.ExperimentReport{}, agents.NewRequestError(fmt.Sprintf("--concurrency %d is not available under agents.execution max_parallel %d; a contained comparison must run serially", r.Concurrency, config.Execution.MaxParallel))
		}
		for _, profile := range r.Profiles {
			plan := plans[profile]
			if err := agents.RequireRunnableExecution(config.Execution, plan.Execution, "comparison plan", plan.Profile.Harness); err != nil {
				return agents.ExperimentReport{}, err
			}
		}
	}
	// Allocate every attempt identity before anything runs, so the plan is
	// durable and a crash still leaves an inspectable experiment.
	for _, profile := range r.Profiles {
		for repeat := 0; repeat < r.Repeat; repeat++ {
			agentID, err := s.deps.NewAgentID()
			if err != nil {
				return agents.ExperimentReport{}, err
			}
			experiment.Attempts = append(experiment.Attempts, agents.Attempt{
				Profile: profile, RepeatIndex: repeat, AgentID: agentID,
				Operation: experimentOperation(experimentID, profile, repeat),
				Status:    agents.AttemptPlanned,
			})
		}
	}
	experiment.UncontrolledDifferences = uncontrolledDifferences(plans, r.Profiles)
	if err := store.CreateExperiment(experiment); err != nil {
		return agents.ExperimentReport{}, err
	}

	prepared := make([]*agents.PreparedRun, 0, len(experiment.Attempts))
	pinnedBase := ""
	for index := range experiment.Attempts {
		attempt := &experiment.Attempts[index]
		plan := plans[attempt.Profile]
		preparedRun, prepareErr := s.deps.PrepareRun(ctx, agents.DispatchRequest{
			Mode: agents.ModeAuto, Worktree: attempt.Operation, Plan: &plan, Task: r.Task,
			Repository: r.Repository, Base: r.Base, Timeout: time.Duration(plan.TimeoutMS) * time.Millisecond,
			AgentID: attempt.AgentID, ExperimentID: experimentID, RepeatIndex: attempt.RepeatIndex,
		}, deps)
		if prepareErr != nil {
			attempt.Status = agents.AttemptFailed
			attempt.Failure = prepareErr.Error()
			s.recordFailedAttempt(store, experimentID, *attempt, r.Task, plan, prepareErr, s.deps.Now)
			continue
		}
		attempt.BaseSHA = preparedRun.Record.BaseSHA
		if pinnedBase == "" {
			pinnedBase = preparedRun.Record.BaseSHA
		}
		if preparedRun.Record.BaseSHA != pinnedBase {
			attempt.Status = agents.AttemptBaseMismatch
			attempt.Failure = fmt.Sprintf("base moved: this worktree started at %s but the experiment is pinned to %s", preparedRun.Record.BaseSHA, pinnedBase)
			_ = preparedRun.Fail(agents.StateFailed, attempt.Failure, agents.FailureBaseMismatch, s.deps.Now)
			continue
		}
		attempt.Status = agents.AttemptDispatched
		prepared = append(prepared, preparedRun)
	}
	experiment.BaseSHA = pinnedBase

	// A bounded pool: start while there is room, then await the oldest attempt.
	// Because every worktree already exists, starting later is cheap and the
	// comparison still runs concurrently.
	deadlineFor := func(run *agents.PreparedRun) time.Time {
		timeout := time.Duration(run.Record.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = agents.DefaultTimeout
		}
		// Validation runs after the harness, so it belongs to the wait bound.
		return s.deps.Now().Add(timeout + agents.ValidationBudget(run.Record.Validation.Steps) + compareSlack)
	}
	awaiter := Awaiter{Load: store.Load, Render: store.Render, Now: s.deps.Now, Wait: s.deps.Wait}
	// A sliding window: start while there is room, then await the oldest attempt
	// and fill the freed slot. Fixed batches would leave capacity idle.
	next, active := 0, make([]*agents.PreparedRun, 0, r.Concurrency)
	for next < len(prepared) || len(active) > 0 {
		for next < len(prepared) && len(active) < r.Concurrency {
			run := prepared[next]
			next++
			if _, err := s.deps.StartRun(run, deps); err != nil {
				// A failed launch is already recorded as a failed execution.
				continue
			}
			active = append(active, run)
		}
		if len(active) == 0 {
			break
		}
		oldest := active[0]
		active = active[1:]
		if _, err := awaiter.Await(ctx, oldest.Record.AgentID, deadlineFor(oldest)); err != nil {
			return agents.ExperimentReport{}, err
		}
	}
	return store.BuildReport(experiment), nil
}

// recordFailedAttempt keeps one row for an attempt that never started, so
// `wb agent experiment` shows every planned comparison.
func (s *Service) recordFailedAttempt(store agents.Store, experimentID string, attempt agents.Attempt, task string, plan agents.Plan, failure error, now func() time.Time) {
	_ = store.Create(failedAttemptRecord(experimentID, attempt, task, plan, failure, now))
}

// failedAttemptRecord maps a planned attempt that never started onto the same
// durable shape a started run uses. The mandatory policy is copied by value so
// a later edit to the configuration or to the plan map cannot rewrite what the
// record claims.
func failedAttemptRecord(experimentID string, attempt agents.Attempt, task string, plan agents.Plan, failure error, now func() time.Time) agents.Record {
	digest, size := agents.TaskIdentity(task)
	category := agents.FailureWorktree
	if agents.IsRequestError(failure) {
		category = agents.FailureConfig
	}
	return agents.Record{
		AgentID: attempt.AgentID, State: agents.StateFailed, ExecutionStatus: agents.StatusFailed,
		FailureCategory: category, Failure: failure.Error(),
		RequestedProfile: plan.Profile.Profile, Resolved: plan.Profile,
		TaskType: plan.TaskType, TaskTypeSource: plan.TaskTypeSource,
		ProfileSource: plan.ProfileSource, WorktreeMode: agents.ModeAuto,
		Worktree: attempt.Operation, ExperimentID: experimentID, RepeatIndex: attempt.RepeatIndex,
		Task: task, TaskSummary: agents.SummaryLine(task), TaskSHA256: digest, TaskBytes: size,
		StartedAt: now().UTC(), FinishedAt: now().UTC(),
		Telemetry:  agents.Telemetry{Available: false, Source: agents.UsageUnavailable, UnavailableReason: "the attempt never started"},
		Cost:       agents.CostRecord{Kind: "api_equivalent", Source: agents.CostUnavailable},
		Validation: agents.ValidationRecord{Status: agents.ValidationSkipped, Reason: "the attempt never started"},
		// The mandatory policy travels with the failed attempt, so a comparison
		// can state which sandbox it was planned under.
		Execution: agents.SnapshotExecution(plan.Execution),
	}
}

func experimentOperation(experimentID, profile string, repeat int) string {
	suffix := experimentID
	if len(suffix) > len("exp-")+8 {
		suffix = suffix[:len("exp-")+8]
	}
	return fmt.Sprintf("%s-%s-r%d", suffix, profile, repeat+1)
}

// uncontrolledDifferences states what WB could not hold equal, so nobody reads
// the comparison as a controlled trial.
func uncontrolledDifferences(plans map[string]agents.Plan, profiles []string) []string {
	differences := []string{"the task text and the base commit are identical; the harness tool set, context window and provider load are not controlled"}
	seen := map[string]bool{}
	for _, profile := range profiles {
		plan, ok := plans[profile]
		if !ok {
			continue
		}
		capability := agents.HarnessCapabilityFor(plan.Profile.Harness)
		key := plan.Profile.Harness + ":" + capability
		if seen[key] {
			continue
		}
		seen[key] = true
		differences = append(differences, fmt.Sprintf("%s runs with its own harness tool set and reports usage as %s", plan.Profile.Harness, usageFor(plan.Profile.Harness)))
	}
	return differences
}

func usageFor(harness string) string {
	if harness == agents.HarnessDSH {
		return "unavailable"
	}
	return "harness events"
}
