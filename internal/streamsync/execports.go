package streamsync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/streams"
)

const defaultCommandTimeout = 30 * time.Minute

// ExecGit runs real Git.
type ExecGit struct{ Timeout time.Duration }

func (git ExecGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	return runBounded(ctx, git.Timeout, dir, nil, "git", args...)
}

// Fetch implements Git.
func (git ExecGit) Fetch(ctx context.Context, dir string) error {
	_, err := git.run(ctx, dir, "fetch", "--quiet", "--prune", "origin")
	return err
}

// CurrentBranch implements Git.
func (git ExecGit) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := git.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	return strings.TrimSpace(out), err
}

// Rebase implements Git, returning conflicting paths rather than leaving the
// worktree mid-rebase. A conflict in one agent's branch must not abort the
// others, so the caller needs it as data and the tree back in a usable state.
func (git ExecGit) Rebase(ctx context.Context, dir, branch, upstream string) ([]string, error) {
	if _, err := git.run(ctx, dir, "checkout", branch); err != nil {
		return nil, fmt.Errorf("check out %s: %w", branch, err)
	}
	if _, err := git.run(ctx, dir, "rebase", upstream); err == nil {
		return nil, nil
	}
	conflicts, listErr := git.run(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if listErr != nil {
		return nil, listErr
	}
	var paths []string
	for _, line := range strings.Split(conflicts, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	if len(paths) == 0 {
		// The rebase failed for a reason other than a content conflict; the
		// caller must not read an empty conflict list as success.
		return nil, fmt.Errorf("rebase %s onto %s failed without reporting a conflicting path", branch, upstream)
	}
	return paths, nil
}

// AbortRebase implements Git.
func (git ExecGit) AbortRebase(ctx context.Context, dir string) error {
	_, err := git.run(ctx, dir, "rebase", "--abort")
	return err
}

// Head implements Git.
func (git ExecGit) Head(ctx context.Context, dir, revision string) (string, error) {
	out, err := git.run(ctx, dir, "rev-parse", revision)
	return strings.TrimSpace(out), err
}

// CommitsAhead implements Git. A branch with no remote counterpart is entirely
// unpushed, which is the normal state under this model rather than an error.
func (git ExecGit) CommitsAhead(ctx context.Context, dir, branch, upstream string) (int, error) {
	if _, err := git.run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/"+strings.TrimPrefix(upstream, "refs/remotes/")); err != nil {
		out, countErr := git.run(ctx, dir, "rev-list", "--count", branch)
		if countErr != nil {
			return 0, nil
		}
		return strconv.Atoi(strings.TrimSpace(out))
	}
	out, err := git.run(ctx, dir, "rev-list", "--count", upstream+".."+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// CommitAll implements Git. ok=false when there was nothing to commit, which
// is what keeps a re-run of sync from writing an empty commit.
func (git ExecGit) CommitAll(ctx context.Context, dir, message string) (string, bool, error) {
	if _, err := git.run(ctx, dir, "add", "-A"); err != nil {
		return "", false, err
	}
	staged, err := git.run(ctx, dir, "diff", "--cached", "--name-only")
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(staged) == "" {
		return "", false, nil
	}
	if _, err := git.run(ctx, dir, "commit", "-m", message); err != nil {
		return "", false, err
	}
	sha, err := git.Head(ctx, dir, "HEAD")
	return sha, true, err
}

// CreateBranch implements Git.
func (git ExecGit) CreateBranch(ctx context.Context, dir, branch, revision string) error {
	_, err := git.run(ctx, dir, "branch", "--force", branch, revision)
	return err
}

// Checkout implements Git.
func (git ExecGit) Checkout(ctx context.Context, dir, branch string) error {
	_, err := git.run(ctx, dir, "checkout", branch)
	return err
}

// ResetHard implements Git.
func (git ExecGit) ResetHard(ctx context.Context, dir, revision string) error {
	_, err := git.run(ctx, dir, "reset", "--hard", revision)
	return err
}

// CherryPick implements Git.
func (git ExecGit) CherryPick(ctx context.Context, dir, sha string) error {
	if _, err := git.run(ctx, dir, "cherry-pick", sha); err != nil {
		_, _ = git.run(ctx, dir, "cherry-pick", "--abort")
		return err
	}
	return nil
}

// DeleteBranch implements Git.
func (git ExecGit) DeleteBranch(ctx context.Context, dir, branch string) error {
	_, err := git.run(ctx, dir, "branch", "-D", branch)
	return err
}

// RestoreTo implements Git.
func (git ExecGit) RestoreTo(ctx context.Context, dir, revision string) error {
	if _, err := git.run(ctx, dir, "reset", "--hard", revision); err != nil {
		return err
	}
	// A failed bump can leave untracked lockfile fragments; a reset alone
	// would leave them for the next run to trip over.
	_, err := git.run(ctx, dir, "clean", "-fd")
	return err
}

// PushWithLease implements Git.
//
// `--force-with-lease` against the head WB recorded is required because a
// rebase of a shared branch is a force-push, and a bare force discards whatever
// another agent pushed in between. The pushed ref is then re-read: a push exit
// code is not evidence the intended commit landed.
func (git ExecGit) PushWithLease(ctx context.Context, dir, branch, expectedRemoteHead string) (string, error) {
	local, err := git.Head(ctx, dir, branch)
	if err != nil {
		return "", err
	}
	args := []string{"push", "--set-upstream"}
	if strings.TrimSpace(expectedRemoteHead) != "" {
		args = append(args, "--force-with-lease="+branch+":"+expectedRemoteHead)
	}
	args = append(args, "origin", branch)
	if _, err := git.run(ctx, dir, args...); err != nil {
		return "", fmt.Errorf("push %s: %w", branch, err)
	}
	if _, err := git.run(ctx, dir, "fetch", "--quiet", "origin", branch); err != nil {
		return "", fmt.Errorf("re-read origin/%s after pushing: %w", branch, err)
	}
	remote, err := git.Head(ctx, dir, "refs/remotes/origin/"+branch)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remote) != strings.TrimSpace(local) {
		return "", fmt.Errorf("pushed %s at %s but origin/%s is %s; the push did not land the intended commit",
			branch, local, branch, remote)
	}
	return strings.TrimSpace(local), nil
}

// IsClean implements Git.
func (git ExecGit) IsClean(ctx context.Context, dir string) (bool, error) {
	out, err := git.run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// ExecBumper applies a library version inside the stream worktree.
//
// It needs a checkout because the toolchain has to refresh `go.sum` /
// `pnpm-lock.yaml`; a manifest edit alone would leave the lockfile describing
// the old version.
type ExecBumper struct{ Timeout time.Duration }

// Required implements Bumper by reading the consumer's own manifests — the
// same canonical dependency sections graph discovery uses.
func (bumper ExecBumper) Required(ctx context.Context, dir string, library Library) (string, bool, error) {
	identity := streams.Identity{Name: library.Name, Ecosystem: streams.Ecosystem(library.Ecosystem)}
	declarations, err := streams.DiscoverDeclarations(dir, []streams.Identity{identity})
	if err != nil {
		return "", false, err
	}
	if len(declarations) == 0 {
		return "", false, nil
	}
	// The lowest declared version decides: if any manifest is still below
	// target the consumer as a whole is.
	lowest := declarations[0].Version
	for _, declaration := range declarations[1:] {
		if below(declaration.Version, lowest) {
			lowest = declaration.Version
		}
	}
	return lowest, true, nil
}

// Apply implements Bumper. It never commits — the engine owns that, so a bump
// that changes nothing leaves no commit behind.
//
// `GOWORK=off` is set for the Go path: `go get` and `go mod tidy` resolve
// against a workspace, so under a live local link they would write a `go.sum`
// describing an unpublished library tree.
func (bumper ExecBumper) Apply(ctx context.Context, dir string, library Library) error {
	switch streams.Ecosystem(library.Ecosystem) {
	case streams.EcosystemGo:
		modules, err := streams.GoModules(dir)
		if err != nil {
			return err
		}
		for _, module := range modules {
			moduleDir := filepath.Join(dir, filepath.FromSlash(module.Directory))
			env := []string{"GOWORK=off"}
			if _, err := runBounded(ctx, bumper.Timeout, moduleDir, env, "go", "get", library.Name+"@"+library.Target); err != nil {
				return err
			}
			if _, err := runBounded(ctx, bumper.Timeout, moduleDir, env, "go", "mod", "tidy"); err != nil {
				return err
			}
		}
		return nil
	case streams.EcosystemNpm:
		manager := "npm"
		if fileExists(filepath.Join(dir, "pnpm-lock.yaml")) {
			manager = "pnpm"
		}
		if _, err := runBounded(ctx, bumper.Timeout, dir, nil, manager, "install", "--lockfile-only"); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("unsupported ecosystem %q for %s", library.Ecosystem, library.Name)
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func runBounded(ctx context.Context, timeout time.Duration, dir string, env []string, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(bounded, name, args...)
	command.Dir = dir
	command.Env = append(console.Env(), env...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := streams.RedactString(strings.TrimSpace(string(output)))
		if bounded.Err() != nil && ctx.Err() == nil {
			return detail, fmt.Errorf("%s %s timed out after %s: %s", name, strings.Join(args, " "), timeout, detail)
		}
		return detail, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
	}
	return string(output), nil
}
