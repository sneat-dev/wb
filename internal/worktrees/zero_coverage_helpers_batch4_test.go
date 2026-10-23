package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

//nolint:paralleltest // installPullRequestResponses changes process-wide command discovery.
func TestGitHubPullRequestsReturnsCommitReceipts(t *testing.T) {
	const head = "0123456789abcdef0123456789abcdef01234567"
	installPullRequestResponses(t, `[{"number":17,"html_url":"https://github.com/acme/app/pull/17","state":"closed"}]`, head)

	pullRequests, err := githubPullRequests(context.Background(), t.TempDir(), "acme/app", head)
	if err != nil {
		t.Fatal(err)
	}
	if len(pullRequests) != 1 || pullRequests[0].Number != 17 || pullRequests[0].URL != "https://github.com/acme/app/pull/17" {
		t.Fatalf("pull requests = %#v", pullRequests)
	}
}

func TestFetchExactRemotePullRequestHeadUsesInjectedGitRunner(t *testing.T) {
	t.Parallel()
	const repository = "/fixture/repository"
	const head = "0123456789abcdef0123456789abcdef01234567"
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "-C", repository, "ls-remote", "--exit-code", "origin", "refs/pull/17/head"},
		runner.Result{CombinedOutput: head + "\trefs/pull/17/head\n"}, nil)
	fake.ExpectArgv([]string{"git", "-C", repository, "fetch", "--no-tags", "--no-write-fetch-head", "--", "origin", "refs/pull/17/head"},
		runner.Result{}, nil)
	fake.ExpectArgv([]string{"git", "-C", repository, "rev-parse", "--verify", "--end-of-options", head + "^{commit}"},
		runner.Result{CombinedOutput: head + "\n"}, nil)

	got, err := landingReceiptService().FetchExactRemotePullRequestHead(withGitRunner(context.Background(), fake), repository, 17, head)
	if err != nil || got != head {
		t.Fatalf("fetched head = (%q, %v), want (%q, nil)", got, err, head)
	}
}

func TestDependencyLockfileSelectsNearestAncestor(t *testing.T) {
	t.Parallel()
	const repository = "/fixture/repository"
	const target = "0123456789abcdef0123456789abcdef01234567"
	contents := "pnpm-lock.yaml\napps/package-lock.json\napps/web/yarn.lock\ngo.sum\n"
	fake := runnertest.New(t)
	for range 4 {
		fake.ExpectArgv([]string{"git", "-C", repository, "ls-tree", "-r", "--name-only", target},
			runner.Result{CombinedOutput: contents}, nil)
	}
	ctx := withGitRunner(context.Background(), fake)

	lockfile, ok, err := supersessionService().DependencyLockfile(ctx, repository, target, SupersessionDependencyDelta{Ecosystem: " npm ", Manifest: "apps/web/package.json"})
	if err != nil || !ok || lockfile != "apps/web/yarn.lock" {
		t.Fatalf("nearest npm lockfile = (%q, %t, %v)", lockfile, ok, err)
	}
	lockfile, ok, err = supersessionService().DependencyLockfile(ctx, repository, target, SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "tools/package.json"})
	if err != nil || !ok || lockfile != "pnpm-lock.yaml" {
		t.Fatalf("root npm lockfile = (%q, %t, %v)", lockfile, ok, err)
	}
	lockfile, ok, err = supersessionService().DependencyLockfile(ctx, repository, target, SupersessionDependencyDelta{Ecosystem: "go", Manifest: "go.mod"})
	if err != nil || !ok || lockfile != "go.sum" {
		t.Fatalf("Go lockfile = (%q, %t, %v)", lockfile, ok, err)
	}
	lockfile, ok, err = supersessionService().DependencyLockfile(ctx, repository, target, SupersessionDependencyDelta{Ecosystem: "cargo", Manifest: "Cargo.toml"})
	if err != nil || ok || lockfile != "" {
		t.Fatalf("unsupported lockfile = (%q, %t, %v)", lockfile, ok, err)
	}
}

func TestDependencyLockfileReportsGitFailure(t *testing.T) {
	t.Parallel()
	const repository = "/fixture/repository"
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "-C", repository, "ls-tree", "-r", "--name-only", "target"},
		runner.Result{}, errors.New("injected tree failure"))

	lockfile, ok, err := supersessionService().DependencyLockfile(withGitRunner(context.Background(), fake), repository, "target", SupersessionDependencyDelta{Ecosystem: "go", Manifest: "go.mod"})
	if err == nil || !strings.Contains(err.Error(), "injected tree failure") || ok || lockfile != "" {
		t.Fatalf("failed lockfile lookup = (%q, %t, %v)", lockfile, ok, err)
	}
}

func TestLifecycleArtifactInspectionParsesOriginSlug(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		remote string
		want   string
		runErr error
	}{
		{name: "scp", remote: "git@github.com:sneat-dev/wb.git", want: "sneat-dev/wb"},
		{name: "HTTPS", remote: "https://github.com/sneat-dev/wb/", want: "sneat-dev/wb"},
		{name: "generic host", remote: "ssh://git@example.test/team/repository.git", want: "team/repository"},
		{name: "Git failure", runErr: errors.New("injected remote failure")},
		{name: "missing owner", remote: "repository"},
		{name: "invalid slug", remote: "https://github.com/owner/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, ".wb-stage-fixture")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "blocker"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("read stage entry = (%#v, %v)", entries, err)
			}
			fake := runnertest.New(t)
			fake.ExpectArgv([]string{"git", "-C", path, "remote", "get-url", "origin"},
				runner.Result{CombinedOutput: tc.remote + "\n"}, tc.runErr)
			artifact, ok := inspectLifecycleArtifact(withGitRunner(context.Background(), fake), root, "task", path, entries[0])
			if !ok || artifact.Repository != tc.want || !strings.Contains(artifact.Reason, "non-empty") {
				t.Fatalf("inspected artifact = (%#v, %t), want repository %q", artifact, ok, tc.want)
			}
		})
	}
}

func TestCountLocalOutboxCountsRecordsAndReportsOversizedLines(t *testing.T) {
	t.Parallel()
	worktree := newJournalWorktree(t)
	if count, err := countLocalOutbox(worktree); err != nil || count != 0 {
		t.Fatalf("missing outbox = (%d, %v)", count, err)
	}
	directory, err := openLocalWorkLogDir(worktree, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomicAt(directory, localWorkLogOutboxName, []byte("one\n\n two \n"), 0o600); err != nil {
		_ = directory.Close()
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if count, err := countLocalOutbox(worktree); err != nil || count != 2 {
		t.Fatalf("outbox count = (%d, %v), want (2, nil)", count, err)
	}

	directory, err = openLocalWorkLogDir(worktree, false)
	if err != nil {
		t.Fatal(err)
	}
	oversized := []byte(strings.Repeat("x", 70*1024))
	if err := writeBytesAtomicAt(directory, localWorkLogOutboxName, oversized, 0o600); err != nil {
		_ = directory.Close()
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if count, err := countLocalOutbox(worktree); err == nil || count != 0 {
		t.Fatalf("oversized outbox = (%d, %v)", count, err)
	}
}
