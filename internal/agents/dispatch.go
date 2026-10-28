package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// Worktree modes. Exactly one is required: creating a new isolated workspace
// and modifying an existing one have materially different intent, so they stay
// two explicit options rather than one ambiguous --worktree.
const (
	ModeNew      = "new"
	ModeExisting = "existing"
)

// DefaultTimeout bounds a dispatched run when the caller names no bound.
const DefaultTimeout = 30 * time.Minute

// DispatchRequest is one requested offload.
type DispatchRequest struct {
	Mode     string
	Worktree string

	// Plan is the resolved execution plan. When it is nil the request is the
	// legacy `dispatch` shape and Profile is resolved on its own.
	Plan    *Plan
	Profile string
	Task    string

	// Repository is owner/repository. Empty means "derive it from the invoking
	// checkout's origin", exactly as `wb worktree create` does.
	Repository string
	// Branch and Base are optional pass-throughs to WB's existing worktree
	// naming policy; empty leaves that policy in charge.
	Branch string
	Base   string

	Timeout        time.Duration
	AllowCanonical bool
	AllowDirty     bool

	// AgentID lets a caller pre-allocate the identity, which an experiment must
	// do before it records its plan. Empty mints a fresh one.
	AgentID string
	// ExperimentID and RepeatIndex place this execution in a comparison.
	ExperimentID string
	RepeatIndex  int

	// RetryOf, FallbackFrom and FallbackReason describe an infrastructure
	// failover: this execution replaces another one after its provider failed.
	RetryOf        string
	FallbackFrom   string
	FallbackReason string
}

// DispatchDeps are the seams dispatch needs from the command layer. Only the
// things internal/agents cannot know about — WB's CLI-layer worktree side
// effects, and how a detached owner is started — are injected; everything else
// is reused directly.
type DispatchDeps struct {
	ConfigPath   string
	LoadConfig   func() (Config, error)
	ProjectsRoot string
	Home         string
	Caller       CallerSession
	// BeforeCreate refreshes managed hooks in the canonical clones, exactly as
	// `wb worktree create` does before it creates anything.
	BeforeCreate func(repositories []string) error
	// AfterCreate writes the checkout marker exactly as `wb worktree create`
	// does after creation. It MUST be best-effort: a marker WB could not write
	// does not make a checkout unusable.
	AfterCreate func(repositories []string, results []worktrees.CreateResult)
	// CreateWorktree, ListWorktrees, and OriginSlug default to WB's own
	// worktree service. They are injectable only so the orchestration above them
	// can be exercised without a live fleet; production never replaces them.
	CreateWorktree func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error)
	ListWorktrees  func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error)
	OriginSlug     func(context.Context, string) (string, error)
	// SpawnOwner starts the detached run owner for a persisted run and returns
	// its process ID. extraFiles carry the inherited writer lease, so the claim
	// lives exactly as long as the owner or anything it starts.
	SpawnOwner func(agentID string, extraFiles []*os.File) (int, error)
	// Preflight proves the resolved profile can start before a worktree exists.
	Preflight func(context.Context, Resolved) error
	// DetectVersion reads the installed harness version, best effort.
	DetectVersion func(context.Context, string) string
	Now           func() time.Time
}

// PreparedRun is an admitted execution: resolved, recorded, leased and waiting
// for its owner. Splitting preparation from start is what lets a comparison
// create every worktree — and therefore pin every base — before any worker runs.
type PreparedRun struct {
	Record Record
	Lease  *Lease
	// Store is the run store the record lives in, so an aborted preparation can
	// still be recorded as a terminal failure.
	Store Store
}

// Dispatch admits one run, creates or resolves its worktree, persists the run,
// and starts the detached owner. It returns as soon as the owner is started:
// the caller is never attached to the worker's stdout and never waits for it.
func Dispatch(ctx context.Context, request DispatchRequest, deps DispatchDeps) (Record, error) {
	prepared, err := PrepareRun(ctx, request, deps)
	if err != nil {
		return Record{}, err
	}
	return StartRun(prepared, deps)
}

