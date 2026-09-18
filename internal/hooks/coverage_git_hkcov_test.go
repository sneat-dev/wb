package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHkCovRepositoryRootResolvesBlankAndNonRepositoryPaths(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	root, err := RepositoryRoot("   ")
	if err != nil {
		t.Fatalf("RepositoryRoot(blank) error = %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("RepositoryRoot(blank) = %q, want an absolute path", root)
	}
	if top := git(t, root, "rev-parse", "--show-toplevel"); filepath.Clean(top) != filepath.Clean(root) {
		t.Fatalf("RepositoryRoot(blank) = %q, but git reports %q", root, top)
	}
	_ = repo

	if _, err := RepositoryRoot(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a Git worktree") {
		t.Fatalf("RepositoryRoot(non-repo) error = %v, want a not-a-worktree error", err)
	}
}

func TestHkCovGitCommonDirRejectsNonRepository(t *testing.T) {
	t.Parallel()
	if _, err := gitCommonDir(t.TempDir()); err == nil {
		t.Fatal("gitCommonDir(non-repo) should fail")
	}
}

func TestHkCovConfiguredHooksPathReturnsEmptyForBlankValue(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	git(t, repo, "config", "--local", "core.hooksPath", "")
	path, err := configuredHooksPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("configuredHooksPath(blank) = %q, want empty", path)
	}
}

func TestHkCovResolveGitPathFallsBackWhenUnresolvable(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := resolveGitPath(missing); got != filepath.Clean(missing) {
		t.Fatalf("resolveGitPath(missing) = %q, want the cleaned path %q", got, filepath.Clean(missing))
	}
	real := t.TempDir()
	if got := resolveGitPath(real); got == "" {
		t.Fatalf("resolveGitPath(existing) = %q, want a path", got)
	}
}

func TestHkCovSetHooksPathAtRequiresDescriptors(t *testing.T) {
	t.Parallel()
	if err := setHooksPathAt(nil, nil, "/tmp/hooks"); err == nil || !strings.Contains(err.Error(), "descriptor is unavailable") {
		t.Fatalf("setHooksPathAt(nil, nil) error = %v", err)
	}
}

func TestHkCovSetHooksPathAtReportsMissingGit(t *testing.T) {
	repo := initRepo(t)
	repoFile, err := os.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repoFile.Close() }()
	common := filepath.Join(repo, ".git")
	commonFile, err := os.Open(common)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = commonFile.Close() }()
	t.Setenv("PATH", "")
	if err := setHooksPathAt(repoFile, commonFile, "/tmp/hooks"); err == nil || !strings.Contains(err.Error(), "locate Git") {
		t.Fatalf("setHooksPathAt without git error = %v", err)
	}
}

// TestHkCovSetHooksPathAtReportsHelperFailure drives the parent side against an
// inherited descriptor that cannot be entered, proving the failure is surfaced
// rather than silently ignored.
func TestHkCovSetHooksPathAtReportsHelperFailure(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	repoFile, err := os.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repoFile.Close() }()
	regular := filepath.Join(t.TempDir(), "not-a-directory")
	mustWrite(t, regular, "plain file\n")
	regularFile, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regularFile.Close() }()
	if err := setHooksPathAt(repoFile, regularFile, "/tmp/hooks"); err == nil || !strings.Contains(err.Error(), "retained repository") {
		t.Fatalf("setHooksPathAt(bad common descriptor) error = %v", err)
	}
}

func TestHkCovSecureHooksGitHelperRejectsWrongArgumentCount(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0], SecureHooksGitHelperArgument, "only-one")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("helper accepted a single argument and exited 0:\n%s", out)
	}
	if code := cmd.ProcessState.ExitCode(); code != 1 {
		t.Fatalf("helper exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(string(out), "expected hooks path and Git executable") {
		t.Fatalf("helper output = %q", out)
	}
}

// TestHkCovSecureHooksGitHelperInheritedDescriptors exercises both inherited
// descriptor failures through a real child process, plus the success path.
func TestHkCovSecureHooksGitHelperInheritedDescriptors(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	gitDir := filepath.Join(repo, ".git")
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitExecutable, err = filepath.Abs(gitExecutable)
	if err != nil {
		t.Fatal(err)
	}

	regular := filepath.Join(t.TempDir(), "not-a-directory")
	mustWrite(t, regular, "plain file\n")

	cases := []struct {
		name       string
		repository string
		common     string
		gitArg     string
		wantCode   int
		wantOutput string
	}{
		{"common descriptor is a regular file", repo, regular, gitExecutable, 1, "enter inherited Git common directory"},
		{"git executable is missing", repo, gitDir, filepath.Join(t.TempDir(), "no-such-git"), 1, "configure core.hooksPath"},
		{"valid descriptors configure hooks path", repo, gitDir, gitExecutable, 0, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, err := os.Open(test.repository)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = repository.Close() }()
			common, err := os.Open(test.common)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = common.Close() }()
			cmd := exec.Command(os.Args[0], SecureHooksGitHelperArgument, "/tmp/hk-cov-hooks-path", test.gitArg)
			cmd.ExtraFiles = []*os.File{repository, common}
			out, runErr := cmd.CombinedOutput()
			if code := cmd.ProcessState.ExitCode(); code != test.wantCode {
				t.Fatalf("helper exit code = %d, want %d\n%s", code, test.wantCode, out)
			}
			if test.wantCode != 0 && runErr == nil {
				t.Fatal("helper reported success but wrote a failure exit code")
			}
			if test.wantOutput != "" && !strings.Contains(string(out), test.wantOutput) {
				t.Fatalf("helper output = %q, want it to mention %q", out, test.wantOutput)
			}
			if test.wantCode == 0 {
				configured := git(t, repo, "config", "--local", "--get", "core.hooksPath")
				if configured != "/tmp/hk-cov-hooks-path" {
					t.Fatalf("core.hooksPath = %q, want %q", configured, "/tmp/hk-cov-hooks-path")
				}
			}
		})
	}
}

