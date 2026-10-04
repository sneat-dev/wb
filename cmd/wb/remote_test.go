package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/spf13/cobra"
)

func remoteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testenv.GitAutoMaintenanceOffEnv(append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// setGitIdentity gives Publish's commits (made through the process env, not
// remoteGit's explicit env) a valid author/committer so tests work under a
// HOME with no git config.
//
// It also disables every directly-started git subprocess's opportunistic
// background maintenance, test-only, for
// TestRemoteClaimForceOnUnreadableFile's "TempDir RemoveAll cleanup:
// unlinkat .../origin.git/objects: directory not empty" failure, via the
// shared testenv.SetGitAutoMaintenanceOff -- see its doc comment for why
// this matters and internal/testenv/testenv.go's gitAutoMaintenanceKeys
// comment for the trace evidence. Unlike a hard-coded GIT_CONFIG_COUNT="3",
// SetGitAutoMaintenanceOff extends whatever GIT_CONFIG_COUNT sequence the
// process already carries (for example one this whole test binary's
// TestMain sets via GitAutoMaintenanceOffProcess) instead of overwriting it.
//
// A bare repository pushed to over a local transport still needs its own
// testenv.ConfigureGitAutoMaintenanceOff call: git strips every
// GIT_CONFIG_* variable from the environment it hands the server-side
// `receive-pack` it spawns for a same-host push, so this env config alone
// never reaches it.
func setGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	testenv.SetGitAutoMaintenanceOff(t)
}

// remoteFixture builds a projects root holding one dirty fleet repo, a bare
// state-repo origin, and a wb.yaml pointing at it.
type remoteFixture struct {
	projectsRoot, origin, configPath string
}

func newRemoteFixture(t *testing.T, machine string) remoteFixture {
	t.Helper()
	setGitIdentity(t)
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	repo := filepath.Join(projectsRoot, "acme", "widgets")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, repo, "init", "-q", "-b", "main")
	remoteGit(t, repo, "commit", "-q", "--allow-empty", "-m", "seed")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(base, "origin.git")
	remoteGit(t, base, "init", "-q", "--bare", "-b", "main", origin)
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)
	seed := filepath.Join(base, "seed")
	remoteGit(t, base, "clone", "-q", origin, seed)
	remoteGit(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	remoteGit(t, seed, "push", "-q", "origin", "main")
	configPath := filepath.Join(base, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: "+machine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_PROJECTS_ROOT", filepath.Join(base, "wbhome"))
	return remoteFixture{projectsRoot: projectsRoot, origin: origin, configPath: configPath}
}

func (f remoteFixture) deps(login string, at time.Time) remoteDeps {
	return remoteDeps{
		configPath: f.configPath,
		login:      func() (string, error) { return login, nil },
		open: func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
			return gitrepo.New(gitrepo.Options{ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()), CloneURL: f.origin}), nil
		},
		now: func() time.Time { return at },
	}
}

// TestRemotePublishIncludesOrphanedWorktrees is the regression test for the
// production bug where `wb remote publish` reported 0 worktrees on fleets
// holding hundreds of them: collectSnapshot called worktrees.List with
// OwnerState: "active", silently dropping every worktree whose owning
// session had already exited. For a fleet-audit snapshot those abandoned
// worktrees are exactly what matters, so publish must include them along
// with their owner state.
//
// This worktree is created with a raw `git worktree add` (the same
// technique cmd/wb/worktree_test.go's setUpMismatchedWorktreeFixture uses),
// not through worktrees.Create, so no owner-claim metadata is ever written
// for it — since #154, "no owner records at all" reports as "unknown"
// (orphaned is reserved for a registered owner whose PID is gone).
// That is the cheapest fixture that honestly exercises the real
// worktrees.List/OwnerState filtering path end to end through the CLI,
// rather than mocking the seam.

func TestRemotePublishAcceptsProjectsRootAndFilterFlags(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"projects-root", "filter"} {
		if !persistentFlagSupport[flag]["remote publish"] {
			t.Errorf("remote publish must accept --%s: it scans the fleet under --projects-root honouring --filter", flag)
		}
	}
	if persistentFlagSupport["org"]["remote publish"] {
		t.Error("remote publish must reject --org; it publishes local state, not GitHub-listed repos")
	}
}

func TestPersistentFlagMatrixRemoteCommands(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"remote publish", "remote status", "remote machines"} {
		if !persistentFlagSupport["projects-root"][cmd] {
			t.Errorf("%s must accept --projects-root: it locates the state-repo clone", cmd)
		}
	}
	for cmd, want := range map[string]bool{"remote publish": true, "remote status": false, "remote machines": false} {
		if persistentFlagSupport["filter"][cmd] != want {
			t.Errorf("persistentFlagSupport[filter][%q] = %v, want %v", cmd, persistentFlagSupport["filter"][cmd], want)
		}
	}
}

