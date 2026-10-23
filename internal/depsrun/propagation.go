package depsrun

import (
	"context"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type PropagationRequest struct {
	ProjectsRoot string
	Options      locallink.Options
}
type PropagationDependencies struct {
	OpenStore func(string) (*streams.Store, error)
	Home      func(string) (string, error)
	Git       func(time.Duration) locallink.Git
	Node      func(cacheRoot, contentHash string, timeout time.Duration) locallink.Node
	Verifier  func(time.Duration) locallink.Verifier
}
type PropagationService struct{ deps PropagationDependencies }

func NewPropagation(dependencies PropagationDependencies) *PropagationService {
	return &PropagationService{deps: dependencies}
}
func DefaultPropagationDependencies() PropagationDependencies {
	return PropagationDependencies{
		OpenStore: streams.Open, Home: wbhome.Root,
		Git: func(timeout time.Duration) locallink.Git { return locallink.ExecGit{Timeout: timeout} },
		Node: func(cacheRoot, hash string, timeout time.Duration) locallink.Node {
			return locallink.ExecNode{CacheRoot: cacheRoot, ContentHash: hash, Timeout: timeout}
		},
		Verifier: func(timeout time.Duration) locallink.Verifier {
			return locallink.QualityVerifier{Options: quality.RunOptions{Timeout: timeout}}
		},
	}
}
func (service *PropagationService) Run(ctx context.Context, request PropagationRequest) (locallink.Result, error) {
	store, err := service.deps.OpenStore(request.ProjectsRoot)
	if err != nil {
		return locallink.Result{}, err
	}
	home, err := service.deps.Home(request.ProjectsRoot)
	if err != nil {
		return locallink.Result{}, err
	}
	engine := &locallink.Engine{Store: store, Git: service.deps.Git(request.Options.Timeout), Verifier: service.deps.Verifier(request.Options.Timeout), CacheRoot: filepath.Join(home, "cache", "local-link")}
	engine.Node = service.deps.Node(engine.CacheRoot, "", request.Options.Timeout)
	if !request.Options.Undo {
		hash, _, hashErr := engine.Git.ContentHash(ctx, request.Options.Library)
		if hashErr != nil {
			return locallink.Result{}, hashErr
		}
		engine.Node = service.deps.Node(engine.CacheRoot, hash, request.Options.Timeout)
	}
	return engine.Run(ctx, request.Options)
}
