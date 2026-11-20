package cmdhooks

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
)

func TestHooksLintPassesTheHookAndLanguageAndExitsWithTheVerdict(t *testing.T) {
	t.Parallel()
	f := fakeCommands()
	var got hooks.LintOptions
	f.git.Lint = func(options hooks.LintOptions) int {
		got = options
		_, _ = io.WriteString(options.Stderr, "linted\n")
		return 1
	}
	code := -1
	f.git.Exit = func(value int) { code = value }
	cmd := New(f.runtime, f.git, f.life, f.agent)
	cmd.SetArgs([]string{"lint", "--hook", "pre-push", "--language", "go"})
	cmd.SetIn(strings.NewReader("refs"))
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if code != 1 || got.Hook != "pre-push" || got.Language != "go" || got.ProjectsRoot != "/projects" ||
		got.WBExecutable != "/verified/wb" || stderr.String() != "linted\n" {
		t.Fatalf("code %d options %#v stderr %q", code, got, stderr.String())
	}
	if input, _ := io.ReadAll(got.Stdin); string(input) != "refs" {
		t.Fatalf("stdin %q", input)
	}
}

// The built-in pre-commit template runs `wb hooks lint` with the commit's
// index in WB_GIT_INDEX_FILE (wb#993); the command hands it to the lint.
func TestHooksLintReadsTheCommitsIndexFromWBGitIndexFile(t *testing.T) {
	t.Setenv("WB_GIT_INDEX_FILE", "/repo/.git/index.lock")
	f := fakeCommands()
	var got hooks.LintOptions
	f.git.Lint = func(options hooks.LintOptions) int {
		got = options
		return 0
	}
	f.git.Exit = func(int) {}
	cmd := New(f.runtime, f.git, f.life, f.agent)
	cmd.SetArgs([]string{"lint", "--hook", "pre-commit", "--language", "go"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.IndexFile != "/repo/.git/index.lock" {
		t.Fatalf("IndexFile = %q", got.IndexFile)
	}
}
