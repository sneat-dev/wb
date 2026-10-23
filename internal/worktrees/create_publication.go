package worktrees

import (
	"context"
	"fmt"
)

// createPublicationPorts are invocation-local boundaries for the physical
// checkout, immutable claim, and compensation phases. Zero values use the
// existing production services.
type createPublicationPorts struct {
	addWorktree   func(context.Context, securePublicationRequest) error
	recordWorkLog func(string, string, CreateResult, WorkLogOptions, workLogPublicationHooks) (WorkLogPublicationOutcome, error)
	recover       func(context.Context, string, CreateOptions, []createAttempt, error) ([]CreateRecoveryOutcome, error)
}

func (ports createPublicationPorts) withDefaults() createPublicationPorts {
	if ports.addWorktree == nil {
		ports.addWorktree = addWorktreeAtSecureDestination
	}
	if ports.recordWorkLog == nil {
		ports.recordWorkLog = recordWorkLogWithHooks
	}
	if ports.recover == nil {
		ports.recover = recoverFailedCreatePublications
	}
	return ports
}

// publishCreatePlans coordinates physical checkout and immutable Work Log
// publication for one Create invocation. A failure compensates attempts in
// reverse order and returns their exact recovery coordinates.
func publishCreatePlans(
	ctx context.Context, home string, options CreateOptions,
	operation preparedOperationRoot, sharedOperation *preparedOperationRoot,
	plans []createPlan, ports createPublicationPorts,
) ([]createAttempt, error) {
	ports = ports.withDefaults()
	attempts := make([]createAttempt, 0, len(plans))
	for index := range plans {
		plan := &plans[index]
		if plan.resumed && plan.resumeClaim != nil {
			continue
		}
		var publication *createdWorktreePublication
		physicalOperation := operation
		if !plan.resumed && !plan.recoveredStage {
			parent, repository := splitCloneRelative(plan.placement.Relative)
			if plan.placement.Local {
				if plan.localRootDir == nil {
					return attempts, fmt.Errorf("local worktree root was not prepared for %s", plan.result.Repository)
				}
				physicalOperation = preparedOperationRoot{Path: plan.localRoot, Worktrees: plan.localRootDir, Directory: plan.localRootDir}
				parent, repository = "", options.Operation
			} else if sharedOperation != nil {
				physicalOperation = *sharedOperation
			}
			if err := ports.addWorktree(ctx, securePublicationRequest{
				canonical: plan.canonical, operationRoot: physicalOperation.Path, operationDirectory: physicalOperation.Directory,
				parent: parent, repository: repository, branch: plan.result.Branch, base: plan.result.Base,
				baseRevision: plan.baseRevision, branchExists: plan.branchExists, publication: &publication,
				hooks: securePublicationHooks{
					beforeAdd:                     options.beforeSecureWorktreeAdd,
					afterStageDirectoryCreated:    options.afterSecureStageDirectoryCreated,
					afterStageValidation:          options.afterSecureStageValidation,
					afterStageVerification:        options.afterSecureStageVerification,
					afterDestinationValidation:    options.afterSecureDestinationValidation,
					afterCheckoutAuthorization:    options.afterSecureCheckoutAuthorization,
					afterCheckoutMove:             options.afterSecureCheckoutMove,
					afterPublishedAuthorization:   options.afterPublishedWorktreeAuthorization,
					afterRegistrationLockAcquired: options.afterRepositoryRegistrationLockAcquired,
					afterRepair:                   options.afterWorktreeRepair,
					beforeStagedWorktreeOpen:      options.beforeStagedWorktreeOpen,
					afterStagedAdd:                options.afterStagedWorktreeAdd,
					beforeRepair:                  options.beforeWorktreeRepair,
				},
			}); err != nil {
				if len(attempts) == 0 {
					return attempts, err
				}
				return attempts, createPublicationFailure(ctx, home, options, attempts, plan.result.Repository, "create worktree", err, ports.recover)
			}
		}
		attempts = append(attempts, createAttempt{plan: plan, operation: physicalOperation, publication: publication})
		attempt := &attempts[len(attempts)-1]
		hooks := workLogPublicationHooks{}
		if options.afterWorkLogClaim != nil {
			hooks.afterClaim = func() error { return options.afterWorkLogClaim(plan.result) }
		}
		if options.afterWorkLogProjection != nil {
			hooks.afterProjection = func() error { return options.afterWorkLogProjection(plan.result) }
		}
		var err error
		attempt.workLog, err = ports.recordWorkLog(home, options.Operation, plan.result, options.WorkLog, hooks)
		plan.result.WorkLogPath = attempt.workLog.ClaimPath
		if err != nil {
			return attempts, createPublicationFailure(ctx, home, options, attempts, plan.result.Repository, "record work log", err, ports.recover)
		}
	}
	return attempts, nil
}

func createPublicationFailure(ctx context.Context, home string, options CreateOptions, attempts []createAttempt, repository, action string, cause error, recover func(context.Context, string, CreateOptions, []createAttempt, error) ([]CreateRecoveryOutcome, error)) error {
	outcomes, recoveryErr := recover(ctx, home, options, attempts, cause)
	publicationErr := &CreatePublicationError{Outcomes: outcomes, Err: fmt.Errorf("%s for %s: %w", action, repository, cause)}
	if recoveryErr != nil {
		publicationErr.Err = fmt.Errorf("%w; coordinated publication recovery: %v", publicationErr.Err, recoveryErr)
	}
	return publicationErr
}
