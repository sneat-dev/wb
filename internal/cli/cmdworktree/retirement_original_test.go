package cmdworktree

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"strings"
	"testing"
	"time"
)

func TestClosedAuditSuffixNamesTheRecordOnlyWhenOneExists(t *testing.T) {
	t.Parallel()
	if closedAuditSuffix("") != "" || closedAuditSuffix("/h/closed-pr-discards/x.json") != "; audit /h/closed-pr-discards/x.json" {
		t.Fatal("unexpected audit suffix")
	}
}

func TestCwCovPrintWorktreeGCRendersEveryRowShape(t *testing.T) {
	t.Parallel()
	outcome := worktrees.GCOutcome{
		SchemaVersion: 1,
		Apply:         true,
		Entries: []worktrees.GCEntry{
			{
				Task: "landed", Repository: "acme/app", Branch: "task/landed", Class: "landed-clean",
				Applied: true, Owner: "lane-a", AgeSeconds: 7200,
				Reason: "landed by squash", Evidence: []string{"pr#42 merged"},
				Warnings: []string{"branch renamed since claim"}, Management: "unmanaged",
			},
			{
				Task: "review", Repository: "acme/app", HeadSHA: "abcdef1234567890", Class: "detached-review",
				Eligible: true, Owner: "lane-b", AgeSeconds: 30,
				Reason: "detached at a landed PR head", SanctionedCommand: "wb worktree abort review --apply",
			},
			{
				Task: "stuck", Repository: "beta/tool", Branch: "task/stuck", Class: "unpushed",
				Owner: "lane-c", AgeSeconds: 0, Reason: "GitHub has never seen this head",
				Error: "could not read origin",
			},
		},
		PartialTasks: []worktrees.GCPartialTask{{Task: "multi", Retired: []string{"acme/app"}, LeftAlone: []string{"beta/tool"}}},
		Artifacts: []worktrees.LifecycleArtifact{
			{Kind: "stage", Path: "/tmp/stage", Reason: "non-empty quarantined stage"},
		},
		Shells: []worktrees.RetiredShell{
			{Task: "empty", Path: "/tmp/empty", Error: "permission denied"},
			{Task: "quiet", Path: "/tmp/quiet"},
		},
		Reclaimed: diskusage.Usage{ApparentBytes: 2 << 20, UnsharedBytes: 1 << 20},
		Totals: map[string]int{
			"retired": 2, "eligible": 3, "refused": 1, "purged_artefacts": 4, "retired_shells": 5,
		},
	}
	command := NewGC(shared.Runtime{}, GCDependencies{})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := printWorktreeGC(command, outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"landed", "landed-clean", "retired", "evidence: [pr#42 merged]",
		"warning: branch renamed since claim", "WB management: unmanaged",
		"detached-review", "would retire", "resolve with: wb worktree abort review --apply",
		"error: could not read origin",
		"partial: task multi retired [acme/app] and left [beta/tool] behind",
		"artifact stage /tmp/stage: non-empty quarantined stage",
		"shell empty /tmp/empty: permission denied",
		"2 retired, 3 eligible, 1 kept, 4 terminal artefacts purged, 0 repository-root stages purged, 5 empty shells retired",
		"reclaimed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("gc text missing %q:\n%s", want, text)
		}
	}
	// Only shells carrying an error are itemized.
	if strings.Contains(text, "shell quiet") {
		t.Errorf("a shell without an error must not be itemized:\n%s", text)
	}

	// A dry run reports reclaimable bytes and the shells it *would* retire.
	dry := outcome
	dry.Apply = false
	dry.Totals = map[string]int{"retired": 0, "eligible": 3, "refused": 1, "purged_artefacts": 0, "eligible_shells": 6}
	dry.Reclaimable = diskusage.Usage{ApparentBytes: 3 << 20, UnsharedBytes: 1 << 20}
	out.Reset()
	if err := printWorktreeGC(command, dry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reclaimable") || !strings.Contains(out.String(), "empty shells to retire") {
		t.Errorf("dry-run footer is wrong:\n%s", out.String())
	}

	// No checkouts at all still prints a footer rather than nothing.
	out.Reset()
	if err := printWorktreeGC(command, worktrees.GCOutcome{Totals: map[string]int{}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no WB worktrees") {
		t.Errorf("empty sweep = %q", out.String())
	}
}

