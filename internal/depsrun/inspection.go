package depsrun

import (
	"context"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/deps"
)

func (service *Service) Graph(ctx context.Context, request GraphRequest) (GraphResult, error) {
	graph, err := service.deps.BuildGraph(ctx, request.Repositories, request.Options)
	if err != nil {
		finish(request.Finish, true)
		return GraphResult{}, err
	}
	finish(request.Finish, false)
	directory := request.ReportDir
	if directory == "" {
		home, err := service.deps.EnsureRoot(request.Options.GitHubDir)
		if err != nil {
			return GraphResult{}, err
		}
		directory = filepath.Join(home, "reports", "deps-graph-"+string(request.Options.Ecosystem))
	}
	paths, err := service.deps.WriteGraphReports(directory, graph, request.View)
	return GraphResult{Graph: graph, Paths: paths}, err
}
func (service *Service) Drift(ctx context.Context, request DriftRequest) (deps.DriftReport, error) {
	report, err := service.deps.AnalyzeDrift(ctx, request.Repositories, request.Options)
	if err != nil {
		finish(request.Finish, true)
		return report, err
	}
	if deps.DriftFailedWith(report, request.Options.FailOnDrift, request.Options.FailOnBehind) {
		if request.Finish != nil {
			request.Finish("completed with findings")
		}
	} else {
		finish(request.Finish, false)
	}
	directory := request.ReportDir
	if directory == "" {
		home, err := service.deps.EnsureRoot(request.Options.GitHubDir)
		if err != nil {
			return report, err
		}
		directory = filepath.Join(home, "reports", "deps-drift")
	}
	return report, service.deps.WriteDriftReports(directory, report)
}
func (service *Service) Peers(ctx context.Context, options deps.PeerOptions) (deps.PeerReport, error) {
	return service.deps.InspectPeers(ctx, options)
}
