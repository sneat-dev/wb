package cmdbranch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"gopkg.in/yaml.v3"
	"reflect"
	"strings"
	"testing"
	"time"
)

func runtimeForTest() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "parsed", Filter: "selected"} }}
}
func depsForTest() Dependencies {
	return Dependencies{List: func(context.Context, worktrees.BranchListOptions) (worktrees.BranchListOutcome, error) {
		return worktrees.BranchListOutcome{}, nil
	}, Cleanup: func(context.Context, worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error) {
		return worktrees.BranchCleanupOutcome{}, nil
	}, Quarantine: func(context.Context, worktrees.BranchQuarantineOptions) (worktrees.BranchQuarantineOutcome, error) {
		return worktrees.BranchQuarantineOutcome{}, nil
	}, ArchiveTarget: func(context.Context, string) (worktrees.RetiredArchivePlan, error) {
		return worktrees.RetiredArchivePlan{}, nil
	}}
}
func TestBranchCleanupDefaultsToSafeDryRun(t *testing.T) {
	t.Parallel()
	command := newCleanup(runtimeForTest(), depsForTest())
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
	t.Parallel()
	command := newList(runtimeForTest(), depsForTest())
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
	t.Parallel()
	command := newList(runtimeForTest(), depsForTest())
	if command.Flags().Lookup("org") == nil || command.Flags().Lookup("include-retired") == nil {
		t.Fatal("branch list is missing organization or retired selector")
	}
	if command.Flags().Lookup("format").DefValue != "text" {
		t.Fatal("branch list no longer defaults to table text")
	}
	if err := shared.RequireOutputFormat("yaml", "text", "json", "yaml"); err != nil {
		t.Fatalf("yaml format rejected: %v", err)
	}
}

func TestBranchQuarantineDefaultsToDryRun(t *testing.T) {
	t.Parallel()
	command := newQuarantine(runtimeForTest(), depsForTest())
	if flag := command.Flags().Lookup("apply"); flag == nil || flag.DefValue != "false" {
		t.Fatal("quarantine must default to dry-run")
	}
	for _, name := range []string{"repo", "branch", "sha", "reason", "manifest", "report-dir"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s", name)
		}
	}
}

func TestBranchArchiveTargetIsReadOnlyAndRendersThePreflight(t *testing.T) {
	t.Parallel()
	deps := depsForTest()
	deps.ArchiveTarget = func(_ context.Context, repository string) (worktrees.RetiredArchivePlan, error) {
		return worktrees.RetiredArchivePlan{SourceRepository: repository, ArchiveRepository: "sneat-co/backstage-retired", Outcome: "refused", Refusal: "archive repository is public"}, nil
	}
	command := newArchiveTarget(runtimeForTest(), deps)
	if command.Flags().Lookup("apply") != nil || command.Flags().Lookup("repo") == nil || command.Flags().Lookup("format") == nil {
		t.Fatal("archive-target must expose only repo and format, with no apply path")
	}
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--repo", "sneat-co/app"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "archive-target: sneat-co/backstage-retired") || !strings.Contains(got, "refusal: archive repository is public") {
		t.Fatalf("text output = %q", got)
	}
	var help bytes.Buffer
	command = newArchiveTarget(runtimeForTest(), deps)
	command.SetOut(&help)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil || !strings.Contains(help.String(), "read-only") || !strings.Contains(help.String(), "--repo") {
		t.Fatalf("help = %q, err=%v", help.String(), err)
	}
}

func TestBranchArchiveTargetYAMLSemanticallyMatchesJSON(t *testing.T) {
	t.Parallel()
	deps := depsForTest()
	deps.ArchiveTarget = func(_ context.Context, repository string) (worktrees.RetiredArchivePlan, error) {
		return worktrees.RetiredArchivePlan{SourceRepository: repository, ArchiveRepository: "sneat-co/backstage-retired", Outcome: "refused", Refusal: "archive repository is unavailable", LocalQuarantine: "preserved", WorkLogExport: "not_started"}, nil
	}
	execute := func(format string) []byte {
		t.Helper()
		command := newArchiveTarget(runtimeForTest(), deps)
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetArgs([]string{"--repo", "sneat-co/app", "--format", format})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	jsonRaw := execute("json")
	yamlRaw := execute("yaml")
	var want, got any
	if err := json.Unmarshal(jsonRaw, &want); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(yamlRaw, &got); err != nil {
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
		t.Fatalf("archive-target YAML differs from JSON\nwant=%#v\ngot=%#v", want, got)
	}
}

func TestBranchCountUsesTheSharedInventorySelectors(t *testing.T) {
	t.Parallel()
	command := newCount(runtimeForTest(), depsForTest())
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
	t.Parallel()
	var out bytes.Buffer
	if err := printBranchCount(&out, worktrees.BranchListOutcome{
		Totals:          map[string]int{worktrees.BranchRetired: 2},
		RetiredBranches: 1,
		RetiredRefs:     map[string]int{worktrees.BranchScopeLocal: 1, worktrees.BranchScopeRemote: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "STATUS       REFS\nretired     2\nretired branches 1 names (1 local refs, 1 remote refs)\nretired tags     0 names (0 local refs, 0 remote refs)\n"; got != want {
		t.Fatalf("retired count text = %q, want %q", got, want)
	}
}

func TestBranchCountDoesNotRenderAnUnavailableRemoteRetiredInventoryAsZero(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	list := newList(runtimeForTest(), depsForTest())
	for _, wanted := range []string{
		"contained", "absorbed", "unique", "protected", "in-use", "unreadable",
		"never eligible for --apply", "read-only in every configuration", "[n/N] repository",
		"bounded quarantine inventory", "does not fetch origin/<base>",
	} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("branch list help does not mention %q", wanted)
		}
	}
	cleanup := newCleanup(runtimeForTest(), depsForTest())
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

func TestBranchArchiveTargetRequiresRepo(t *testing.T) {
	t.Parallel()
	deps := depsForTest()
	deps.ArchiveTarget = func(context.Context, string) (worktrees.RetiredArchivePlan, error) {
		t.Fatal("preflight called without repo")
		return worktrees.RetiredArchivePlan{}, nil
	}
	command := newArchiveTarget(runtimeForTest(), deps)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(nil)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--repo is required") {
		t.Fatalf("error = %v; want a --repo is required refusal", err)
	}
}

// TestBranchArchiveTargetPropagatesWriteFailure drives the RunE branch that
// returns the fmt.Fprintf error instead of swallowing it, reusing the
// package's shared failingWriter (dispatch_test.go).
func TestBranchArchiveTargetPropagatesWriteFailure(t *testing.T) {
	t.Parallel()
	deps := depsForTest()
	deps.ArchiveTarget = func(_ context.Context, repository string) (worktrees.RetiredArchivePlan, error) {
		return worktrees.RetiredArchivePlan{SourceRepository: repository, ArchiveRepository: "sneat-co/backstage-retired", Outcome: "ok"}, nil
	}
	command := newArchiveTarget(runtimeForTest(), deps)
	failure := errors.New("write refused")
	command.SetOut(failingWriter{err: failure})
	command.SetArgs([]string{"--repo", "sneat-co/app"})
	if err := command.Execute(); err != failure || !strings.Contains(err.Error(), "write refused") {
		t.Fatalf("error = %v; want the underlying write failure to propagate", err)
	}
}