func TestCwWtCleanupTaskNameHelpers(t *testing.T) {
	t.Parallel()
	outcome := worktrees.CleanupOutcome{
		Results: []worktrees.CleanupResult{
			{ListResult: worktrees.ListResult{Task: "alpha"}, Applied: true},
			{ListResult: worktrees.ListResult{Task: "beta"}},
		},
		Artifacts: []worktrees.LifecycleArtifact{{Task: "gamma", Applied: true}},
	}
	applied := appliedCleanupTaskNames(outcome)
	if !applied["alpha"] || applied["beta"] || !applied["gamma"] {
		t.Fatalf("applied task names = %v", applied)
	}

	if !namedCleanupApplySatisfied([]string{"alpha"}, outcome) {
		t.Fatal("a single applied task must satisfy the check")
	}
	if namedCleanupApplySatisfied([]string{"beta"}, outcome) {
		t.Fatal("an unapplied task must not satisfy the check")
	}
	if !namedCleanupApplySatisfied([]string{"alpha", "beta"}, outcome) {
		t.Fatal("multi-task selections are always satisfied")
	}
	// An empty resolution falls back to the requested name.
	if namedCleanupApplySatisfied([]string{"alpha"}, worktrees.CleanupOutcome{}) {
		t.Fatal("an empty outcome must not satisfy a named apply")
	}
	if namedCleanupApplySatisfied(nil, worktrees.CleanupOutcome{}) != true {
		t.Fatal("an empty selection is trivially satisfied")
	}
	// A resolved identity that differs from the selector is judged by the
	// resolved name.
	resolved := worktrees.CleanupOutcome{
		ResolvedTasks: []string{"session-resume-1"},
		Results:       []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "session-resume-1"}, Applied: true}},
	}
	if !namedCleanupApplySatisfied([]string{"logical-effort"}, resolved) {
		t.Fatal("a resolved identity that applied must satisfy the check")
	}
}

func TestCwWtFormatWorktreeGCOutcomeInProcess(t *testing.T) {
	t.Parallel()
	command := NewGC(shared.Runtime{}, GCDependencies{})
	var out bytes.Buffer
	command.SetOut(&out)
	outcome := worktrees.GCOutcome{
		SchemaVersion: 1,
		Entries: []worktrees.GCEntry{
			{
				Task: "alpha", Repository: "acme/app", WorktreeDir: "/tmp/wt/alpha",
				Class: "dirty", Reason: "uncommitted changes", Evidence: []string{"a.go"},
				Warnings: []string{"branch renamed"}, SanctionedCommand: "wb worktree cleanup alpha",
				Management: "unmanaged", Error: "boom",
			},
			{Task: "beta", Repository: "acme/app", Class: "unpushed", Reason: "never pushed", Management: "managed"},
		},
		PartialTasks: []worktrees.GCPartialTask{{Task: "gamma", Retired: []string{"a"}, LeftAlone: []string{"b"}}},
		Artifacts:    []worktrees.LifecycleArtifact{{Kind: "stage", Path: "/tmp/stage", Reason: "quarantined"}},
		Shells:       []worktrees.RetiredShell{{Task: "alpha", Path: "/tmp/shell", Error: "shell failed"}},
		Totals: map[string]int{
			"retired": 1, "eligible": 2, "refused": 3, "purged_artefacts": 4,
			"retired_shells": 5, "eligible_shells": 6,
		},
		Reclaimable: diskusage.Usage{ApparentBytes: 1024, UnsharedBytes: 512},
	}
	if err := printWorktreeGC(command, outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"dirty", "uncommitted changes", "evidence: [a.go]", "warning: branch renamed",
		"resolve with: wb worktree cleanup alpha", "WB management: unmanaged", "error: boom",
		"partial: task gamma retired [a] and left [b] behind",
		"artifact stage /tmp/stage: quarantined",
		"shell alpha /tmp/shell: shell failed",
		"\n1 retired, 2 eligible, 3 kept, 4 terminal artefacts purged, 0 repository-root stages to purge, 6 empty shells to retire; reclaimable",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("gc text missing %q:\n%s", want, text)
		}
	}

	// --apply switches the footer to the reclaimed figure and retired shells.
	out.Reset()
	outcome.Apply = true
	if err := printWorktreeGC(command, outcome); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "reclaimed") || !strings.Contains(got, "5 empty shells retired") {
		t.Fatalf("apply footer = %q", got)
	}

	// An empty sweep still says so.
	out.Reset()
	if err := printWorktreeGC(command, worktrees.GCOutcome{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "no WB worktrees") {
		t.Fatalf("empty gc output = %q", got)
	}
}

