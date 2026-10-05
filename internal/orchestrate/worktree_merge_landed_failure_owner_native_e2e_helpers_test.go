//go:build e2e

package orchestrate

import (
	"context"
	"reflect"

	"github.com/sneat-dev/wb/internal/runner"
)

type landedFailureOwnerRunner struct {
	runner.Runner
	path          string
	args          []string
	ordinal, seen int
	sentinel      error
}

func (r *landedFailureOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.path && name == "git" && reflect.DeepEqual(args, r.args) {
		r.seen++
		if r.seen == r.ordinal {
			return runner.Result{}, r.sentinel
		}
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}
