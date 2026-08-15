package worktrees

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

const (
	listFilterGitHelperEnv    = "WB_TEST_LIST_FILTER_GIT_HELPER"
	listFilterGitHelperLogEnv = "WB_TEST_LIST_FILTER_GIT_HELPER_LOG"
)

// init runs before the Go test flag parser. That lets the copied test binary
// act as a portable fake git executable even though Git passes -C and other
// arguments that the test binary would otherwise reject.
func init() {
	if os.Getenv(listFilterGitHelperEnv) != "1" {
		return
	}
	for _, argument := range os.Args[1:] {
		if argument == "check-ref-format" {
			os.Exit(0)
		}
	}
	if path := os.Getenv(listFilterGitHelperLogEnv); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = file.WriteString("git invoked\n")
			_ = file.Close()
		}
	}
	os.Exit(1)
}

// TestListWithFilterSkipsUnselectedGitCandidates proves that --filter is
// applied before subprocess-backed Git-root validation. A large historical
// population can contain many valid-looking .git markers, but an unrelated
// filter must not execute Git once for every excluded checkout.
func TestListWithFilterSkipsUnselectedGitCandidates(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	for index := 0; index < 240; index++ {
		candidate := filepath.Join(home, "worktrees", fmt.Sprintf("historical-%03d", index), "unrelated", "repository")
		if err := os.MkdirAll(candidate, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(candidate, ".git"), []byte("gitdir: /nonexistent\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	gitDirectory := t.TempDir()
	gitExecutable := filepath.Join(gitDirectory, "git")
	if runtime.GOOS == "windows" {
		gitExecutable += ".exe"
	}
	copyTestBinary(t, gitExecutable)
	logPath := filepath.Join(root, "git-invocations.log")
	t.Setenv("PATH", gitDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(listFilterGitHelperEnv, "1")
	t.Setenv(listFilterGitHelperLogEnv, logPath)
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(wbhome.EnvMigrationCompat, "")

	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: projects,
		Base:         "main",
		Filter:       "specscore",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 0 || len(outcome.Diagnostics) != 0 {
		t.Fatalf("filtered outcome = %#v", outcome)
	}
	if content, err := os.ReadFile(logPath); err == nil && len(content) != 0 {
		t.Fatalf("unselected candidates invoked git %d times", strings.Count(string(content), "git invoked"))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func BenchmarkListWithFilterLargeHistoricalPopulation(b *testing.B) {
	root := b.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	for index := 0; index < 610; index++ {
		candidate := filepath.Join(home, "worktrees", fmt.Sprintf("historical-%03d", index), "unrelated", "repository")
		if err := os.MkdirAll(candidate, 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(candidate, ".git"), []byte("gitdir: /nonexistent\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.Setenv(wbhome.EnvOverride, home)
	b.Setenv(wbhome.EnvMigrationCompat, "")
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: projects, Base: "main", Filter: "specscore"})
		if err != nil {
			b.Fatal(err)
		}
		if len(outcome.Results) != 0 || len(outcome.Diagnostics) != 0 {
			b.Fatalf("filtered outcome = %#v", outcome)
		}
	}
}

func copyTestBinary(t *testing.T, target string) {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := input.Close(); err != nil {
			t.Errorf("close source test binary: %v", err)
		}
	}()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
