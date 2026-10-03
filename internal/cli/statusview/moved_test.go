package statusview

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
)

func TestStatusMarkdownAnnouncesHiddenRepositories(t *testing.T) {
	t.Parallel()
	partial := Markdown(repostatus.Index{HiddenClean: 4, Repositories: []repostatus.Row{
		{Repository: "acme/dirty", Status: "attention", Summary: "1 modified file"},
	}}, false, "# WB local repository status\n\n")
	for _, want := range []string{"acme/dirty", "4 clean repositories hidden", "--all"} {
		if !strings.Contains(partial, want) {
			t.Errorf("filtered markdown missing %q:\n%s", want, partial)
		}
	}

	singular := Markdown(repostatus.Index{HiddenClean: 1, Repositories: []repostatus.Row{
		{Repository: "acme/dirty", Status: "attention"},
	}}, false, "# WB local repository status\n\n")
	if !strings.Contains(singular, "1 clean repository hidden") {
		t.Errorf("filtered markdown does not read naturally for one Repository:\n%s", singular)
	}
}

func TestStatusMarkdownReportsAnAllCleanFleet(t *testing.T) {
	t.Parallel()
	markdown := Markdown(repostatus.Index{SchemaVersion: 1, HiddenClean: 12}, false, "# WB local repository status\n\n")
	if !strings.Contains(markdown, "All 12 inspected repositories are clean.") {
		t.Errorf("all-clean markdown does not say so:\n%s", markdown)
	}
	if strings.Contains(markdown, "| Repository |") {
		t.Errorf("all-clean markdown still prints an empty table:\n%s", markdown)
	}

	one := Markdown(repostatus.Index{SchemaVersion: 1, HiddenClean: 1}, false, "# WB local repository status\n\n")
	if !strings.Contains(one, "The inspected repository is clean.") {
		t.Errorf("all-clean markdown does not read naturally for one Repository:\n%s", one)
	}
}

func TestStatusMarkdownUnfilteredHasNoNote(t *testing.T) {
	t.Parallel()
	markdown := Markdown(repostatus.Index{SchemaVersion: 1, Repositories: []repostatus.Row{
		{Repository: "acme/clean", Status: "clean"},
	}}, false, "# WB local repository status\n\n")
	if strings.Contains(markdown, "--all") {
		t.Errorf("an unfiltered report advertises --all:\n%s", markdown)
	}
}