func TestCwWtPrintRetireTaskShells(t *testing.T) {
	t.Parallel()
	outcome := worktrees.RetireShellsOutcome{
		Results: []worktrees.RetiredShell{
			{Task: "applied", Path: "/tmp/a", Applied: true},
			{Task: "eligible", Path: "/tmp/b", Eligible: true},
			{Task: "failed", Path: "/tmp/c", Error: "boom"},
			{Task: "skipped", Path: "/tmp/d", Reason: "still has members"},
		},
		Totals: map[string]int{"retired": 1, "would_retire": 2},
	}
	var out bytes.Buffer
	if err := printRetireTaskShells(cwWtCmd(&out), outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"retired      applied /tmp/a", "would retire eligible /tmp/b",
		"failed       failed /tmp/c: boom", "skip         skipped /tmp/d: still has members",
		"2 would retire",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("shells output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	outcome.Apply = true
	if err := printRetireTaskShells(cwWtCmd(&out), outcome); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 retired") {
		t.Fatalf("shells apply output = %q", out.String())
	}
	out.Reset()
	if err := printRetireTaskShells(cwWtCmd(&out), worktrees.RetireShellsOutcome{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB task directories found\n" {
		t.Fatalf("empty shells = %q", got)
	}

	for allow := 0; allow < 2; allow++ {
		if err := printRetireTaskShells(cwWtCmdWriter(&cwWtFailWriter{Allow: allow}), outcome); err == nil {
			t.Fatalf("printRetireTaskShells with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtPrintWorktreeCleanupAndRename(t *testing.T) {
	t.Parallel()
	cleanup := []worktrees.CleanupResult{
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/a"}, Applied: true, RemoteDeleted: true, WorktreeResidueRemoved: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/b"}, Applied: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/c"}, Eligible: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/d"}, Reason: "not merged"},
	}
	var out bytes.Buffer
	if err := printWorktreeCleanup(cwWtCmd(&out), cleanup, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"removed t acme/a and remote branch (WB removed the checkout Git unregistered but could not delete)",
		"removed t acme/b\n", "would remove t acme/c", "skip t acme/d: not merged",
		"1 eligible; dry-run only, pass --apply to remove",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("cleanup output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	if err := printWorktreeCleanup(cwWtCmd(&out), cleanup, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 removed") {
		t.Fatalf("cleanup apply output = %q", out.String())
	}
	out.Reset()
	if err := printWorktreeCleanup(cwWtCmd(&out), nil, true); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB worktrees matched\n" {
		t.Fatalf("empty cleanup = %q", got)
	}

	// Cleanup write failures propagate.
	for allow := 0; allow < 3; allow++ {
		if err := printWorktreeCleanup(cwWtCmdWriter(&cwWtFailWriter{Allow: allow}), cleanup, false); err == nil {
			t.Fatalf("printWorktreeCleanup with %d writes allowed returned nil", allow)
		}

	}
}

func TestCwWtPrintWorktreeGCPropagatesWriteFailures(t *testing.T) {
	t.Parallel()
	outcome := worktrees.GCOutcome{
		Entries:      []worktrees.GCEntry{{Task: "alpha", Repository: "acme/app", Reason: "dirty", Evidence: []string{"e"}, Warnings: []string{"w"}, SanctionedCommand: "cmd", Management: "unmanaged", Error: "boom"}},
		PartialTasks: []worktrees.GCPartialTask{{Task: "gamma"}},
		Artifacts:    []worktrees.LifecycleArtifact{{Kind: "k", Path: "p", Reason: "r"}},
		Shells:       []worktrees.RetiredShell{{Task: "s", Path: "p", Error: "e"}},
		Totals:       map[string]int{"retired": 1},
	}
	for allow := 0; allow < 11; allow++ {
		command := NewGC(shared.Runtime{}, GCDependencies{})
		command.SetOut(&cwWtFailWriter{Allow: allow})
		if err := printWorktreeGC(command, outcome); err == nil {
			t.Fatalf("printWorktreeGC with %d writes allowed returned nil, want write failure", allow)
		}
	}
	// An empty outcome still writes one line.
	command := NewGC(shared.Runtime{}, GCDependencies{})
	command.SetOut(&cwWtFailWriter{Allow: 0})
	if err := printWorktreeGC(command, worktrees.GCOutcome{}); err == nil {
		t.Fatal("empty printWorktreeGC did not propagate the write failure")
	}
}

func TestCwWtWriterSweepCleanupRenameShellsAdopt(t *testing.T) {
	t.Parallel()
	cleanup := []worktrees.CleanupResult{
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/a"}, Applied: true, RemoteDeleted: true, WorktreeResidueRemoved: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/b"}, Applied: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/c"}, Eligible: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/d"}, Reason: "not merged"},
	}
	cwWtSweepWrites(t, 8, func(writer *cwWtFailWriter) error {
		return printWorktreeCleanup(cwWtCmdWriter(writer), cleanup, false)
	})

	shells := worktrees.RetireShellsOutcome{
		Results: []worktrees.RetiredShell{
			{Task: "a", Path: "/tmp/a", Applied: true},
			{Task: "b", Path: "/tmp/b", Eligible: true},
			{Task: "c", Path: "/tmp/c", Error: "boom"},
			{Task: "d", Path: "/tmp/d", Reason: "still has members"},
		},
		Totals: map[string]int{"would_retire": 1},
	}
	cwWtSweepWrites(t, 8, func(writer *cwWtFailWriter) error {
		return printRetireTaskShells(cwWtCmdWriter(writer), shells)
	})

}

