package discover

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// lgCovGhCall is one canned `gh` invocation: the exact argument string the
// fixture should answer, the process output, and the exit status. Any
// invocation that does not match a call fails loudly so a test can never pass
// by silently reaching an unexpected gh command.
type lgCovGhCall struct {
	args   string
	stdout string
	stderr string
	exit   int
}

// lgCovShellQuote wraps value in single quotes for a POSIX shell, escaping any
// embedded single quote.
func lgCovShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// lgCovInstallFakeGh writes a fake `gh` executable at the front of PATH and
// points the GitHub observer cache at a private directory. CODE under test
// reaches it through exec.CommandContext, so PATH is the only seam.
func lgCovInstallFakeGh(t *testing.T, calls ...lgCovGhCall) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh fixture is a POSIX shell script")
	}
	testenv.Isolate(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	binDir := t.TempDir()
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	for _, call := range calls {
		script.WriteString("if [ \"$*\" = " + lgCovShellQuote(call.args) + " ]; then\n")
		script.WriteString("\tprintf '%s' " + lgCovShellQuote(call.stdout) + "\n")
		if call.stderr != "" {
			script.WriteString("\tprintf '%s' " + lgCovShellQuote(call.stderr) + " >&2\n")
		}
		script.WriteString("\texit " + strconv.Itoa(call.exit) + "\n")
		script.WriteString("fi\n")
	}
	script.WriteString("printf 'unexpected gh invocation: %s\\n' \"$*\" >&2\nexit 3\n")
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// lgCovInstallNoGh provides a PATH with no gh on it at all, so the observer's
// exec lookup itself fails.
func lgCovInstallNoGh(t *testing.T) {
	t.Helper()
	testenv.Isolate(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
}

// lgCovRunGit runs git in dir and fails the test on any error.
func lgCovRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

// lgCovGitRepoWithOrigin creates a real git repository whose origin remote is
// origin (empty leaves the repository without an origin). Global and system git
// configuration are neutralized so ambient url.<base>.insteadOf rewriting
// cannot change what `git remote get-url origin` reports.
func lgCovGitRepoWithOrigin(t *testing.T, origin, org, name string) Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required to build the fixture: %v", err)
	}
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_SYSTEM", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repositoryPath := filepath.Join(t.TempDir(), "clone")
	if err := os.MkdirAll(repositoryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	lgCovRunGit(t, repositoryPath, "init", "--quiet")
	if origin != "" {
		lgCovRunGit(t, repositoryPath, "remote", "add", "origin", origin)
	}
	return Repo{Org: org, Name: name, Path: repositoryPath}
}

func TestResolveCanonicalRepositoryReturnsGitHubsCurrentIdentity(t *testing.T) {
	lgCovInstallFakeGh(t, lgCovGhCall{
		args:   "api repos/oldco/app --include",
		stdout: "HTTP/2 200 OK\n\n" + `{"full_name":"newco/renamed","default_branch":"trunk"}`,
	})
	canonical, err := ResolveCanonicalRepository(context.Background(), lgCovGitRepoWithOrigin(t, "https://github.com/oldco/app", "oldco", "app"))
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Slug != "newco/renamed" || canonical.CloneURL != "https://github.com/newco/renamed" || canonical.DefaultBranch != "trunk" {
		t.Fatalf("canonical repository = %#v", canonical)
	}
}

func TestResolveCanonicalRepositoryRejectsNonMatchingOrigins(t *testing.T) {
	lgCovInstallFakeGh(t)
	for name, origin := range map[string]string{
		"other host":       "git@gitlab.com:oldco/app.git",
		"other repository": "https://github.com/other/app",
		"unparseable url":  "https://github.com",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveCanonicalRepository(context.Background(), lgCovGitRepoWithOrigin(t, origin, "oldco", "app"))
			if err == nil || !strings.Contains(err.Error(), "origin does not identify github.com/oldco/app") {
				t.Fatalf("ResolveCanonicalRepository(%s) = %v", origin, err)
			}
		})
	}
}

func TestResolveCanonicalRepositoryReportsMissingOrigin(t *testing.T) {
	lgCovInstallFakeGh(t)
	if _, err := ResolveCanonicalRepository(context.Background(), lgCovGitRepoWithOrigin(t, "", "oldco", "app")); err == nil {
		t.Fatal("ResolveCanonicalRepository() without an origin remote returned no error")
	}
}