// TestRemoteMachinesTableHasPublishedAtColumn proves the human-readable
// table carries the exact RFC3339 UTC publish timestamp, not just the
// coarse relative age: an operator diffing snapshots across machines needs
// the real instant, and "9h" alone cannot be compared across two rows
// published on different days.

// TestRemoteStatusMachineFilterNoMatchWritesToStderr proves an unmatched
// --machine reports on stderr (a typo'd key must not look like "everyone is
// clean") without failing the command: the store itself is fine, only the
// filter matched nothing.

// TestMachineRowsSeenReflectsLastSeen proves SEEN and STALE key off the
// effective heartbeat (max of published_at and last_seen_at), while
// PUBLISHED keeps showing the raw publish-data age: a machine that
// published a day ago but claimed a task an hour ago is live (fresh claim
// activity), and an operator diffing PUBLISHED vs SEEN must be able to see
// that the two diverged.

// TestMachineRowsZeroPublishedButLastSeenIsNotError proves the spec's
// distinction precisely: a snapshot with a zero PublishedAt is only an
// error row when LastSeenAt is ALSO zero. A non-zero LastSeenAt alone is
// enough of a liveness signal to render normally.

func TestSyncPublishFlagIsRegistered(t *testing.T) {
	t.Parallel()
	cmd := newSyncCmd(&invocation{})
	flag := cmd.Flags().Lookup("publish")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("--publish flag = %+v, want bool default false", flag)
	}
}

func TestFinishSyncPublishFailureIsReportedButExitStaysZero(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	deps := f.deps("alice", time.Now().UTC())
	deps.open = func(remotestate.Config, string) (remotestate.Provider, error) {
		return nil, errors.New("store unreachable")
	}
	var out, errOut bytes.Buffer
	if code := finishSync(&invocation{}, fleetsync.RunMeta{}, nil, true, false, deps, f.projectsRoot, "", 1, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(errOut.String(), "remote publish failed (sync itself succeeded): store unreachable") {
		t.Fatalf("stderr = %q, want the publish failure reported", errOut.String())
	}
}

func TestFinishSyncPublishesAfterCleanSync(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out, errOut bytes.Buffer
	if code := finishSync(&invocation{}, fleetsync.RunMeta{}, nil, true, false, f.deps("alice", time.Now().UTC()), f.projectsRoot, "", 1, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "published alice/laptop") {
		t.Fatalf("stdout = %q, want publish confirmation", out.String())
	}
}

func TestFinishSyncSkipsPublishWhenSyncFailed(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	configPath := filepath.Join(projectsRoot, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := remoteDeps{configPath: configPath, open: func(remotestate.Config, string) (remotestate.Provider, error) {
		return nil, errors.New("must not be called")
	}}
	var out, errOut bytes.Buffer
	failed := []fleetsync.Result{{Status: fleetsync.Failed}}
	if code := finishSync(&invocation{}, fleetsync.RunMeta{}, failed, true, false, deps, projectsRoot, "", 1, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if strings.Contains(out.String(), "published") || strings.Contains(errOut.String(), "publish") {
		t.Fatalf("publish must not run after a failed sync: out=%q err=%q", out.String(), errOut.String())
	}
}

// TestFinishSyncDryRunSkipsPublish proves `wb sync --dry-run --publish` never
// calls the remote publish path at all — deps.open would fail loudly if it
// were invoked, so reaching a clean exit with the "skipping" message and no
// stderr output proves the guard runs before anything provider-shaped.
func TestFinishSyncDryRunSkipsPublish(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	configPath := filepath.Join(projectsRoot, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := remoteDeps{configPath: configPath, open: func(remotestate.Config, string) (remotestate.Provider, error) {
		return nil, errors.New("must not be called")
	}}
	var out, errOut bytes.Buffer
	if code := finishSync(&invocation{}, fleetsync.RunMeta{}, nil, true, true, deps, projectsRoot, "", 1, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "skipping remote publish") {
		t.Fatalf("stdout = %q, want dry-run skip message", out.String())
	}
	if errOut.String() != "" {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

// TestRemotePublishCommandDispatchesToRunRemotePublishWithProgress proves that
// "wb remote publish" wires its production defaultRemoteDeps() and inv through
// to runRemotePublishWithProgress via the real command tree, not just through
// direct unit calls to that function: with no wb.yaml configured under an
// isolated XDG_CONFIG_HOME, it surfaces the same named usage error that
// TestRemotePublishUnconfiguredIsUsageError proves for the direct call.
func TestRemotePublishCommandDispatchesToRunRemotePublishWithProgress(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := cwCovExec(t, root, func() *cobra.Command { return remoteCommandForTest(&invocation{}, "publish") }, "--dry-run")
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("err = %v, want the same usage error as an unconfigured direct runRemotePublish call", err)
	}
}

var _ = context.Background