// PrepareRun resolves the plan, proves it can start, resolves or creates the
// worktree, takes the writer lease, and persists the run record. It starts no
// process, so a caller can prepare several attempts and start them later.
func PrepareRun(ctx context.Context, request DispatchRequest, deps DispatchDeps) (*PreparedRun, error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.CreateWorktree == nil {
		deps.CreateWorktree = worktrees.Create
	}
	if deps.ListWorktrees == nil {
		deps.ListWorktrees = worktrees.List
	}
	if deps.OriginSlug == nil {
		deps.OriginSlug = worktrees.OriginSlug
	}
	if err := validateDispatchRequest(request); err != nil {
		return nil, err
	}
	if deps.LoadConfig == nil {
		return nil, fmt.Errorf("agent dispatch requires a configuration loader")
	}
	config, err := deps.LoadConfig()
	if err != nil {
		return nil, err
	}
	plan, err := resolvePlan(config, request)
	if err != nil {
		return nil, err
	}
	// Enforce a declared mandatory sandbox before anything is created. The
	// loaded configuration is the authority: a plan whose snapshot disagrees is
	// refused, and while container execution is unimplemented a matching policy
	// means "refuse", never "run it on the host anyway".
	if err := RequireRunnableExecution(config.Execution, plan.Execution, "plan", plan.Profile.Harness); err != nil {
		return nil, err
	}
	// A Codex run authenticates with a credential WB injects; refuse a dispatch
	// that cannot authenticate before a worktree exists. This check is
	// independent of the injected preflight seam so a direct caller cannot skip
	// it.
	if plan.Profile.Harness == HarnessCodex {
		if _, err := ResolveCredential(plan.Profile.Routing); err != nil {
			// A missing credential is an environment problem, not a malformed
			// invocation: it stays a finding, as it was before this feature.
			return nil, fmt.Errorf("provider %q: %w", plan.Profile.Provider, err)
		}
	}
	// Prove the harness can start before creating a worktree: a dispatch that
	// cannot authenticate must fail without leaving an unused checkout behind.
	if deps.Preflight != nil && (request.Plan == nil || !request.Plan.probed) {
		if err := deps.Preflight(ctx, plan.Profile); err != nil {
			return nil, err
		}
	}
	adapter, err := HarnessFor(plan.Profile.Harness)
	if err != nil {
		return nil, requestErrorf("%v", err)
	}

	store := NewStore(deps.Home)
	request.AgentID = strings.TrimSpace(request.AgentID)
	if request.AgentID == "" {
		// NewID uses crypto/rand.Read's fill-or-terminate contract.
		request.AgentID, _ = NewID()
	}
	if err := validateAgentID(request.AgentID); err != nil {
		return nil, requestErrorf("%v", err)
	}
	operation := strings.TrimSpace(request.Worktree)
	if operation == "" && (request.Mode == ModeAuto || request.Mode == ModeNew) {
		operation = autoOperationName(plan.TaskType, request.Task, request.AgentID)
	}

	var target worktreeResolution
	switch request.Mode {
	case ModeNew, ModeAuto:
		target, err = createWorktree(ctx, request, operation, plan.Profile, deps)
	case ModeExisting:
		target, err = resolveWorktree(ctx, request, operation, deps)
	case ModeCurrent, ModePath:
		target, err = resolveExistingTargetWithPolicy(ctx, request, deps, config.Execution != nil)
	default:
		return nil, requestErrorf("unsupported worktree mode %q", request.Mode)
	}
	if err != nil {
		return nil, err
	}
	var executionWorkspace *ContainerWorkspace
	if config.Execution != nil {
		// Trusted admission data only: the runtime does not run at this stage,
		// and the scoped input directory is generated by the owner later.
		executionWorkspace, err = snapshotExecutionWorkspace(target)
		if err != nil {
			return nil, err
		}
	}

	lease, leaseErr := acquireWriterLease(store, request, target, deps)
	if leaseErr != nil {
		return nil, leaseErr
	}

	capabilities := adapter.Capabilities()
	if deps.DetectVersion != nil {
		capabilities.Version = deps.DetectVersion(ctx, plan.Profile.Harness)
	}
	taskDigest, taskBytes := TaskIdentity(request.Task)
	// The mandatory policy is snapshotted beside the profile facts, so a
	// configuration edit after admission cannot change what is recorded.
	record := Record{
		State:                StateRunning,
		ExecutionStatus:      StatusQueued,
		RequestedProfile:     plan.Profile.Profile,
		Resolved:             plan.Profile,
		TaskType:             plan.TaskType,
		TaskTypeSource:       plan.TaskTypeSource,
		ProfileSource:        plan.ProfileSource,
		RoutingReason:        routingReason(plan),
		Complexity:           plan.Complexity,
		ComplexitySource:     plan.ComplexitySource,
		ComplexityOverridden: plan.ComplexityOverridden,
		EffortRequested:      plan.EffortRequested,
		EffortSource:         plan.EffortSource,
		EffortEffective:      plan.EffortEffective,
		EffortApplied:        plan.EffortApplied,
		EffortNote:           plan.EffortNote,
		WorktreeSource:       plan.WorktreeSource,
		TimeoutSource:        plan.TimeoutSource,
		HarnessCapabilities:  capabilities,
		Execution:            SnapshotExecution(plan.Execution),
		ExecutionWorkspace:   executionWorkspace,
		Task:                 request.Task,
		TaskSummary:          SummaryLine(request.Task),
		TaskSHA256:           taskDigest,
		TaskBytes:            taskBytes,
		TaskVisibility:       capabilities.TaskDelivery,
		TaskNormalized:       capabilities.TaskDelivery == TaskDeliveryArgv && dshTaskNormalized(request.Task),
		Repository:           target.Repository,
		Caller:               deps.Caller,
		WorktreeMode:         request.Mode,
		Worktree:             target.Worktree,
		WorktreeDir:          target.Dir,
		Branch:               target.Branch,
		Base:                 target.Base,
		BaseSHA:              target.BaseSHA,
		SourceSHA:            target.SourceSHA,
		SnapshotSHA:          target.SnapshotSHA,
		SourceDirty:          target.Dirty,
		DirtyFiles:           target.DirtyFiles,
		WorkLogClaimPath:     target.ClaimPath,
		WorkLogRunID:         target.ClaimRunID,
		ExperimentID:         request.ExperimentID,
		RepeatIndex:          request.RepeatIndex,
		RetryOf:              request.RetryOf,
		FallbackFrom:         firstNonEmpty(request.FallbackFrom, plan.FallbackFrom),
		FallbackReason:       firstNonEmpty(request.FallbackReason, plan.FallbackReason),
		TimeoutMS:            plan.TimeoutMS,
		StartedAt:            deps.Now().UTC(),
		// A prepared attempt may wait behind a concurrency bound before its
		// owner exists; the dispatching process is its liveness evidence.
		QueueOwnerPID:      os.Getpid(),
		Validation:         ValidationRecord{Status: ValidationPending, Steps: pendingValidationSteps(plan.ValidationSteps)},
		AcceptanceRequired: plan.AcceptanceRequired,
	}
	if lease != nil {
		record.LeasePath = lease.Path()
	}
	record.LogPath = store.LogPath(request.AgentID)
	record.AgentID = request.AgentID
	if err := store.Create(record); err != nil {
		_ = lease.Release()
		return nil, err
	}
	return &PreparedRun{Record: record, Lease: lease, Store: store}, nil
}

