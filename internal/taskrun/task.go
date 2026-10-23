// Package taskrun composes isolated task creation and pickup using existing
// worktree, task-store and receipt-gated session-move authorities.
package taskrun

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/taskoffload"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type Request struct {
	ProjectsRoot, Task                                  string
	Repositories                                        []string
	Continuation                                        []byte
	ContextFile, OriginalPrompt, Harness, Model, Target string
	ParkOnly                                            bool
}
type PickupRequest struct{ ProjectsRoot, TaskID, Harness, Model, Target string }
type Result struct {
	TaskID      string `json:"task_id"`
	Task        string `json:"task"`
	WorktreeDir string `json:"worktree_dir"`
	Status      string `json:"status"`
	Harness     string `json:"harness,omitempty"`
	Model       string `json:"model,omitempty"`
	Target      string `json:"target,omitempty"`
}
type MoveOperation func(context.Context, sessionrun.MoveRequest, func([]secretscan.Finding)) (sessionrun.MoveResult, error)
type Dependencies struct {
	Create         func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error)
	OriginSlug     func(context.Context, string) (string, error)
	PrepareWorkLog func(string, string, worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error)
	Store          func(string) (taskoffload.Store, error)
	NewID          func() (string, error)
	Now            func() time.Time
	Move           MoveOperation
}
type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }
func DefaultDependencies(move MoveOperation) Dependencies {
	return Dependencies{Create: worktrees.Create, OriginSlug: worktrees.OriginSlug, PrepareWorkLog: worktrees.PrepareWorkLogOptions,
		Store: func(root string) (taskoffload.Store, error) {
			home, err := wbhome.Root(root)
			if err != nil {
				return taskoffload.Store{}, err
			}
			return taskoffload.NewStore(filepath.Join(home, taskoffload.DirName)), nil
		}, NewID: taskoffload.NewID, Now: time.Now, Move: move}
}
func (service *Service) Offload(ctx context.Context, request Request, warnings func([]secretscan.Finding), render func(sessionrun.MoveResult) error) (Result, error) {
	continuation := string(bytes.TrimSpace(request.Continuation))
	if continuation == "" {
		verb := "offload"
		if request.ParkOnly {
			verb = "park"
		}
		return Result{}, fmt.Errorf("task %s requires non-empty continuation via --context-file", verb)
	}
	harness := ""
	var err error
	if strings.TrimSpace(request.Harness) != "" {
		harness, err = sessionlaunch.NormalizeRuntime("", request.Harness)
		if err != nil {
			return Result{}, err
		}
	}
	model := sessionlaunch.NormalizeModel(request.Model)
	originalPrompt := request.OriginalPrompt
	if originalPrompt == "" {
		originalPrompt = request.ContextFile
	}
	repositories := request.Repositories
	if len(repositories) == 0 {
		repository, err := service.deps.OriginSlug(ctx, ".")
		if err != nil {
			return Result{}, fmt.Errorf("derive current repository: %w", err)
		}
		repositories = []string{repository}
	}
	repositories, err = worktrees.ValidateRepositories(repositories)
	if err != nil {
		return Result{}, err
	}
	workLog := worktrees.WorkLogOptions{OriginalPrompt: originalPrompt, RequireOriginalPrompt: true, TaskSummary: "offload " + request.Task, Model: "unknown"}
	if originalPrompt == "-" {
		// The same bytes.TrimSpace precondition above excludes this method's sole error.
		workLog, _ = workLog.WithOriginalPromptFromStdin(request.Continuation)
	}
	workLog, err = service.deps.PrepareWorkLog(request.ProjectsRoot, request.Task, workLog)
	if err != nil {
		return Result{}, err
	}
	results, err := service.deps.Create(ctx, repositories, worktrees.CreateOptions{ProjectsRoot: request.ProjectsRoot, Operation: request.Task, WorkLog: workLog, SessionRequired: true})
	if err != nil {
		return Result{}, err
	}
	if len(results) == 0 {
		return Result{}, fmt.Errorf("task %s created no worktree", request.Task)
	}
	store, err := service.deps.Store(request.ProjectsRoot)
	if err != nil {
		return Result{}, err
	}
	id, err := service.deps.NewID()
	if err != nil {
		return Result{}, err
	}
	status := taskoffload.StatusParked
	if !request.ParkOnly {
		status = taskoffload.StatusOffload
	}
	record := taskoffload.Record{SchemaVersion: 1, TaskID: id, Task: request.Task, WorktreeDir: results[0].WorktreeDir, Repository: results[0].Repository, Harness: harness, Model: model, Target: strings.TrimSpace(request.Target), Status: status, CreatedAt: service.deps.Now().UTC()}
	if err := store.Save(record, continuation); err != nil {
		return Result{}, err
	}
	if !request.ParkOnly {
		if err := service.launch(request.ProjectsRoot, record, filepath.Join(store.Root, id, "context.md"), warnings, render); err != nil {
			return Result{}, err
		}
	}
	return result(record), nil
}
func (service *Service) Pickup(_ context.Context, request PickupRequest, warnings func([]secretscan.Finding), render func(sessionrun.MoveResult) error) (Result, error) {
	store, err := service.deps.Store(request.ProjectsRoot)
	if err != nil {
		return Result{}, err
	}
	record, _, err := store.Load(request.TaskID)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(request.Harness) != "" {
		record.Harness, err = sessionlaunch.NormalizeRuntime("", request.Harness)
		if err != nil {
			return Result{}, err
		}
	}
	if model := sessionlaunch.NormalizeModel(request.Model); model != "" {
		record.Model = model
	}
	if strings.TrimSpace(request.Target) != "" {
		record.Target = strings.TrimSpace(request.Target)
	}
	contextPath := filepath.Join(store.Root, record.TaskID, "context.md")
	if err := service.launch(request.ProjectsRoot, record, contextPath, warnings, render); err != nil {
		return Result{}, err
	}
	record.Status = taskoffload.StatusOffload
	body, err := os.ReadFile(contextPath)
	if err != nil {
		return Result{}, err
	}
	if err := store.Save(record, string(body)); err != nil {
		return Result{}, err
	}
	return result(record), nil
}
func (service *Service) launch(root string, record taskoffload.Record, path string, warnings func([]secretscan.Finding), render func(sessionrun.MoveResult) error) error {
	moved, err := service.deps.Move(context.Background(), sessionrun.MoveRequest{ProjectsRoot: root, Worktree: record.WorktreeDir, HandoverFile: path, Target: record.Target, Harness: record.Harness, Model: record.Model}, warnings)
	if err != nil {
		return err
	}
	return render(moved)
}
func result(record taskoffload.Record) Result {
	return Result{TaskID: record.TaskID, Task: record.Task, WorktreeDir: record.WorktreeDir, Status: string(record.Status), Harness: record.Harness, Model: record.Model, Target: record.Target}
}
