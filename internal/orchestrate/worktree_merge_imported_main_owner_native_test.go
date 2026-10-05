package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/runner"
	"strings"
)

type importedMainArchiveRefusal struct {
	runner.Runner
	dir, sha string
	cause    error
	consumed *bool
}

func (r *importedMainArchiveRefusal) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.dir && name == "git" && len(args) == 4 && args[0] == "archive" && args[1] == "--format=tar" && strings.HasPrefix(args[2], "--output=") && args[3] == r.sha {
		*r.consumed = true
		return runner.Result{}, r.cause
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}
