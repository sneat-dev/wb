package worktrees

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// gitQueryMemo remembers the answers to read-only Git queries for the life of
// one WB command. A single `wb worktree list` over a large fleet was measured
// spawning 8,998 git processes, 51.8% of them exact repeats of a query already
// answered in the same invocation (`rev-parse HEAD` 4.4 times per worktree,
// every `merge-base --is-ancestor` pair four times). At ~10ms per spawn that
// was roughly 49 seconds of pure repetition on a 45-second command.
//
// The memo is opt-in: it exists only where a command installs it with
// withGitQueryMemo, and it only ever caches queries memoizableGitQuery accepts
// -- verbs whose answer cannot legitimately change between two calls inside
// one command. Commands that mutate state never install it, so they keep
// seeing fresh answers.
type gitQueryMemo struct {
	mu      sync.Mutex
	results map[string]string
}

type gitQueryMemoContextKey struct{}

// withGitQueryMemo installs a fresh memo on ctx, or returns ctx unchanged if
// one is already installed so nested callers share their caller's scope.
func withGitQueryMemo(ctx context.Context) context.Context {
	if ctx.Value(gitQueryMemoContextKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, gitQueryMemoContextKey{}, &gitQueryMemo{results: map[string]string{}})
}

func gitQueryMemoFrom(ctx context.Context) *gitQueryMemo {
	memo, _ := ctx.Value(gitQueryMemoContextKey{}).(*gitQueryMemo)
	return memo
}

func gitQueryMemoKey(dir string, args []string) string {
	return dir + "\x00" + strings.Join(args, "\x00")
}

func (m *gitQueryMemo) get(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	output, ok := m.results[key]
	return output, ok
}

func (m *gitQueryMemo) put(key, output string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results[key] = output
}

// memoizableGitQuery reports whether args name a read-only query whose answer
// is stable for the duration of one command. The list is deliberately narrow:
// anything that inspects the working tree (status, diff) or can mutate is left
// out, because a concurrent WB write in the same repository could change it.
func memoizableGitQuery(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "rev-parse", "show", "merge-base":
		return true
	case "branch":
		return len(args) == 2 && args[1] == "--show-current"
	case "worktree":
		return len(args) == 3 && args[1] == "list" && args[2] == "--porcelain"
	}
	return false
}

// validBranchMemo caches `git check-ref-format --branch` verdicts by branch
// name. The verdict is a pure function of the string and Git's ref grammar,
// yet one `wb worktree list` was measured asking 793 times for 19 distinct
// strings, 761 of them the literal "main" -- about 8 seconds, 18% of the
// command, re-validating names already validated in the same process.
var validBranchMemo sync.Map

var (
	validBranchGitOnce sync.Once
	validBranchGitPath string
)

// validBranchGit resolves the git executable once per process. exec.LookPath
// walks PATH and stats each candidate; doing it on every validation was part
// of the measured cost.
func validBranchGit() string {
	validBranchGitOnce.Do(func() {
		gitPath, err := exec.LookPath("git")
		if err != nil {
			return
		}
		if !filepath.IsAbs(gitPath) {
			if gitPath, err = filepath.Abs(gitPath); err != nil {
				return
			}
		}
		validBranchGitPath = gitPath
	})
	return validBranchGitPath
}