func TestResolveCanonicalRepositoryReportsGitHubFailures(t *testing.T) {
	const apiCall = "api repos/oldco/app --include"
	t.Run("gh failure", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: apiCall, stderr: "gh: not logged into any GitHub hosts", exit: 1})
		repo := lgCovGitRepoWithOrigin(t, "https://github.com/oldco/app", "oldco", "app")
		if _, err := ResolveCanonicalRepository(context.Background(), repo); err == nil {
			t.Fatal("ResolveCanonicalRepository() with a failing gh returned no error")
		}
	})
	t.Run("malformed payload", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: apiCall, stdout: "HTTP/2 200 OK\n\nnot json"})
		repo := lgCovGitRepoWithOrigin(t, "https://github.com/oldco/app", "oldco", "app")
		if _, err := ResolveCanonicalRepository(context.Background(), repo); err == nil {
			t.Fatal("ResolveCanonicalRepository() with a non-JSON payload returned no error")
		}
	})
	t.Run("missing default branch", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: apiCall, stdout: "HTTP/2 200 OK\n\n" + `{"full_name":"newco/renamed","default_branch":""}`})
		repo := lgCovGitRepoWithOrigin(t, "https://github.com/oldco/app", "oldco", "app")
		_, err := ResolveCanonicalRepository(context.Background(), repo)
		if err == nil || !strings.Contains(err.Error(), "invalid canonical repository identity") {
			t.Fatalf("ResolveCanonicalRepository() with no default branch = %v", err)
		}
	})
	t.Run("missing full name", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: apiCall, stdout: "HTTP/2 200 OK\n\n" + `{"full_name":"","default_branch":"main"}`})
		repo := lgCovGitRepoWithOrigin(t, "https://github.com/oldco/app", "oldco", "app")
		_, err := ResolveCanonicalRepository(context.Background(), repo)
		if err == nil || !strings.Contains(err.Error(), "invalid canonical repository identity") {
			t.Fatalf("ResolveCanonicalRepository() with no full name = %v", err)
		}
	})
}

func TestReconcileTransfersKeepsUnresolvableAndUnmovedLocalRepos(t *testing.T) {
	t.Parallel()
	repos := Reconcile(
		[]Repo{
			{Org: "acme", Name: "broken", Path: "/p/acme/broken"},
			{Org: "acme", Name: "same", Path: "/p/acme/same"},
		},
		[]Repo{{Org: "acme", Name: "same", CloneURL: "git@github.com:acme/same.git"}},
	)
	got := ReconcileTransfers(context.Background(), repos, func(_ context.Context, repo Repo) (CanonicalRepository, error) {
		if repo.Slug() == "acme/broken" {
			return CanonicalRepository{}, errors.New("gh unavailable")
		}
		return CanonicalRepository{Slug: repo.Slug()}, nil
	})
	if len(got) != 2 {
		t.Fatalf("reconciled %d repos, want the 2 local repositories unchanged: %#v", len(got), got)
	}
	by := map[string]Repo{}
	for _, repo := range got {
		by[repo.Slug()] = repo
	}
	broken := by["acme/broken"]
	if !broken.Local || broken.Remote || broken.TransferFrom != "" || broken.Path != "/p/acme/broken" {
		t.Fatalf("unresolvable local repo = %#v", broken)
	}
	same := by["acme/same"]
	if !same.Local || !same.Remote || same.TransferFrom != "" || same.CloneURL != "git@github.com:acme/same.git" {
		t.Fatalf("unmoved local repo = %#v", same)
	}
}

