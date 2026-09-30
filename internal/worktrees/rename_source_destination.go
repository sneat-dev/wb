package worktrees

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/wbhome"
	"os"
)

type renameSourceProofPhase uint8

const (
	renamePreflightProof renameSourceProofPhase = iota
	renameApplyProof
)

// renameSourceProofPorts are scoped to one source recheck. The caller retains
// its own authority: preflight also proves base, branch policy and Work Log;
// apply repeats this proof immediately before the first terminal write.
type renameSourceProofPorts struct {
	Inspect     func(context.Context, RenameOptions, *renamePlan) (ListResult, error)
	VerifyCache func(context.Context, string, []string) error
}

func productionRenameSourceProofPorts() renameSourceProofPorts {
	return renameSourceProofPorts{
		Inspect: func(ctx context.Context, options RenameOptions, plan *renamePlan) (ListResult, error) {
			return inspectLifecycleWorktree(ctx, options.ProjectsRoot, "",
				wbhome.Layout{WorktreesRoot: plan.entry.WorktreesRoot, Local: plan.entry.Local},
				options.OldTask, plan.entry.WorktreeDir, options.Base, "", false, false, false, inspectPolicy{})
		},
		VerifyCache: verifyRecycleState,
	}
}

func proveRenameSource(ctx context.Context, options RenameOptions, plan *renamePlan, phase renameSourceProofPhase, ports renameSourceProofPorts) (ListResult, error) {
	refreshed, err := ports.Inspect(ctx, options, plan)
	if err != nil {
		if phase == renamePreflightProof {
			return ListResult{}, fmt.Errorf("preflight %s: %w", plan.entry.Repository, err)
		}
		return ListResult{}, fmt.Errorf("recheck %s before renaming: %w", plan.entry.Repository, err)
	}
	if phase == renamePreflightProof {
		if !refreshed.Clean || refreshed.HeadSHA != plan.entry.HeadSHA {
			return ListResult{}, fmt.Errorf("preflight %s: worktree/head changed", plan.entry.Repository)
		}
		if err := ports.VerifyCache(ctx, refreshed.WorktreeDir, options.PreserveCachePaths); err != nil {
			return ListResult{}, fmt.Errorf("preflight %s: %w", plan.entry.Repository, err)
		}
		return refreshed, nil
	}
	if !refreshed.Clean {
		return ListResult{}, fmt.Errorf("rename safety changed for %s: worktree has local changes", refreshed.Repository)
	}
	if refreshed.HeadSHA != plan.entry.HeadSHA {
		return ListResult{}, fmt.Errorf("rename safety changed for %s: branch head moved", refreshed.Repository)
	}
	if err := ports.VerifyCache(ctx, plan.entry.WorktreeDir, options.PreserveCachePaths); err != nil {
		return ListResult{}, fmt.Errorf("prepare %s for recycle: %w", refreshed.Repository, err)
	}
	if refreshed.HeadSHA != plan.refreshed.HeadSHA {
		return ListResult{}, fmt.Errorf("rename safety changed for %s after coordinated preflight", refreshed.Repository)
	}
	return refreshed, nil
}

// renamePreflightPolicyPorts binds the read-only Git and immutable Work Log
// witnesses for one locked task member. Destination preflight may probe or
// create a root, but no claim is sealed until every member passes this gate.
type renamePreflightPolicyPorts struct {
	SyncBase             func() (string, error)
	DeriveBranch         func(string) (string, error)
	LocalBranchExists    func() (bool, error)
	IsAncestor           func() (bool, error)
	RemoteHead           func() (string, error)
	PreflightClaim       func() error
	PreflightDestination func() error
}

func productionRenamePreflightPolicyPorts(ctx context.Context, options RenameOptions, plan *renamePlan, refreshed ListResult, canonical *canonicalRepository) renamePreflightPolicyPorts {
	return renamePreflightPolicyPorts{
		SyncBase: func() (string, error) {
			return synchronizeCanonical(ctx, canonical, plan.entry.Repository, options.Base)
		},
		DeriveBranch: func(baseRevision string) (string, error) {
			return deriveBranchName(ctx, branchNamingOptions{
				Task: options.NewTask, ExactBranch: options.Branch, ExactBranchChosen: options.BranchChosen,
				CLIPrefix: options.BranchPrefix, CLIPrefixChosen: options.BranchPrefixChosen,
				Canonical: canonical, BaseRevision: baseRevision, Base: options.Base,
			})
		},
		LocalBranchExists: func() (bool, error) {
			return localBranchExistsCanonical(ctx, canonical, plan.result.NewBranch)
		},
		IsAncestor: func() (bool, error) {
			return isAncestor(ctx, plan.entry.CanonicalDir, refreshed.HeadSHA, "origin/"+options.Base)
		},
		RemoteHead: func() (string, error) {
			return remoteBranchHead(ctx, plan.entry.CanonicalDir, refreshed.Branch)
		},
		PreflightClaim: func() error {
			resolution, err := wbhome.Resolve(options.ProjectsRoot)
			if err != nil {
				return err
			}
			if err := preflightWorkLogSeal(resolution.Write.Home, refreshed.WorktreeDir, refreshed.HeadSHA); err != nil {
				return fmt.Errorf("preflight Work Log for %s: %w", plan.entry.Repository, err)
			}
			return nil
		},
		PreflightDestination: func() error {
			if err := preflightRenamePhysicalDestination(ctx, options.NewTask, plan); err != nil {
				return fmt.Errorf("preflight destination for %s: %w", plan.entry.Repository, err)
			}
			return nil
		},
	}
}

