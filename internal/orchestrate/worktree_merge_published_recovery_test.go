package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"strings"
	"testing"
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

func TestPublishedRecoveryDriftErrorsPreserveCauseAndTrimNotes(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("original drift")
	for _, tc := range []struct{ note, want string }{{"", "original drift"}, {" \t ", "original drift"}, {"  prior local failure \n", "original drift (prior local failure)"}} {
		t.Run(tc.want+tc.note, func(t *testing.T) {
			t.Parallel()
			receipt := WorktreeMergeReceipt{LocalSync: tc.note}
			err := worktreeMergeDriftError(&receipt, sentinel)
			if !errors.Is(err, sentinel) || err.Error() != tc.want || receipt.LocalSync != tc.note {
				t.Fatalf("drift identity=%v note=%q", err, receipt.LocalSync)
			}
			if strings.TrimSpace(tc.note) == "" && err != sentinel {
				t.Fatal("empty note replaced error identity")
			}
		})
	}
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
