package orchestrate

import (
	"context"

	"github.com/sneat-dev/wb/internal/runner"
)

// prepareOwnerObservedRunner has no successful-result substitution. A named
// negative observation or private native mutation runs before delegating Git.
type prepareOwnerObservedRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
}

func (r prepareOwnerObservedRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if err := r.before(ctx, dir, name, args); err != nil {
		return runner.Result{}, err
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}
