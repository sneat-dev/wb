package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateSynchronizesCanonicalAndCreatesCentralWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	fixture.pushRemoteCommit(t, "remote change")

	results, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "issue-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	result := results[0]
	wantWorktree := filepath.Join(fixture.projectsRoot, ".wb", "worktrees", "issue-123", "acme", "app")
	if result.WorktreeDir != wantWorktree || result.Branch != "codex/issue-123" || result.Action != "created" {
		t.Fatalf("result = %#v", result)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main") {
		t.Fatalf("canonical HEAD %s did not synchronize with origin/main", got)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "branch", "--show-current"); got != "codex/issue-123" {
		t.Fatalf("worktree branch = %q", got)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD"); got != gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD") {
		t.Fatal("new worktree was not based on synchronized main")
	}

	guarded, err := Guard(context.Background(), result.WorktreeDir, GuardOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if guarded.Kind != "linked" || guarded.CanonicalDir != fixture.canonical {
		t.Fatalf("guard result = %#v", guarded)
	}
}

func TestCreateRefusesUnsafeCanonicalClone(t *testing.T) {
	tests := []struct {
		name string
		trip func(*testing.T, *gitFixture)
		want string
	}{
		{
			name: "dirty",
			trip: func(t *testing.T, fixture *gitFixture) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(fixture.canonical, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "is dirty",
		},
		{
			name: "feature branch",
			trip: func(t *testing.T, fixture *gitFixture) {
				t.Helper()
				gitTest(t, fixture.canonical, "switch", "-c", "feature")
			},
			want: `is on "feature"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			test.trip(t, fixture)
			_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    "unsafe",
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCreateResumeIsExplicitAndPreservesChanges(t *testing.T) {
	fixture := newGitFixture(t)
	options := CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "resume-me"}
	first, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(first[0].WorktreeDir, "in-progress.txt")
	if err := os.WriteFile(marker, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), []string{"acme/app"}, options); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("non-resume error = %v", err)
	}
	options.Resume = true
	resumed, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if resumed[0].Action != "resumed" {
		t.Fatalf("resume result = %#v", resumed[0])
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep\n" {
		t.Fatalf("in-progress work was not preserved: %q, %v", content, err)
	}
}

func TestGuardRejectsFeatureBranchesAndChangesInCanonicalClone(t *testing.T) {
	fixture := newGitFixture(t)
	if result, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || result.Kind != "canonical" {
		t.Fatalf("clean main guard = %#v, %v", result, err)
	}

	gitTest(t, fixture.canonical, "switch", "-c", "feature")
	if _, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "wb worktree create") {
		t.Fatalf("feature guard error = %v", err)
	}
	gitTest(t, fixture.canonical, "switch", "main")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "must remain clean") {
		t.Fatalf("dirty guard error = %v", err)
	}
}

func TestGuardRejectsLinkedWorktreeOutsideCentralHierarchy(t *testing.T) {
	fixture := newGitFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature", outside, "main")
	if _, err := Guard(context.Background(), outside, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), ".wb/worktrees") {
		t.Fatalf("outside guard error = %v", err)
	}
}

type gitFixture struct {
	projectsRoot string
	canonical    string
	remote       string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "clone", remote, canonical)
	configureGitUser(t, canonical)
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", "README.md")
	gitTest(t, canonical, "commit", "-m", "initial")
	gitTest(t, canonical, "push", "-u", "origin", "main")
	var err error
	projectsRoot, err = filepath.EvalSymlinks(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return &gitFixture{projectsRoot: projectsRoot, canonical: canonical, remote: remote}
}

func (fixture *gitFixture) pushRemoteCommit(t *testing.T, message string) {
	t.Helper()
	clone := filepath.Join(filepath.Dir(fixture.projectsRoot), "remote-writer")
	command := exec.Command("git", "clone", fixture.remote, clone)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone remote writer: %v\n%s", err, output)
	}
	for _, pair := range [][2]string{{"user.name", "WB Test"}, {"user.email", "wb@example.test"}} {
		command = exec.Command("git", "-C", clone, "config", pair[0], pair[1])
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("configure remote writer: %v\n%s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(clone, "remote.txt"), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "remote.txt"}, {"commit", "-m", message}, {"push", "origin", "main"}} {
		command = exec.Command("git", append([]string{"-C", clone}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
}

func configureGitUser(t *testing.T, dir string) {
	t.Helper()
	gitTest(t, dir, "config", "user.name", "WB Test")
	gitTest(t, dir, "config", "user.email", "wb@example.test")
}

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
