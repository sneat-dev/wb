package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublishedOwnerExpectationPinsRejectEveryMissingBoundary(t *testing.T) {
	t.Parallel()
	valid := WorktreeMergePublishedForwardRepairOptions{ProjectsRoot: "root", Receipt: "receipt", ExpectedReceiptSHA256: "receipt-hash", ExpectedImmutableClaimSHA256: "claim-hash", ExpectedSupersessionSHA256: "supersession-hash", ExpectedCurrentTargetSHA: "target", Sources: []string{"source"}, ExpectedSourceSHAs: []string{"source-sha"}}
	for _, test := range []struct {
		name   string
		change func(*WorktreeMergePublishedForwardRepairOptions)
		want   string
	}{
		{"root", func(o *WorktreeMergePublishedForwardRepairOptions) { o.ProjectsRoot = " " }, "one expected SHA per source are required"},
		{"receipt", func(o *WorktreeMergePublishedForwardRepairOptions) { o.Receipt = " " }, "one expected SHA per source are required"},
		{"receipt digest", func(o *WorktreeMergePublishedForwardRepairOptions) { o.ExpectedReceiptSHA256 = " " }, "one expected SHA per source are required"},
		{"claim digest", func(o *WorktreeMergePublishedForwardRepairOptions) { o.ExpectedImmutableClaimSHA256 = " " }, "one expected SHA per source are required"},
		{"supersession digest", func(o *WorktreeMergePublishedForwardRepairOptions) { o.ExpectedSupersessionSHA256 = " " }, "one expected SHA per source are required"},
		{"target", func(o *WorktreeMergePublishedForwardRepairOptions) { o.ExpectedCurrentTargetSHA = " " }, "one expected SHA per source are required"},
		{"sources absent", func(o *WorktreeMergePublishedForwardRepairOptions) { o.Sources = nil }, "one expected SHA per source are required"},
		{"count", func(o *WorktreeMergePublishedForwardRepairOptions) { o.ExpectedSourceSHAs = nil }, "one expected SHA per source are required"},
		{"source SHA", func(o *WorktreeMergePublishedForwardRepairOptions) { o.ExpectedSourceSHAs = []string{" "} }, "each repair source must have an expected SHA"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			o := valid
			test.change(&o)
			if err := requirePublishedForwardRepairExpectations(o); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("pin refusal=%v", err)
			}
		})
	}
	if err := requirePublishedForwardRepairExpectations(valid); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedOwnerSourcePinsPreservePositionAndExactBytes(t *testing.T) {
	t.Parallel()
	sources := []WorktreeMergeSource{{Worktree: "first", SHA: "first-sha"}, {Worktree: "second", SHA: "second-sha"}}
	for _, test := range []struct {
		name     string
		expected []string
		want     string
	}{
		{"exact", []string{"first-sha", "second-sha"}, ""},
		{"count", []string{"first-sha"}, "count does not match"},
		{"reordered", []string{"second-sha", "first-sha"}, "repair source first SHA first-sha does not match expected second-sha"},
		{"untrimmed", []string{"first-sha", " second-sha "}, "repair source second SHA second-sha does not match expected  second-sha "},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := requireExpectedPublishedForwardRepairSources(sources, test.expected)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("source pin refusal=%v", err)
			}
		})
	}
}

