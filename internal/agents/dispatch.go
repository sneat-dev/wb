package agents

import (
	"context"
	"fmt"
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

	Profile string
	Task    string

	// Repository is owner/repository. Empty means "derive it from the invoking
	// checkout's origin", exactly as `wb worktree create` does.
	Repository string
	// Branch and Base are optional pass-throughs to WB's existing worktree
	// naming policy; empty leaves that policy in charge.
	Branch string
	Base   string

	Timeout time.Duration
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
	// its process ID. The PID is returned rather than left to the owner to
	// record, so a record a later process reads always names the process that
	// owns it — which is what makes "the owner is gone" conclusive evidence
	// instead of a guess about a run that has not started yet.
	SpawnOwner func(agentID string) (int, error)
	Now        func() time.Time
}

// Dispatch admits one run, creates or resolves its worktree, persists the run,
// and starts the detached owner. It returns as soon as the owner is started:
// the caller is never attached to the worker's stdout and never waits for it.
func Dispatch(ctx context.Context, request DispatchRequest, deps DispatchDeps) (Record, error) {
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
		return Record{}, err
	}
	if deps.LoadConfig == nil {
		return Record{}, fmt.Errorf("agent dispatch requires a configuration loader")
	}
	config, err := deps.LoadConfig()
	if err != nil {
		return Record{}, err
	}
	resolved, err := config.Resolve(request.Profile)
	if err != nil {
		return Record{}, err
	}
	if name, missing := MissingCredential(resolved.Routing.CredentialEnv); missing {
		return Record{}, fmt.Errorf("provider %q needs %s in the environment; WB never stores provider credentials in configuration", resolved.Provider, name)
	}

	store := NewStore(deps.Home)
	record := Record{
		State:            StateRunning,
		RequestedProfile: resolved.Profile,
		Resolved:         resolved,
		Task:             request.Task,
		TaskSummary:      SummaryLine(request.Task),
		WorktreeMode:     request.Mode,
		Worktree:         request.Worktree,
		TimeoutMS:        request.Timeout.Milliseconds(),
		StartedAt:        deps.Now().UTC(),
	}

	var worktree worktreeResolution
	switch request.Mode {
	case ModeNew:
		worktree, err = createWorktree(ctx, request, resolved, deps)
	case ModeExisting:
		worktree, err = resolveWorktree(ctx, request, deps)
	default:
		return Record{}, fmt.Errorf("unsupported worktree mode %q", request.Mode)
	}
	if err != nil {
		return Record{}, err
	}
	record.Repository = worktree.Repository
	record.WorktreeDir = worktree.Dir
	record.Branch = worktree.Branch
	record.Base = worktree.Base
	record.BaseSHA = worktree.BaseSHA
	record.WorkLogClaimPath = worktree.ClaimPath
	record.WorkLogRunID = worktree.ClaimRunID

	agentID, err := NewID()
	if err != nil {
		return Record{}, err
	}
	record.AgentID = agentID
	record.LogPath = store.LogPath(agentID)
	if err := store.Create(record); err != nil {
		return Record{}, err
	}
	ownerPID, err := deps.SpawnOwner(agentID)
	if err != nil {
		// The run is recorded before it starts precisely so a launch failure
		// is diagnosable rather than invisible.
		record.State = StateFailed
		record.Failure = fmt.Sprintf("start detached run owner: %v", err)
		record.FinishedAt = deps.Now().UTC()
		_ = store.Save(record)
		return record, fmt.Errorf("start detached run owner for %s: %w", agentID, err)
	}
	record.OwnerPID = ownerPID
	if err := store.Save(record); err != nil {
		return record, err
	}
	return record, nil
}

func validateDispatchRequest(request DispatchRequest) error {
	name := strings.TrimSpace(request.Worktree)
	if name == "" {
		return requestErrorf("exactly one of --new-worktree or --use-worktree is required")
	}
	if strings.TrimSpace(request.Task) == "" {
		return requestErrorf("--task or --task-file is required")
	}
	if request.Timeout < 0 {
		return requestErrorf("--timeout must not be negative")
	}
	return nil
}

// worktreeResolution is the subset of a WB worktree that a dispatched run
// records, gathered from whichever of the two modes supplied it.
type worktreeResolution struct {
	Repository string
	Dir        string
	Branch     string
	Base       string
	BaseSHA    string
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
func createWorktree(ctx context.Context, request DispatchRequest, resolved Resolved, deps DispatchDeps) (worktreeResolution, error) {
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
	workLog, err := (worktrees.WorkLogOptions{
		Model:                 resolved.Model,
		Provider:              resolved.Provider,
		CLI:                   resolved.Harness,
		AgentRuntime:          resolved.Harness,
		RequireOriginalPrompt: true,
	}).WithOriginalPromptFromStdin([]byte(request.Task))
	if err != nil {
		return worktreeResolution{}, err
	}
	workLog, err = worktrees.PrepareWorkLogOptions(deps.ProjectsRoot, request.Worktree, workLog)
	if err != nil {
		return worktreeResolution{}, err
	}
	results, err := deps.CreateWorktree(ctx, repositories, worktrees.CreateOptions{
		ProjectsRoot: deps.ProjectsRoot,
		Operation:    request.Worktree,
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
		Repository: created.Repository,
		Dir:        created.WorktreeDir,
		Branch:     created.Branch,
		Base:       created.Base,
		BaseSHA:    created.BaseSHA,
		ClaimPath:  created.WorkLogPath,
		ClaimRunID: workLog.RunID,
	}, nil
}

// resolveWorktree resolves an existing WB-managed worktree by name through WB's
// own inventory. It never creates one, and it refuses an ambiguous name rather
// than choosing a checkout on the caller's behalf.
func resolveWorktree(ctx context.Context, request DispatchRequest, deps DispatchDeps) (worktreeResolution, error) {
	results, err := deps.ListWorktrees(ctx, worktrees.ListOptions{
		ProjectsRoot: deps.ProjectsRoot,
		Tasks:        []string{request.Worktree},
	})
	if err != nil {
		return worktreeResolution{}, fmt.Errorf("resolve worktree %q: %w", request.Worktree, err)
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
		return worktreeResolution{}, fmt.Errorf("no WB-managed worktree named %q under projects root %s; create it with `wb worktree create %s` or dispatch with --new-worktree", request.Worktree, deps.ProjectsRoot, request.Worktree)
	case 1:
	default:
		descriptions := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			descriptions = append(descriptions, fmt.Sprintf("%s (%s)", candidate.Repository, candidate.WorktreeDir))
		}
		return worktreeResolution{}, fmt.Errorf("worktree name %q is ambiguous; it resolves to %d checkouts: %s. Pass --repo to select one", request.Worktree, len(candidates), strings.Join(descriptions, ", "))
	}
	chosen := candidates[0]
	return worktreeResolution{
		Repository: chosen.Repository,
		Dir:        chosen.WorktreeDir,
		Branch:     chosen.Branch,
		Base:       chosen.RecordedBase,
	}, nil
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
