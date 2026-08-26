package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitops"
)

// TestHideCleanRepositoriesKeepsTheWorklist pins the default filter to the
// rows a caller can act on: clean is noise, attention is work, and error still
// has to reach statusFailed so the exit code does not change with the filter.
func TestHideCleanRepositoriesKeepsTheWorklist(t *testing.T) {
	t.Parallel()
	report := hideCleanRepositories(statusIndex{SchemaVersion: 1, Repositories: []repositoryStatusInfo{
		{Repository: "acme/clean", Status: "clean"},
		{Repository: "acme/dirty", Status: "attention", Summary: "1 modified file"},
		{Repository: "acme/broken", Status: "error", Error: "not a git repository"},
		{Repository: "acme/also-clean", Status: "clean"},
	}})
	if report.HiddenClean != 2 {
		t.Errorf("HiddenClean = %d, want 2", report.HiddenClean)
	}
	var kept []string
	for _, repository := range report.Repositories {
		kept = append(kept, repository.Repository)
	}
	if len(kept) != 2 || kept[0] != "acme/dirty" || kept[1] != "acme/broken" {
		t.Errorf("kept repositories = %v, want [acme/dirty acme/broken]", kept)
	}
	if !statusFailed(report) {
		t.Error("an error row was filtered away; the run would exit 0 despite an uninspectable repository")
	}
}

// TestStatusMarkdownAnnouncesHiddenRepositories guards against a silent
// filter: a shorter report must say what it left out and how to get it back.
func TestStatusMarkdownAnnouncesHiddenRepositories(t *testing.T) {
	t.Parallel()
	partial := statusMarkdown(statusIndex{HiddenClean: 4, Repositories: []repositoryStatusInfo{
		{Repository: "acme/dirty", Status: "attention", Summary: "1 modified file"},
	}}, false, "# WB local repository status\n\n")
	for _, want := range []string{"acme/dirty", "4 clean repositories hidden", "--all"} {
		if !strings.Contains(partial, want) {
			t.Errorf("filtered markdown missing %q:\n%s", want, partial)
		}
	}

	singular := statusMarkdown(statusIndex{HiddenClean: 1, Repositories: []repositoryStatusInfo{
		{Repository: "acme/dirty", Status: "attention"},
	}}, false, "# WB local repository status\n\n")
	if !strings.Contains(singular, "1 clean repository hidden") {
		t.Errorf("filtered markdown does not read naturally for one repository:\n%s", singular)
	}
}

// TestStatusMarkdownReportsAnAllCleanFleet keeps the empty worklist readable:
// with every row filtered away, a bare table header would look like a fleet
// that was never scanned.
func TestStatusMarkdownReportsAnAllCleanFleet(t *testing.T) {
	t.Parallel()
	markdown := statusMarkdown(statusIndex{SchemaVersion: 1, HiddenClean: 12}, false, "# WB local repository status\n\n")
	if !strings.Contains(markdown, "All 12 inspected repositories are clean.") {
		t.Errorf("all-clean markdown does not say so:\n%s", markdown)
	}
	if strings.Contains(markdown, "| Repository |") {
		t.Errorf("all-clean markdown still prints an empty table:\n%s", markdown)
	}

	one := statusMarkdown(statusIndex{SchemaVersion: 1, HiddenClean: 1}, false, "# WB local repository status\n\n")
	if !strings.Contains(one, "The inspected repository is clean.") {
		t.Errorf("all-clean markdown does not read naturally for one repository:\n%s", one)
	}
}

// TestStatusMarkdownUnfilteredHasNoNote makes sure the note is a consequence
// of filtering, not decoration on every report.
func TestStatusMarkdownUnfilteredHasNoNote(t *testing.T) {
	t.Parallel()
	markdown := statusMarkdown(statusIndex{SchemaVersion: 1, Repositories: []repositoryStatusInfo{
		{Repository: "acme/clean", Status: "clean"},
	}}, false, "# WB local repository status\n\n")
	if strings.Contains(markdown, "--all") {
		t.Errorf("an unfiltered report advertises --all:\n%s", markdown)
	}
}

func TestStatusMarkdownAttributesUnpushedCommits(t *testing.T) {
	t.Parallel()
	markdown := statusMarkdown(statusIndex{Repositories: []repositoryStatusInfo{{
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

// TestStatusFiltersACleanFleetEndToEnd runs the built binary over a two-repo
// fleet, because the filter is only useful if the flag is actually wired to
// the fleet path and left off the single-repository path.
func TestStatusFiltersACleanFleetEndToEnd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clean := initTestRepository(t, filepath.Join(root, "acme", "clean"))
	initTestRepository(t, filepath.Join(root, "acme", "dirty"))
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fleet := decodeStatusIndex(t, runWB(t, "status", "--projects-root", root, "--format", "json"))
	if len(fleet.Repositories) != 1 || fleet.Repositories[0].Repository != "acme/dirty" {
		t.Errorf("default fleet report = %+v, want only acme/dirty", fleet.Repositories)
	}
	if fleet.HiddenClean != 1 {
		t.Errorf("hidden_clean = %d, want 1", fleet.HiddenClean)
	}

	everything := decodeStatusIndex(t, runWB(t, "status", "--projects-root", root, "--format", "json", "--all"))
	if len(everything.Repositories) != 2 {
		t.Errorf("--all report = %+v, want both repositories", everything.Repositories)
	}
	if everything.HiddenClean != 0 {
		t.Errorf("--all hid %d repositories; it must hide none", everything.HiddenClean)
	}
	selected := decodeStatusIndex(t, runWB(t, "status", "--projects-root", root, "--filter", "acme/clean", "--format", "json", "--all"))
	if len(selected.Repositories) != 1 || selected.Repositories[0].Repository != "acme/clean" {
		t.Errorf("--projects-root + --filter selection = %+v, want only acme/clean", selected.Repositories)
	}

	single := decodeStatusIndex(t, runWB(t, "status", clean, "--format", "json"))
	if len(single.Repositories) != 1 || single.Repositories[0].Status != "clean" {
		t.Errorf("naming one clean repository reported %+v; it must answer for that checkout", single.Repositories)
	}
}

func decodeStatusIndex(t *testing.T, result smokeResult) statusIndex {
	t.Helper()
	if result.exitCode != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", result.exitCode, exitOK, result.stderr)
	}
	var report statusIndex
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("stdout is not a status index: %v\nstdout: %s", err, result.stdout)
	}
	return report
}

// initTestRepository creates a repository with no commits and no remote, which
// gitops.Status reports as clean, and returns its path.
func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init %s: %v\n%s", path, err, output)
	}
	return path
}
