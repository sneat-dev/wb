// Package depsrun owns dependency selection and operation/report custody.
package depsrun

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/fleetdiscovery"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type Selection struct {
	ProjectsRoot, Filter, RepositoryPath, Match, Regex string
	ExtraOrgs                                          []string
	Fleet                                              bool
	Parallel, Retry                                    int
	Timeout                                            time.Duration
	Progress                                           progress.Reporter
}
type GraphRequest struct {
	Repositories []deps.Repository
	Options      deps.GraphOptions
	View         deps.GraphView
	ReportDir    string
	Finish       func(string)
}
type GraphResult struct {
	Graph deps.Graph
	Paths deps.GraphReportPaths
}
type DriftRequest struct {
	Repositories []deps.Repository
	Options      deps.DriftOptions
	ReportDir    string
	Finish       func(string)
}
type SetRequest struct {
	Target       deps.Target
	Repositories []deps.Repository
	Options      deps.Options
	Finish       func(string)
}
type SetResult struct {
	Report   deps.Report
	RunError error
}
type BumpRequest struct {
	ProjectsRoot           string
	ReportDir              string
	Resume                 bool
	Events                 []deps.ReleaseEvent
	Repositories           []deps.Repository
	Options                deps.BumpOptions
	ResumeParallelExplicit bool
	Finish                 func(string)
}
type BumpResult struct {
	Report    deps.BumpReport
	ReportDir string
}
type SeedRequest struct {
	Ecosystem       deps.Ecosystem
	Changed, Scopes []string
	Latest          bool
	Repositories    []deps.Repository
	Options         deps.BumpOptions
}
type SeedResult struct {
	Events      []deps.ReleaseEvent
	Resolutions []deps.LatestScopeResolution
}
type Dependencies struct {
	DiscoverModules   func(string) []string
	AssessDirective   func(context.Context, string, deps.DirectivePolicy, deps.Options) (deps.DirectiveAssessment, error)
	ApplyDirective    func(context.Context, string, deps.DirectivePolicy, deps.Options) (deps.DirectiveAssessment, error)
	Fleet             func(root, filter string, extra []string) ([]deps.Repository, error)
	Identity          func(path, root string) (string, string, error)
	Abs               func(string) (string, error)
	EnsureRoot        func(string) (string, error)
	BuildGraph        func(context.Context, []deps.Repository, deps.GraphOptions) (deps.Graph, error)
	AnalyzeDrift      func(context.Context, []deps.Repository, deps.DriftOptions) (deps.DriftReport, error)
	InspectPeers      func(context.Context, deps.PeerOptions) (deps.PeerReport, error)
	RunSet            func(context.Context, deps.Target, []deps.Repository, deps.Options) (deps.Report, error)
	RunBump           func(context.Context, []deps.ReleaseEvent, []deps.Repository, deps.BumpOptions) (deps.BumpReport, error)
	DeriveLatest      func(context.Context, []deps.Repository, []string, deps.BumpOptions) ([]deps.ReleaseEvent, []deps.LatestScopeResolution, error)
	WriteGraphReports func(string, deps.Graph, deps.GraphView) (deps.GraphReportPaths, error)
	WriteDriftReports func(string, deps.DriftReport) error
	WriteSetReports   func(string, deps.Report) error
	WriteBumpReports  func(string, deps.BumpReport) error
	LoadBumpReport    func(string) (deps.BumpReport, error)
}

type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }
func DefaultDependencies(diagnostics io.Writer) Dependencies {
	resolver := fleetdiscovery.New(diagnostics)
	return Dependencies{DiscoverModules: DiscoverModules, AssessDirective: deps.AssessDirective, ApplyDirective: deps.ApplyDirective,
		Fleet: func(root, filter string, extra []string) ([]deps.Repository, error) {
			found, err := resolver.Discover(root, filter, func() []string { return resolver.Owners(extra) })
			if err != nil {
				return nil, err
			}
			result := make([]deps.Repository, 0, len(found))
			for _, repo := range found {
				result = append(result, deps.Repository{Slug: repo.Slug(), Path: repo.Path, CloneURL: repo.CloneURL, Archived: repo.Archived})
			}
			return result, nil
		}, Identity: repositoryIdentity, Abs: filepath.Abs, EnsureRoot: wbhome.EnsureRoot,
		BuildGraph: deps.BuildGraph, AnalyzeDrift: deps.AnalyzeDrift, InspectPeers: deps.InspectPeers,
		RunSet: deps.Run, RunBump: deps.RunBump, DeriveLatest: deps.DeriveLatestReleaseEvents,
		WriteGraphReports: deps.WriteGraphReports, WriteDriftReports: deps.WriteDriftReports,
		WriteSetReports: deps.WriteReports, WriteBumpReports: deps.WriteBumpReports, LoadBumpReport: deps.LoadBumpReport,
	}
}
func finish(callback func(string), failed bool) {
	if callback != nil {
		if failed {
			callback("failed")
		} else {
			callback("completed")
		}
	}
}
