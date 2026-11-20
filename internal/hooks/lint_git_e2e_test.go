//go:build e2e

package hooks

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testenv"
)

// These tests pin, against real git, the git behaviour lint_test.go's fake
// models: what a tree diff reports for merges, renames, force pushes and
// stream commits, and that `git show --end-of-options <ref>:<file>` reads a
// reusable workflow at its pinned ref.

func realCapture(dir string, env []string, name string, args ...string) (string, error) {
	return captureWith(runner.New(), dir, env, name, args...)
}

// lintRepo is a repository with a pushed main on a local bare origin.
func lintRepo(t *testing.T) string {
	t.Helper()
	repo := initRepo(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	testenv.InitBareRemoteForTest(t, bare)
	git(t, repo, "remote", "add", "origin", bare)
	git(t, repo, "push", "-q", "origin", "main")
	return repo
}

func commitFiles(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		mustMkdirAll(t, filepath.Dir(path))
		mustWrite(t, path, content)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "change")
	return git(t, repo, "rev-parse", "HEAD")
}

func TestE2EPushedFilesSeeMergesRenamesForcePushesAndStreamCommits(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	mainTip := commitFiles(t, repo, map[string]string{"backend/go.mod": "module b\n", "backend/a.go": "package b\n", "other/go.mod": "module o\n"})
	git(t, repo, "push", "-q", "origin", "main")
	h := newLintHarness(t, repo)
	runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
	runner.options.Tools.Capture = realCapture
	files := func(update RefUpdate) []string {
		t.Helper()
		got, all := runner.pushedFiles([]RefUpdate{update})
		if all {
			t.Fatalf("no base for %#v", update)
		}
		return got
	}

	// A rename reports the path it left, so the old package is checked too.
	git(t, repo, "mv", "backend/a.go", "other/a.go")
	git(t, repo, "commit", "-q", "-m", "move")
	moved := git(t, repo, "rev-parse", "HEAD")
	if got := files(RefUpdate{LocalSHA: moved, RemoteSHA: mainTip}); !reflect.DeepEqual(got, []string{"backend/a.go", "other/a.go"}) {
		t.Fatalf("rename: %q", got)
	}

	// A commit first pushed to a stream branch (Tier 0, unlinted) and then
	// merged into main still counts when main is pushed.
	git(t, repo, "checkout", "-q", "-b", "stream/x", mainTip)
	streamTip := commitFiles(t, repo, map[string]string{"backend/s.go": "package b\n"})
	git(t, repo, "push", "-q", "origin", "stream/x")
	git(t, repo, "checkout", "-q", "main")
	git(t, repo, "reset", "-q", "--hard", mainTip)
	git(t, repo, "merge", "-q", "--no-ff", "-m", "merge stream", streamTip)
	merged := git(t, repo, "rev-parse", "HEAD")
	if got := files(RefUpdate{LocalSHA: merged, RemoteSHA: mainTip}); !reflect.DeepEqual(got, []string{"backend/s.go"}) {
		t.Fatalf("merge of a stream commit: %q", got)
	}

	// A force push compares trees, so the files of dropped commits count.
	git(t, repo, "push", "-q", "origin", merged+":refs/heads/main")
	git(t, repo, "reset", "-q", "--hard", mainTip)
	rewritten := commitFiles(t, repo, map[string]string{"other/o.go": "package o\n"})
	if got := files(RefUpdate{LocalSHA: rewritten, RemoteSHA: merged}); !reflect.DeepEqual(got, []string{"backend/s.go", "other/o.go"}) {
		t.Fatalf("force push: %q", got)
	}
}

func TestE2EResolveGolangciPinReadsAReusableWorkflowAtItsRefWithRealGit(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	cicd := filepath.Join(projects, "github.com", "strongo", "cicd")
	mustMkdirAll(t, filepath.Join(cicd, ".github", "workflows"))
	git(t, projects, "init", "-q", "-b", "main", cicd)
	git(t, cicd, "config", "user.name", "t")
	git(t, cicd, "config", "user.email", "t@example.invalid")
	mustWrite(t, filepath.Join(cicd, ".github", "workflows", "workflow.yml"), `jobs:
  nested:
    uses: other/repo/.github/workflows/x.yml@v1
  golangci:
    steps:
      - uses: golangci/golangci-lint-action@v9
        with:
          version: v2.15.0
`)
	git(t, cicd, "add", "-A")
	git(t, cicd, "commit", "-q", "-m", "pin")
	ref := git(t, cicd, "rev-parse", "HEAD")
	mustWrite(t, filepath.Join(cicd, ".github", "workflows", "workflow.yml"), "jobs: {}\n")

	repo := t.TempDir()
	mustMkdirAll(t, filepath.Join(repo, ".github", "workflows"))
	mustWrite(t, filepath.Join(repo, ".github", "workflows", "ci.yml"), fmt.Sprintf(`jobs:
  local:
    uses: ./.github/workflows/local.yml
  missing:
    uses: absent/repo/.github/workflows/workflow.yml@v1
  backend:
    uses: strongo/cicd/.github/workflows/workflow.yml@%s
`, ref))
	pin := resolveGolangciPin(repo, projects, realCapture)
	if pin.Version != "v2.15.0" || pin.Source != "strongo/cicd/.github/workflows/workflow.yml@"+ref {
		t.Fatalf("reusable pin: %#v", pin)
	}
	if pin := resolveGolangciPin(repo, "", realCapture); pin.Version != DefaultGolangciLintVersion {
		t.Fatalf("no projects root: %#v", pin)
	}
	mustWrite(t, filepath.Join(cicd, ".github", "workflows", "workflow.yml"), "jobs:\n  x:\n    steps: []\n")
	git(t, cicd, "add", "-A")
	git(t, cicd, "commit", "-q", "-m", "drop pin")
	empty := git(t, cicd, "rev-parse", "HEAD")
	mustWrite(t, filepath.Join(repo, ".github", "workflows", "ci.yml"), "jobs:\n  b:\n    uses: strongo/cicd/.github/workflows/workflow.yml@"+empty+"\n")
	if pin := resolveGolangciPin(repo, projects, realCapture); pin.Version != DefaultGolangciLintVersion {
		t.Fatalf("reusable without a pin: %#v", pin)
	}
}
