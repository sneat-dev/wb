package checkoutmarker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	ProjectsRoot string
	Canonical    string
	Worktree     string
}

// newFixture builds a real canonical clone with a real linked worktree. The
// whole design rests on what Git actually does with `.git`, `info/exclude`,
// and `git status`, so nothing here is faked.
func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "sneat-dev", "wb")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("create the canonical clone: %v", err)
	}
	git(t, canonical, "init", "-q", "-b", "main")
	git(t, canonical, "config", "user.email", "marker@example.test")
	git(t, canonical, "config", "user.name", "marker")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	git(t, canonical, "add", "-A")
	git(t, canonical, "commit", "-qm", "init")
	worktree := filepath.Join(root, "wbhome", "worktrees", "some-task", "sneat-dev", "wb")
	git(t, canonical, "worktree", "add", "-q", "-b", "some-task", worktree)
	return fixture{ProjectsRoot: projectsRoot, Canonical: canonical, Worktree: worktree}
}

func git(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(arguments, " "), directory, err, output)
	}
	return string(output)
}

func describeOptions(repositories fixture) DescribeOptions {
	return DescribeOptions{
		ProjectsRoot: repositories.ProjectsRoot,
		BaseBranch:   "main",
		Version:      "wb v9.9.9",
		Now:          func() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) },
	}
}

// TestDescribeReadsACanonicalClone pins the schema agents key their decisions
// off. A wrong `kind` or `writable` here is worse than no marker at all.
func TestDescribeReadsACanonicalClone(t *testing.T) {
	repositories := newFixture(t)
	inspection, err := Describe(repositories.Canonical, describeOptions(repositories))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	descriptor := inspection.Descriptor
	if descriptor.Kind != KindCanonical || descriptor.Writable {
		t.Fatalf("canonical clone described as kind=%s writable=%v", descriptor.Kind, descriptor.Writable)
	}
	if descriptor.Repository != "sneat-dev/wb" || descriptor.Branch != "main" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	if descriptor.CanonicalPath != repositories.Canonical || descriptor.CheckoutPath != repositories.Canonical {
		t.Fatalf("paths = %q / %q", descriptor.CheckoutPath, descriptor.CanonicalPath)
	}
	want := filepath.Join(repositories.Canonical, ".git", "info", "exclude")
	if inspection.ExcludePath != want {
		t.Fatalf("exclude path = %q, want %q", inspection.ExcludePath, want)
	}
}

// TestDescribeReadsALinkedWorktree checks the harder half: a worktree has to
// name the canonical clone it came from and the task it carries.
func TestDescribeReadsALinkedWorktree(t *testing.T) {
	repositories := newFixture(t)
	inspection, err := Describe(repositories.Worktree, describeOptions(repositories))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	descriptor := inspection.Descriptor
	if descriptor.Kind != KindWorktree || !descriptor.Writable {
		t.Fatalf("worktree described as kind=%s writable=%v", descriptor.Kind, descriptor.Writable)
	}
	if descriptor.Repository != "sneat-dev/wb" {
		t.Fatalf("repository = %q", descriptor.Repository)
	}
	if descriptor.Branch != "some-task" || descriptor.Task != "some-task" {
		t.Fatalf("branch=%q task=%q", descriptor.Branch, descriptor.Task)
	}
	if descriptor.CanonicalPath != repositories.Canonical {
		t.Fatalf("canonical path = %q, want %q", descriptor.CanonicalPath, repositories.Canonical)
	}
	// The exclude file is the canonical clone's: a linked worktree has none of
	// its own, which is what lets one rule cover the whole family. Git records
	// physical paths, so compare through the filesystem rather than by string.
	want := filepath.Join(repositories.Canonical, ".git", "info", "exclude")
	if !sameFile(t, inspection.ExcludePath, want) {
		t.Fatalf("exclude path = %q, want %q", inspection.ExcludePath, want)
	}
}

// sameFile compares two paths that may differ only by symlink resolution,
// which is exactly how macOS presents a temporary directory.
func sameFile(t *testing.T, left, right string) bool {
	t.Helper()
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			return filepath.Join(resolved, filepath.Base(path))
		}
		return path
	}
	return resolve(left) == resolve(right)
}

