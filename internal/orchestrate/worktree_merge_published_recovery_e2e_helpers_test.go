//go:build e2e

package orchestrate

import (
	"context"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
)

// publishedObservedRunner records exact dispatch while every result remains
// an actual native Git observation. Only the named error case refuses a read.
type publishedObservedRunner struct {
	runner.Runner
	reads               []string
	faultDir, faultArgs string
	fault               error
}

func (r *publishedObservedRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.reads = append(r.reads, dir+"\x00"+name+" "+strings.Join(args, " "))
	if dir == r.faultDir && name == "git" && strings.Join(args, " ") == r.faultArgs {
		return runner.Result{}, r.fault
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

// publishedPrivateDirectoryRunner supplies an explicit private process cwd
// only when the command requests its ordinary empty-directory default. Every
// HEAD result is native; this contract never substitutes commit/custody facts.
type publishedPrivateDirectoryRunner struct {
	runner.Runner
	directory string
	requested []string
}

func (r *publishedPrivateDirectoryRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.requested = append(r.requested, dir+"\x00"+name+" "+strings.Join(args, " "))
	if dir == "" {
		dir = r.directory
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}
