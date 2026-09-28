package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
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
			_, _ = file.WriteString("git invoked " + strings.Join(os.Args[1:], " ") + "\n")
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
	projects := filepath.Join(root, "projects")
	home := filepath.Join(projects, ".wb")
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
	t.Setenv(wbhome.EnvOverride, projects)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	// Two mechanisms read the real operator machine no matter what
	// wbhome.EnvOverride says, and an operator machine that has ever used
	// WB before its per-project layout existed trips both:
	//
	//   - wbhome.legacyUserLayout reads os.UserHomeDir() directly (real
	//     $HOME) and, whenever that machine's $HOME/.wb/worktrees exists,
	//     unconditionally adds it as a *read* layout -- so a real, unrelated
	//     population of worktrees gets walked alongside the 240 synthetic
	//     candidates above. os.UserHomeDir() honours $HOME on every
	//     platform WB supports (unix; Go substitutes USERPROFILE on
	//     Windows, which this override doesn't reach, but WB's supported
	//     local runners are Darwin and Linux -- see internal/process).
	//   - appendConfiguredSharedWorktreesLayout reads a global, per-operator
	//     config file (~/.config/wb/worktrees.yaml, or
	//     $XDG_CONFIG_HOME/wb/…), independent of wbhome.EnvOverride too.
	//
	// Point both at this test's own empty directory so no real machine
	// state is ever found -- the real-HOME leak this test exists to rule
	// out.
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)

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

// An exact repository landing must not pay one `git worktree list` per
// canonical clone to recover a checkout from an old shared root. Repository
// filtering is known before registry discovery, so unrelated clones stay off
// the subprocess path entirely.
func TestListWithFilterSkipsUnselectedCanonicalRegistryGit(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	selected := filepath.Join(projects, "acme", "selected")
	unrelated := filepath.Join(projects, "other", "repository")
	for _, canonical := range []string{selected, unrelated} {
		if err := os.MkdirAll(filepath.Join(canonical, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(canonical, ".worktrees"), 0o755); err != nil {
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
	t.Setenv(wbhome.EnvOverride, projects)
	t.Setenv(wbhome.EnvMigrationCompat, "")

	_, _ = ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: projects,
		Filter:       "acme/selected",
	})
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), selected) {
		t.Fatalf("selected canonical clone did not reach Git:\n%s", content)
	}
	if strings.Contains(string(content), unrelated) {
		t.Fatalf("unselected canonical clone reached Git:\n%s", content)
	}
}

func BenchmarkListWithFilterLargeHistoricalPopulation(b *testing.B) {
	root := b.TempDir()
	projects := filepath.Join(root, "projects")
	home := filepath.Join(projects, ".wb")
	for index := 0; index < 610; index++ {
		candidate := filepath.Join(home, "worktrees", fmt.Sprintf("historical-%03d", index), "unrelated", "repository")
		if err := os.MkdirAll(candidate, 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(candidate, ".git"), []byte("gitdir: /nonexistent\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.Setenv(wbhome.EnvOverride, projects)
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
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	// testenv.WriteExecutableFile (not os.OpenFile+io.Copy) is required here:
	// opening target directly for write leaves a writable fd on its inode
	// while another goroutine's fork elsewhere in this parallel test binary
	// may inherit it before its own exec, racing "text file busy"
	// (golang/go#22315; task-21, #739) -- and target is itself about to be
	// exec'd as a fake git helper by this same test.
	if err := testenv.WriteExecutableFile(target, content, 0o755); err != nil {
		t.Fatal(err)
	}
}
