package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnalyzeDriftReportsDivergentFleetVersions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	api := newBumpRepository(t, root, githubDir, "api", "module example.com/api\n\ngo 1.22\n\nrequire example.com/sdk v1.8.0\n")
	facade := newBumpRepository(t, root, githubDir, "facade", "module example.com/facade\n\ngo 1.22\n\nrequire example.com/sdk v1.7.2\n")
	renderer := newBumpRepository(t, root, githubDir, "renderer", "module example.com/renderer\n\ngo 1.22\n\nrequire example.com/sdk v1.5.0\n")

	report, err := AnalyzeDrift(context.Background(), []Repository{api, facade, renderer}, DriftOptions{
		GitHubDir: githubDir, Ref: "main", Parallel: 2, Now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Repositories != 3 {
		t.Fatalf("repositories = %d", report.Summary.Repositories)
	}
	group := driftGroup(t, report, "example.com/sdk")
	if group.Classification != DriftDivergent {
		t.Fatalf("classification = %s, want divergent: %+v", group.Classification, group)
	}
	if distinctSelectedVersions(group.Versions) != 3 {
		t.Fatalf("versions = %+v", group.Versions)
	}
	if group.Latest == nil || group.Latest.Source != "not_queried_offline" {
		t.Fatalf("latest = %+v", group.Latest)
	}
	if DriftFailed(report, false) {
		t.Fatal("divergent drift must not fail without --fail-on-drift")
	}
	if !DriftFailed(report, true) {
		t.Fatal("--fail-on-drift must fail on divergent selections")
	}
}

func TestAnalyzeDriftClassifiesLocalReplace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	payments := newBumpRepository(t, root, githubDir, "payments", ""+
		"module example.com/payments\n\n"+
		"go 1.22\n\n"+
		"require example.com/money v0.9.4\n\n"+
		"replace example.com/money => ../money\n")

	report, err := AnalyzeDrift(context.Background(), []Repository{payments}, DriftOptions{
		GitHubDir: githubDir, Ref: "main", Parallel: 1,
		Now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	group := driftGroup(t, report, "example.com/money")
	if group.Classification != DriftReplaced {
		t.Fatalf("classification = %s, want replaced: %+v", group.Classification, group)
	}
	dependency := report.Repositories[0].Dependencies[0]
	if dependency.Replacement == nil || !dependency.Replacement.Local || dependency.Replacement.NewPath != "../money" {
		t.Fatalf("replacement = %+v", dependency.Replacement)
	}
	if !DriftFailed(report, true) {
		t.Fatal("--fail-on-drift must fail on replaced modules")
	}
}

func TestAnalyzeDriftReportsMajorPathSplit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	search := newBumpRepository(t, root, githubDir, "search", ""+
		"module example.com/search\n\n"+
		"go 1.22\n\n"+
		"require (\n"+
		"\texample.com/query v1.2.0\n"+
		"\texample.com/query/v2 v2.0.0\n"+
		")\n")

	report, err := AnalyzeDrift(context.Background(), []Repository{search}, DriftOptions{
		GitHubDir: githubDir, Ref: "main", Parallel: 1,
		Now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	foundSplit := false
	for _, group := range report.Groups {
		if group.Classification == DriftMajorPathSplit {
			foundSplit = true
			if len(group.MajorPaths) < 2 {
				t.Fatalf("major paths = %+v", group.MajorPaths)
			}
		}
	}
	if !foundSplit {
		t.Fatalf("groups = %+v, want a major_path_split classification", report.Groups)
	}
	if !DriftFailed(report, true) {
		t.Fatal("--fail-on-drift must fail on major-path splits")
	}
}

func TestAnalyzeDriftFiltersExactDependency(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	app := newBumpRepository(t, root, githubDir, "app", ""+
		"module example.com/app\n\n"+
		"go 1.22\n\n"+
		"require (\n"+
		"\texample.com/sdk v1.0.0\n"+
		"\texample.com/other v0.2.0\n"+
		")\n")

	report, err := AnalyzeDrift(context.Background(), []Repository{app}, DriftOptions{
		GitHubDir: githubDir, Dependencies: []string{"example.com/sdk"}, Parallel: 1,
		Now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 1 || report.Groups[0].Dependency != "example.com/sdk" {
		t.Fatalf("groups = %+v", report.Groups)
	}
}

func TestDriftMarkdownAndReportsAreDeterministic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repo := newBumpRepository(t, root, githubDir, "lib", "module example.com/lib\n\ngo 1.22\n\nrequire example.com/sdk v1.0.0\n")
	report, err := AnalyzeDrift(context.Background(), []Repository{repo}, DriftOptions{
		GitHubDir: githubDir, Parallel: 1,
		Now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	markdown := report.Markdown()
	if !strings.Contains(markdown, "example.com/sdk") || !strings.Contains(markdown, "`converged`") {
		t.Fatalf("markdown missing expected markers:\n%s", markdown)
	}
	dir := filepath.Join(root, "report")
	if err := WriteDriftReports(dir, report); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deps-drift.md", "deps-drift.yaml", "deps-drift.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func driftGroup(t *testing.T, report DriftReport, dependency string) DriftVersionGroup {
	t.Helper()
	for _, group := range report.Groups {
		if group.Dependency == dependency {
			return group
		}
	}
	t.Fatalf("missing group %s in %+v", dependency, report.Groups)
	return DriftVersionGroup{}
}
