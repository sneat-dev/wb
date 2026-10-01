package worktrees

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/wbhome"
	"os"
	"path/filepath"
	"time"
)

type renameInvocation struct {
	options RenameOptions
	home    string
	now     time.Time
	listed  ListOutcome
}

// Admission is ordered before planning so malformed options, prompt evidence,
// and unavailable filesystem capabilities cannot start a task transaction.
type renameAdmissionPorts struct {
	Normalize   func(RenameOptions) (RenameOptions, error)
	ResolveHome func(string) (wbhome.Resolution, error)
	PrepareLog  func(string, string, WorkLogOptions) (WorkLogOptions, error)
	Capability  func() error
	Inventory   func(ListOptions) (ListOutcome, error)
}

func productionRenameAdmissionPorts(ctx context.Context) renameAdmissionPorts {
	return renameAdmissionPorts{
		Normalize:   normalizeRenameOptions,
		ResolveHome: wbhome.Resolve,
		PrepareLog:  PrepareWorkLogOptions,
		Capability:  requireGitFilesystemCapability,
		Inventory: func(options ListOptions) (ListOutcome, error) {
			return ListWithDiagnostics(ctx, options)
		},
	}
}

func admitRenameInvocation(options RenameOptions, ports renameAdmissionPorts) (renameInvocation, error) {
	normalized, err := ports.Normalize(options)
	if err != nil {
		return renameInvocation{}, err
	}
	resolution, err := ports.ResolveHome(normalized.ProjectsRoot)
	if err != nil {
		return renameInvocation{}, err
	}
	normalized.WorkLog, err = ports.PrepareLog(normalized.ProjectsRoot, normalized.NewTask, normalized.WorkLog)
	if err != nil {
		return renameInvocation{}, err
	}
	if normalized.Apply {
		if err := ports.Capability(); err != nil {
			return renameInvocation{}, err
		}
	}
	now := normalized.Now()
	if normalized.ReportDir == "" && normalized.Apply {
		normalized.ReportDir = DefaultRenameReportDir(resolution.Write.Home, now)
	}
	listed, err := ports.Inventory(ListOptions{
		ProjectsRoot: normalized.ProjectsRoot,
		Task:         normalized.OldTask,
		Base:         normalized.Base,
		Filter:       normalized.Filter,
		GitHub:       false,
	})
	if err != nil {
		return renameInvocation{}, err
	}
	return renameInvocation{options: normalized, home: resolution.Write.Home, now: now, listed: listed}, nil
}

type renameFacadePorts struct {
	Admit     func(RenameOptions) (renameInvocation, error)
	Plan      func(RenameOptions, []ListResult) ([]renamePlan, string, error)
	ApplyTask func(renameInvocation, []renamePlan) (RenameOutcome, error)
}

func productionRenameFacadePorts(ctx context.Context) renameFacadePorts {
	return renameFacadePorts{
		Admit: func(options RenameOptions) (renameInvocation, error) {
			return admitRenameInvocation(options, productionRenameAdmissionPorts(ctx))
		},
		Plan: func(options RenameOptions, entries []ListResult) ([]renamePlan, string, error) {
			return planRenameTaskMembers(options, entries, productionRenamePlanningPorts(ctx, options))
		},
		ApplyTask: func(request renameInvocation, plans []renamePlan) (RenameOutcome, error) {
			return applyRenameTask(request.options, plans, request.listed.Diagnostics,
				productionRenameTaskApplicationPorts(ctx, request.options, request.now, request.home))
		},
	}
}

func renameWithPorts(options RenameOptions, ports renameFacadePorts) (RenameOutcome, error) {
	request, err := ports.Admit(options)
	if err != nil {
		return RenameOutcome{}, err
	}
	normalized := request.options
	listed := request.listed
	if len(listed.Results) == 0 && len(listed.Diagnostics) == 0 {
		return RenameOutcome{}, fmt.Errorf("WB worktree task %q was not found", normalized.OldTask)
	}
	plans, destinationReason, err := ports.Plan(normalized, listed.Results)
	if err != nil {
		return RenameOutcome{}, err
	}
	blockRenameTask(plans, listed.Diagnostics, destinationReason)
	if !normalized.Apply {
		return RenameOutcome{Results: collectRenameResults(plans), Diagnostics: listed.Diagnostics}, nil
	}
	return ports.ApplyTask(request, plans)
}

// Planning is confined to the old inventory snapshot. A shared destination
// task is checked once, while each repository-local destination is checked
// separately; neither result grants apply authority without locked preflight.
type renamePlanningPorts struct {
	ResolveBranch    func(ListResult) (string, string, error)
	ResolvePlacement func(ListResult, string) (WorktreePlacement, error)
	Path             func(WorktreePlacement, string) (string, error)
	Lstat            func(string) (os.FileInfo, error)
}

func productionRenamePlanningPorts(ctx context.Context, options RenameOptions) renamePlanningPorts {
	return renamePlanningPorts{
		ResolveBranch: func(entry ListResult) (string, string, error) {
			return resolveRenameBranch(ctx, options, entry)
		},
		ResolvePlacement: func(entry ListResult, baseRevision string) (WorktreePlacement, error) {
			return ResolveWorktreePlacement(ctx, options.ProjectsRoot, entry.CanonicalDir, baseRevision)
		},
		Path: func(placement WorktreePlacement, repository string) (string, error) {
			return placement.Path(options.NewTask, repository)
		},
		Lstat: os.Lstat,
	}
}

func planRenameTaskMembers(options RenameOptions, entries []ListResult, ports renamePlanningPorts) ([]renamePlan, string, error) {
	plans := make([]renamePlan, len(entries))
	destinationReason := ""
	sharedTaskDestinations := make(map[string]bool)
	for index, entry := range entries {
		branch, baseRevision, err := ports.ResolveBranch(entry)
		if err != nil {
			return nil, "", err
		}
		placement, err := ports.ResolvePlacement(entry, baseRevision)
		if err != nil {
			return nil, "", err
		}
		newWorktreeDir, err := ports.Path(placement, entry.Repository)
		if err != nil {
			return nil, "", err
		}
		collisionPath := newWorktreeDir
		if !placement.RepositoryLocal {
			collisionPath = filepath.Join(placement.Root, options.NewTask)
			if sharedTaskDestinations[collisionPath] {
				collisionPath = ""
			} else {
				sharedTaskDestinations[collisionPath] = true
			}
		}
		if collisionPath != "" {
			if _, statErr := ports.Lstat(collisionPath); statErr == nil {
				destinationReason = fmt.Sprintf("destination task already exists: %s", collisionPath)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return nil, "", fmt.Errorf("inspect destination task %s: %w", collisionPath, statErr)
			}
		}
		eligible, reason := renameEligibility(entry)
		plans[index] = renamePlan{entry: entry, destinationRoot: placement.Root, destinationLocal: placement.RepositoryLocal, baseRevision: baseRevision, result: RenameResult{
			OldTask: options.OldTask, NewTask: options.NewTask,
			Repository: entry.Repository, CanonicalDir: entry.CanonicalDir,
			OldWorktreeDir: entry.WorktreeDir,
			NewWorktreeDir: newWorktreeDir,
			OldBranch:      entry.Branch, NewBranch: branch, Base: options.Base,
			Eligible: eligible, Reason: reason,
		}}
	}
	return plans, destinationReason, nil
}