func TestPublishedOwnerRootOrderingRetainsHistoricalSourceRefreshes(t *testing.T) {
	t.Parallel()
	r := WorktreeMergeReceipt{TargetSHA: "receipt-target", Sources: []WorktreeMergeSource{{Task: "old", SHA: "old-sha"}}, SourceRefreshes: []WorktreeMergeSourceRefresh{{Sources: []WorktreeMergeSource{{Task: "refresh", SHA: "refresh-sha"}}}}}
	s := WorktreeMergeValidationFailureSupersession{CurrentTargetSHA: "supersession-target"}
	current := []WorktreeMergeSource{{Task: "current", SHA: "current-sha"}}
	got := publishedForwardRepairRoots("claim-base", r, s, "remote-target", current)
	want := []WorktreeMergeValidationFailureSealRoot{{Kind: "current_repair_source:current", SHA: "current-sha"}, {Kind: "failed_candidate_claim_base", SHA: "claim-base"}, {Kind: "receipt_target", SHA: "receipt-target"}, {Kind: "self_supersession_current_target", SHA: "supersession-target"}, {Kind: "current_remote_target", SHA: "remote-target"}, {Kind: "receipted_source:old", SHA: "old-sha"}, {Kind: "receipted_refresh_source:refresh", SHA: "refresh-sha"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered roots=%+v want%+v", got, want)
	}
	historical := immutableHistoricalWorktreeMergeSources(r)
	if !reflect.DeepEqual(historical, []WorktreeMergeSource{r.Sources[0], r.SourceRefreshes[0].Sources[0]}) {
		t.Fatalf("historical concat=%+v", historical)
	}
	historical[0].SHA = "changed copy"
	if r.Sources[0].SHA != "old-sha" {
		t.Fatal("historical return aliases original receipt")
	}
}

func TestPublishedOwnerCorrectionBindingRejectsEachChangedPin(t *testing.T) {
	t.Parallel()
	r := WorktreeMergeReceipt{ReceiptPath: "receipt", Candidate: WorktreeMergeCandidate{Task: "failed", Worktree: "old-path", Branch: "old-branch", SHA: "old-sha"}}
	s := WorktreeMergeValidationFailureSupersession{AcknowledgementPath: "supersession", OriginalClaimBaseSHA: "base", CurrentTargetSHA: "target"}
	c := WorktreeMergeSelfSupersessionCorrection{ReceiptPath: r.ReceiptPath, ReceiptSHA256: "receipt-hash", ImmutableClaimSHA256: "claim-hash", SupersessionPath: s.AcknowledgementPath, SupersessionSHA256: "supersession-hash", OriginalCandidate: r.Candidate, OriginalClaimBaseSHA: s.OriginalClaimBaseSHA, CurrentTargetSHA: s.CurrentTargetSHA, CorrectedReplacement: WorktreeMergeCandidate{Task: "corrected", Worktree: "new-path", Branch: "new-branch", SHA: "new-sha"}}
	if err := validatePublishedForwardRepairCorrectionBinding(c, r, s, "receipt-hash", "claim-hash", "supersession-hash"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"ReceiptPath", "ReceiptSHA256", "ImmutableClaimSHA256", "SupersessionPath", "SupersessionSHA256", "OriginalCandidate", "OriginalClaimBaseSHA", "CurrentTargetSHA", "replacement SHA", "replacement Task", "replacement Worktree", "replacement Branch"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			changed := c
			want := "does not retain immutable"
			switch field {
			case "OriginalCandidate":
				changed.OriginalCandidate.SHA = "other"
			case "replacement SHA":
				changed.CorrectedReplacement.SHA = ""
				want = "incomplete recorded replacement"
			case "replacement Task":
				changed.CorrectedReplacement.Task = ""
				want = "incomplete recorded replacement"
			case "replacement Worktree":
				changed.CorrectedReplacement.Worktree = ""
				want = "incomplete recorded replacement"
			case "replacement Branch":
				changed.CorrectedReplacement.Branch = ""
				want = "incomplete recorded replacement"
			default:
				reflect.ValueOf(&changed).Elem().FieldByName(field).SetString("other")
			}
			if err := validatePublishedForwardRepairCorrectionBinding(changed, r, s, "receipt-hash", "claim-hash", "supersession-hash"); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("correction refusal=%v", err)
			}
		})
	}
}

func TestPublishedOwnerInitialRefusalsDoNotCreateNativeEffects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	missing := filepath.Join(root, "missing-receipt.json")
	options := WorktreeMergePublishedForwardRepairOptions{ProjectsRoot: root, Receipt: missing, ExpectedReceiptSHA256: "receipt", ExpectedImmutableClaimSHA256: "claim", ExpectedSupersessionSHA256: "supersession", ExpectedCurrentTargetSHA: "target", Sources: []string{"source"}, ExpectedSourceSHAs: []string{"sha"}, Apply: true}
	if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || err.Error() != "--actor and --reason are required with --apply" {
		t.Fatalf("actor refusal=%v", err)
	}
	options.Actor, options.Reason = "actor", "reason"
	if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("actual missing receipt refusal=%v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("early refusal created effects: %v %v", entries, err)
	}
	for _, o := range []WorktreeMergePublishedCandidateAdoptionOptions{{}, {PullRequest: "7", Apply: true}} {
		if _, err := AdoptPublishedWorktreeMergeCandidate(context.Background(), o); err == nil {
			t.Fatalf("adoption accepted missing required inputs %+v", o)
		}
	}
}
