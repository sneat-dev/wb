package layout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTailCovAuditRootValidationErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	if _, err := Audit(ctx, "   "); err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("Audit(blank) err = %v, want required error", err)
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := Audit(ctx, missing); err == nil {
		t.Fatal("Audit(missing) must fail")
	}
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Audit(ctx, file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Audit(file) err = %v, want not-a-directory error", err)
	}
	if _, err := Clean(ctx, missing, CleanOptions{}); err == nil {
		t.Fatal("Clean(missing) must fail")
	}
	if _, err := Counts(ctx, "  "); err == nil {
		t.Fatal("Counts(blank) must fail")
	}
}

func TestTailCovAuditSkipsFilesAndHiddenEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(root, ".cache")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	// A hidden canonical-looking directory must still be skipped.
	if err := os.MkdirAll(filepath.Join(hidden, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", report.Findings)
	}
	if report.Summary.Inspected != 0 {
		t.Fatalf("summary = %+v, want zero inspected", report.Summary)
	}
	if Failed(report) {
		t.Fatal("an empty audit must not fail")
	}
	markdown := report.Markdown()
	if !strings.Contains(markdown, "No Git checkouts found under the projects root.") {
		t.Fatalf("markdown = %q", markdown)
	}
}

func TestTailCovAuditReportsUnreadableOwnerDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not deny reads")
	}
	t.Parallel()
	root := t.TempDir()
	locked := filepath.Join(root, "acme")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Unreadable != 1 || report.Summary.Inspected != 1 {
		t.Fatalf("summary = %+v, want one unreadable finding", report.Summary)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %+v", report.Findings)
	}
	finding := report.Findings[0]
	if finding.Kind != KindUnreadable || finding.PathSlug != "acme" {
		t.Fatalf("finding = %+v", finding)
	}
	if !strings.Contains(finding.Reason, "cannot read owner directory") {
		t.Fatalf("reason = %q", finding.Reason)
	}
	if !Failed(report) {
		t.Fatal("unreadable findings must fail the audit")
	}
	counts, err := Counts(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Unreadable != 1 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestTailCovAuditReportsNoOriginCheckouts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Canonical-shaped path with no origin remote at all.
	noOriginCanonical := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(filepath.Join(noOriginCanonical, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Top-level git directory with no origin remote.
	noOriginTop := filepath.Join(root, "solo")
	if err := os.MkdirAll(filepath.Join(noOriginTop, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.NoOrigin != 2 {
		t.Fatalf("summary = %+v, want two no-origin findings", report.Summary)
	}
	for _, finding := range report.Findings {
		if finding.Kind != KindNoOrigin {
			t.Fatalf("finding = %+v, want no_origin", finding)
		}
		if finding.OriginSlug != "" {
			t.Fatalf("origin slug = %q, want empty", finding.OriginSlug)
		}
	}
	if !Failed(report) {
		t.Fatal("no-origin findings must fail the audit")
	}
}

func TestTailCovAuditReportsUnreadableProjectsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not deny reads")
	}
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if _, err := Audit(context.Background(), root); err == nil {
		t.Fatal("Audit on an unreadable projects root must fail")
	}
}

func TestTailCovAbsoluteRootAndRemoveContainedPathRejectUnresolvablePaths(t *testing.T) {
	t.Parallel()
	base := t.TempDir()

	if _, err := absoluteRoot(filepath.Join(base, "does-not-exist")); err == nil {
		t.Fatal("absoluteRoot must fail for a missing path")
	}
	// filepath.Abs resolves a relative path against the current directory, so a
	// relative path that does not exist must fail the directory check.
	if _, err := absoluteRoot(filepath.Join("relative", "missing", filepath.Base(base))); err == nil {
		t.Fatal("absoluteRoot must fail for a relative path that does not exist")
	}
	if err := removeContainedPath(filepath.Join(base, "missing-root"), filepath.Join(base, "target")); err == nil {
		t.Fatal("removeContainedPath must fail when the target does not exist")
	}
	if err := removeContainedPath(filepath.Join(base, "missing-root"), "relative-target"); err == nil {
		t.Fatal("removeContainedPath must fail when the target does not exist")
	}
}

func TestTailCovOriginSlugPathShapes(t *testing.T) {
	// Not parallel: the subprocess inherits a neutralized global git config so
	// that a developer's url.*.insteadOf rewrite cannot change the remote URL.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()

	for _, test := range []struct {
		name string
		url  string
		want string
	}{
		{name: "scp style", url: "git@github.com:acme/app.git", want: "acme/app"},
		{name: "https style", url: "https://github.com/acme/app.git", want: "acme/app"},
		{name: "https trailing slash", url: "https://github.com/acme/app/", want: "acme/app"},
		{name: "self hosted", url: "https://git.example.test/team/sub/app.git", want: "sub/app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(root, test.name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, dir, "git", "init", "-b", "main")
			run(t, dir, "git", "remote", "add", "origin", test.url)

			got, err := OriginSlug(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("OriginSlug(%q) = %q, want %q", test.url, got, test.want)
			}
		})
	}
}

func TestTailCovOriginSlugRejectsSingleElementRemotes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	for _, test := range []struct {
		name string
		url  string
	}{
		{name: "bare name", url: "solo"},
		{name: "github colon empty", url: "git@github.com:.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(root, test.name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, dir, "git", "init", "-b", "main")
			run(t, dir, "git", "remote", "add", "origin", test.url)

			got, err := OriginSlug(context.Background(), dir)
			if err == nil {
				t.Fatalf("OriginSlug(%q) = %q, want error", test.url, got)
			}
			if !strings.Contains(err.Error(), "cannot derive owner/repository") &&
				!strings.Contains(err.Error(), "does not identify owner/repository") {
				t.Fatalf("err = %v", err)
			}
		})
	}

	if _, err := OriginSlug(context.Background(), filepath.Join(root, "not-a-repo")); err == nil {
		t.Fatal("OriginSlug on a non-repo must fail")
	}
}

