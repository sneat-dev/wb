package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
	"gopkg.in/yaml.v3"
)

func TestBranchCleanupDefaultsToSafeDryRun(t *testing.T) {
	command := newBranchCleanupCmd()
	apply := command.Flags().Lookup("apply")
	if apply == nil || apply.DefValue != "false" {
		t.Fatalf("--apply default = %#v, want false", apply)
	}
	scope := command.Flags().Lookup("scope")
	if scope == nil || scope.DefValue != "local" {
		t.Fatalf("--scope default = %#v, want local", scope)
	}
	olderThan := command.Flags().Lookup("older-than")
	if olderThan == nil || olderThan.DefValue != (24*time.Hour).String() {
		t.Fatalf("--older-than default = %#v, want %s", olderThan, 24*time.Hour)
	}
	if command.Flags().Lookup("report-dir") == nil {
		t.Fatal("cleanup command has no --report-dir")
	}
	if command.Flags().Lookup("remote") != nil {
		t.Fatal("wb branch cleanup must not define a --remote boolean; scope is selected only by --scope")
	}
	absorbedBy := command.Flags().Lookup("absorbed-by")
	if absorbedBy == nil || absorbedBy.DefValue != "" {
		t.Fatalf("--absorbed-by = %#v, want an empty-default string flag", absorbedBy)
	}
}

func TestBranchListDefaultsShowEveryAgeAndDisposition(t *testing.T) {
	command := newBranchListCmd()
	scope := command.Flags().Lookup("scope")
	if scope == nil || scope.DefValue != "local" {
		t.Fatalf("--scope default = %#v, want local", scope)
	}
	olderThan := command.Flags().Lookup("older-than")
	if olderThan == nil || olderThan.DefValue != "0s" {
		t.Fatalf("--older-than default = %#v, want 0s", olderThan)
	}
	only := command.Flags().Lookup("only")
	if only == nil || only.DefValue != "" {
		t.Fatalf("--only default = %#v, want empty", only)
	}
	if command.Flags().Lookup("apply") != nil {
		t.Fatal("wb branch list must be read-only and must not accept --apply")
	}
}

func TestBranchListSupportsRetiredAndOrganizationSelectors(t *testing.T) {
	command := newBranchListCmd()
	if command.Flags().Lookup("org") == nil || command.Flags().Lookup("include-retired") == nil {
		t.Fatal("branch list is missing organization or retired selector")
	}
	if command.Flags().Lookup("format").DefValue != "text" {
		t.Fatal("branch list no longer defaults to table text")
	}
	if err := requireOutputFormat("yaml", "text", "json", "yaml"); err != nil {
		t.Fatalf("yaml format rejected: %v", err)
	}
}

func TestBranchQuarantineDefaultsToDryRun(t *testing.T) {
	command := newBranchQuarantineCmd()
	if flag := command.Flags().Lookup("apply"); flag == nil || flag.DefValue != "false" {
		t.Fatal("quarantine must default to dry-run")
	}
	for _, name := range []string{"repo", "branch", "sha", "reason", "manifest", "report-dir"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s", name)
		}
	}
}

func TestBranchCountUsesTheSharedInventorySelectors(t *testing.T) {
	command := newBranchCountCmd()
	for _, name := range []string{"org", "repo", "scope", "only", "name", "older-than", "format"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("count missing --%s", name)
		}
	}
	if command.Flags().Lookup("include-retired") != nil {
		t.Fatal("count must share list's one-pass inventory rather than request a second presentation mode")
	}
}

