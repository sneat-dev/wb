package depsrun

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type PublicationRequest struct {
	Repositories, Workflows, Packages, Versions, WorkflowInputs []string
	Registry, Checks                                            string
	Apply, NoVerify                                             bool
	WorkflowPoll, ReleasePoll, RefreshAfter                     time.Duration
	MaxWaves                                                    int
	Selection                                                   Selection
	Lifecycle                                                   deps.Options
}
type PublicationOutput struct {
	Publication npmrelease.Report `json:"publication" yaml:"publication"`
	Propagation *deps.BumpReport  `json:"propagation,omitempty" yaml:"propagation,omitempty"`
}
type PublicationProgress struct {
	Reporter progress.Reporter
	Finish   func(string)
}
type PublicationCallbacks struct {
	ValidateOutput func() error
	Emit           func(PublicationOutput) error
	StartProgress  func(string) PublicationProgress
}
type PublicationDependencies struct {
	Select           func(context.Context, Selection) ([]deps.Repository, error)
	Bump             func(context.Context, BumpRequest) (BumpResult, error)
	Run              func(context.Context, []npmrelease.Release, npmrelease.Options) (npmrelease.Report, error)
	AcquireLock      func(string, string, bool) (orchestrate.OperationLock, error)
	EnsureRoot       func(string) (string, error)
	ReportExists     func(string) (bool, error)
	LoadPublication  func(string) (npmrelease.Report, error)
	LoadBump         func(string) (deps.BumpReport, error)
	WritePublication func(string, npmrelease.Report) error
}
type PublicationService struct {
	deps      PublicationDependencies
	exitError func(int, string) error
}

func NewPublication(deps PublicationDependencies, exitError func(int, string) error) *PublicationService {
	return &PublicationService{deps: deps, exitError: exitError}
}
func DefaultPublicationDependencies(diagnostics io.Writer) PublicationDependencies {
	service := New(DefaultDependencies(diagnostics))
	return PublicationDependencies{Select: service.Select, Bump: service.Bump, Run: npmrelease.Run, AcquireLock: orchestrate.AcquireOperationLock, EnsureRoot: wbhome.EnsureRoot, ReportExists: npmrelease.ReportExists, LoadPublication: npmrelease.LoadReport, LoadBump: deps.LoadBumpReport, WritePublication: npmrelease.WriteReport}
}

type npmPublishPrepared struct {
	releases     []npmrelease.Release
	checks       []quality.Check
	repositories []deps.Repository
	reportDir    string
	operation    string
	previous     *npmrelease.Report
	bumpPrevious *deps.BumpReport
}

type npmPublishPreflight func(context.Context, PublicationRequest, PublicationCallbacks) (npmPublishPrepared, error)

