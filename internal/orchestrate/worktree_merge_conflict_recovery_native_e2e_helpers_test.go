//go:build e2e

package orchestrate

import (
	"context"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
)

// conflictTargetDriftRunner models physical TOCTOU drift, separately from
// negative read failures. All returned Git observations come from native Git.
type conflictTargetDriftRunner struct {
	runner.Runner
	worktree, earlier string
	consumed          bool
}

func (r *conflictTargetDriftRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if !r.consumed && dir == r.worktree && name == "git" && strings.Join(args, " ") == "rev-parse --verify HEAD^{commit}" {
		r.consumed = true
		if result, err := r.Runner.RunOpts(ctx, dir, options, "git", "reset", "--hard", r.earlier); err != nil {
			return result, err
		}
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}