func TestTailCovSplitSlug(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		slug      string
		owner     string
		name      string
		ok        bool
		assertion string
	}{
		{slug: "acme/app", owner: "acme", name: "app", ok: true},
		{slug: "/acme/app/", owner: "acme", name: "app", ok: true},
		{slug: "acme", ok: false},
		{slug: "/app", ok: false},
		{slug: "acme/", ok: false},
		{slug: "acme/group/app", ok: false},
	} {
		owner, name, ok := splitSlug(test.slug)
		if ok != test.ok || owner != test.owner || name != test.name {
			t.Fatalf("splitSlug(%q) = (%q, %q, %t), want (%q, %q, %t)",
				test.slug, owner, name, ok, test.owner, test.name, test.ok)
		}
	}
}

func TestTailCovCleanSortsMultipleTopLevelActions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seedRemoteClone(t, root, "zeta", "acme/zeta", filepath.Join(root, "zeta"))
	seedRemoteClone(t, root, "alpha", "acme/alpha", filepath.Join(root, "alpha"))

	report, err := Clean(context.Background(), root, CleanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Actions) != 2 {
		t.Fatalf("actions = %+v, want two", report.Actions)
	}
	if report.Actions[0].Path >= report.Actions[1].Path {
		t.Fatalf("actions are not sorted: %+v", report.Actions)
	}
	if !report.DryRun {
		t.Fatal("dry-run report must set DryRun")
	}
	if CleanFailed(report) {
		t.Fatal("planned actions must not fail a dry run")
	}
	markdown := report.Markdown()
	if !strings.Contains(markdown, "- Mode: `dry-run`") {
		t.Fatalf("markdown = %q", markdown)
	}
	if !strings.Contains(markdown, "| Path | Status | Origin | Reason |") {
		t.Fatalf("markdown = %q", markdown)
	}
}

func TestTailCovCleanApplyFailureMarksError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not deny writes")
	}
	t.Parallel()
	root := t.TempDir()
	_ = initRemoteClone(t, root, "acme", "app", "acme/app")
	top := filepath.Join(root, "app")
	cloneFrom(t, filepath.Join(root, "acme", "app.git"), top)

	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	report, err := Clean(context.Background(), root, CleanOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Actions) != 1 || report.Actions[0].Status != "error" {
		t.Fatalf("actions = %+v, want one error action", report.Actions)
	}
	if report.Actions[0].Reason == "" {
		t.Fatal("error action must explain the failure")
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatalf("failed removal must leave the clone in place: %v", err)
	}
	if !CleanFailed(report) {
		t.Fatal("an error action must fail the clean report")
	}
	if report.DryRun {
		t.Fatal("apply report must not set DryRun")
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "- Mode: `apply`") {
		t.Fatalf("markdown = %q", markdown)
	}
}

func TestTailCovCleanAllowsMissingCanonicalWhenPermitted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	top := filepath.Join(root, "solo")
	seedRemoteClone(t, root, "solo", "acme/solo", top)

	blocked, err := Clean(context.Background(), root, CleanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Actions) != 1 || blocked.Actions[0].Status != "skipped" {
		t.Fatalf("actions = %+v, want a skip without --allow-missing-canonical", blocked.Actions)
	}
	if !strings.Contains(blocked.Actions[0].Reason, "canonical clone is missing") {
		t.Fatalf("reason = %q", blocked.Actions[0].Reason)
	}

	allowed, err := Clean(context.Background(), root, CleanOptions{AllowMissingCanonical: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Actions) != 1 || allowed.Actions[0].Status != "planned" {
		t.Fatalf("actions = %+v, want planned", allowed.Actions)
	}
	if !strings.Contains(allowed.Actions[0].Reason, "would remove the only local copy") {
		t.Fatalf("reason = %q", allowed.Actions[0].Reason)
	}
	if allowed.Actions[0].OriginSlug != "acme/solo" {
		t.Fatalf("origin slug = %q", allowed.Actions[0].OriginSlug)
	}
}

func TestTailCovEvaluateTopLevelCleanGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()

	noOrigin := evaluateTopLevelClean(ctx, root, Finding{Path: filepath.Join(root, "a"), Kind: KindTopLevel}, CleanOptions{})
	if noOrigin.Status != "skipped" || !strings.Contains(noOrigin.Reason, "without a usable origin") {
		t.Fatalf("action = %+v", noOrigin)
	}

	noExpected := evaluateTopLevelClean(ctx, root, Finding{
		Path: filepath.Join(root, "b"), Kind: KindTopLevel, OriginSlug: "acme/app",
	}, CleanOptions{})
	if noExpected.Status != "skipped" || !strings.Contains(noExpected.Reason, "cannot derive canonical path") {
		t.Fatalf("action = %+v", noExpected)
	}

	bogus := evaluateTopLevelClean(ctx, root, Finding{
		Path:         filepath.Join(root, "not-a-repo"),
		Kind:         KindTopLevel,
		OriginSlug:   "acme/app",
		ExpectedPath: filepath.Join(root, "acme", "app"),
	}, CleanOptions{AllowMissingCanonical: true})
	if bogus.Status != "error" {
		t.Fatalf("action = %+v, want error from git status", bogus)
	}
	if bogus.Reason == "" {
		t.Fatal("error action must carry the git failure")
	}
}

func TestTailCovEvaluateTopLevelCleanSkipsDirtyTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "app", "acme/app")
	_ = canonical
	top := filepath.Join(root, "app")
	cloneFrom(t, filepath.Join(root, "acme", "app.git"), top)
	if err := os.WriteFile(filepath.Join(top, "wip.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	action := evaluateTopLevelClean(context.Background(), root, Finding{
		Path:            top,
		Kind:            KindTopLevel,
		OriginSlug:      "acme/app",
		ExpectedPath:    canonical,
		CanonicalExists: true,
	}, CleanOptions{Apply: true})
	if action.Status != "skipped" || !strings.Contains(action.Reason, "not clean") {
		t.Fatalf("action = %+v", action)
	}
}

func TestTailCovRemoveContainedPathGuards(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	if err := removeContainedPath(root, root); err == nil || !strings.Contains(err.Error(), "outside projects root") {
		t.Fatalf("removing the root err = %v", err)
	}
	outside := filepath.Join(filepath.Dir(root), "elsewhere-"+filepath.Base(root))
	if err := removeContainedPath(root, outside); err == nil || !strings.Contains(err.Error(), "outside projects root") {
		t.Fatalf("removing an outside path err = %v", err)
	}
	nested := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeContainedPath(root, nested); err == nil || !strings.Contains(err.Error(), "non-top-level") {
		t.Fatalf("removing a nested path err = %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("guarded path must survive: %v", err)
	}

	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeContainedPath(root, child); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatalf("top-level child must be removed, stat err = %v", err)
	}
}

func TestTailCovReportMarkdownRendersFindings(t *testing.T) {
	t.Parallel()
	report := Report{
		SchemaVersion: 1,
		ProjectsRoot:  "/fleet/projects",
		ObservedAt:    time.Date(2026, 9, 15, 13, 12, 36, 0, time.UTC),
		Summary:       Summary{Inspected: 2, OK: 1, TopLevel: 1},
		Findings: []Finding{
			{Path: "/fleet/projects/app", Kind: KindTopLevel, PathSlug: "app", OriginSlug: "acme/app", Reason: "needs | escaping\nand newline"},
			{Path: "/fleet/projects/acme/app", Kind: KindOK, PathSlug: "acme/app", Reason: "ok"},
		},
	}
	markdown := report.Markdown()
	for _, want := range []string{
		"# WB layout audit",
		"- Projects root: `/fleet/projects`",
		"- Observed at: `2026-09-15T13:12:36Z`",
		"- Inspected: `2` · ok: `1` · top-level: `1` · misowned: `0` · no-origin: `0` · unreadable: `0`",
		"| `/fleet/projects/app` | `top_level` | `app` | `acme/app` | needs \\| escaping and newline |",
		"| `/fleet/projects/acme/app` | `ok` | `acme/app` | `—` | ok |",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestTailCovCleanReportMarkdownEmpty(t *testing.T) {
	t.Parallel()
	report := CleanReport{SchemaVersion: 1, ProjectsRoot: "/fleet/projects", DryRun: true}
	markdown := report.Markdown()
	if !strings.Contains(markdown, "No top-level clones to consider.") {
		t.Fatalf("markdown = %q", markdown)
	}
	if !strings.Contains(markdown, "- Mode: `dry-run`") {
		t.Fatalf("markdown = %q", markdown)
	}
}