// TestDescribeRefusesAnUnmanagedPath keeps the marker from claiming authority
// over a checkout WB does not manage.
func TestDescribeRefusesAnUnmanagedPath(t *testing.T) {
	repositories := newFixture(t)
	if _, err := Describe(t.TempDir(), describeOptions(repositories)); err == nil {
		t.Fatal("a path in no repository was described")
	}
}

// TestApplyKeepsGitStatusClean is the objection the whole design answers: an
// untracked marker would make every checkout dirty, and WB's own hooks refuse
// an untracked path. It must be invisible in BOTH a canonical clone and a
// linked worktree, from one rule.
func TestApplyKeepsGitStatusClean(t *testing.T) {
	repositories := newFixture(t)
	for _, path := range []string{repositories.Canonical, repositories.Worktree} {
		inspection, err := Describe(path, describeOptions(repositories))
		if err != nil {
			t.Fatalf("Describe(%s): %v", path, err)
		}
		if _, err := Apply(inspection.Descriptor, inspection.ExcludePath); err != nil {
			t.Fatalf("Apply(%s): %v", path, err)
		}
	}
	for _, path := range []string{repositories.Canonical, repositories.Worktree} {
		if _, err := os.Stat(filepath.Join(path, FileName)); err != nil {
			t.Fatalf("%s has no marker: %v", path, err)
		}
		if status := git(t, path, "status", "--porcelain=v1"); status != "" {
			t.Fatalf("%s is dirty after the marker was written:\n%s", path, status)
		}
		if ignored := git(t, path, "status", "--porcelain", "--ignored"); !strings.Contains(ignored, FileName) {
			t.Fatalf("%s does not report the marker as ignored:\n%s", path, ignored)
		}
	}
	// One rule, in the common directory, covers both checkouts.
	exclude := filepath.Join(repositories.Canonical, ".git", "info", "exclude")
	contents, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatalf("read the exclude file: %v", err)
	}
	if strings.Count(string(contents), ExcludePattern) != 1 {
		t.Fatalf("the exclude rule was written more than once:\n%s", contents)
	}
}

// TestApplyIsIdempotent keeps a refresh on every sync and every create free,
// and keeps a re-run from rewriting a marker whose only difference is a clock.
func TestApplyIsIdempotent(t *testing.T) {
	repositories := newFixture(t)
	options := describeOptions(repositories)
	inspection, err := Describe(repositories.Worktree, options)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	first, err := Apply(inspection.Descriptor, inspection.ExcludePath)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if !first.MarkerWritten || !first.ExcludeWritten {
		t.Fatalf("the first apply wrote nothing: %+v", first)
	}
	second, err := Apply(inspection.Descriptor, inspection.ExcludePath)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if second.Changed() {
		t.Fatalf("a repeated apply changed something: %+v", second)
	}
	// Only the timestamp differs; the file must be left alone.
	later := inspection.Descriptor
	later.GeneratedAt = later.GeneratedAt.Add(time.Hour)
	third, err := Apply(later, inspection.ExcludePath)
	if err != nil {
		t.Fatalf("third Apply: %v", err)
	}
	if third.Changed() {
		t.Fatal("a marker was rewritten for a changed timestamp alone")
	}
	// A real change is written.
	moved := inspection.Descriptor
	moved.Branch = "another-branch"
	fourth, err := Apply(moved, inspection.ExcludePath)
	if err != nil {
		t.Fatalf("fourth Apply: %v", err)
	}
	if !fourth.MarkerWritten {
		t.Fatal("a changed branch did not rewrite the marker")
	}
}