func TestBranchCountRetiredTextFormatKeepsScopedRefTotals(t *testing.T) {
	var out bytes.Buffer
	if err := printBranchCount(&out, worktrees.BranchListOutcome{
		Totals:          map[string]int{worktrees.BranchRetired: 2},
		RetiredBranches: 1,
		RetiredRefs:     map[string]int{worktrees.BranchScopeLocal: 1, worktrees.BranchScopeRemote: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "STATUS       REFS\nretired     2\nretired      1 names (1 local refs, 1 remote refs)\n"; got != want {
		t.Fatalf("retired count text = %q, want %q", got, want)
	}
}

func TestBranchCountDoesNotRenderAnUnavailableRemoteRetiredInventoryAsZero(t *testing.T) {
	var out bytes.Buffer
	if err := printBranchCount(&out, worktrees.BranchListOutcome{
		Scope:                    worktrees.BranchScopeRemote,
		RetiredRefs:              map[string]int{},
		RetiredRemoteUnavailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unavailable remote refs") {
		t.Fatalf("remote retired failure rendered as a count: %q", out.String())
	}
}

func TestPrintBranchDiagnosticsUsesSeparateStream(t *testing.T) {
	var diagnostics bytes.Buffer
	if err := printBranchDiagnostics(&diagnostics, []string{"retired namespace inventory skipped fetch of origin/main", "acme/app: retired remote refs: unavailable"}); err != nil {
		t.Fatal(err)
	}
	want := "diagnostic: retired namespace inventory skipped fetch of origin/main\ndiagnostic: acme/app: retired remote refs: unavailable\n"
	if diagnostics.String() != want {
		t.Fatalf("diagnostics = %q, want %q", diagnostics.String(), want)
	}
}

func TestYAMLBranchListSemanticallyMatchesJSON(t *testing.T) {
	outcome := worktrees.BranchListOutcome{Org: "acme", RetiredBranches: 1, RetiredRefs: map[string]int{"local": 1}, Entries: []worktrees.BranchEntry{{Repository: "acme/app", Branch: "retired/example", Author: "Alex", Title: "old work"}}}
	raw, err := yamlCompatibleBranchList(outcome)
	if err != nil {
		t.Fatal(err)
	}
	jsonRaw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(jsonRaw, &want); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("yaml differs from JSON\nwant=%#v\ngot=%#v", want, got)
	}
}

func TestBranchOutcomeAlwaysSerializesZeroRetiredBranchNames(t *testing.T) {
	raw, err := json.Marshal(worktrees.BranchListOutcome{RetiredRefs: map[string]int{worktrees.BranchScopeLocal: 0}})
	if err != nil {
		t.Fatal(err)
	}
	var outcome map[string]any
	if err := json.Unmarshal(raw, &outcome); err != nil {
		t.Fatal(err)
	}
	if got, ok := outcome["retired_branches"]; !ok || got != float64(0) {
		t.Fatalf("retired_branches = %#v (present=%t), want explicit zero", got, ok)
	}
}

func TestBranchHelpExplainsEvidenceTaxonomyAndInvariants(t *testing.T) {
	list := newBranchListCmd()
	for _, wanted := range []string{
		"contained", "absorbed", "unique", "protected", "in-use", "unreadable",
		"never eligible for --apply", "read-only in every configuration", "[n/N] repository",
		"bounded quarantine inventory", "does not fetch origin/<base>",
	} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("branch list help does not mention %q", wanted)
		}
	}
	cleanup := newBranchCleanupCmd()
	for _, wanted := range []string{
		"dry-run plan", "absorbed is never eligible", "compare-and-delete",
		"force-with-lease", "pull-request evidence", "never removes, moves, or modifies any working tree",
		"between plan and apply refuses only itself", "--absorbed-by",
	} {
		if !strings.Contains(cleanup.Long, wanted) {
			t.Errorf("branch cleanup help does not mention %q", wanted)
		}
	}
}

func TestBranchCommandIsASiblingOfWorktreeNotNestedUnderIt(t *testing.T) {
	root := newRootCmd()
	branch, _, err := root.Find([]string{"branch"})
	if err != nil {
		t.Fatal(err)
	}
	if branch.Parent() != root {
		t.Fatalf("wb branch parent = %v, want root", branch.Parent())
	}
	if branch.Parent().Name() == "worktree" {
		t.Fatal("wb branch must not be nested under wb worktree")
	}
	worktreeCleanup, _, err := root.Find([]string{"worktree", "cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	if worktreeCleanup.Flags().Lookup("scope") != nil {
		t.Fatal("wb worktree cleanup must not gain a branch-scope flag")
	}
}

func TestBranchListRejectsUnsupportedScopeAndOnlyAsUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bad scope", []string{"branch", "list", "--scope", "bogus", "--projects-root", t.TempDir()}, "unsupported --scope"},
		{"bad only", []string{"branch", "list", "--only", "bogus", "--projects-root", t.TempDir()}, "unsupported --only"},
		{"bad format", []string{"branch", "list", "--format", "toml", "--projects-root", t.TempDir()}, "unsupported format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != exitFindings && code != exitUsage {
				t.Fatalf("run(%q) exit = %d, stderr=%s", test.args, code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want to contain %q", stderr.String(), test.want)
			}
		})
	}
}

func TestBranchCleanupRejectsUnsupportedScopeAsFindings(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"branch", "cleanup", "--scope", "bogus", "--projects-root", t.TempDir()}
	if code := run(args, &stdout, &stderr); code != exitFindings && code != exitUsage {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported --scope") {
		t.Fatalf("stderr = %q, want to mention --scope", stderr.String())
	}
}

func TestBranchListEmptyProjectsRootReportsNoBranches(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"branch", "list", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no branches matched") {
		t.Fatalf("stdout = %q, want \"no branches matched\"", stdout.String())
	}
}

func TestBranchCleanupDryRunOnEmptyProjectsRootWritesNoReport(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"branch", "cleanup", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if strings.Contains(stdout.String(), "report:") {
		t.Fatalf("dry run reported a report path: %s", stdout.String())
	}
}

// TestBranchCleanupUnreadableSkipRowNamesRepository is the CLI-level
// regression for the founder's `wb branch cleanup --scope all` report: 41
// rows read exactly "  skip           (unreadable): disposition unreadable
// is never eligible for --apply" with no repository, no branch, and no
// underlying cause — nothing an operator could act on. specscore/winget-pkgs
// had no refs/heads/main on origin, so fetching the exact target failed and
// the whole repository was reported unreadable in a single row with empty
// Scope and Branch; that row must still name the repository and the real
// fetch failure inline, not rely solely on the group header above it.
func TestBranchCleanupUnreadableSkipRowNamesRepository(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	var stdout, stderr bytes.Buffer
	// "does-not-exist" was never pushed, so fetching it from origin fails
	// exactly as it did for the repository with no refs/heads/main.
	args := []string{
		"branch", "cleanup", "--scope", "all", "--base", "does-not-exist",
		"--projects-root", projects,
	}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "unreadable") {
		t.Fatalf("stdout = %q, want an unreadable row", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "unreadable") {
			continue
		}
		if !strings.Contains(line, "acme/app") {
			t.Fatalf("unreadable row = %q, want it to name repository acme/app inline", line)
		}
		if !strings.Contains(line, "fetch exact origin/does-not-exist target") {
			t.Fatalf("unreadable row = %q, want it to name the underlying fetch failure", line)
		}
	}
}
