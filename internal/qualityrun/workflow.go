// Package qualityrun composes quality mechanisms, selection and durable reports.
// It does not depend on CLI commands or contracts.
package qualityrun

import (
	"context"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/reposelection"
	"time"
)

type Observer struct {
	Started  func(int)
	Progress func(quality.Progress)
	Finished func()
}
type CoverageRequest struct {
	Selection reposelection.Request
	Run       quality.RunOptions
	Resume    bool
	ReportDir string
	Observer  Observer
}
type CoverageResult struct {
	Report    quality.CoverageReport
	NoWork    bool
	Artifacts CoverageArtifacts
}
type VerificationRequest struct {
	Selection                reposelection.Request
	Run                      quality.RunOptions
	Checks                   []quality.Check
	Name, Profile, ReportDir string
	Resume                   bool
	Observer                 Observer
}
type VerificationResult struct {
	Report VerificationIndex
	NoWork bool
}

func Coverage(ctx context.Context, request CoverageRequest) (CoverageResult, error) {
	return coverageWith(ctx, request, realBatchOperations(), reposelection.Select)
}
func coverageWith(_ context.Context, request CoverageRequest, ops batchOperations, selectTargets func(reposelection.Request) ([]reposelection.Target, error)) (CoverageResult, error) {
	targets, err := selectTargets(request.Selection)
	if err != nil {
		return CoverageResult{}, err
	}
	var previous quality.CoverageReport
	if request.Resume {
		targets, previous, err = resumeCoverageTargets(targets, request.ReportDir)
		if err != nil {
			return CoverageResult{}, err
		}
		if len(targets) == 0 {
			return CoverageResult{NoWork: true}, nil
		}
	}
	if request.Observer.Started != nil {
		request.Observer.Started(len(targets))
	}
	options := request.Run
	options.Progress = request.Observer.Progress
	reports := ops.runCoverageTargets(targets, request.Selection.Parallel, options)
	if request.Observer.Finished != nil {
		request.Observer.Finished()
	}
	report := quality.NewCoverageReport(reports)
	if request.Resume {
		report = mergeCoverageReports(previous, report)
	}
	artifacts, err := PersistCoverage(report, request.ReportDir)
	if err != nil {
		return CoverageResult{}, err
	}
	return CoverageResult{Report: report, Artifacts: artifacts}, nil
}
func Verification(ctx context.Context, request VerificationRequest) (VerificationResult, error) {
	return verificationWith(ctx, request, realBatchOperations(), reposelection.Select, time.Now)
}
func verificationWith(_ context.Context, request VerificationRequest, ops batchOperations, selectTargets func(reposelection.Request) ([]reposelection.Target, error), now func() time.Time) (VerificationResult, error) {
	targets, err := selectTargets(request.Selection)
	if err != nil {
		return VerificationResult{}, err
	}
	var previous VerificationIndex
	if request.Resume {
		targets, previous, err = resumeVerificationTargets(targets, request.ReportDir, request.Name)
		if err != nil {
			return VerificationResult{}, err
		}
		if len(targets) == 0 {
			return VerificationResult{NoWork: true}, nil
		}
	}
	if request.Observer.Started != nil {
		request.Observer.Started(len(targets))
	}
	options := request.Run
	options.Progress = request.Observer.Progress
	reports := ops.runVerificationTargets(targets, request.Checks, request.Selection.Parallel, options)
	if request.Observer.Finished != nil {
		request.Observer.Finished()
	}
	quality.SortVerificationReports(reports)
	report := VerificationIndex{SchemaVersion: 1, GeneratedAt: now().UTC(), Profile: request.Profile, Checks: request.Checks, Repositories: reports}
	if request.Resume {
		report = mergeVerificationReports(previous, report)
	}
	if err := PersistVerification(report, request.ReportDir, request.Name); err != nil {
		return VerificationResult{}, err
	}
	return VerificationResult{Report: report}, nil
}
