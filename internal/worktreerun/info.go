package worktreerun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// InfoRequest identifies a checkout in one projects root.
type InfoRequest struct{ ProjectsRoot, Worktree string }

// InfoDocument joins a redacted work log with merger and collaboration authority.
type InfoDocument struct {
	worktrees.WorkLogView
	MergerLaneClaim *orchestrate.MergeLaneClaim `json:"merger_lane_claim,omitempty"`
	Collaboration   *worktreecollab.View        `json:"collaboration,omitempty"`
}

type infoPorts struct {
	load          func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error)
	lane          func(string, worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error)
	collaboration func(string) (worktreecollab.Service, error)
}

// InfoService inspects native checkout state without disclosing prompt bodies.
type InfoService struct{ ports infoPorts }

// DefaultInfoService composes the existing native work log and custody services.
func DefaultInfoService() InfoService {
	return InfoService{ports: infoPorts{load: worktrees.LoadWorkLogView, lane: activeMergeLaneClaimForInfo, collaboration: NewCollaborationService}}
}

// Inspect preserves the load, merger-lane and collaboration failure order.
func (service InfoService) Inspect(ctx context.Context, request InfoRequest) (InfoDocument, error) {
	view, err := service.ports.load(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: request.ProjectsRoot, Worktree: request.Worktree, IncludePromptBodies: false})
	if err != nil {
		return InfoDocument{}, err
	}
	lane, err := service.ports.lane(request.ProjectsRoot, view)
	if err != nil {
		return InfoDocument{}, err
	}
	collaboration, err := service.ports.collaboration(request.ProjectsRoot)
	if err != nil {
		return InfoDocument{}, err
	}
	coordination, err := collaboration.Inspect(ctx, request.Worktree)
	if err != nil && !errors.Is(err, worktrees.ErrCollaborationCanonicalClone) {
		return InfoDocument{}, err
	}
	document := InfoDocument{WorkLogView: view, MergerLaneClaim: lane}
	if err == nil {
		document.Collaboration = &coordination
	}
	return document, nil
}

func activeMergeLaneClaimForInfo(projectsRoot string, view worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) {
	repository, branch := "", view.Git.Branch
	if view.Manifest != nil {
		repository = view.Manifest.Repository
		if view.Manifest.Branch != "" {
			branch = view.Manifest.Branch
		}
	}
	return orchestrate.ActiveMergeLaneClaim(projectsRoot, repository, branch)
}
