package worktrees

import (
	"context"
	"os"

	"github.com/sneat-dev/wb/internal/pathguard"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// createWorkflowPorts are scoped to one Create invocation. Each zero value
// selects the existing production service; tests can refuse a precise phase
// without replacing package globals or weakening an external API.
type createWorkflowPorts struct {
	gitCapability        func() error
	resolveHome          func(string) (wbhome.Resolution, error)
	storePolicy          func(string) (userStorePolicy, error)
	writableRequirements func(string, string, []string, userStorePolicy, bool) ([]string, []pathguard.Requirement, error)
	checkWritable        func(string, []pathguard.Requirement, pathguard.Probe) error
	prepareLog           func(string, string, WorkLogOptions) (WorkLogOptions, error)
	reserveLog           func(string, string, WorkLogOptions) error
	prepareOperation     func(string, string, func()) (preparedOperationRoot, error)
	acquireLock          func(*os.File, string) (operationLock, error)
	openCanonical        func(string) (*canonicalRepository, error)
	userPlacement        func(context.Context, userStorePolicy, string, string) (WorktreePlacement, error)
	worktreePath         func(WorktreePlacement, string, string) (string, error)
	locateResumable      func(context.Context, *canonicalRepository, string, string, string, string) (string, error)
	directoryExists      func(string) (bool, error)
	registeredBranch     func(context.Context, *canonicalRepository, string) (string, error)
	activeClaim          func(string, string) (workLogClaim, workLogProjection, string, error)
	validateResume       func(string, WorkLogOptions, workLogClaim) error
	validateExisting     func(context.Context, *canonicalRepository, string, string) error
	extendLog            func(string, WorkLogOptions, workLogClaim) (WorkLogOptions, error)
	synchronize          func(context.Context, *canonicalRepository, string, string) (string, error)
	git                  func(context.Context, *canonicalRepository, ...string) (string, error)
	configuredPlacement  func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error)
	deriveBranch         func(context.Context, branchNamingOptions) (string, error)
	branchExists         func(context.Context, *canonicalRepository, string) (bool, error)
	branchWorktree       func(context.Context, *canonicalRepository, string) (bool, string, error)
	prepareLocalRoot     func(context.Context, *canonicalRepository, string) (string, *os.File, error)
	prepareSharedRoot    func(string, string) (preparedOperationRoot, error)
	recoverLocalStage    func(context.Context, *canonicalRepository, string, string, string) (bool, error)
	publish              func(context.Context, string, CreateOptions, preparedOperationRoot, *preparedOperationRoot, []createPlan, createPublicationPorts) ([]createAttempt, error)
	recordOwner          func(string, string, string, string, int) (OwnerRegistration, error)
}

func (ports createWorkflowPorts) withDefaults() createWorkflowPorts {
	if ports.gitCapability == nil {
		ports.gitCapability = requireGitFilesystemCapability
	}
	if ports.resolveHome == nil {
		ports.resolveHome = wbhome.Resolve
	}
	if ports.storePolicy == nil {
		ports.storePolicy = resolveUserStorePolicy
	}
	if ports.writableRequirements == nil {
		ports.writableRequirements = createWritableRequirements
	}
	if ports.checkWritable == nil {
		ports.checkWritable = pathguard.Check
	}
	if ports.prepareLog == nil {
		ports.prepareLog = PrepareWorkLogOptions
	}
	if ports.reserveLog == nil {
		ports.reserveLog = reserveOriginalPromptArchive
	}
	if ports.prepareOperation == nil {
		ports.prepareOperation = prepareOperationRoot
	}
	if ports.acquireLock == nil {
		ports.acquireLock = acquireLockAt
	}
	if ports.openCanonical == nil {
		ports.openCanonical = openCanonicalRepository
	}
	if ports.userPlacement == nil {
		ports.userPlacement = func(ctx context.Context, policy userStorePolicy, root, canonical string) (WorktreePlacement, error) {
			return policy.placement(ctx, root, canonical)
		}
	}
	if ports.worktreePath == nil {
		ports.worktreePath = func(placement WorktreePlacement, task, repository string) (string, error) {
			return placement.Path(task, repository)
		}
	}
	if ports.locateResumable == nil {
		ports.locateResumable = locateResumableWorktree
	}
	if ports.directoryExists == nil {
		ports.directoryExists = directoryExistsNoFollow
	}
	if ports.registeredBranch == nil {
		ports.registeredBranch = registeredBranchNameCanonical
	}
	if ports.activeClaim == nil {
		ports.activeClaim = activeWorkLogClaim
	}
	if ports.validateResume == nil {
		ports.validateResume = validateResumeWorkLogRequest
	}
	if ports.validateExisting == nil {
		ports.validateExisting = validateExistingWorktree
	}
	if ports.extendLog == nil {
		ports.extendLog = workLogOptionsForClaimExtension
	}
	if ports.synchronize == nil {
		ports.synchronize = synchronizeCanonical
	}
	if ports.git == nil {
		ports.git = gitCanonical
	}
	if ports.configuredPlacement == nil {
		ports.configuredPlacement = configuredWorktreePlacement
	}
	if ports.deriveBranch == nil {
		ports.deriveBranch = deriveBranchName
	}
	if ports.branchExists == nil {
		ports.branchExists = localBranchExistsCanonical
	}
	if ports.branchWorktree == nil {
		ports.branchWorktree = branchWorktreeCanonical
	}
	if ports.prepareLocalRoot == nil {
		ports.prepareLocalRoot = prepareCanonicalWorktreesRoot
	}
	if ports.prepareSharedRoot == nil {
		ports.prepareSharedRoot = prepareOperationRootAt
	}
	if ports.recoverLocalStage == nil {
		ports.recoverLocalStage = recoverTaskBoundLocalStage
	}
	if ports.publish == nil {
		ports.publish = publishCreatePlans
	}
	if ports.recordOwner == nil {
		ports.recordOwner = recordOwner
	}
	return ports
}
