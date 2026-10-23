//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

//nolint:paralleltest // the native Git fixture configures process-wide Git and WB environment.
func TestE2EBranchReconciliationReplaysEffectBeforeStageRecord(t *testing.T) {
	for _, stage := range []struct{ name, persisted string }{
		{"bundle preservation", reconciliationStageBundles},
		{"remote retirement", reconciliationStageRemote},
		{"local retirement", reconciliationStageLocal},
		{"branch rebind", reconciliationStageRebound},
		{"event append", reconciliationStageEvent},
	} {
		//nolint:paralleltest // each case creates a native Git fixture that changes process-wide environment.
		t.Run(stage.name, func(t *testing.T) {
			fixture, created, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
			options := reconcileOptions(fixture, created, liveBranch, head)
			options.Apply = true
			fault := errors.New("stage record unavailable after effect")
			failed := false
			ports := reconciliationPorts{writeRecord: func(directory *os.File, record branchReconciliationRecord) error {
				if record.Stage == stage.persisted && !failed {
					failed = true
					return fault
				}
				return reconciliationClaimPorts().WriteRecord(directory, record)
			}}
			if stage.persisted == reconciliationStageEvent {
				// The first event's observation is immutable even when a later
				// process sees different non-authoritative Git status details.
				ports.observeGit = func(context.Context, string) LocalGitEvidence {
					return LocalGitEvidence{Branch: created.Branch, Head: head, Status: "first observation"}
				}
			}
			if _, err := reconcileClaimBranchWithPorts(context.Background(), options, ports); !errors.Is(err, fault) || !failed {
				t.Fatalf("after %s, error=%v write failed=%t", stage.name, err, failed)
			}
			if stage.persisted == reconciliationStageRebound {
				_, err := reconcileClaimBranchWithPorts(context.Background(), options, reconciliationPorts{requireAbsent: func(context.Context, *canonicalRepository, string) error { return fault }})
				if !errors.Is(err, fault) {
					t.Fatalf("rebound retry absence guard = %v", err)
				}
			}
			result, err := LogRecover(context.Background(), options)
			if err != nil || !result.Applied || !result.ReadyForNormalCleanup {
				t.Fatalf("retry after %s = %+v, %v", stage.name, result, err)
			}
			if got := gitTestOutput(t, created.WorktreeDir, "branch", "--show-current"); got != created.Branch {
				t.Fatalf("retry after %s left branch %q", stage.name, got)
			}
			events, err := readLocalEvents(created.WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event.ID == options.EventID && event.Type == LocalEventBranchReconciled {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("retry after %s yielded %d reconciliation events", stage.name, count)
			}
			if result.Event == nil || strings.TrimSpace(result.Event.ID) != options.EventID {
				t.Fatalf("retry after %s event=%+v", stage.name, result.Event)
			}
		})
	}
}