func TestNamedCleanupApplySatisfiedUsesResolvedPhysicalTasks(t *testing.T) {
	t.Parallel()
	logical := "logical-session-effort"
	physical := []string{
		"session-resume-resume-cli-m-002-bbbbbbbb",
		"session-resume-resume-cli-m-001-aaaaaaaa",
	}
	outcome := worktrees.CleanupOutcome{
		ResolvedTasks: physical,
		Results: []worktrees.CleanupResult{
			{ListResult: worktrees.ListResult{Task: physical[0]}, Applied: true, WorktreeGone: true, BranchDeleted: true},
			{ListResult: worktrees.ListResult{Task: physical[1]}, Applied: true, WorktreeGone: true, BranchDeleted: true},
		},
	}
	if !namedCleanupApplySatisfied([]string{logical}, outcome) {
		t.Fatal("logical selector whose resolved session-resume members all applied must satisfy named cleanup")
	}
	if namedCleanupApplySatisfied([]string{logical}, worktrees.CleanupOutcome{ResolvedTasks: physical}) {
		t.Fatal("logical selector with no applied members must not satisfy named cleanup")
	}
	if namedCleanupApplySatisfied([]string{"delivered-task"}, worktrees.CleanupOutcome{}) {
		t.Fatal("an unresolved named selector that applied nothing must not satisfy named cleanup")
	}
}

func TestCwCovDisabledWhenZeroMapsTheOffSwitch(t *testing.T) {
	t.Parallel()
	if got := disabledWhenZero(0); got != worktrees.DisableSessionFreshness {
		t.Fatalf("disabledWhenZero(0) = %v, want the explicit disable value", got)
	}
	if got := disabledWhenZero(90 * time.Minute); got != 90*time.Minute {
		t.Fatalf("disabledWhenZero(90m) = %v, want the window unchanged", got)
	}
}

func TestCwWtDisabledWhenZero(t *testing.T) {
	t.Parallel()
	if got := disabledWhenZero(0); got != worktrees.DisableSessionFreshness {
		t.Fatalf("disabledWhenZero(0) = %v, want the library disable value", got)
	}
	if got := disabledWhenZero(3 * time.Hour); got != 3*time.Hour {
		t.Fatalf("disabledWhenZero(3h) = %v", got)
	}
}

type cwWtFailWriter struct {
	Allow  int
	Writes int
}

var errCwWtWrite = errors.New("cwWt: injected write failure")

func (writer *cwWtFailWriter) Write(payload []byte) (int, error) {
	if writer.Writes >= writer.Allow {
		return 0, errCwWtWrite
	}
	writer.Writes++
	return len(payload), nil
}

func cwWtCmd(out *bytes.Buffer) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(out)
	var errOut bytes.Buffer
	command.SetErr(&errOut)
	return command
}
func cwWtCmdWriter(writer *cwWtFailWriter) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(writer)
	return command
}
func cwWtSweepWrites(t *testing.T, max int, run func(writer *cwWtFailWriter) error) {
	t.Helper()
	for allow := 0; allow <= max; allow++ {
		if err := run(&cwWtFailWriter{Allow: allow}); err == nil {
			return
		}
	}
	t.Fatalf("no write budget up to %d let the renderer finish", max)
}

func TestWorktreeCleanupReportNamesTheTargetThatProvedTheWork(t *testing.T) {
	t.Parallel()
	const proof = "contained in origin/main at 0123456789ab, via recorded base integration (absent)"
	results := func(applied bool) []worktrees.CleanupResult {
		return []worktrees.CleanupResult{
			{ListResult: worktrees.ListResult{Task: "fixture-integration-task", Repository: "acme/app", IntegrationProof: proof},
				Eligible: true, Applied: applied, RemoteDeleted: applied},
			{ListResult: worktrees.ListResult{Task: "fixture-plain-task", Repository: "acme/app"},
				Eligible: true, Applied: applied, RemoteDeleted: applied},
		}
	}
	for _, test := range []struct {
		name    string
		applied bool
		want    string
	}{
		{name: "plan", want: "would remove fixture-integration-task acme/app (" + proof + ")\n" +
			"would remove fixture-plain-task acme/app\n" +
			"2 eligible; dry-run only, pass --apply to remove\n"},
		{name: "apply", applied: true, want: "removed fixture-integration-task acme/app and remote branch (" + proof + ")\n" +
			"removed fixture-plain-task acme/app and remote branch\n" +
			"2 removed\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&stdout)
			if err := printWorktreeCleanup(command, results(test.applied), test.applied); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != test.want {
				t.Fatalf("cleanup report = %q, want %q", stdout.String(), test.want)
			}
		})
	}
}