// StartRun starts the detached owner for a prepared run and returns without
// waiting. The run is already durable, so a launch failure is diagnosable.
func StartRun(prepared *PreparedRun, deps DispatchDeps) (Record, error) {
	if prepared == nil {
		return Record{}, fmt.Errorf("start agent run: nothing was prepared")
	}
	if deps.SpawnOwner == nil {
		_ = prepared.Fail(StateFailed, "start detached run owner: no spawn seam", FailureInternal, deps.Now)
		return Record{}, fmt.Errorf("agent dispatch requires a run owner spawner")
	}
	extraFiles := []*os.File{}
	if prepared.Lease != nil && prepared.Lease.File() != nil {
		extraFiles = append(extraFiles, prepared.Lease.File())
	}
	ownerPID, err := deps.SpawnOwner(prepared.Record.AgentID, extraFiles)
	if err != nil {
		_ = prepared.Fail(StateFailed, fmt.Sprintf("start detached run owner: %v", err), FailureInternal, deps.Now)
		return prepared.Record, fmt.Errorf("start detached run owner for %s: %w", prepared.Record.AgentID, err)
	}
	// The owner records its own PID as its first durable act, so the dispatcher
	// deliberately does not write the record again: a second writer racing the
	// owner could clobber the worker PID the owner records moments later.
	prepared.Record.OwnerPID = ownerPID
	if prepared.Lease != nil {
		_ = prepared.Lease.SetOwner(ownerPID)
	}
	return prepared.Record, nil
}