// TestEnsureExcludePreservesTheUsersRules keeps WB additive in a file it does
// not own.
func TestEnsureExcludePreservesTheUsersRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "# personal\nscratch/\n*.local"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	written, err := EnsureExclude(path)
	if err != nil || !written {
		t.Fatalf("EnsureExclude: written=%v err=%v", written, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"# personal", "scratch/", "*.local", ExcludePattern} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("%q is missing from the exclude file:\n%s", expected, contents)
		}
	}
	// A file with no trailing newline must not have its last rule joined to
	// WB's comment.
	if strings.Contains(string(contents), "*.local#") {
		t.Fatalf("the last existing rule was corrupted:\n%s", contents)
	}
	again, err := EnsureExclude(path)
	if err != nil || again {
		t.Fatalf("a repeated EnsureExclude wrote again: written=%v err=%v", again, err)
	}
}

// TestRenderStatesTheContract checks the fields and the remedy an agent acts
// on, for both kinds.
func TestRenderStatesTheContract(t *testing.T) {
	canonical := Render(Descriptor{
		Kind: KindCanonical, Writable: false, Repository: "sneat-co/backstage",
		CheckoutPath: "/p/sneat-co/backstage", CanonicalPath: "/p/sneat-co/backstage",
		Branch: "main", BaseBranch: "main", GeneratedBy: "wb v1", GeneratedAt: time.Unix(0, 0),
	})
	for _, expected := range []string{
		"kind: canonical",
		"writable: false",
		`repository: "sneat-co/backstage"`,
		"wb worktree create <task> sneat-co/backstage",
		"wb worktree rescue /p/sneat-co/backstage",
		"do not write here",
	} {
		if !strings.Contains(canonical, expected) {
			t.Fatalf("the canonical marker is missing %q:\n%s", expected, canonical)
		}
	}
	worktree := Render(Descriptor{
		Kind: KindWorktree, Writable: true, Repository: "sneat-co/backstage",
		CheckoutPath: "/w/task/sneat-co/backstage", CanonicalPath: "/p/sneat-co/backstage",
		Branch: "feature/x", BaseBranch: "main", Task: "task",
		GeneratedBy: "wb v1", GeneratedAt: time.Unix(0, 0),
	})
	for _, expected := range []string{"kind: worktree", "writable: true", "write here", "feature/x"} {
		if !strings.Contains(worktree, expected) {
			t.Fatalf("the worktree marker is missing %q:\n%s", expected, worktree)
		}
	}
}

// TestRenderQuotesPathsThatYAMLWouldMisread keeps a path holding a colon from
// silently producing an unparseable document.
func TestRenderQuotesPathsThatYAMLWouldMisread(t *testing.T) {
	rendered := Render(Descriptor{
		Kind: KindWorktree, Writable: true, Repository: "owner/name",
		CheckoutPath: `/tmp/odd: path/"quoted"`, Branch: "yes", BaseBranch: "main",
		GeneratedBy: "wb v1", GeneratedAt: time.Unix(0, 0),
	})
	if !strings.Contains(rendered, `checkout_path: "/tmp/odd: path/\"quoted\""`) {
		t.Fatalf("an awkward path was not quoted:\n%s", rendered)
	}
	if !strings.Contains(rendered, `branch: "yes"`) {
		t.Fatalf("a YAML boolean-looking branch was not quoted:\n%s", rendered)
	}
}

// TestApplyWritesTheExcludeRuleBeforeTheMarker guards the ordering. A marker
// written before its rule exists is a dirty checkout for as long as the gap
// lasts, which is exactly what WB's hooks refuse.
func TestApplyWritesTheExcludeRuleBeforeTheMarker(t *testing.T) {
	repositories := newFixture(t)
	inspection, err := Describe(repositories.Canonical, describeOptions(repositories))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	// An unwritable exclude file makes the rule fail; the marker must then not
	// exist either.
	excludeDirectory := filepath.Dir(inspection.ExcludePath)
	if err := os.MkdirAll(excludeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inspection.ExcludePath, []byte(""), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(excludeDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(excludeDirectory, 0o755) })
	if _, err := Apply(inspection.Descriptor, inspection.ExcludePath); err == nil {
		t.Skip("this filesystem allowed the write; ordering is asserted by the happy path instead")
	}
	if _, err := os.Stat(filepath.Join(repositories.Canonical, FileName)); err == nil {
		t.Fatal("a marker was written even though its ignore rule could not be")
	}
}
