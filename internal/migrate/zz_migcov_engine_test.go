package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migCovTextReplaceSpec(id string) Spec {
	return Spec{Format: MigrationFormatV1, ID: id, Steps: []Step{{Kind: "text.replace", From: "old", To: "new"}}}
}

func TestMigCovBuildPlanRejectsInvalidSpecAndMissingRoot(t *testing.T) {
	t.Parallel()
	if _, err := BuildPlan(Spec{}); err == nil || !strings.Contains(err.Error(), "missing id") {
		t.Fatalf("BuildPlan(invalid spec) = %v", err)
	}
	if _, err := BuildPlan(migCovTextReplaceSpec("no-roots")); err == nil || !strings.Contains(err.Error(), "at least one root is required") {
		t.Fatalf("BuildPlan(no roots) = %v", err)
	}
}

func TestMigCovBuildPlanSkipsIgnoredDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var ignored []string
	for _, name := range []string{".git", ".hg", ".svn", "node_modules", "vendor", ".venv", "dist", "build", ".hidden"} {
		ignored = append(ignored, name)
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "ignored.py"), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "kept.py"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(migCovTextReplaceSpec("skip-ignored"), root)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %+v, want only src/kept.py", plan.Changes)
	}
	if got := filepath.Base(plan.Changes[0].Path); got != "kept.py" {
		t.Fatalf("changed file = %q, want kept.py", plan.Changes[0].Path)
	}
	for _, name := range ignored {
		if strings.Contains(plan.Changes[0].Path, string(filepath.Separator)+name+string(filepath.Separator)) {
			t.Errorf("ignored directory %q was migrated: %s", name, plan.Changes[0].Path)
		}
	}
}

func TestMigCovBuildPlanReportsScanAndReadFailures(t *testing.T) {
	t.Parallel()
	if _, err := BuildPlan(migCovTextReplaceSpec("scan"), filepath.Join(t.TempDir(), "absent")); err == nil || !strings.Contains(err.Error(), "scan ") {
		t.Fatalf("BuildPlan(missing root) = %v, want scan error", err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "good.py"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "absent-target"), filepath.Join(root, "broken.go")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	if _, err := BuildPlan(migCovTextReplaceSpec("read"), root); err == nil || !strings.Contains(err.Error(), "broken.go") {
		t.Fatalf("BuildPlan(dangling symlink) = %v, want read error naming broken.go", err)
	}
}

func TestMigCovBuildPlanHonoursIncludeAndDeduplicatesRoots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "a.py"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.py"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	filtered := migCovTextReplaceSpec("include")
	filtered.Scope.Include = []string{"pkg/**"}
	plan, err := BuildPlan(filtered, root)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Changes) != 1 || filepath.Base(plan.Changes[0].Path) != "a.py" {
		t.Fatalf("include filter changes = %+v", plan.Changes)
	}

	// The same file reached through two roots is planned once.
	deduped, err := BuildPlan(migCovTextReplaceSpec("dedupe"), root, root)
	if err != nil {
		t.Fatalf("BuildPlan(duplicate roots) error = %v", err)
	}
	if len(deduped.Changes) != 2 {
		t.Fatalf("deduplicated changes = %+v", deduped.Changes)
	}
}

func TestMigCovBuildPlanReportsTransformFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.go"), []byte("package p\nfunc (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		Format: MigrationFormatV1, ID: "transform-error",
		Steps: []Step{{Kind: "import.replace", Language: "go", From: "a", To: "b"}},
	}
	_, err := BuildPlan(spec, root)
	if err == nil || !strings.Contains(err.Error(), "broken.go") || !strings.Contains(err.Error(), "parse Go source") {
		t.Fatalf("BuildPlan(syntax error) = %v", err)
	}
}

func TestMigCovApplyRefusesMissingFileAndUnwritableDirectory(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "gone.py")
	if err := Apply(Plan{Changes: []FileChange{{Path: missing, Updated: []byte("new")}}}); err == nil ||
		!strings.Contains(err.Error(), "read "+missing) {
		t.Fatalf("Apply(missing file) = %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "target.py")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(migCovTextReplaceSpec("unwritable"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %+v", plan.Changes)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	// Filesystems and platforms that do not enforce directory write bits
	// (Windows, or a root user) cannot express this refusal; the source file
	// is still unchanged there, so the test has nothing to assert.
	if probe, probeErr := os.CreateTemp(dir, ".probe-*"); probeErr == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("filesystem does not enforce directory write permissions")
	}
	err = Apply(plan)
	if err == nil {
		t.Fatalf("Apply(unwritable directory) = nil, want a temp-file error")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "old\n" {
		t.Fatalf("source file changed despite refusal: %q %v", got, readErr)
	}
}