func TestReconcileTransfersFlagsAmbiguousTransfers(t *testing.T) {
	t.Parallel()
	repos := Reconcile(
		[]Repo{
			{Org: "oldco", Name: "app", Path: "/p/oldco/app"},
			{Org: "oldco", Name: "app-mirror", Path: "/p/oldco/app-mirror"},
		},
		[]Repo{{Org: "newco", Name: "renamed", CloneURL: "git@github.com:newco/renamed.git"}},
	)
	got := ReconcileTransfers(context.Background(), repos, func(context.Context, Repo) (CanonicalRepository, error) {
		return CanonicalRepository{Slug: "newco/renamed", CloneURL: "git@github.com:newco/renamed.git", DefaultBranch: "main"}, nil
	})
	if len(got) != 2 {
		t.Fatalf("ambiguous transfer produced %d repos, want one per local candidate: %#v", len(got), got)
	}
	paths := map[string]string{}
	for _, repo := range got {
		if repo.Slug() != "newco/renamed" {
			t.Fatalf("ambiguous transfer repo slug = %q", repo.Slug())
		}
		if !strings.Contains(repo.TransferError, "ambiguous transfer: 2 local repositories resolve to newco/renamed") {
			t.Fatalf("TransferError = %q", repo.TransferError)
		}
		if repo.TransferFrom == "" || !repo.Local || !repo.Remote || repo.DefaultBranch != "main" {
			t.Fatalf("ambiguous transfer repo = %#v", repo)
		}
		paths[repo.TransferFrom] = repo.Path
	}
	if paths["oldco/app"] != "/p/oldco/app" || paths["oldco/app-mirror"] != "/p/oldco/app-mirror" {
		t.Fatalf("ambiguous transfer kept the wrong paths: %#v", paths)
	}
}

func TestListRemoteMapsRepositoryFields(t *testing.T) {
	lgCovInstallFakeGh(t, lgCovGhCall{
		args: "repo list acme --limit 1000 --json name,isArchived,isFork,sshUrl",
		stdout: `[{"name":"widgets","isArchived":false,"isFork":false,"sshUrl":"git@github.com:acme/widgets.git"},` +
			`{"name":"legacy","isArchived":true,"isFork":true,"sshUrl":"git@github.com:acme/legacy.git"}]`,
	})
	repositories, err := ListRemote("acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 {
		t.Fatalf("ListRemote() = %#v, want 2 repositories", repositories)
	}
	if repositories[0].Slug() != "acme/widgets" || repositories[0].Archived || repositories[0].IsFork ||
		repositories[0].CloneURL != "git@github.com:acme/widgets.git" || repositories[0].Local || repositories[0].Remote {
		t.Fatalf("active repository = %#v", repositories[0])
	}
	if repositories[1].Slug() != "acme/legacy" || !repositories[1].Archived || !repositories[1].IsFork {
		t.Fatalf("archived fork = %#v", repositories[1])
	}
}

func TestListRemoteReturnsNoRepositoriesForAnEmptyOwner(t *testing.T) {
	lgCovInstallFakeGh(t, lgCovGhCall{
		args:   "repo list empty --limit 1000 --json name,isArchived,isFork,sshUrl",
		stdout: `[]`,
	})
	repositories, err := ListRemote("empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 0 {
		t.Fatalf("ListRemote() = %#v, want none", repositories)
	}
}

func TestListRemoteReportsFailures(t *testing.T) {
	const listCall = "repo list acme --limit 1000 --json name,isArchived,isFork,sshUrl"
	t.Run("gh error", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: listCall, stderr: "gh: not logged into any GitHub hosts", exit: 1})
		if _, err := ListRemote("acme"); err == nil {
			t.Fatal("ListRemote() with a failing gh returned no error")
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: listCall, stdout: "not json"})
		if _, err := ListRemote("acme"); err == nil {
			t.Fatal("ListRemote() with a non-JSON payload returned no error")
		}
	})
	t.Run("missing gh", func(t *testing.T) {
		lgCovInstallNoGh(t)
		if _, err := ListRemote("acme"); err == nil {
			t.Fatal("ListRemote() without gh on PATH returned no error")
		}
	})
}

func TestIsArchivedRejectsUnexpectedOutput(t *testing.T) {
	for name, output := range map[string]string{"non boolean": "yes", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			installFakeGhRepoView(t, "acme/widgets", output, true)
			archived, err := IsArchived("acme/widgets")
			if err == nil || !strings.Contains(err.Error(), "unexpected gh output") {
				t.Fatalf("IsArchived() = %v, %v", archived, err)
			}
		})
	}
}

func TestAuthUserReturnsTheAuthenticatedLogin(t *testing.T) {
	lgCovInstallFakeGh(t, lgCovGhCall{
		args:   "api user --include",
		stdout: "HTTP/2 200 OK\n\n" + `{"login":"octocat"}`,
	})
	login, err := AuthUser()
	if err != nil {
		t.Fatal(err)
	}
	if login != "octocat" {
		t.Fatalf("AuthUser() = %q, want %q", login, "octocat")
	}
}