// Fail records a terminal failure for a run that never started, or whose owner
// could not be launched.
func (prepared *PreparedRun) Fail(state State, failure, category string, now func() time.Time) error {
	if prepared == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	prepared.Record.State = state
	prepared.Record.ExecutionStatus = executionStatusFromState(state)
	prepared.Record.Failure = failure
	prepared.Record.FailureCategory = category
	prepared.Record.FinishedAt = now().UTC()
	if err := prepared.Store.Save(prepared.Record); err != nil {
		return err
	}
	return prepared.Lease.Release()
}

// autoOperationName is deterministic and collision-free for reruns of the same
// task: the task hash names the work, and the agent ID's first bytes make a
// second run of the same work a new checkout rather than a resume.
// firstNonEmpty prefers an explicit retry value over a plan-level one.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func autoOperationName(taskType, task, agentID string) string {
	digest, _ := TaskIdentity(task)
	suffix := ""
	if len(agentID) >= len(IDPrefix)+8 {
		suffix = agentID[len(IDPrefix) : len(IDPrefix)+8]
	}
	name := strings.TrimSpace(taskType) + "-" + digest[:8]
	if suffix != "" {
		name += "-" + suffix
	}
	return name
}

func acquireWriterLease(store Store, request DispatchRequest, target worktreeResolution, deps DispatchDeps) (*Lease, error) {
	lease, err := acquireLeaseForMode(store, request, target, deps.Now())
	if err == nil {
		return lease, nil
	}
	var held *LeaseHeldError
	if errors.As(err, &held) {
		return nil, requestErrorf("%v", err)
	}
	return nil, err
}