func TestStatusMarkdownAttributesUnpushedCommits(t *testing.T) {
	t.Parallel()
	markdown := Markdown(repostatus.Index{Repositories: []repostatus.Row{{
		Repository: "acme/app",
		Status:     "attention",
		Summary:    "1 unpushed commit",
		Unpushed:   []string{"abc1234 local work"},
		UnpushedBranches: []gitops.UnpushedBranch{{
			Branch: "feature", Worktree: "/projects/.wb/worktrees/task/acme/app", Commits: []string{"abc1234 local work"},
		}},
	}}}, true, "# Status\n\n")
	for _, want := range []string{"acme/app — Unpushed", "Branch `feature`", "worktree `/projects/.wb/worktrees/task/acme/app`", "`abc1234 local work`"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("attributed markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestStatusMarkdownReportsAttention(t *testing.T) {
	t.Parallel()
	report := repostatus.Index{Repositories: []repostatus.Row{{Repository: "acme/repo", Status: "attention", Summary: "1 modified file", Modified: []string{"main.go"}}}}
	markdown := Markdown(report, true, "# WB local repository status\n\n")
	for _, want := range []string{"attention", "1 modified file", "main.go"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestStatusMarkdownTitleChoosesTheRightHeading(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		kind  statusTitleKind
		fleet bool
		want  string
	}{
		{statusTitleFleet, true, "# WB fleet status\n\n"},
		{statusTitleFleet, false, "# WB fleet status\n\n"},
		{statusTitleRepo, true, "# WB repository status\n\n"},
		{statusTitleRepo, false, "# WB repository status\n\n"},
		{statusTitleAuto, true, "# WB local repository status\n\n"},
		{statusTitleAuto, false, "# WB repository status\n\n"},
	} {
		if got := statusMarkdownTitle(test.kind, test.fleet); got != test.want {
			t.Errorf("statusMarkdownTitle(%d, fleet=%t) = %q, want %q", test.kind, test.fleet, got, test.want)
		}
	}
}

func TestStatusMarkdownAndOutput(t *testing.T) {
	t.Parallel()
	clean := repostatus.Index{SchemaVersion: 1, HiddenClean: 3}
	markdown := Markdown(clean, false, "# WB fleet status\n\n")
	if !strings.Contains(markdown, "All 3 inspected repositories are clean.") {
		t.Errorf("all-clean markdown = %q", markdown)
	}
	single := Markdown(repostatus.Index{SchemaVersion: 1, HiddenClean: 1}, false, "# WB fleet status\n\n")
	if !strings.Contains(single, "The inspected repository is clean.") {
		t.Errorf("single-clean markdown = %q", single)
	}

	report := repostatus.Index{SchemaVersion: 1, HiddenClean: 1, Repositories: []repostatus.Row{
		{Repository: "acme/dirty", Status: "attention", Summary: "2 modified", Modified: []string{"a.go", "b.go"}},
		{Repository: "acme/broken", Status: "error", Error: "not a repository"},
		{Repository: "acme/quiet", Status: "attention"},
	}}
	detailed := Markdown(report, true, "# WB status\n\n")
	for _, want := range []string{
		"# WB status", "| `acme/dirty` | `attention` | 2 modified |",
		"| `acme/broken` | `error` | not a repository |",
		"| `acme/quiet` | `attention` | — |",
		"clean repository hidden; pass `--all` to include it._",
	} {
		if !strings.Contains(detailed, want) {
			t.Errorf("status markdown missing %q:\n%s", want, detailed)
		}
	}
	brief := Markdown(report, false, "# WB status\n\n")
	if strings.Contains(brief, "a.go") {
		t.Errorf("details rendered without --details:\n%s", brief)
	}

	reportDir := filepath.Join(t.TempDir(), "reports")
	var err error
	var buffer bytes.Buffer
	err = WriteReports(report, reportDir, false, "")
	if err == nil {
		err = writeOutput(&buffer, report, "json", false, "")
	}
	out := buffer.String()
	if err != nil {
		t.Fatalf("status json: %v", err)
	}
	if !strings.Contains(out, `"repository": "acme/dirty"`) {
		t.Errorf("status json = %s", out)
	}
	for _, name := range []string{"status.md", "status.yaml"} {
		if _, statErr := os.Stat(filepath.Join(reportDir, name)); statErr != nil {
			t.Errorf("status report did not write %s: %v", name, statErr)
		}
	}
	if err := writeOutput(io.Discard, report, "toml", false, ""); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown status format = %v", err)
	}
	// An empty title falls back to the local-repository heading.
	buffer.Reset()
	err = writeOutput(&buffer, report, "markdown", false, "")
	out = buffer.String()
	if err != nil || !strings.Contains(out, "# WB local repository status") {
		t.Fatalf("default title output = %q (%v)", out, err)
	}
}

func TestStatusHiddenNoteAndDetails(t *testing.T) {
	t.Parallel()
	if got := statusHiddenNote(1); !strings.Contains(got, "1 clean repository hidden") || !strings.Contains(got, "include it") {
		t.Errorf("statusHiddenNote(1) = %q", got)
	}
	if got := statusHiddenNote(4); !strings.Contains(got, "4 clean repositories hidden") || !strings.Contains(got, "include them") {
		t.Errorf("statusHiddenNote(4) = %q", got)
	}

	var out strings.Builder
	writeStatusDetails(&out, repostatus.Row{
		Repository: "acme/app",
		Modified:   []string{"a.go"},
		Untracked:  []string{"notes.txt"},
		Conflicted: []string{"merge.go"},
		Stashed:    []string{"stash@{0}"},
	})
	text := out.String()
	for _, want := range []string{
		"acme/app — Modified:", "- `a.go`",
		"acme/app — Untracked:", "- `notes.txt`",
		"acme/app — Conflicted:", "- `merge.go`",
		"acme/app — Stashed:", "- `stash@{0}`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status details missing %q:\n%s", want, text)
		}
	}

	// Unpushed commits render as a group when there is no branch detail, and
	// as per-branch sections when there is.
	out.Reset()
	writeStatusDetails(&out, repostatus.Row{Repository: "acme/app", Unpushed: []string{"deadbee"}})
	if !strings.Contains(out.String(), "acme/app — Unpushed:") || !strings.Contains(out.String(), "- `deadbee`") {
		t.Errorf("plain unpushed details = %q", out.String())
	}

	out.Reset()
	writeStatusDetails(&out, repostatus.Row{
		Repository: "acme/app",
		UnpushedBranches: []gitops.UnpushedBranch{
			{Branch: "task/one", Worktree: "/tmp/wt/task-one", Commits: []string{"c1", "c2"}},
			{Branch: "task/two"},
		},
	})
	text = out.String()
	for _, want := range []string{
		"acme/app — Unpushed:",
		"- Branch `task/one` in worktree `/tmp/wt/task-one`:",
		"- `c1`", "- `c2`",
		"- Branch `task/two`:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unpushed-branch details missing %q:\n%s", want, text)
		}
	}

	// An empty detail set writes nothing at all.
	out.Reset()
	writeStatusDetails(&out, repostatus.Row{Repository: "acme/clean"})
	if out.Len() != 0 {
		t.Errorf("empty details wrote %q", out.String())
	}
}

