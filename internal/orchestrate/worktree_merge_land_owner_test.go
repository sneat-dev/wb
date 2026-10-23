package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// landOwnerRecord writes actual bytes for early record/terminal contracts. It
// does not claim a native candidate, custody, validation or landing proof.
func landOwnerRecord(t *testing.T, status WorktreeMergeStatus) (string, WorktreeMergeReceipt) {
	t.Helper()
	root := t.TempDir()
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: "early-land-contract", ReceiptPath: filepath.Join(home, "reports", "worktree-merge", "early-land-contract.json"), Repository: "acme/app", Target: "main", Phase: WorktreeMergePhasePrepare, Status: status, UpdatedAt: time.Now().UTC()}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return root, receipt
}

func TestLandOwnerReadAndTerminalContracts(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		mutate func(*testing.T, *WorktreeMergeReceipt)
		want   string
	}{
		{name: "ordinary missing receipt", mutate: func(t *testing.T, r *WorktreeMergeReceipt) {
			if err := os.Remove(r.ReceiptPath); err != nil {
				t.Fatal(err)
			}
		}, want: ""},
		{name: "invalid record bytes", mutate: func(t *testing.T, r *WorktreeMergeReceipt) {
			if err := os.WriteFile(r.ReceiptPath, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
		}, want: "decode merge receipt"},
		{name: "invalid receipt identity", mutate: func(t *testing.T, r *WorktreeMergeReceipt) {
			r.SchemaVersion = 0
			if err := persistWorktreeMergeReceipt(*r); err != nil {
				t.Fatal(err)
			}
		}, want: "invalid identity"},
		{name: "complete untouched", mutate: func(t *testing.T, r *WorktreeMergeReceipt) {}, want: "success"},
		{name: "complete refuses missing cleanup evidence", mutate: func(t *testing.T, r *WorktreeMergeReceipt) {
			r.Failure = "prior failure"
			if err := persistWorktreeMergeReceipt(*r); err != nil {
				t.Fatal(err)
			}
		}, want: "retains failure but has no cleanup intent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, original := landOwnerRecord(t, WorktreeMergeComplete)
			tt.mutate(t, &original)
			var events []progress.Event
			admission := &WorktreeMergeHostLoadAdmission{SkippedReason: "explicit contract"}
			got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: root, Receipt: original.ReceiptPath, HostLoadAdmission: admission, Progress: func(e progress.Event) { events = append(events, e) }})
			if tt.want == "success" {
				if err != nil || got.Status != WorktreeMergeComplete || got.HostLoadAdmission != admission {
					t.Fatalf("terminal = %+v, %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("receipt=%+v error=%v want %q", got, err, tt.want)
			}
			if len(events) == 0 || events[0].Phase != "read_receipt" || events[0].State != progress.Started {
				t.Fatalf("progress = %+v", events)
			}
		})
	}
	t.Run("unresolvable locator", func(t *testing.T) {
		t.Parallel()
		got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: t.TempDir(), Receipt: ""})
		if err == nil || got.ReceiptPath != "" {
			t.Fatalf("missing locator=%+v %v", got, err)
		}
	})
}

func TestLandOwnerRefusesMalformedAuditBeforeAdmission(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{worktreeMergeLandedFailureAcknowledgementSuffix, worktreeMergeValidationFailureSupersessionSuffix, worktreeMergePreparedRebatchSuffix} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			root, receipt := landOwnerRecord(t, WorktreeMergePrepared)
			if suffix == worktreeMergeValidationFailureSupersessionSuffix {
				fixture, prepared := landOwnerNativeFixture(t)
				root, receipt = fixture.githubDir, prepared
				receipt.Status = WorktreeMergeConflict
				if err := validatePrepareFailureSupersessionReceipt(receipt, receipt.ReceiptPath); err != nil {
					t.Fatalf("native unpublished conflict is not supersession eligible: %v", err)
				}
				if err := persistWorktreeMergeReceipt(receipt); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(receipt.ReceiptPath+suffix, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: root, Receipt: receipt.ReceiptPath})
			if err == nil || !strings.Contains(err.Error(), "decode") || got.ID != receipt.ID || got.Status != receipt.Status {
				t.Fatalf("audit refusal=%+v %v", got, err)
			}
			actual, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil || actual.Status != receipt.Status {
				t.Fatalf("early audit changed durable record: %+v %v", actual, readErr)
			}
		})
	}
}