func resolvePlan(config Config, request DispatchRequest) (Plan, error) {
	if request.Plan != nil {
		return *request.Plan, nil
	}
	planner := Planner{Config: config}
	plan, err := planner.Plan(RunRequest{
		Task: request.Task, Profile: request.Profile, TaskType: "coding",
		Timeout: request.Timeout, TimeoutSet: request.Timeout > 0,
	})
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func pendingValidationSteps(steps []ValidationStep) []ValidationStepResult {
	if len(steps) == 0 {
		return nil
	}
	pending := make([]ValidationStepResult, 0, len(steps))
	for _, step := range steps {
		pending = append(pending, ValidationStepResult{Name: step.Name, Argv: append([]string(nil), step.Argv...), Timeout: step.Timeout, Status: ValidationPending})
	}
	return pending
}

func routingReason(plan Plan) string {
	parts := []string{"task type " + plan.TaskType + " (" + plan.TaskTypeSource + ")"}
	if plan.Complexity != "" {
		parts = append(parts, "complexity "+plan.Complexity+" ("+plan.ComplexitySource+")")
	}
	parts = append(parts, "profile "+plan.Profile.Profile+" ("+plan.ProfileSource+")")
	if plan.FallbackFrom != "" {
		parts = append(parts, "fallback from "+plan.FallbackFrom)
	}
	return strings.Join(parts, "; ")
}

func validateDispatchRequest(request DispatchRequest) error {
	name := strings.TrimSpace(request.Worktree)
	switch request.Mode {
	case ModePath:
		if name == "" {
			return requestErrorf("--worktree path requires an absolute path")
		}
	case ModeCurrent, ModeAuto:
		// The invoking checkout, or a name WB derives, is the target.
	default:
		if name == "" {
			return requestErrorf("a worktree name is required")
		}
	}
	if strings.TrimSpace(request.Task) == "" {
		return requestErrorf("--task or --task-file is required")
	}
	if request.Timeout <= 0 {
		// A run must carry a bound: leaving enforcement to the harness would
		// make it advisory, and "no bound" is how a stuck worker becomes a
		// stray process nobody is waiting for.
		return requestErrorf("--timeout must be positive; every dispatched run carries a bound")
	}
	return nil
}

// worktreeResolution is the subset of a WB worktree that a dispatched run
// records, gathered from whichever mode supplied it.
type worktreeResolution struct {
	Repository  string
	Dir         string
	Worktree    string
	Branch      string
	Base        string
	BaseSHA     string
	SourceSHA   string
	SnapshotSHA string
	Dirty       bool
	DirtyFiles  []string
	// CanonicalDir is the canonical clone WB's own worktree inventory reports for
	// this checkout. It is trusted admission data, never read from the editable
	// .worktree.md marker or a .git pointer.
	CanonicalDir string
	// ClaimPath and ClaimRunID point at the immutable Work Log claim the
	// worktree service already published for this work, so one dispatch never
	// produces two unreferenced records of the same task.
	ClaimPath  string
	ClaimRunID string
}

// createWorktree creates the checkout through WB's existing worktree creation
// service, in process. It deliberately does not reimplement branch, base, or
// worktree semantics: it supplies what the service needs and records what the
// service decided.
func createWorktree(ctx context.Context, request DispatchRequest, operation string, resolved Resolved, deps DispatchDeps) (worktreeResolution, error) {
	repository, err := resolveRepository(ctx, request.Repository, deps.OriginSlug)
	if err != nil {
		return worktreeResolution{}, err
	}
	repositories, err := worktrees.ValidateRepositories([]string{repository})
	if err != nil {
		return worktreeResolution{}, err
	}
	if deps.BeforeCreate != nil {
		if err := deps.BeforeCreate(repositories); err != nil {
			return worktreeResolution{}, err
		}
	}
	// The task is the exact originating request, so it is archived as the
	// private original prompt rather than invented or left blank.
	workLog, _ := (worktrees.WorkLogOptions{
		Model:                 resolved.Model,
		Provider:              resolved.Provider,
		CLI:                   resolved.Harness,
		AgentRuntime:          resolved.Harness,
		RequireOriginalPrompt: true,
	}).WithOriginalPromptFromStdin([]byte(request.Task))
	// Dispatch's request boundary already rejects blank tasks, the sole failure
	// of WithOriginalPromptFromStdin. Preserve exact original bytes here.
	workLog, err = worktrees.PrepareWorkLogOptions(deps.ProjectsRoot, operation, workLog)
	if err != nil {
		return worktreeResolution{}, err
	}
	results, err := deps.CreateWorktree(ctx, repositories, worktrees.CreateOptions{
		ProjectsRoot: deps.ProjectsRoot,
		Operation:    operation,
		Branch:       request.Branch,
		BranchChosen: request.Branch != "",
		Base:         request.Base,
		// A dispatched worker is an agent-mode mutation: it must belong to a
		// live registered session, exactly like `wb worktree create` in agent
		// mode. This gate is reused, never weakened.
		SessionRequired: true,
		WorkLog:         workLog,
	})
	if err != nil {
		return worktreeResolution{}, err
	}
	if deps.AfterCreate != nil {
		deps.AfterCreate(repositories, results)
	}
	created := results[0]
	return worktreeResolution{
		Repository:   created.Repository,
		Dir:          created.WorktreeDir,
		CanonicalDir: created.CanonicalDir,
		Worktree:     operation,
		Branch:       created.Branch,
		Base:         created.Base,
		BaseSHA:      created.BaseSHA,
		ClaimPath:    created.WorkLogPath,
		ClaimRunID:   workLog.RunID,
	}, nil
}

// resolveWorktree resolves an existing WB-managed worktree by name through WB's
// own inventory. It never creates one, and it refuses an ambiguous name rather
// than choosing a checkout on the caller's behalf.
func resolveWorktree(ctx context.Context, request DispatchRequest, operation string, deps DispatchDeps) (worktreeResolution, error) {
	results, err := deps.ListWorktrees(ctx, worktrees.ListOptions{
		ProjectsRoot: deps.ProjectsRoot,
		Tasks:        []string{operation},
	})
	if err != nil {
		return worktreeResolution{}, fmt.Errorf("resolve worktree %q: %w", operation, err)
	}
	repository := strings.TrimSpace(request.Repository)
	candidates := make([]worktrees.ListResult, 0, len(results))
	for _, result := range results {
		if result.WorktreeDir == "" {
			continue
		}
		if repository != "" && !strings.EqualFold(result.Repository, repository) {
			continue
		}
		candidates = append(candidates, result)
	}
	switch len(candidates) {
	case 0:
		return worktreeResolution{}, fmt.Errorf("no WB-managed worktree named %q under projects root %s; create it with `wb worktree create %s` or dispatch with --new-worktree", operation, deps.ProjectsRoot, operation)
	case 1:
	default:
		descriptions := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			descriptions = append(descriptions, fmt.Sprintf("%s (%s)", candidate.Repository, candidate.WorktreeDir))
		}
		return worktreeResolution{}, fmt.Errorf("worktree name %q is ambiguous; it resolves to %d checkouts: %s. Pass --repo to select one", operation, len(candidates), strings.Join(descriptions, ", "))
	}
	chosen := candidates[0]
	return worktreeResolution{
		Repository:   chosen.Repository,
		Dir:          chosen.WorktreeDir,
		CanonicalDir: chosen.CanonicalDir,
		Worktree:     operation,
		Branch:       chosen.Branch,
		Base:         chosen.RecordedBase,
		BaseSHA:      chosen.HeadSHA,
	}, nil
}

