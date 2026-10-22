package cmdworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func targetAbsorbedInvocation(results []orchestrate.WorktreeMergeTargetAbsorbedSweepResult, sweepErr, admissionErr error, got *orchestrate.WorktreeMergeTargetAbsorbedSweepOptions) *mergeInvocation {
	flags := shared.Flags{ProjectsRoot: "/projects"}
	inv := mergeRecordingInvocation(&flags)
	inv.bindings.Admission = func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
		return worktrees.AgentIdentity{}, func() {}, admissionErr
	}
	inv.operations.SweepTargetAbsorbedWorktreeMergeReceipts = func(_ context.Context, options orchestrate.WorktreeMergeTargetAbsorbedSweepOptions) (orchestrate.WorktreeMergeTargetAbsorbedSweep, error) {
		*got = options
		return orchestrate.WorktreeMergeTargetAbsorbedSweep{Results: results}, sweepErr
	}
	return inv
}

func runTargetAbsorbed(inv *mergeInvocation, args ...string) (string, error) {
	command := newWorktreeMergeAcknowledgeTargetAbsorbedCmd(inv)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetContext(context.Background())
	err := mergeExecute(command, args...)
	return out.String(), err
}

func TestAcknowledgeTargetAbsorbedReportsEachSweptReceipt(t *testing.T) {
	t.Parallel()
	results := []orchestrate.WorktreeMergeTargetAbsorbedSweepResult{
		{Receipt: "/r/a.json", Status: "prepared", Outcome: orchestrate.WorktreeMergeTargetAbsorbedWouldRelease},
		{Receipt: "/r/b.json", Status: "conflict", Outcome: orchestrate.WorktreeMergeTargetAbsorbedNotAbsorbed, Detail: "commit c is not in main"},
		{Receipt: "/r/c.json", Status: "checks_pending", Outcome: orchestrate.WorktreeMergeTargetAbsorbedAlreadyReleased, AcknowledgementPath: "/r/c.json.x.target-absorbed.ack.json"},
	}
	var got orchestrate.WorktreeMergeTargetAbsorbedSweepOptions
	out, err := runTargetAbsorbed(targetAbsorbedInvocation(results, nil, nil, &got), "/r/a.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectsRoot != "/projects" || got.Apply || len(got.Receipts) != 1 || got.Receipts[0] != "/r/a.json" {
		t.Fatalf("sweep options = %+v", got)
	}
	for _, want := range []string{
		"would_release\tprepared\t/r/a.json\n",
		"not_absorbed\tconflict\t/r/b.json\tcommit c is not in main\n",
		"already_released\tchecks_pending\t/r/c.json\tacknowledgement: /r/c.json.x.target-absorbed.ack.json\n",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}

	if !strings.Contains(out, "released: 0, would_release: 1, already_released: 1, not_absorbed: 1, ineligible: 0") {
		t.Fatalf("output %q lacks the outcome summary", out)
	}

	out, err = runTargetAbsorbed(targetAbsorbedInvocation(results[:1], nil, nil, &got), "--apply", "--actor", "operator", "--reason", "drain absorbed lanes", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded orchestrate.WorktreeMergeTargetAbsorbedSweep
	if err := json.Unmarshal([]byte(out), &decoded); err != nil || len(decoded.Results) != 1 || decoded.Results[0].Receipt != "/r/a.json" || !got.Apply || got.Actor != "operator" || got.Reason != "drain absorbed lanes" {
		t.Fatalf("json output = %q (%v), apply = %t", out, err, got.Apply)
	}
	out, err = runTargetAbsorbed(targetAbsorbedInvocation(nil, nil, nil, &got), "--apply")
	if err != nil || strings.Contains(out, "dry-run") {
		t.Fatalf("applied text output = %q, %v", out, err)
	}
}

func TestAcknowledgeTargetAbsorbedRefusesBadFormatAdmissionAndSweepFailure(t *testing.T) {
	t.Parallel()
	var got orchestrate.WorktreeMergeTargetAbsorbedSweepOptions
	if _, err := runTargetAbsorbed(targetAbsorbedInvocation(nil, nil, nil, &got), "--format", "yaml"); err == nil {
		t.Fatal("an unsupported format was accepted")
	}
	admission := errors.New("admission refused")
	if _, err := runTargetAbsorbed(targetAbsorbedInvocation(nil, nil, admission, &got)); !errors.Is(err, admission) {
		t.Fatalf("admission failure = %v", err)
	}
	sweep := errors.New("sweep failed")
	if _, err := runTargetAbsorbed(targetAbsorbedInvocation(nil, sweep, nil, &got)); !errors.Is(err, sweep) {
		t.Fatalf("sweep failure = %v", err)
	}
}

func TestAcknowledgeTargetAbsorbedReturnsAReceiptLineWriteFailure(t *testing.T) {
	t.Parallel()
	var got orchestrate.WorktreeMergeTargetAbsorbedSweepOptions
	results := []orchestrate.WorktreeMergeTargetAbsorbedSweepResult{{Receipt: "/r/a.json", Outcome: orchestrate.WorktreeMergeTargetAbsorbedWouldRelease}}
	command := newWorktreeMergeAcknowledgeTargetAbsorbedCmd(targetAbsorbedInvocation(results, nil, nil, &got))
	refused := errors.New("stdout closed")
	command.SetOut(mergeRefusingWriter{refused})
	command.SetContext(context.Background())
	if err := mergeExecute(command); !errors.Is(err, refused) {
		t.Fatalf("receipt line write failure = %v", err)
	}
}