func TestStatusProgressRendersCompletionCounter(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgress(&out, true)
	progress.start(2)
	progress.complete(
		reposelection.Target{Repository: "acme/one"},
		repostatus.Row{Repository: "acme/one", Status: "clean"},
	)
	progress.complete(
		reposelection.Target{Repository: "acme/two"},
		repostatus.Row{Repository: "acme/two", Status: "attention"},
	)
	progress.finish()

	rendered := out.String()
	for _, want := range []string{
		"status: 0/2 repositories inspected",
		"status: 1/2 repositories inspected; acme/one: clean",
		"status: 2/2 repositories inspected; acme/two: attention",
		"status: inspected 2 repositories in",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		t.Errorf("finished progress did not end its live line: %q", rendered)
	}
}

func TestStatusProgressHeartbeatRefreshesAndStops(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgressWithHeartbeat(&out, true, 5*time.Millisecond)
	progress.start(1)
	time.Sleep(18 * time.Millisecond)
	progress.finish()
	finishedLength := out.Len()
	if count := strings.Count(out.String(), "status: 0/1 repositories inspected"); count < 2 {
		t.Fatalf("heartbeat rendered status %d times, want at least 2: %q", count, out.String())
	}
	time.Sleep(12 * time.Millisecond)
	if out.Len() != finishedLength {
		t.Fatalf("heartbeat wrote after finish: before=%d after=%d", finishedLength, out.Len())
	}
}

func TestStatusProgressCanBeDisabled(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgress(&out, false)
	progress.start(1)
	progress.complete(
		reposelection.Target{Repository: "acme/one"},
		repostatus.Row{Repository: "acme/one", Status: "clean"},
	)
	progress.finish()
	if out.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", out.String())
	}
}

func TestWriteStatusOutputYAMLFormatSucceeds(t *testing.T) {
	t.Parallel()
	if err := writeOutput(io.Discard, repostatus.Index{SchemaVersion: 1}, "yaml", false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteStatusOutputJSONFormatSucceeds(t *testing.T) {
	t.Parallel()
	if err := writeOutput(io.Discard, repostatus.Index{SchemaVersion: 1}, "json", false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStatusProgressStartWithoutHeartbeatClosesStoppedDirectly(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgressWithHeartbeat(&out, true, 0)
	progress.start(2)
	progress.finish()
	if !strings.Contains(out.String(), "status: inspected 0 repositories in") {
		t.Fatalf("want a finish summary in output, got %q", out.String())
	}
}