// resolveExistingTarget turns `current` or an explicit path into the facts a run
// records. It never creates or cleans anything, and it keeps the legacy
// marker-based behaviour used when the operator declared no mandatory sandbox.
func resolveExistingTarget(ctx context.Context, request DispatchRequest, deps DispatchDeps) (worktreeResolution, error) {
	return resolveExistingTargetWithPolicy(ctx, request, deps, false)
}

// resolveExistingTargetWithPolicy resolves current/path targets. Under a
// mandatory sandbox the editable .git pointer and .worktree.md marker cannot
// grant host mounts: the exact checkout must be an entry in WB's own managed
// worktree inventory, matched by its real path and repository, and its canonical
// clone must come from that inventory. A canonical whole clone is always
// refused, even when the legacy --allow-canonical option was supplied.
func resolveExistingTargetWithPolicy(ctx context.Context, request DispatchRequest, deps DispatchDeps, mandatory bool) (worktreeResolution, error) {
	path := strings.TrimSpace(request.Worktree)
	if request.Mode == ModeCurrent && path == "" {
		// An empty target means the checkout the caller is standing in.
		path = "."
	}
	target, err := ResolveTarget(ctx, path, request.AllowCanonical, request.AllowDirty, TargetDeps{
		OriginSlug: deps.OriginSlug,
	})
	if err != nil {
		return worktreeResolution{}, err
	}
	if mandatory {
		return resolveMandatoryTarget(ctx, target, deps)
	}
	name := target.WorktreeName
	if name == "" {
		name = filepath.Base(target.Dir)
	}
	return worktreeResolution{
		Repository:  target.Repository,
		Dir:         target.Dir,
		Worktree:    name,
		Branch:      target.Branch,
		Base:        target.Base,
		BaseSHA:     target.BaseSHA,
		SourceSHA:   target.HeadSHA,
		SnapshotSHA: target.SnapshotSHA,
		Dirty:       target.Dirty,
		DirtyFiles:  target.DirtyFiles,
	}, nil
}

// resolveMandatoryTarget is the trusted-admission boundary for current/path
// targets. The inventory entry, not the checkout's own marker, supplies the
// canonical clone a contained execution may mount.
func resolveMandatoryTarget(ctx context.Context, target Target, deps DispatchDeps) (worktreeResolution, error) {
	if target.Canonical {
		return worktreeResolution{}, fmt.Errorf("%s is a canonical clone; a contained execution may only mount an exact WB-managed linked worktree, never a whole clone, even with the legacy allow-canonical option", target.Dir)
	}
	entry, err := managedWorktreeEntry(ctx, target, deps)
	if err != nil {
		return worktreeResolution{}, err
	}
	name := target.WorktreeName
	if name == "" {
		name = filepath.Base(target.Dir)
	}
	return worktreeResolution{
		Repository:   entry.Repository,
		Dir:          entry.WorktreeDir,
		CanonicalDir: entry.CanonicalDir,
		Worktree:     name,
		Branch:       target.Branch,
		Base:         target.Base,
		BaseSHA:      target.BaseSHA,
		SourceSHA:    target.HeadSHA,
		SnapshotSHA:  target.SnapshotSHA,
		Dirty:        target.Dirty,
		DirtyFiles:   target.DirtyFiles,
	}, nil
}

