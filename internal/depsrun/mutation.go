package depsrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/deps"
)

func (service *Service) Set(ctx context.Context, request SetRequest) (SetResult, error) {
	report, runErr := service.deps.RunSet(ctx, request.Target, request.Repositories, request.Options)
	finish(request.Finish, runErr != nil)
	result := SetResult{Report: report, RunError: runErr}
	directory := request.Options.ReportDir
	if directory == "" && report.Operation != "" {
		home, err := service.deps.EnsureRoot(request.Options.GitHubDir)
		if err != nil {
			return result, err
		}
		directory = filepath.Join(home, "reports", report.Operation)
	}
	if directory != "" {
		if err := service.deps.WriteSetReports(directory, report); err != nil {
			return result, err
		}
	}
	return result, nil
}
func (service *Service) Bump(ctx context.Context, request BumpRequest) (BumpResult, error) {
	finished := false
	defer func() {
		if !finished {
			finish(request.Finish, true)
		}
	}()
	options := request.Options
	operation := deps.BumpOperationIDFor(options.Ecosystem, request.Events)
	directory := request.ReportDir
	if directory == "" {
		home, err := service.deps.EnsureRoot(request.ProjectsRoot)
		if err != nil {
			return BumpResult{}, err
		}
		directory = filepath.Join(home, "reports", operation)
	}
	result := BumpResult{ReportDir: directory}
	if request.Resume {
		loaded, err := service.deps.LoadBumpReport(directory)
		if err != nil {
			if os.IsNotExist(err) {
				return result, fmt.Errorf("--resume requires %s: %w", filepath.Join(directory, "deps-bump.yaml"), err)
			}
			return result, err
		}
		options.Options, loaded, err = resolveDepsBumpResumeParallel(options.Options, loaded, request.ResumeParallelExplicit)
		if err != nil {
			return result, err
		}
		options.Previous = &loaded
	}
	options.Persist = func(report deps.BumpReport) error { return service.deps.WriteBumpReports(directory, report) }
	report, runErr := service.deps.RunBump(ctx, request.Events, request.Repositories, options)
	finish(request.Finish, runErr != nil)
	finished = true
	result.Report = report
	if report.Operation == "" {
		return result, runErr
	}
	if err := service.deps.WriteBumpReports(directory, report); err != nil {
		return result, err
	}
	return result, runErr
}
func resolveDepsBumpResumeParallel(lifecycle deps.Options, report deps.BumpReport, explicit bool) (deps.Options, deps.BumpReport, error) {
	if explicit {
		// Parallelism bounds only the live worker pool. It neither selects
		// repositories nor changes the campaign's identity, so an operator may
		// safely raise or lower it while resuming.
		report.Parallel = lifecycle.Parallel
		report.ParallelExplicit = true
		return lifecycle, report, nil
	}
	if report.Parallel < 1 {
		return deps.Options{}, deps.BumpReport{}, fmt.Errorf("resume report has invalid parallelism %d", report.Parallel)
	}
	lifecycle.Parallel = report.Parallel
	// Restore the original run's explicit-parallel authority too: a resumed
	// `--parallel 1` campaign must not regain the read-only worker floor
	// merely because the resume invocation itself omitted the flag.
	lifecycle.ParallelExplicit = report.ParallelExplicit
	return lifecycle, report, nil
}
