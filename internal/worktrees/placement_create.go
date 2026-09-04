package worktrees

import (
	"context"
	"fmt"
	"path/filepath"
)

// PlacementWorktree retains the published checkout identity until the caller
// has recorded the journal data that makes its new path recoverable.
type PlacementWorktree struct {
	Path        string
	publication *createdWorktreePublication
}

// Close releases retained descriptors after the caller has recorded its
// journal/manifest. It does not remove the published checkout.
func (created *PlacementWorktree) Close() {
	if created != nil && created.publication != nil {
		created.publication.close()
		created.publication = nil
	}
}

// CreateWorktreeAtPlacement publishes one linked checkout through WB's
// descriptor-anchored staging path. Callers keep their operation lock and
// journal/manifest ownership; this helper owns only the physical checkout
// transaction. placement must be the result of ResolveWorktreePlacement for
// canonicalPath and baseRevision, so a caller cannot direct Git to an
// arbitrary directory by constructing WorktreePlacement itself.
func CreateWorktreeAtPlacement(
	ctx context.Context,
	canonicalPath string,
	placement WorktreePlacement,
	task, repository, branch, base, baseRevision string,
) (*PlacementWorktree, error) {
	canonical, err := openCanonicalRepository(canonicalPath)
	if err != nil {
		return nil, err
	}
	defer canonical.close()

	configured, err := configuredWorktreePlacement(ctx, canonical, baseRevision)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(placement.Root) != filepath.Clean(configured.Root) || placement.RepositoryLocal != configured.Local {
		return nil, fmt.Errorf("worktree placement does not match the configured policy for %s", canonical.path)
	}
	worktree, err := placement.Path(task, repository)
	if err != nil {
		return nil, err
	}
	owner, name, err := splitRepository(repository)
	if err != nil {
		return nil, err
	}
	branchExists, err := localBranchExistsCanonical(ctx, canonical, branch)
	if err != nil {
		return nil, err
	}
	if branchExists {
		if occupied, path, occupiedErr := branchWorktreeCanonical(ctx, canonical, branch); occupiedErr != nil {
			return nil, occupiedErr
		} else if occupied {
			return nil, fmt.Errorf("branch %q is already checked out at %s", branch, path)
		}
	}

	physicalOperation := preparedOperationRoot{}
	physicalOwner, physicalRepository := owner, name
	if placement.RepositoryLocal {
		root, directory, rootErr := prepareCanonicalWorktreesRoot(ctx, canonical, baseRevision)
		if rootErr != nil {
			return nil, rootErr
		}
		defer func() { _ = directory.Close() }()
		if filepath.Clean(root) != filepath.Clean(placement.Root) {
			return nil, fmt.Errorf("resolved local worktree root changed before creation")
		}
		physicalOperation = preparedOperationRoot{Path: root, Worktrees: directory, Directory: directory}
		physicalOwner, physicalRepository = "", task
	} else {
		prepared, prepareErr := prepareOperationRootAt(placement.Root, task)
		if prepareErr != nil {
			return nil, prepareErr
		}
		defer prepared.close()
		physicalOperation = prepared
	}
	planned, exists, err := prepareWorktreeDestination(physicalOperation.Path, physicalOperation.Directory, physicalOwner, physicalRepository)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(planned) != filepath.Clean(worktree) {
		return nil, fmt.Errorf("prepared worktree destination does not match resolved placement")
	}
	if exists {
		return nil, fmt.Errorf("worktree already exists: %s", worktree)
	}

	var publication *createdWorktreePublication
	if err := addWorktreeAtSecureDestination(
		ctx, canonical, physicalOperation.Path, physicalOperation.Directory,
		physicalOwner, physicalRepository, branch, base, baseRevision, branchExists,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &publication,
	); err != nil {
		return nil, err
	}
	if publication == nil {
		return nil, fmt.Errorf("secure worktree creation produced no publication receipt")
	}
	return &PlacementWorktree{Path: worktree, publication: publication}, nil
}
