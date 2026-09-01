package fleetsync

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
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

func TestIssuesMarkdownRendersDivergedEntry(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "sneat-co", Name: "competios", Path: "/home/ai/projects/sneat-co/competios"},
		Status: Diverged,
		Tracking: gitops.TrackingState{
			Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 5, Configured: true,
		},
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"## Needs attention",
		"### sneat-co/competios — diverged",
		"**Clone:** `/home/ai/projects/sneat-co/competios`",
		"main is 2 ahead, 5 behind origin/main",
		"not pulled",
		"**Inspect**",
		"git -C /home/ai/projects/sneat-co/competios log --oneline --left-right main...origin/main",
		"**Resolve**",
		"wb worktree create",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownRendersNoUpstreamEntry(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "sneat-dev", Name: "wb", Path: "/home/ai/projects/sneat-dev/wb"},
		Status: NoUpstream,
		Tracking: gitops.TrackingState{
			Branch: "fix/auth", Configured: true,
		},
		Detail: gitops.RepoStatus{Unpushed: []string{"2fb7069 fix(sync): fail on auth"}},
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"### sneat-dev/wb — no upstream",
		"fix/auth tracks an upstream that no longer resolves",
		"push -u origin fix/auth",
		"git -C /home/ai/projects/sneat-dev/wb log --oneline origin/main..HEAD",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownRendersUnpushedEntry(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"},
		Status: Unpushed,
		Detail: gitops.RepoStatus{Unpushed: []string{"abc1234 wip"}},
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"### o/r — unpushed commits",
		"pulled, but holds 1 unpushed commit",
		"git -C /p/o/r log --oneline --branches --not --remotes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownRendersArchivedUnlandableEntryWithReason(t *testing.T) {
	results := []Result{{
		Repo:     discover.Repo{Org: "o", Name: "old", Path: "/p/o/old"},
		Status:   ArchivedUnlandable,
		Archived: true,
		Detail:   gitops.RepoStatus{Unpushed: []string{"abc1234 wip", "def5678 more"}},
		Reason:   "2 unpushed commits on branch main",
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"### o/old — archived, holds unpushed commits",
		"can never be pushed",
		"2 unpushed commits on branch main",
		"Unarchive the repository",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownSeparatesArchivedNotPrunedFromDefects(t *testing.T) {
	results := []Result{{
		Repo:              discover.Repo{Org: "o", Name: "stale", Path: "/p/o/stale"},
		Status:            Pulled,
		Archived:          true,
		ArchivedNotPruned: true,
	}}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "## Archived, not pruned") {
		t.Errorf("missing informational section:\n%s", got)
	}
	if !strings.Contains(got, "Nothing is broken") {
		t.Errorf("informational section must say nothing is broken:\n%s", got)
	}
	if !strings.Contains(got, "--prune-archived") {
		t.Errorf("informational section must name the flag:\n%s", got)
	}
	if strings.Contains(got, "## Needs attention") {
		t.Errorf("an archived-not-pruned repo is not a defect:\n%s", got)
	}
}

func TestIssuesMarkdownQuotesPathsNeedingIt(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "o", Name: "r", Path: "/p/with space/o/r"},
		Status: Unpushed,
		Detail: gitops.RepoStatus{Unpushed: []string{"abc1234 wip"}},
	}}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "git -C '/p/with space/o/r'") {
		t.Errorf("path with a space must be shell-quoted:\n%s", got)
	}
}

func TestIssuesMarkdownDetachedHeadOffersNoBranchPublish(t *testing.T) {
	results := []Result{{
		Repo:     discover.Repo{Org: "o", Name: "detached", Path: "/p/o/detached"},
		Status:   NoUpstream,
		Tracking: gitops.TrackingState{},
	}}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "detached HEAD") {
		t.Errorf("detached state not described:\n%s", got)
	}
	if strings.Contains(got, "push -u origin \n") || strings.Contains(got, "push -u origin `") {
		t.Errorf("a detached HEAD has no branch to publish, but a push command was rendered:\n%s", got)
	}
	if !strings.Contains(got, "switch -c <branch>") {
		t.Errorf("detached HEAD needs a put-it-on-a-branch option:\n%s", got)
	}
}

func TestIssuesMarkdownAnchorsEveryMutatingCommandToItsClone(t *testing.T) {
	results := []Result{{
		Repo:     discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"},
		Status:   NoUpstream,
		Tracking: gitops.TrackingState{Branch: "feature", Configured: true},
	}}
	got := IssuesMarkdown(testMeta(), results)
	if strings.Contains(got, "`git push") {
		t.Errorf("a mutating command must carry -C <path> so it cannot act on the wrong repository:\n%s", got)
	}
	if !strings.Contains(got, "git -C /p/o/r push -u origin feature") {
		t.Errorf("push command not anchored to the clone:\n%s", got)
	}
}

func TestIssuesMarkdownRendersErrorsVerbatim(t *testing.T) {
	results := []Result{{
		Repo:   discover.Repo{Org: "o", Name: "broken", Path: "/p/o/broken"},
		Status: Failed,
		Err:    errors.New("git pull: could not read Username for 'https://github.com'"),
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"## Errors",
		"### o/broken — failed",
		"could not read Username for 'https://github.com'",
		"gh auth status -h github.com",
		"gh auth login -h github.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestIssuesMarkdownCountsErrorsInHeader(t *testing.T) {
	results := []Result{
		{Repo: discover.Repo{Org: "o", Name: "a", Path: "/p/o/a"}, Status: Failed, Err: errors.New("boom")},
		{Repo: discover.Repo{Org: "o", Name: "b", Path: "/p/o/b"}, Status: Unpushed,
			Detail: gitops.RepoStatus{Unpushed: []string{"abc1234 wip"}}},
	}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "**Needs attention:** 1") {
		t.Errorf("attention count wrong:\n%s", got)
	}
	if !strings.Contains(got, "**Errors:** 1") {
		t.Errorf("error count wrong:\n%s", got)
	}
	if strings.Contains(got, "**Issues:** none") {
		t.Errorf("a run with issues must not claim none:\n%s", got)
	}
}

func TestIssuesMarkdownReportsRunThatFailedBeforeScanning(t *testing.T) {
	meta := testMeta()
	meta.Scanned = 0
	meta.RunErr = errors.New("GitHub authentication failed: gh: not logged in")
	got := IssuesMarkdown(meta, nil)
	for _, want := range []string{
		"## Run failed",
		"gh: not logged in",
		"no repository was scanned",
		"gh auth login -h github.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "All repositories are in sync") {
		t.Errorf("a failed run must never claim the fleet is in sync:\n%s", got)
	}
}

func TestIssuesMarkdownOmitsTheInspectNoteWhenNothingIsInspectable(t *testing.T) {
	results := []Result{{
		Repo:              discover.Repo{Org: "o", Name: "stale", Path: "/p/o/stale"},
		Status:            Pulled,
		Archived:          true,
		ArchivedNotPruned: true,
	}}
	got := IssuesMarkdown(testMeta(), results)
	if !strings.Contains(got, "## Archived, not pruned") {
		t.Fatalf("informational section missing:\n%s", got)
	}
	if strings.Contains(got, "Inspection commands are read-only") {
		t.Errorf("a report with no Inspect section must not carry the inspect-first note:\n%s", got)
	}
}