func (service *PublicationService) Run(ctx context.Context, request PublicationRequest, callbacks PublicationCallbacks) error {
	return service.runWithPreflight(ctx, request, service.preflight, callbacks)
}
func (service *PublicationService) runWithPreflight(ctx context.Context, request PublicationRequest, preflight npmPublishPreflight, callbacks PublicationCallbacks) error {
	releases, operation, err := npmPublicationIdentity(request)
	if err != nil {
		return err
	}
	// Claim the report campaign and every package-version publication before
	// preflight selects the fleet. A concurrent invocation must fail without
	// even walking downstream repositories, including an overlapping subset or
	// superset campaign that would otherwise dispatch the same npm version.
	locks, err := service.acquireNpmPublicationLocks(request.Selection.ProjectsRoot, operation, releases, request.Lifecycle.Resume)
	if err != nil {
		return err
	}
	defer locks.Release()
	selectionProgress := callbacks.StartProgress("deps publish npm")
	request.Selection.Progress = selectionProgress.Reporter
	prepared, err := preflight(ctx, request, callbacks)
	if err != nil {
		selectionProgress.Finish("failed")
		return err
	}
	selectionProgress.Finish("selection completed")
	// Publication and downstream propagation own later progress lines. Do not
	// reuse a renderer whose selection heartbeat has already been stopped.
	request.Selection.Progress = nil
	if prepared.operation != operation {
		return fmt.Errorf("npm publication preflight changed the requested operation; refusing to dispatch")
	}
	return service.runPrepared(ctx, request, prepared, callbacks)
}
func (service *PublicationService) runPrepared(ctx context.Context, request PublicationRequest, prepared npmPublishPrepared, callbacks PublicationCallbacks) error {
	publication, err := service.plannedNpmPublication(ctx, prepared, request)
	if err != nil {
		return err
	}
	if !request.Apply {
		// A plan deliberately reaches the existing recalculated wave engine in
		// dry-run mode. It never calls GitHub's workflow dispatch or npm, but it
		// does retain true fleet findings (including duplicate declarations)
		// rather than making a provider-only plan look deceptively clean.
		// Its report lives below /plan and can never overwrite an apply/resume
		// deps-bump receipt in the publication report directory.
		planPrepared := prepared
		planPrepared.reportDir = npmPublicationPlanReportDir(prepared.reportDir)
		bumpReport, bumpErr := service.runNpmPublicationBump(ctx, planPrepared, request, callbacks, plannedNpmReleaseEvents(publication), true, false, true)
		attachNpmPropagation(&publication, bumpReport)
		if err := callbacks.Emit(PublicationOutput{Publication: publication, Propagation: publication.Propagation}); err != nil {
			return err
		}
		return bumpErr
	}

	// Recheck inside the operation lock: a concurrent invocation cannot create
	// a fresh report between preflight and the irreversible dispatch boundary.
	previous, err := service.npmPublicationResumeReport(prepared.reportDir, request.Lifecycle.Resume)
	if err != nil {
		return err
	}
	prepared.previous = previous
	prepared.bumpPrevious, err = service.npmPublicationBumpPrevious(prepared.reportDir, request.Lifecycle.Resume)
	if err != nil {
		return err
	}
	if err := validateNpmPublicationBump(request, prepared.checks, prepared.reportDir, releaseEventsForReleases(prepared.releases), !request.Lifecycle.Merge, prepared.bumpPrevious != nil, prepared.bumpPrevious); err != nil {
		return err
	}

	var preDispatchBump deps.BumpReport
	if !publicationHasDispatch(previous) {
		preDispatchBump, err = service.runNpmPublicationBump(ctx, prepared, request, callbacks, plannedNpmReleaseEvents(publication), true, false, true)
		attachNpmPropagation(&publication, preDispatchBump)
		if err != nil {
			if outputErr := callbacks.Emit(PublicationOutput{Publication: publication, Propagation: publication.Propagation}); outputErr != nil {
				return outputErr
			}
			return err
		}
	}

	publicationProgress := callbacks.StartProgress("deps publish npm")
	publication, publicationErr := service.deps.Run(ctx, prepared.releases, npmrelease.Options{
		Apply: true, Resume: request.Lifecycle.Resume, Ref: request.Lifecycle.Ref,
		Timeout: request.Lifecycle.Timeout, PollInterval: request.WorkflowPoll, Registry: request.Registry,
		ReportDir: prepared.reportDir, Previous: prepared.previous,
		Persist:  func(report npmrelease.Report) error { return service.deps.WritePublication(prepared.reportDir, report) },
		Progress: publicationProgress.Reporter,
	})
	if publicationErr != nil {
		publicationProgress.Finish("failed")
	} else {
		publicationProgress.Finish("published")
	}
	if preDispatchBump.Operation != "" {
		attachNpmPropagation(&publication, preDispatchBump)
	}
	if publicationErr != nil {
		if err := service.deps.WritePublication(prepared.reportDir, publication); err != nil {
			return err
		}
		if outputErr := callbacks.Emit(PublicationOutput{Publication: publication, Propagation: publication.Propagation}); outputErr != nil {
			return outputErr
		}
		return service.exitError(1, "npm publication did not reach registry evidence: "+publicationErr.Error())
	}
	events, err := npmrelease.EventsFor(publication)
	if err != nil {
		return err
	}
	resumeBump := request.Lifecycle.Resume && prepared.bumpPrevious != nil
	bumpReport, bumpErr := service.runNpmPublicationBump(ctx, prepared, request, callbacks, events, !request.Lifecycle.Merge, resumeBump, false)
	attachNpmPropagation(&publication, bumpReport)
	if err := service.deps.WritePublication(prepared.reportDir, publication); err != nil {
		return err
	}
	if err := callbacks.Emit(PublicationOutput{Publication: publication, Propagation: publication.Propagation}); err != nil {
		return err
	}
	return bumpErr
}
func (service *PublicationService) runNpmPublicationBump(ctx context.Context, prepared npmPublishPrepared, request PublicationRequest, callbacks PublicationCallbacks, events []deps.ReleaseEvent, dryRun, resume, noRegistry bool) (deps.BumpReport, error) {
	lifecycle := npmPublicationPropagationOptions(request.Lifecycle, prepared.reportDir, dryRun, resume)
	lifecycle.Checks = prepared.checks
	campaign := callbacks.StartProgress("deps bump")
	lifecycle.Progress = campaign.Reporter
	result, err := service.deps.Bump(ctx, BumpRequest{ProjectsRoot: request.Selection.ProjectsRoot, ReportDir: prepared.reportDir, Resume: resume, Events: events, Repositories: prepared.repositories, ResumeParallelExplicit: request.Lifecycle.ParallelExplicit, Finish: campaign.Finish, Options: deps.BumpOptions{Options: lifecycle, Ecosystem: deps.EcosystemNPM, MaxWaves: request.MaxWaves, PollInterval: request.ReleasePoll, RefreshAfter: request.RefreshAfter, NoRegistry: noRegistry}})
	return result.Report, err
}
