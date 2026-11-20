//go:build e2e

package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

// wb#993 against real Git: a temporary index built the way `git commit -a`
// builds one, which the real index does not reflect. Real-process e2e tests,
// run in the e2e CI job (go test -tags e2e -run ^TestE2E).

// stageInTemporaryIndex reproduces what `git commit -a` builds: a copy of the
// real index with path staged in it, which the real index does not hold.
func stageInTemporaryIndex(t *testing.T, repo, path string) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "next-index.lock")
	cmd := exec.Command("git", "add", "--", path)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	current, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, index, string(current))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add into the temporary index: %v\n%s", err, output)
	}
	return index
}

//nolint:paralleltest // Git hands the hook its index in the process environment (GIT_INDEX_FILE), which hooks.Run reads; t.Setenv forbids t.Parallel.
func TestE2EBuiltinPreCommitChecksWhitespaceInTheHooksTemporaryIndex(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	mustMkdirAll(t, filepath.Join(repo, ".wb"))
	mustWrite(t, filepath.Join(repo, ".wb", "hooks.yaml"), "version: 1\nhooks:\n  pre-commit:\n    template: "+BuiltinPreCommit+"\nmetrics:\n  enabled: false\n")
	git(t, repo, "add", ".wb/hooks.yaml")
	git(t, repo, "commit", "-m", "policy")
	mustWrite(t, filepath.Join(repo, "README.md"), "trailing whitespace \n")
	index := stageInTemporaryIndex(t, repo, "README.md")

	t.Setenv("GIT_INDEX_FILE", index)
	var stdout bytes.Buffer
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err == nil || result.ExitCode == 0 || !strings.Contains(stdout.String(), "README.md:1: trailing whitespace.") {
		t.Fatalf("a whitespace error staged in the hook's index passed: result %#v err %v stdout %q", result, err, stdout.String())
	}
}

func TestE2EGoPreCommitLintListsTheFilesStagedInTheHooksIndex(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	mustWrite(t, filepath.Join(repo, "go.mod"), "module r\n")
	git(t, repo, "add", "go.mod")
	git(t, repo, "commit", "-q", "-m", "module")
	mustWrite(t, filepath.Join(repo, "a.go"), "package r\n")
	index := stageInTemporaryIndex(t, repo, "a.go")
	h := newLintHarness(t, repo)
	linter := h.plantLinter("go1.27.4", DefaultGolangciLintVersion)

	// The staged-file listing runs real git with the environment the lint
	// passes; every other helper keeps the harness's scripted answers.
	options := func(indexFile string) LintOptions {
		options := h.options("pre-commit", "go")
		scripted := options.Tools.Capture
		real := runner.New()
		options.Tools.Capture = func(dir string, env []string, name string, args ...string) (string, error) {
			if name == "git" && len(args) > 1 && args[0] == "diff" && args[1] == "--cached" {
				return captureWith(real, dir, env, name, args...)
			}
			return scripted(dir, env, name, args...)
		}
		options.IndexFile = indexFile
		return options
	}

	// The default index holds nothing staged, which is what the hook saw
	// before wb#993.
	if code := Lint(options("")); code != 0 || len(h.calls) != 0 {
		t.Fatalf("default index: exit %d calls %q", code, h.calls)
	}
	if code := Lint(options(index)); code != 0 || !reflect.DeepEqual(h.calls, []string{".|" + linter + " run --new-from-rev=HEAD ."}) {
		t.Fatalf("hook index: exit %d calls %q stderr %s", code, h.calls, h.stderr.String())
	}
}