func TestHkCovOriginSlugReadsHostedRemotes(t *testing.T) {
	// Neutralise any ambient git config: a developer's own `url.*.insteadOf`
	// rewrite (for instance mapping https://github.com/ onto git@github.com:)
	// would change which branch of originSlug a hosted remote takes.
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	mustWrite(t, globalConfig, "")
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	cases := []struct {
		remote string
		want   string
	}{
		{"https://github.com/acme/widget.git", "acme/widget"},
		{"git@github.com:acme/widget.git", "acme/widget"},
		{"https://gitlab.com/acme/widget.git", "acme/widget"},
		// scp-style remotes other than github.com are recognised as hosted
		// (the host carries a dot and no slash) but keep the host prefix:
		// only github.com: has a dedicated prefix strip.
		{"git@gitlab.com:acme/widget", "git@gitlab.com:acme/widget"},
	}
	for _, test := range cases {
		t.Run(test.remote, func(t *testing.T) {
			t.Parallel()
			repo := initRepo(t)
			git(t, repo, "remote", "add", "origin", test.remote)
			if got := originSlug(repo); got != test.want {
				t.Fatalf("originSlug(%s) = %q, want %q", test.remote, got, test.want)
			}
		})
	}
}

func TestHkCovCanonicalRootFromCheckoutRejectsSymlinkedGitEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	mustMkdirAll(t, target)
	if err := os.Symlink(target, filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if got := canonicalRootFromCheckout(dir); got != "" {
		t.Fatalf("canonicalRootFromCheckout(symlinked .git) = %q, want empty", got)
	}
}

func TestHkCovCanonicalRootFromCheckoutReportsMissingGitEntry(t *testing.T) {
	t.Parallel()
	if got := canonicalRootFromCheckout(t.TempDir()); got != "" {
		t.Fatalf("canonicalRootFromCheckout(no .git) = %q, want empty", got)
	}
}

func TestHkCovCanonicalRootFromCheckoutUnreadableGitfile(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		// Root bypasses the mode bits, so the read cannot be made to fail.
		return
	}
	dir := t.TempDir()
	gitfile := filepath.Join(dir, ".git")
	mustWrite(t, gitfile, "gitdir: /tmp/canonical/.git\n")
	if err := os.Chmod(gitfile, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitfile, 0o644) })
	if got := canonicalRootFromCheckout(dir); got != "" {
		t.Fatalf("canonicalRootFromCheckout(unreadable gitfile) = %q, want empty", got)
	}
}

func TestHkCovCanonicalRootFromGitfileParsesEveryShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		repoRoot string
		contents string
		want     string
	}{
		{"ignores unrelated lines", "/tmp/canonical", "not-a-gitdir\ngitdir: /tmp/canonical/.git\n", "/tmp/canonical"},
		{"ignores an empty gitdir", "/tmp/canonical", "gitdir:   \ngitdir: /tmp/canonical/.git\n", "/tmp/canonical"},
		{"joins a relative gitdir", filepath.FromSlash("/tmp/scratch"), "gitdir: ../.git/worktrees/task\n", filepath.FromSlash("/tmp")},
		{"accepts a bare .git directory", "/tmp/canonical", "gitdir: /tmp/canonical/.git\n", "/tmp/canonical"},
		{"rejects unrelated layouts", "/tmp/canonical", "gitdir: /tmp/somewhere/else\n", ""},
		{"rejects content with no gitdir", "/tmp/canonical", "hello\n", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := canonicalRootFromGitfile(test.repoRoot, test.contents); got != test.want {
				t.Fatalf("canonicalRootFromGitfile(%q) = %q, want %q", test.contents, got, test.want)
			}
		})
	}
}

func TestHkCovCheckoutSlugAtFilesystemRoot(t *testing.T) {
	t.Parallel()
	if got := checkoutSlug(string(filepath.Separator)); got != string(filepath.Separator) {
		t.Fatalf("checkoutSlug(/) = %q, want %q", got, string(filepath.Separator))
	}
	if got := checkoutSlug(filepath.Join("/srv", "wb", "acme", "widget")); got != "acme/widget" {
		t.Fatalf("checkoutSlug = %q, want acme/widget", got)
	}
}
