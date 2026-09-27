package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCleanupOptionsRequireExactTaskScopeForSensitiveActions(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	base := CleanupOptions{ProjectsRoot: projects, Task: "task"}
	valid, err := normalizeCleanupOptions(base)
	if err != nil {
		t.Fatal(err)
	}
	if valid.Task != "task" || len(valid.Tasks) != 1 || valid.Base != "main" || valid.Workers != DefaultInspectWorkers || valid.Now == nil {
		t.Fatalf("normalization lost required defaults or task identity: %+v", valid)
	}
	for _, test := range []struct {
		name string
		edit func(*CleanupOptions)
		want string
	}{
		{"missing selection", func(o *CleanupOptions) { o.Task = "" }, "supply one or more tasks"},
		{"mixed task selectors", func(o *CleanupOptions) { o.Tasks = []string{"other"} }, "task and tasks cannot be combined"},
		{"all merged with task", func(o *CleanupOptions) { o.AllMerged = true }, "tasks and --all-merged"},
		{"negative grace", func(o *CleanupOptions) { o.OlderThan = -time.Second }, "cannot be negative"},
		{"resume needs one task", func(o *CleanupOptions) { o.Tasks = []string{"task", "other"}; o.Task = ""; o.ResumeInterrupted = true }, "one explicit task"},
		{"exact repository needs one task", func(o *CleanupOptions) { o.Task = ""; o.AllMerged = true; o.ExactRepository = "acme/app" }, "one explicit task"},
		{"exact repository disagrees with filter", func(o *CleanupOptions) { o.ExactRepository = "acme/app"; o.Filter = "acme/lib" }, "different substring filter"},
		{"invalid exact repository", func(o *CleanupOptions) { o.ExactRepository = "bad repo" }, "repository"},
		{"supersession needs one task", func(o *CleanupOptions) { o.Task = ""; o.AllMerged = true; o.SupersededBy = "receipt.json" }, "one explicit task"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := base
			test.edit(&options)
			if _, err := normalizeCleanupOptions(options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalization error = %v, want %q", err, test.want)
			}
		})
	}
	exact := base
	exact.ExactRepository = " acme/app "
	exact.Workers = 3
	exact.ReportDir = "reports"
	exact.AbsorbedBy = "  receipt "
	normalized, err := normalizeCleanupOptions(exact)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Filter != "acme/app" || normalized.ExactRepository != "acme/app" || normalized.Workers != 3 ||
		normalized.AbsorbedBy != "receipt" || !filepath.IsAbs(normalized.ReportDir) {
		t.Fatalf("exact repository cleanup changed scope or report directory: %+v", normalized)
	}
}

func TestInventoryWalkClassifiesForeignDebrisAndReservedStagesWithoutGit(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	root := filepath.Join(projects, ".wb", "worktrees")
	paths := []string{
		filepath.Join(root, "task", "acme", "app"),
		filepath.Join(root, "task", "github.com", "team", "service"),
		filepath.Join(root, "task", "bad name", "repo"),
		filepath.Join(root, "bad task", "acme", "app"),
		filepath.Join(root, ".hidden", "acme", "app"),
		filepath.Join(root, "task", ".wb-stage-123"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "task", ".wb-retired-stage-"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "task", ".wb-stage-link")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	listing := &layoutListing{ctx: context.Background(), projectsRoot: projects,
		layout: wbhome.Layout{WorktreesRoot: root}}
	listing.walkTasks(entries)
	if len(listing.pending) != 0 {
		t.Fatalf("foreign debris was queued as a Git worktree: %+v", listing.pending)
	}
	byPath := map[string]ListDiagnostic{}
	for _, d := range listing.diagnostics {
		byPath[d.Path] = d
	}
	for _, path := range []string{paths[0], paths[1]} {
		if d, ok := byPath[path]; !ok || !d.NonBlocking {
			t.Fatalf("foreign non-Git candidate not reported as nonblocking: %s => %+v", path, d)
		}
	}
	if d := byPath[filepath.Join(root, "task", "bad name")]; !strings.Contains(d.Message, "invalid owner") {
		t.Fatalf("invalid owner diagnostic = %+v", d)
	}
	if d := byPath[filepath.Join(root, "bad task")]; !strings.Contains(d.Message, "invalid task") {
		t.Fatalf("invalid task diagnostic = %+v", d)
	}
	if _, found := byPath[filepath.Join(root, ".hidden")]; found {
		t.Fatal("hidden task was inventoried")
	}
	byArtifact := map[string]LifecycleArtifact{}
	for _, artifact := range listing.artifacts {
		byArtifact[filepath.Base(artifact.Path)] = artifact
	}
	if a := byArtifact[".wb-stage-123"]; !a.Eligible || a.Disposition != "archive_empty_stage" {
		t.Fatalf("empty WB stage not eligible for archival: %+v", a)
	}
	if a := byArtifact[".wb-retired-stage-"]; a.Eligible || !strings.Contains(a.Reason, "suffix") {
		t.Fatalf("reserved stage without identity accepted: %+v", a)
	}
	if a := byArtifact[".wb-stage-link"]; a.Eligible || !strings.Contains(a.Reason, "no-follow") {
		t.Fatalf("symlinked stage accepted: %+v", a)
	}
}

//nolint:paralleltest // Subtests share one open cleanup task descriptor and lock.
func TestCleanupHandleOpensOnlyAuthorizedHierarchy(t *testing.T) {
	task := newHostLevelCleanupTaskFixture(t)
	root := task.taskPath
	for _, path := range []string{
		filepath.Join(root, "app"),
		filepath.Join(root, "acme", "app"),
		filepath.Join(root, "github.com", "acme", "app"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		handle, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: path}})
		if err != nil {
			t.Fatalf("valid hierarchy %s: %v", path, err)
		}
		if err := handle.validate(); err != nil {
			t.Fatal(err)
		}
		handle.close()
	}
	for _, test := range []struct{ path, want string }{
		{filepath.Join(root, "bad name"), "invalid cleanup repository"},
		{filepath.Join(root, "bad name", "app"), "invalid cleanup worktree hierarchy"},
		{filepath.Join(root, "bad host", "acme", "app"), "invalid cleanup worktree hierarchy"},
		{filepath.Join(root, "example.com", "acme", "app"), "open cleanup worktree host"},
		{filepath.Join(root, "github.com", "missing", "app"), "open cleanup worktree parent"},
		{filepath.Join(root, "github.com", "acme", "missing"), "open cleanup worktree"},
		{filepath.Join(root, "github.com", "acme", "app", "nested"), "unsupported hierarchy"},
	} {
		//nolint:paralleltest // Every case uses the same cleanup task descriptor.
		t.Run(test.path, func(t *testing.T) {
			if _, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: test.path}}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("hierarchy %s error = %v, want %q", test.path, err, test.want)
			}
		})
	}
	outside := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, result := range []CleanupResult{
		{ListResult: ListResult{WorktreeDir: outside}},
		{ListResult: ListResult{WorktreeDir: outside, External: true}},
		{ListResult: ListResult{WorktreeDir: outside, Local: true}},
	} {
		handle, err := openCleanupWorktree(task, result)
		if err != nil {
			t.Fatalf("valid relocated/local/adopted path: %v", err)
		}
		if err := handle.validate(); err != nil {
			t.Fatal(err)
		}
		handle.close()
	}
}

