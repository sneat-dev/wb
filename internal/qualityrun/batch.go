package qualityrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/reposelection"
	"os/exec"
	"regexp"
	"strings"
)

type batchOperations struct {
	Cover    func(context.Context, string, string, quality.RunOptions) quality.RepositoryCoverage
	Verify   func(context.Context, string, string, []quality.Check, quality.RunOptions) quality.VerificationReport
	Policy   func(string, quality.RunOptions) (quality.RunOptions, error)
	Snapshot func(string) GitState
}

func realBatchOperations() batchOperations {
	return batchOperations{Cover: quality.CoverWithOptions, Verify: quality.VerifyWithOptions, Policy: quality.RepositoryRunOptions, Snapshot: verificationGitSnapshot}
}

var exactGitObjectID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type GitState struct {
	Revision string
	Clean    bool
	Err      error
}

func (ops batchOperations) runCoverageTargets(targets []reposelection.Target, parallel int, options quality.RunOptions) []quality.RepositoryCoverage {
	reports := make([]quality.RepositoryCoverage, len(targets))
	reposelection.ForEach(len(targets), parallel, func(index int) {
		target := targets[index]
		targetOptions, err := ops.runOptionsForTarget(options, target)
		if err != nil {
			reports[index] = quality.RepositoryCoverage{
				Repository: target.Repository, Path: target.Path,
				Status: quality.StatusFailed, Error: err.Error(),
			}
			reportQualityRepositoryCompleted(options, target.Repository, reports[index].Status)
			return
		}
		reports[index] = ops.Cover(context.Background(), target.Repository, target.Path, targetOptions)
		reportQualityRepositoryCompleted(options, target.Repository, reports[index].Status)
	})
	return reports
}

func (ops batchOperations) runVerificationTargets(targets []reposelection.Target, checks []quality.Check, parallel int, options quality.RunOptions) []quality.VerificationReport {
	reports := make([]quality.VerificationReport, len(targets))
	reposelection.ForEach(len(targets), parallel, func(index int) {
		target := targets[index]
		targetOptions, err := ops.runOptionsForTarget(options, target)
		if err != nil {
			reports[index] = quality.VerificationReport{
				Repository: target.Repository,
				Path:       target.Path,
				Status:     quality.StatusFailed,
				Results:    []quality.VerificationEntry{{Status: quality.StatusFailed, Detail: err.Error()}},
			}
			reportQualityRepositoryCompleted(options, target.Repository, reports[index].Status)
			return
		}
		before := ops.Snapshot(target.Path)
		report := ops.Verify(context.Background(), target.Repository, target.Path, checks, targetOptions)
		after := ops.Snapshot(target.Path)
		if before.Err == nil && after.Err == nil && before.Clean && after.Clean && before.Revision == after.Revision {
			report.Revision = before.Revision
			report.WorkspaceClean = true
		}
		reports[index] = report
		reportQualityRepositoryCompleted(options, target.Repository, reports[index].Status)
	})
	return reports
}

func (ops batchOperations) runOptionsForTarget(options quality.RunOptions, target reposelection.Target) (quality.RunOptions, error) {
	return ops.Policy(target.Path, qualityRunOptionsForTarget(options, target.Repository))
}

func verificationGitSnapshot(repositoryPath string) GitState {
	return snapshotWith(repositoryPath, func(path string, args ...string) ([]byte, error) {
		return exec.Command("git", append([]string{"-C", path}, args...)...).Output()
	})
}
func snapshotWith(repositoryPath string, output func(string, ...string) ([]byte, error)) GitState {
	revisionOutput, err := output(repositoryPath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return GitState{Err: err}
	}
	revision := strings.ToLower(strings.TrimSpace(string(revisionOutput)))
	if !exactGitObjectID.MatchString(revision) {
		return GitState{Err: fmt.Errorf("invalid Git revision %q", revision)}
	}
	statusOutput, err := output(repositoryPath, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return GitState{Err: err}
	}
	return GitState{Revision: revision, Clean: len(statusOutput) == 0}
}

func qualityRunOptionsForTarget(options quality.RunOptions, repository string) quality.RunOptions {
	options.CoverageDiagnosticsRepository = repository
	progress := options.Progress
	if progress == nil {
		return options
	}
	options.Progress = func(event quality.Progress) {
		event.Repository = repository
		progress(event)
	}
	return options
}

func reportQualityRepositoryCompleted(options quality.RunOptions, repository string, status quality.Status) {
	if options.Progress != nil {
		options.Progress(quality.Progress{Repository: repository, State: quality.ProgressRepositoryCompleted, Status: status})
	}
}
