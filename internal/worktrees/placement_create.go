package worktrees

import (
	"context"
	"fmt"
	"os"
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
// arbitrary directory by constructing WorktreePlacement itself. projectsRoot is
// the root the staged checkout's hooks will resolve, and is passed through so
// the sandbox authorizes the same hook runtime directory.
func CreateWorktreeAtPlacement(
	ctx context.Context,
	projectsRoot string,
	canonicalPath string,
	placement WorktreePlacement,
	task, repository, branch, base, baseRevision string,
) (*PlacementWorktree, error) {
	return createWorktreeAtPlacementWith(ctx, projectsRoot, canonicalPath, placement,
		task, repository, branch, base, baseRevision, placementCreatePorts{})
}

type placementCreatePorts struct {
	openCanonical      func(string) (*canonicalRepository, error)
	configured         func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error)
	path               func(WorktreePlacement, string, string) (string, error)
	branchExists       func(context.Context, *canonicalRepository, string) (bool, error)
	branchWorktree     func(context.Context, *canonicalRepository, string) (bool, string, error)
	prepareLocal       func(context.Context, *canonicalRepository, string) (string, *os.File, error)
	prepareOperation   func(string, string) (preparedOperationRoot, error)
	prepareDestination func(string, *os.File, string, string) (string, bool, error)
	publish            func(context.Context, securePublicationRequest) error
}

func (ports placementCreatePorts) withDefaults() placementCreatePorts {
	if ports.openCanonical == nil {
		ports.openCanonical = openCanonicalRepository
	}
	if ports.configured == nil {
		ports.configured = configuredWorktreePlacement
	}
	if ports.path == nil {
		ports.path = func(placement WorktreePlacement, task, repository string) (string, error) {
			return placement.Path(task, repository)
		}
	}
	if ports.branchExists == nil {
		ports.branchExists = localBranchExistsCanonical
	}
	if ports.branchWorktree == nil {
		ports.branchWorktree = branchWorktreeCanonical
	}
	if ports.prepareLocal == nil {
		ports.prepareLocal = prepareCanonicalWorktreesRoot
	}
	if ports.prepareOperation == nil {
		ports.prepareOperation = prepareOperationRootAt
	}
	if ports.prepareDestination == nil {
		ports.prepareDestination = prepareWorktreeDestination
	}
	if ports.publish == nil {
		ports.publish = addWorktreeAtSecureDestination
	}
	return ports
}

func createWorktreeAtPlacementWith(
	ctx context.Context,
	projectsRoot string,
	canonicalPath string,
	placement WorktreePlacement,
	task, repository, branch, base, baseRevision string,
	ports placementCreatePorts,
) (*PlacementWorktree, error) {
	ports = ports.withDefaults()
	ctx = withProjectsRoot(ctx, projectsRoot)
	canonical, err := ports.openCanonical(canonicalPath)
	if err != nil {
		return nil, err
	}
	defer canonical.close()

	configured, err := ports.configured(ctx, projectsRoot, canonical, baseRevision)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(placement.Root) != filepath.Clean(configured.Root) ||
		placement.RepositoryLocal != configured.Local ||
		placement.relative != configured.Relative {
		return nil, fmt.Errorf("worktree placement does not match the configured policy for %s", canonical.path)
	}
	worktree, err := ports.path(placement, task, repository)
	if err != nil {
		return nil, err
	}
	_, name, err := splitRepository(repository)
	if err != nil {
		return nil, err
	}
	branchExists, err := ports.branchExists(ctx, canonical, branch)
	if err != nil {
		return nil, err
	}
	if branchExists {
		if occupied, path, occupiedErr := ports.branchWorktree(ctx, canonical, branch); occupiedErr != nil {
			return nil, occupiedErr
		} else if occupied {
			return nil, fmt.Errorf("branch %q is already checked out at %s", branch, path)
		}
	}

	physicalOperation := preparedOperationRoot{}
	physicalParent, physicalRepository := splitCloneRelative(configured.Relative)
	if physicalRepository == "" {
		physicalRepository = name
	}
	if placement.RepositoryLocal {
		root, directory, rootErr := ports.prepareLocal(ctx, canonical, baseRevision)
		if rootErr != nil {
			return nil, rootErr
		}
		defer func() { _ = directory.Close() }()
		if filepath.Clean(root) != filepath.Clean(placement.Root) {
			return nil, fmt.Errorf("resolved local worktree root changed before creation")
		}
		physicalOperation = preparedOperationRoot{Path: root, Worktrees: directory, Directory: directory}
		physicalParent, physicalRepository = "", task
	} else {
		prepared, prepareErr := ports.prepareOperation(placement.Root, task)
		if prepareErr != nil {
			return nil, prepareErr
		}
		defer prepared.close()
		physicalOperation = prepared
	}
	planned, exists, err := ports.prepareDestination(physicalOperation.Path, physicalOperation.Directory, physicalParent, physicalRepository)
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
	if err := ports.publish(ctx, securePublicationRequest{
		canonical: canonical, operationRoot: physicalOperation.Path, operationDirectory: physicalOperation.Directory,
		parent: physicalParent, repository: physicalRepository, branch: branch, base: base,
		baseRevision: baseRevision, branchExists: branchExists, publication: &publication,
	}); err != nil {
		return nil, err
	}
	if publication == nil {
		return nil, fmt.Errorf("secure worktree creation produced no publication receipt")
	}
	return &PlacementWorktree{Path: worktree, publication: publication}, nil
}