// managedWorktreeEntry finds the one inventory entry whose real checkout path
// and repository match the resolved target. Anything ambiguous, unmanaged, or
// shaped like a canonical whole clone is refused rather than guessed.
func managedWorktreeEntry(ctx context.Context, target Target, deps DispatchDeps) (worktrees.ListResult, error) {
	if deps.ListWorktrees == nil {
		return worktrees.ListResult{}, fmt.Errorf("resolve the managed worktree inventory for %s: no inventory reader is available", target.Dir)
	}
	results, err := deps.ListWorktrees(ctx, worktrees.ListOptions{ProjectsRoot: deps.ProjectsRoot})
	if err != nil {
		return worktrees.ListResult{}, fmt.Errorf("resolve the managed worktree inventory for %s: %w", target.Dir, err)
	}
	directory := filepath.Clean(target.Dir)
	matches := make([]worktrees.ListResult, 0, 1)
	for _, result := range results {
		if strings.TrimSpace(result.WorktreeDir) == "" {
			continue
		}
		if filepath.Clean(result.WorktreeDir) != directory {
			continue
		}
		if repository := strings.TrimSpace(target.Repository); repository != "" && !strings.EqualFold(result.Repository, repository) {
			continue
		}
		matches = append(matches, result)
	}
	switch len(matches) {
	case 0:
		return worktrees.ListResult{}, fmt.Errorf("%s is not an entry in WB's managed worktree inventory; a contained execution may only mount an exact managed worktree, so a canonical clone, an unmanaged checkout or an edited marker cannot grant host mounts", directory)
	case 1:
	default:
		return worktrees.ListResult{}, fmt.Errorf("the managed worktree inventory reports %d entries for %s; a contained execution refuses to guess which checkout to mount", len(matches), directory)
	}
	entry := matches[0]
	canonical := strings.TrimSpace(entry.CanonicalDir)
	if canonical == "" {
		return worktrees.ListResult{}, fmt.Errorf("the managed worktree inventory entry for %s carries no canonical directory; a contained execution cannot mount an unverified canonical clone", directory)
	}
	if filepath.Clean(canonical) == directory || filepath.Clean(entry.WorktreeDir) == filepath.Clean(canonical) {
		return worktrees.ListResult{}, fmt.Errorf("%s resolves to a canonical whole clone; a contained execution requires a linked worktree below a separate canonical clone", directory)
	}
	return entry, nil
}

// snapshotExecutionWorkspace turns trusted inventory facts into the exact
// workspace a contained execution may mount. Only the worktree and canonical
// .git directory are admitted; the scoped input directory is generated by the
// owner later, so it stays empty.
func snapshotExecutionWorkspace(target worktreeResolution) (*ContainerWorkspace, error) {
	worktree := strings.TrimSpace(target.Dir)
	canonical := strings.TrimSpace(target.CanonicalDir)
	if worktree == "" {
		return nil, fmt.Errorf("a mandatory execution policy requires a managed worktree directory, but none was resolved")
	}
	if canonical == "" {
		return nil, fmt.Errorf("a mandatory execution policy requires the canonical clone of %s from WB's worktree inventory, but none was resolved", worktree)
	}
	if !filepath.IsAbs(worktree) || filepath.Clean(worktree) != worktree {
		return nil, fmt.Errorf("the resolved worktree directory %q must be a clean absolute path before it can be snapshotted", worktree)
	}
	if !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical {
		return nil, fmt.Errorf("the resolved canonical directory %q must be a clean absolute path before it can be snapshotted", canonical)
	}
	gitCommon := filepath.Join(canonical, ".git")
	if filepath.Clean(gitCommon) == worktree {
		return nil, fmt.Errorf("worktree %s and canonical git directory %s overlap; a contained execution requires two exact disjoint roots", worktree, gitCommon)
	}
	return SnapshotExecutionWorkspace(&ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon}), nil
}

func resolveRepository(ctx context.Context, requested string, origin func(context.Context, string) (string, error)) (string, error) {
	if value := strings.TrimSpace(requested); value != "" {
		return value, nil
	}
	repository, err := origin(ctx, ".")
	if err != nil {
		return "", fmt.Errorf("derive repository from the current checkout (pass --repo to name it explicitly): %w", err)
	}
	return repository, nil
}
