package depsrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

type npmPublicationLocks struct{ locks []orchestrate.OperationLock }

func (service *PublicationService) acquireNpmPublicationLocks(root string, operation string, releases []npmrelease.Release, resume bool) (npmPublicationLocks, error) {
	campaign, err := service.deps.AcquireLock(root, operation, resume)
	if err != nil {
		return npmPublicationLocks{}, err
	}
	locks := npmPublicationLocks{locks: []orchestrate.OperationLock{campaign}}
	for _, claim := range npmrelease.PublicationClaimOperationIDs(releases) {
		lock, err := service.deps.AcquireLock(root, claim, resume)
		if err != nil {
			locks.Release()
			return npmPublicationLocks{}, fmt.Errorf("acquire npm publication claim %q: %w", claim, err)
		}
		locks.locks = append(locks.locks, lock)
	}
	return locks, nil
}
func (locks npmPublicationLocks) Release() {
	for index := len(locks.locks) - 1; index >= 0; index-- {
		_ = locks.locks[index].Release()
	}
}

func (service *PublicationService) npmPublicationReportDir(root string, releases []npmrelease.Release, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	home, err := service.deps.EnsureRoot(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "reports", npmrelease.OperationIDFor(releases)), nil
}
func npmPublicationPlanReportDir(reportDir string) string {
	return filepath.Join(reportDir, "plan")
}
func (service *PublicationService) npmPublicationResumeReport(reportDir string, resume bool) (*npmrelease.Report, error) {
	exists, err := service.deps.ReportExists(reportDir)
	if err != nil {
		return nil, fmt.Errorf("inspect npm publication report: %w", err)
	}
	if !resume && exists {
		return nil, fmt.Errorf("existing npm publication report in %s requires --resume; refusing to overwrite or redispatch", reportDir)
	}
	if !resume {
		return nil, nil
	}
	if !exists {
		return nil, fmt.Errorf("--resume requires %s", filepath.Join(reportDir, "npm-publish.yaml"))
	}
	loaded, err := service.deps.LoadPublication(reportDir)
	if err != nil {
		return nil, fmt.Errorf("--resume requires readable %s: %w", filepath.Join(reportDir, "npm-publish.yaml"), err)
	}
	return &loaded, nil
}
func (service *PublicationService) npmPublicationBumpPrevious(reportDir string, resume bool) (*deps.BumpReport, error) {
	if !resume {
		return nil, nil
	}
	previous, err := service.deps.LoadBump(reportDir)
	if os.IsNotExist(err) {
		// Publication can have reached the registry before its first handoff.
		// In that case --resume starts the shared bump engine exactly once rather
		// than treating a missing downstream report as permission to redispatch.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load persisted dependency-wave report: %w", err)
	}
	return &previous, nil
}
func publicationHasDispatch(report *npmrelease.Report) bool {
	if report == nil {
		return false
	}
	for _, receipt := range report.Releases {
		if !receipt.DispatchAt.IsZero() {
			return true
		}
	}
	return false
}
func (service *PublicationService) plannedNpmPublication(ctx context.Context, prepared npmPublishPrepared, request PublicationRequest) (npmrelease.Report, error) {
	return service.deps.Run(ctx, prepared.releases, npmrelease.Options{
		DryRun: true, Ref: request.Lifecycle.Ref, Registry: request.Registry, Timeout: request.Lifecycle.Timeout, PollInterval: request.WorkflowPoll,
	})
}
func releaseEventsForReleases(releases []npmrelease.Release) []deps.ReleaseEvent {
	events := make([]deps.ReleaseEvent, len(releases))
	checkedAt := time.Now().UTC()
	for index, release := range releases {
		events[index] = deps.ReleaseEvent{Dependency: release.Package, Version: release.Version, Source: "npm_workflow_plan", CheckedAt: checkedAt}
	}
	return events
}
func attachNpmPropagation(publication *npmrelease.Report, propagation deps.BumpReport) {
	if propagation.Operation == "" {
		return
	}
	publication.PropagationOperation = propagation.Operation
	publication.Propagation = &propagation
}
func plannedNpmReleaseEvents(report npmrelease.Report) []deps.ReleaseEvent {
	releases := make([]npmrelease.Release, len(report.Releases))
	for index, receipt := range report.Releases {
		releases[index] = receipt.Release
	}
	return releaseEventsForReleases(releases)
}