func TestRetiredStageClaimSkipsForeignAndOccupiedEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	occupied := filepath.Join(root, ".wb-retired-stage-aa-occupied")
	if err := os.Mkdir(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "checkout"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(occupied, filepath.Join(root, ".wb-retired-stage-ab-link")); err != nil {
		t.Fatal(err)
	}
	reservedLocal := filepath.Join(root, ".wb-retired-stage-task-local")
	if err := os.Mkdir(reservedLocal, 0o700); err != nil {
		t.Fatal(err)
	}
	reusable := filepath.Join(root, ".wb-retired-stage-zz-empty")
	if err := os.Mkdir(reusable, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	name, claimed, err := claimRetiredStageDirectory(parent, ".wb-stage-", ".wb-retired-stage-")
	if err != nil || !claimed || !strings.HasPrefix(name, ".wb-stage-") {
		t.Fatalf("empty generic stage not reclaimed: name=%q claimed=%t err=%v", name, claimed, err)
	}
	if _, err := os.Stat(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(reusable); !os.IsNotExist(err) {
		t.Fatalf("retired name survived claim: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(occupied, "checkout")); err != nil || string(got) != "preserve" {
		t.Fatalf("occupied stage changed: %q, %v", got, err)
	}
	if info, err := os.Stat(reservedLocal); err != nil || !info.IsDir() {
		t.Fatalf("task-bound stage consumed by generic allocator: %v, %v", info, err)
	}
	if name, claimed, err := claimRetiredStageDirectory(parent, ".wb-stage-", ".wb-retired-stage-"); err != nil || claimed || name != "" {
		t.Fatalf("foreign or occupied retirement unexpectedly claimed: %q, %t, %v", name, claimed, err)
	}
}

func TestAdoptedRegistrationRemovalPreservesSiblingAndRejectsRedirect(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	owner := filepath.Join(task.taskPath, "acme")
	for _, repo := range []string{"app", "lib"} {
		path := filepath.Join(owner, repo)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, adoptedWorktreePointerName), []byte("external"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeAdoptedRegistration(task, "acme", "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(owner, "app")); !os.IsNotExist(err) {
		t.Fatalf("retired registration remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(owner, "lib", adoptedWorktreePointerName)); err != nil {
		t.Fatalf("sibling registration removed: %v", err)
	}
	if err := removeAdoptedRegistration(task, "acme", "lib"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owner); !os.IsNotExist(err) {
		t.Fatalf("empty owner not retired: %v", err)
	}
	if err := removeAdoptedRegistration(task, "acme", "app"); err != nil {
		t.Fatalf("repeated retirement failed: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, owner); err != nil {
		t.Fatal(err)
	}
	if err := removeAdoptedRegistration(task, "acme", "app"); err == nil || !strings.Contains(err.Error(), "without following links") {
		t.Fatalf("redirected registration owner accepted: %v", err)
	}
}