func TestLandOwnerValidationLimitsPreserveUnchangedAndDistinctOverrides(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		check, shard time.Duration
		failure      bool
	}{
		{name: "no override"}, {name: "check only", check: 7 * time.Minute}, {name: "shard only", shard: 3 * time.Minute}, {name: "both", check: 5 * time.Minute, shard: 2 * time.Minute}, {name: "write refuses", check: time.Minute, failure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, receipt := landOwnerRecord(t, WorktreeMergePreparing)
			receipt.ValidationTimeouts = worktreeMergeValidationTimeouts(11*time.Minute, 4*time.Minute)
			before := receipt
			failure := errors.New("selected override persistence error")
			calls := 0
			err := applyWorktreeMergeValidationLimits(&receipt, WorktreeMergeLandOptions{CheckTimeout: tt.check, ShardAttemptTimeout: tt.shard}, func(r WorktreeMergeReceipt) error {
				calls++
				if tt.failure {
					return failure
				}
				return persistWorktreeMergeReceipt(r)
			})
			if tt.failure {
				if err != failure {
					t.Fatalf("save identity=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tt.check == 0 && tt.shard == 0 {
				if calls != 0 || !reflect.DeepEqual(receipt, before) {
					t.Fatalf("unchanged path wrote/mutated: calls=%d receipt=%+v", calls, receipt)
				}
				return
			}
			check, shard := receiptWorktreeMergeValidationTimeouts(receipt)
			wantCheck, wantShard := 11*time.Minute, 4*time.Minute
			if tt.check > 0 {
				wantCheck = tt.check
			}
			if tt.shard > 0 {
				wantShard = tt.shard
			}
			if calls != 1 || check != wantCheck || shard != wantShard || receipt.UpdatedAt.Before(before.UpdatedAt) {
				t.Fatalf("override calls=%d limits=%s/%s receipt=%+v", calls, check, shard, receipt)
			}
			if !tt.failure {
				stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
				if readErr != nil || !reflect.DeepEqual(stored.ValidationTimeouts, receipt.ValidationTimeouts) {
					t.Fatalf("durable override=%+v %v", stored, readErr)
				}
			}
		})
	}
}

func TestLandOwnerFailureReceiptPreservesPrimaryAndSecondaryCustody(t *testing.T) {
	t.Parallel()
	for _, secondary := range []bool{false, true} {
		t.Run(map[bool]string{false: "real save", true: "selected save refusal"}[secondary], func(t *testing.T) {
			t.Parallel()
			_, receipt := landOwnerRecord(t, WorktreeMergePrepared)
			primary := errors.New("primary land refusal")
			saveErr := errors.New("secondary storage refusal")
			calls := 0
			got, err := failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, primary, func(r WorktreeMergeReceipt) error {
				calls++
				if secondary {
					return saveErr
				}
				return persistWorktreeMergeReceipt(r)
			})
			if calls != 1 || !errors.Is(err, primary) || errors.Is(err, saveErr) || got.Status != WorktreeMergeConflict || got.Failure != primary.Error() || got.UpdatedAt.Before(receipt.UpdatedAt) {
				t.Fatalf("failure receipt=%+v error=%v calls=%d", got, err, calls)
			}
			if secondary {
				if !strings.Contains(err.Error(), "; persist failure receipt: "+saveErr.Error()) {
					t.Fatalf("secondary context=%v", err)
				}
			} else if err != primary {
				t.Fatalf("primary identity lost: %v", err)
			}
		})
	}
	// Exercise the genuine default wrapper and an ordinary real filesystem refusal,
	// independently of the selected per-invocation observation above.
	root, receipt := landOwnerRecord(t, WorktreeMergePrepared)
	primary := errors.New("default primary")
	got, err := failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, primary)
	if err != primary || got.Failure != primary.Error() {
		t.Fatalf("default real save=%+v %v", got, err)
	}
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt.ReceiptPath = filepath.Join(blocker, "record.json")
	_, err = failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, primary)
	if !errors.Is(err, primary) || !strings.Contains(err.Error(), "persist failure receipt:") {
		t.Fatalf("default native refusal=%v", err)
	}
}