func TestMigCovIgnoredDirectoryAndMatchPath(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".git", ".hg", ".svn", "node_modules", "vendor", ".venv", "dist", "build", ".cache"} {
		if !ignoredDirectory(name) {
			t.Errorf("ignoredDirectory(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"src", "internal", "cmd"} {
		if ignoredDirectory(name) {
			t.Errorf("ignoredDirectory(%q) = true, want false", name)
		}
	}

	tests := []struct {
		path    string
		pattern string
		want    bool
	}{
		{path: "pkg/a.go", pattern: "pkg/a.go", want: true},
		{path: "pkg/a.go", pattern: "pkg/b.go", want: false},
		{path: "pkg/a.go", pattern: "**/pkg/a.go", want: true},
		{path: "pkg/a.go", pattern: "pkg/**", want: true},
		{path: "other/a.go", pattern: "pkg/**", want: false},
		{path: "a.go", pattern: "**/*.go", want: true},
		// Only the leading "**/" is stripped; the rest is a single path
		// segment glob, so a nested path is deliberately not matched.
		{path: "pkg/a.go", pattern: "**/*.go", want: false},
		{path: "pkg/a.go", pattern: "pkg/*.go", want: true},
		{path: "pkg/nested/a.go", pattern: "pkg/*.go", want: false},
		{path: "pkg/a.go", pattern: "[", want: false},
	}
	for _, test := range tests {
		if got := matchPath(test.path, test.pattern); got != test.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", test.path, test.pattern, got, test.want)
		}
	}
}

func TestMigCovScopeMatchesExcludeThenInclude(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		scope Scope
		path  string
		want  bool
	}{
		{name: "empty scope matches everything", scope: Scope{}, path: "pkg/a.go", want: true},
		{name: "exclude wins", scope: Scope{Exclude: []string{"pkg/generated/**"}}, path: "pkg/generated/a.go", want: false},
		{name: "include listed", scope: Scope{Include: []string{"pkg/**"}}, path: "pkg/a.go", want: true},
		{name: "include unlisted", scope: Scope{Include: []string{"pkg/**"}}, path: "cmd/a.go", want: false},
		{name: "include beats default", scope: Scope{Include: []string{"cmd/**"}, Exclude: []string{"pkg/**"}}, path: "cmd/a.go", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.scope.matches(test.path); got != test.want {
				t.Fatalf("matches(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestMigCovReviewFindingsSkipsUnmatchedAndExcluded(t *testing.T) {
	t.Parallel()
	source := []byte("package p\n\nvar a = legacy.One\nvar b = legacy.Two\n")
	rules := []ReviewRule{
		{ID: "other-language", Language: "python", Pattern: "legacy", Message: "python only"},
		{ID: "no-match", Language: "go", Pattern: "absent", Message: "never matches"},
		{ID: "all-excluded", Language: "go", Pattern: "legacy[.]", ExcludePattern: "legacy[.]", Message: "excluded"},
		{ID: "kept", Language: "go", Pattern: "legacy[.](One)", Message: "kept"},
	}
	findings := reviewFindings(rules, "go", source, "example.go")
	if len(findings) != 1 || findings[0].RuleID != "kept" || len(findings[0].Lines) != 1 || findings[0].Lines[0] != 3 {
		t.Fatalf("findings = %+v, want only the kept rule on line 3", findings)
	}
}

func TestMigCovSortFindingsOrdersByPathThenRule(t *testing.T) {
	t.Parallel()
	findings := []Finding{
		{Path: "b.go", RuleID: "a"},
		{Path: "a.go", RuleID: "z"},
		{Path: "a.go", RuleID: "a"},
		{Path: "a.go", RuleID: "m"},
	}
	sortFindings(findings)
	want := []string{"a.go/a", "a.go/m", "a.go/z", "b.go/a"}
	got := make([]string, len(findings))
	for i, finding := range findings {
		got[i] = finding.Path + "/" + finding.RuleID
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sorted findings = %v, want %v", got, want)
	}
}
