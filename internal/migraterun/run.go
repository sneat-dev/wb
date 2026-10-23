// Package migraterun coordinates migration effects without CLI policy or streams.
package migraterun

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/migrate"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type Operations struct {
	Load                     func(string) (migrate.Spec, error)
	BuildPlan                func(migrate.Spec, ...string) (migrate.Plan, error)
	Apply                    func(migrate.Plan) error
	EnsureRoot               func(string) (string, error)
	RunCampaign              func(migrate.Spec, string, migrate.CampaignOptions) (migrate.CampaignReport, error)
	CleanupCampaignWorktrees func(string, string) ([]string, error)
	WriteReports             func(string, migrate.Report) error
	WriteCampaignReports     func(string, migrate.CampaignReport) error
}

func DefaultOperations() Operations {
	return Operations{Load: migrate.Load, BuildPlan: migrate.BuildPlan, Apply: migrate.Apply, EnsureRoot: wbhome.EnsureRoot,
		RunCampaign: migrate.RunCampaign, CleanupCampaignWorktrees: migrate.CleanupCampaignWorktrees,
		WriteReports: migrate.WriteReports, WriteCampaignReports: migrate.WriteCampaignReports}
}

type LocalRequest struct {
	SpecPath  string
	Roots     []string
	Apply     bool
	ReportDir string
}
type LocalResult struct {
	Report     migrate.Report
	HasChanges bool
}

// Local preserves the synchronous engine's existing context-independent behavior.
func (ops Operations) Local(_ context.Context, request LocalRequest) (LocalResult, error) {
	spec, err := ops.Load(request.SpecPath)
	if err != nil {
		return LocalResult{}, err
	}
	plan, err := ops.BuildPlan(spec, request.Roots...)
	if err != nil {
		return LocalResult{}, err
	}
	result := LocalResult{Report: migrate.NewReport(spec, plan, request.Roots, "planned"), HasChanges: len(plan.Changes) > 0}
	if request.Apply {
		if err := ops.Apply(plan); err != nil {
			return LocalResult{}, err
		}
		result.Report.Status = "applied"
	}
	if request.ReportDir != "" {
		if err := ops.WriteReports(request.ReportDir, result.Report); err != nil {
			return LocalResult{}, err
		}
	}
	return result, nil
}

type CampaignRequest struct {
	SpecPath                                        string
	Roots                                           []string
	GitHubDir, ReportDir, Ref                       string
	ModuleRefs                                      map[string]string
	Apply, Resume, Commit, Push, PR, Merge, Cleanup bool
	Verify                                          migrate.Verification
	Parallel                                        int
	PrepareProgress                                 func(specID string) progress.Reporter
	BeforePersist                                   func(CampaignCompletion)
}
type CampaignCompletion struct {
	SpecID string
	Failed bool
}
type CampaignResult struct {
	Report   migrate.CampaignReport
	Removed  []string
	Cleanup  bool
	RunError error
}

// Campaign returns an execution error separately so presentation can render the
// persisted report first. Setup/persistence errors take precedence over that error.
func (ops Operations) Campaign(_ context.Context, request CampaignRequest) (CampaignResult, error) {
	spec, err := ops.Load(request.SpecPath)
	if err != nil {
		return CampaignResult{}, err
	}
	if request.Cleanup {
		if len(request.Roots) != 0 {
			return CampaignResult{}, fmt.Errorf("--cleanup does not take a source root")
		}
		removed, err := ops.CleanupCampaignWorktrees(request.GitHubDir, spec.ID)
		return CampaignResult{Removed: removed, Cleanup: true}, err
	}
	if len(request.Roots) != 1 {
		return CampaignResult{}, fmt.Errorf("--hierarchical requires exactly one source root")
	}
	reportDir := request.ReportDir
	if reportDir == "" {
		home, err := ops.EnsureRoot(request.GitHubDir)
		if err != nil {
			return CampaignResult{}, err
		}
		reportDir = filepath.Join(home, "reports", spec.ID)
	}
	var reporter progress.Reporter
	if request.PrepareProgress != nil {
		reporter = request.PrepareProgress(spec.ID)
	}
	report, runErr := ops.RunCampaign(spec, request.Roots[0], migrate.CampaignOptions{
		GitHubDir: request.GitHubDir, Ref: request.Ref, ModuleRefs: request.ModuleRefs, Apply: request.Apply,
		Verify: request.Verify, Commit: request.Commit, Push: request.Push, PR: request.PR, Merge: request.Merge,
		Resume: request.Resume, Parallel: request.Parallel, ReportDir: reportDir, Progress: reporter})
	if request.BeforePersist != nil {
		request.BeforePersist(CampaignCompletion{SpecID: spec.ID, Failed: runErr != nil})
	}
	if err := ops.WriteCampaignReports(reportDir, report); err != nil {
		return CampaignResult{}, err
	}
	return CampaignResult{Report: report, RunError: runErr}, nil
}