func proveRenamePreflightPolicy(options RenameOptions, plan *renamePlan, refreshed ListResult, ports renamePreflightPolicyPorts) (string, string, error) {
	baseRevision, err := ports.SyncBase()
	if err != nil {
		return "", "", fmt.Errorf("fetch base before recycling %s: %w", plan.entry.Repository, err)
	}
	if baseRevision != plan.baseRevision {
		return "", "", fmt.Errorf("origin/%s advanced for %s during rename preflight from %s to %s; rerun so every branch policy and collision check is pinned to one exact base", options.Base, plan.entry.Repository, plan.baseRevision, baseRevision)
	}
	branch, err := ports.DeriveBranch(baseRevision)
	if err != nil {
		return "", "", err
	}
	if branch != plan.result.NewBranch {
		return "", "", fmt.Errorf("branch policy changed for %s during rename preflight from %q to %q; rerun so the planned report is exact", plan.entry.Repository, plan.result.NewBranch, branch)
	}
	if exists, existsErr := ports.LocalBranchExists(); existsErr != nil {
		return "", "", existsErr
	} else if exists {
		return "", "", fmt.Errorf("branch %q already exists in %s; choose another --branch", plan.result.NewBranch, plan.entry.Repository)
	}
	merged, err := ports.IsAncestor()
	if err != nil {
		return "", "", err
	}
	if !merged && !options.Force {
		return "", "", fmt.Errorf("branch %q is not integrated into origin/%s; use `wb worktree abort --disposition handoff|not_landed`, or explicitly authorize discard before recycle", refreshed.Branch, options.Base)
	}
	remoteHead, err := ports.RemoteHead()
	if err != nil {
		return "", "", fmt.Errorf("inspect old remote branch before recycling %s: %w", plan.entry.Repository, err)
	}
	if remoteHead != "" && remoteHead != refreshed.HeadSHA {
		return "", "", fmt.Errorf("refuse to recycle %s: origin/%s is %s, expected exact old head %s", refreshed.Repository, refreshed.Branch, remoteHead, refreshed.HeadSHA)
	}
	if remoteHead != "" && !options.DeleteRemote {
		return "", "", fmt.Errorf("origin/%s remains cleanup backlog; rerun recycle with --remote", refreshed.Branch)
	}
	if err := ports.PreflightClaim(); err != nil {
		return "", "", err
	}
	if err := ports.PreflightDestination(); err != nil {
		return "", "", err
	}
	return baseRevision, remoteHead, nil
}

type renameDestinationPhase uint8

const (
	renameDestinationPreflight renameDestinationPhase = iota
	renameDestinationMove
)

// openRenameDestinationRoot keeps both phases anchored to the same typed
// directory authority. Preflight may create a local root or probe a shared
// root, but only the move phase creates the shared task/owner parent.
func openRenameDestinationRoot(ctx context.Context, plan *renamePlan, phase renameDestinationPhase) (*os.File, error) {
	if plan.destinationLocal {
		rootPath, root, err := openLocalRenameDestination(ctx, plan)
		if err != nil {
			return nil, err
		}
		if rootPath != plan.destinationRoot || !directoryStillMatches(rootPath, root) {
			_ = root.Close()
			if phase == renameDestinationPreflight {
				return nil, fmt.Errorf("local rename destination root changed during preflight: %s", plan.destinationRoot)
			}
			return nil, fmt.Errorf("local rename destination root changed before move: %s", plan.destinationRoot)
		}
		return root, nil
	}
	if phase == renameDestinationPreflight {
		return openSharedRenameDestinationRoot(plan, "during preflight")
	}
	return openSharedRenameDestinationRoot(plan, "before move")
}

func requireLocalRenameDestinationAbsent(root *os.File, task string) error {
	return requireAbsentNoFollowChild(int(root.Fd()), task)
}
