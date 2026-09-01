package fleetsync

import (
	"strings"
	"testing"
	"time"
)

func testMeta() RunMeta {
	return RunMeta{
		StartedAt:    time.Date(2026, 9, 1, 10, 15, 0, 0, time.UTC),
		ProjectsRoot: "/home/ai/projects",
		Scanned:      214,
	}
}

func TestIssuesMarkdownCleanRunSaysNothingRequiresAttention(t *testing.T) {
	got := IssuesMarkdown(testMeta(), nil)
	for _, want := range []string{
		"# WB sync issues",
		"2026-09-01T10:15:00Z",
		"/home/ai/projects",
		"**Scanned:** 214 repositories",
		"**Issues:** none",
		"All repositories are in sync. Nothing requires attention.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Needs attention") {
		t.Errorf("clean run must not render an attention section:\n%s", got)
	}
}

func TestIssuesMarkdownStampsDryRun(t *testing.T) {
	meta := testMeta()
	meta.DryRun = true
	got := IssuesMarkdown(meta, nil)
	if !strings.Contains(got, "**Dry run:**") {
		t.Errorf("dry run not stamped:\n%s", got)
	}
	if !strings.Contains(got, "the fleet was not modified") {
		t.Errorf("dry run stamp must explain the consequence:\n%s", got)
	}
}

func TestIssuesMarkdownIsDeterministic(t *testing.T) {
	first := IssuesMarkdown(testMeta(), nil)
	second := IssuesMarkdown(testMeta(), nil)
	if first != second {
		t.Fatal("two renders of identical input differ")
	}
}
