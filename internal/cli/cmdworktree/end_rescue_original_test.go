package cmdworktree

import (
	"bytes"
	"fmt"
	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func TestPrintWorktreeEndNotApplied(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	buf := &bytes.Buffer{}
	command.SetOut(buf)
	result := worktreeend.Result{Task: "demo", Applied: false}
	if err := printWorktreeEnd(command, "text", result); err != nil {
		t.Fatalf("printWorktreeEnd: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing was changed; re-run with --apply") {
		t.Fatalf("expected re-run hint, got %q", buf.String())
	}
}

func TestCwWtPrintWorktreeEndTextAndJSON(t *testing.T) {
	t.Parallel()
	result := worktreeend.Result{
		Task:    "gc-cli",
		Applied: false,
		Members: []worktreeend.MemberResult{
			{
				Repository: "acme/app", Worktree: "/tmp/wt", Action: "would retire",
				Dirty: []string{"a.go", "b.go"}, CaptureRef: "refs/stash@{0}", Detail: "unmerged branch",
			},
			{Repository: "acme/other", Worktree: "/tmp/wt2", Action: "skip"},
		},
		ClaimOutcome: "claim retained",
	}
	var out bytes.Buffer
	command := NewEnd(shared.Runtime{}, nil)
	command.SetOut(&out)
	if err := printWorktreeEnd(command, "text", result); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"would end task gc-cli",
		"acme/app", "uncommitted: a.go, b.go",
		"captured at refs/stash@{0} — recover with `git stash apply refs/stash@{0}`",
		"! unmerged branch", "claim: claim retained",
		"nothing was changed; re-run with --apply",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("end text missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	result.Applied = true
	if err := printWorktreeEnd(command, "text", result); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "ended task gc-cli") || strings.Contains(got, "nothing was changed") {
		t.Fatalf("applied end text = %q", got)
	}

	out.Reset()
	if err := printWorktreeEnd(command, "json", result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"task\": \"gc-cli\"") {
		t.Fatalf("end json = %q", out.String())
	}

	// Every write failure is propagated.
	for allow := 0; allow < 6; allow++ {
		failing := NewEnd(shared.Runtime{}, nil)
		failing.SetOut(&cwWtFailWriter{Allow: allow})
		if err := printWorktreeEnd(failing, "text", result); err == nil {
			t.Fatalf("printWorktreeEnd with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtRenderRescueReportTruncationAndFailures(t *testing.T) {
	t.Parallel()
	changes := make([]canonicalrescue.Change, 0, 25)
	for index := 0; index < 25; index++ {
		changes = append(changes, canonicalrescue.Change{Status: "M", Path: "file-" + string(rune('a'+index))})
	}
	report := canonicalrescue.Report{Path: "/tmp/clone", Changes: changes, UntrackedCount: 25}
	var out strings.Builder
	if err := renderRescueReport(endRescueRuntime(), cwWtStringCmd(&out), "text", false, report); err == nil {
		t.Fatal("a dirty report without --apply must return the findings exit error")
	}
	if !strings.Contains(out.String(), "… and 5 more") {
		t.Fatalf("truncated rescue report = %q", out.String())
	}

	out.Reset()
	applied := report
	applied.RescueBranch, applied.RescueCommit, applied.Pushed, applied.Restored = "rescue/x", "deadbeef", true, true
	if err := renderRescueReport(endRescueRuntime(), cwWtStringCmd(&out), "text", true, applied); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pushed to the remote") || !strings.Contains(out.String(), "is now clean") {
		t.Fatalf("restored rescue report = %q", out.String())
	}

	out.Reset()
	if err := renderRescueReport(endRescueRuntime(), cwWtStringCmd(&out), "text", true, canonicalrescue.Report{Path: "/tmp/clone", Changes: []canonicalrescue.Change{{Status: "M", Path: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "is still dirty on purpose") {
		t.Fatalf("unrestored rescue report = %q", out.String())
	}

	out.Reset()
	if err := renderRescueReport(endRescueRuntime(), cwWtStringCmd(&out), "json", false, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"changes\"") {
		t.Fatalf("rescue json report = %q", out.String())
	}

	// Every write failure is propagated.
	for allow := 0; allow < 3; allow++ {
		command := NewRescue(endRescueRuntime(), RescueOperations{})
		command.SetOut(&cwWtFailWriter{Allow: allow})
		if err := renderRescueReport(endRescueRuntime(), command, "text", true, applied); err == nil {
			t.Fatalf("renderRescueReport with %d writes allowed returned nil", allow)
		}
	}
	command := NewRescue(endRescueRuntime(), RescueOperations{})
	command.SetOut(&cwWtFailWriter{Allow: 0})
	if err := renderRescueReport(endRescueRuntime(), command, "text", false, canonicalrescue.Report{Path: "/tmp/clean"}); err == nil {
		t.Fatal("clean renderRescueReport did not propagate the write failure")
	}
}

func endRescueRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }, ExitError: func(code int, message string) error { return fmt.Errorf("exit %d: %s", code, message) }}
}

func cwWtStringCmd(out *strings.Builder) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(out)
	return command
}

func TestEndOriginalWriterSweep(t *testing.T) {
	t.Parallel()
	end := worktreeend.Result{
		Task: "t", Applied: true,
		Members: []worktreeend.MemberResult{
			{Repository: "acme/a", Worktree: "/tmp/w", Action: "retired", Dirty: []string{"a.go"}, CaptureRef: "ref", Detail: "detail"},
			{Repository: "acme/b", Worktree: "/tmp/w2", Action: "skip"},
		},
		ClaimOutcome: "released",
	}
	cwWtSweepWrites(t, 10, func(writer *cwWtFailWriter) error {
		return printWorktreeEnd(cwWtCmdWriter(writer), "text", end)
	})
}

func TestRescueOriginalWriterSweep(t *testing.T) {
	t.Parallel()
	changes := make([]canonicalrescue.Change, 0, 25)
	for index := 0; index < 25; index++ {
		changes = append(changes, canonicalrescue.Change{Status: "M", Path: "file"})
	}
	// The applied spelling is used because the dry-run spelling always ends in
	// the findings exit error; only a captured, pushed, restored report returns
	// nil, which is what lets the sweep detect the true end of the output.
	rescue := canonicalrescue.Report{
		Path: "/tmp/clone", Changes: changes, UntrackedCount: 25,
		RescueBranch: "rescue/x", RescueCommit: "deadbeef", Pushed: true, Restored: true,
	}
	cwWtSweepWrites(t, 30, func(writer *cwWtFailWriter) error {
		return renderRescueReport(endRescueRuntime(), cwWtCmdWriter(writer), "text", true, rescue)
	})
}
