package deps

import "context"

type adapter interface {
	inspect(context.Context, string, string, Target, Options) ([]Decision, error)
	apply(context.Context, string, Target, Options) ([]Decision, error)
}

func adapterFor(ecosystem Ecosystem) adapter {
	switch ecosystem {
	case EcosystemGitHubActions:
		return githubActionsAdapter{}
	case EcosystemGo:
		return goAdapter{}
	default:
		return nil
	}
}