func TestAuthUserReportsFailures(t *testing.T) {
	t.Run("gh error", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: "api user --include", stderr: "gh: not logged into any GitHub hosts", exit: 1})
		if _, err := AuthUser(); err == nil {
			t.Fatal("AuthUser() with a failing gh returned no error")
		}
	})
	t.Run("malformed payload", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: "api user --include", stdout: "HTTP/2 200 OK\n\nnot json"})
		if _, err := AuthUser(); err == nil {
			t.Fatal("AuthUser() with a non-JSON payload returned no error")
		}
	})
}

func TestMemberOrgsReturnsTrimmedLogins(t *testing.T) {
	lgCovInstallFakeGh(t, lgCovGhCall{
		args:   "api user/orgs --include",
		stdout: "HTTP/2 200 OK\n\n" + `[{"login":"acme"},{"login":"  "},{"login":"beta"}]`,
	})
	orgs, err := MemberOrgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 2 || orgs[0] != "acme" || orgs[1] != "beta" {
		t.Fatalf("MemberOrgs() = %#v, want [acme beta]", orgs)
	}
}

func TestMemberOrgsReturnsNothingForAUserWithoutOrganizations(t *testing.T) {
	lgCovInstallFakeGh(t, lgCovGhCall{args: "api user/orgs --include", stdout: "HTTP/2 200 OK\n\n[]"})
	orgs, err := MemberOrgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 0 {
		t.Fatalf("MemberOrgs() = %#v, want none", orgs)
	}
}

func TestMemberOrgsReportsFailures(t *testing.T) {
	t.Run("gh error", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: "api user/orgs --include", stderr: "gh: not logged into any GitHub hosts", exit: 1})
		if _, err := MemberOrgs(); err == nil {
			t.Fatal("MemberOrgs() with a failing gh returned no error")
		}
	})
	t.Run("malformed payload", func(t *testing.T) {
		lgCovInstallFakeGh(t, lgCovGhCall{args: "api user/orgs --include", stdout: "HTTP/2 200 OK\n\nnot json"})
		if _, err := MemberOrgs(); err == nil {
			t.Fatal("MemberOrgs() with a non-JSON payload returned no error")
		}
	})
}

func TestScanLocalReportsUnreadableProjectsRoot(t *testing.T) {
	t.Parallel()
	repositories, err := ScanLocal(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("ScanLocal() on a missing projects root returned no error")
	}
	if len(repositories) != 0 {
		t.Fatalf("ScanLocal() = %#v, want none", repositories)
	}
}

func TestScanLocalSkipsNonRepositoryEntries(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectsRoot, ".hidden", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectsRoot, "README.md"), []byte("not an org\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustIndexedRepository(t, projectsRoot, "acme", "widgets")
	if err := os.MkdirAll(filepath.Join(projectsRoot, "acme", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectsRoot, "acme", "notes.txt"), []byte("not a repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(projectsRoot, "acme", "widgets-feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: ../widgets/.git/worktrees/widgets-feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repositories, err := ScanLocal(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Slug() != "acme/widgets" || repositories[0].Path != filepath.Join(projectsRoot, "acme", "widgets") {
		t.Fatalf("ScanLocal() = %#v, want only acme/widgets", repositories)
	}
}

func TestScanLocalSkipsAnUnreadableOrganizationDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions are unavailable")
	}
	projectsRoot := t.TempDir()
	sealed := filepath.Join(projectsRoot, "aaa-sealed")
	if err := os.MkdirAll(sealed, 0o755); err != nil {
		t.Fatal(err)
	}
	mustIndexedRepository(t, projectsRoot, "zzz-readable", "widgets")
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o700) })
	if entries, err := os.ReadDir(sealed); err == nil {
		_ = entries
		t.Skip("filesystem permissions are not enforced for this user")
	}

	repositories, err := ScanLocal(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Slug() != "zzz-readable/widgets" {
		t.Fatalf("ScanLocal() = %#v, want the readable organization scanned and the sealed one skipped", repositories)
	}
}
