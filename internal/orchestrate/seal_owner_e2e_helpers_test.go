//go:build e2e

package orchestrate

import (
	"context"

	"github.com/sneat-dev/wb/internal/runner"
)

// sealOwnerRunner only refuses selected native observations or mutates a private
// fixture around a real command. It never supplies successful Git/custody output.
type sealOwnerRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
	after  func(context.Context, string, string, []string, runner.Result, error)
}

func (r *sealOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.before != nil {
		if err := r.before(ctx, dir, name, args); err != nil {
			return runner.Result{}, err
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if r.after != nil {
		r.after(ctx, dir, name, args, result, err)
	}
	return result, err
}
