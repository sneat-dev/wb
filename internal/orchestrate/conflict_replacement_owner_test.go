package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConflictReplacementOwnerDeterministicTaskAndInputPolicy(t *testing.T) {
	t.Parallel()
	base := WorktreeMergeConflictCandidateRefreshOptions{ProjectsRoot: "root", Receipt: "receipt", ExpectedReceiptSHA256: " receipt ", ExpectedImmutableClaimSHA256: "claim", ExpectedCurrentTargetSHA: " target ", Sources: []string{"source"}, ExpectedSourceSHAs: []string{"head"}}
	trimmed := base
	trimmed.ExpectedReceiptSHA256 = "receipt"
	trimmed.ExpectedCurrentTargetSHA = "target"
	if base.RefreshTask() != trimmed.RefreshTask() || !strings.HasPrefix(base.RefreshTask(), "conflict-candidate-refresh-") {
		t.Fatal("trimmed native task identity differs")
	}
	for _, change := range []func(*WorktreeMergeConflictCandidateRefreshOptions){func(o *WorktreeMergeConflictCandidateRefreshOptions) { o.ExpectedReceiptSHA256 = "different" }, func(o *WorktreeMergeConflictCandidateRefreshOptions) { o.ExpectedCurrentTargetSHA = "different" }, func(o *WorktreeMergeConflictCandidateRefreshOptions) { o.ExpectedSourceSHAs = []string{"different"} }} {
		o := base
		change(&o)
		if o.RefreshTask() == base.RefreshTask() {
			t.Fatal("task omitted pinned identity")
		}
	}
	for _, field := range []string{"root", "receipt", "receipt hash", "claim hash", "target", "sources", "count", "empty source hash", "actor", "reason"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			o := base
			o.ExpectedSourceSHAs = append([]string(nil), base.ExpectedSourceSHAs...)
			switch field {
			case "root":
				o.ProjectsRoot = ""
			case "receipt":
				o.Receipt = ""
			case "receipt hash":
				o.ExpectedReceiptSHA256 = ""
			case "claim hash":
				o.ExpectedImmutableClaimSHA256 = ""
			case "target":
				o.ExpectedCurrentTargetSHA = ""
			case "sources":
				o.Sources = nil
			case "count":
				o.ExpectedSourceSHAs = nil
			case "empty source hash":
				o.ExpectedSourceSHAs[0] = " "
			case "actor":
				o.Apply = true
				o.Reason = "reason"
			case "reason":
				o.Apply = true
				o.Actor = "actor"
			}
			called := false
			read := func(string) (WorktreeMergeReceipt, error) {
				called = true
				return WorktreeMergeReceipt{}, errors.New("unexpected read")
			}
			got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, defaultRunner, read, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
			if err == nil || called || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
				t.Fatalf("early policy got=%+v err=%v read=%t", got, err, called)
			}
		})
	}
}

func TestConflictReplacementOwnerPromptBytesAndNativeScratch(t *testing.T) {
	t.Parallel()
	r := WorktreeMergeReceipt{ReceiptPath: "pinned.json", Target: "main"}
	sources := []WorktreeMergeSource{{Branch: "feature/a", SHA: "source-a"}, {Branch: "feature/b", SHA: "source-b"}}
	roots := []WorktreeMergeValidationFailureSealRoot{{Kind: "claim", SHA: "base"}, {Kind: "receipt", SHA: "candidate"}}
	path, err := writeConflictCandidateRefreshPrompt(r, "target", sources, roots, "actor", "reason")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "WB prepares one receipt-bound conflict replacement for pinned.json.\nTarget: main@target.\n- source feature/a source-a\n- source feature/b source-b\n- immutable root claim base\n- immutable root receipt candidate\nActor: actor\nReason: reason\n"
	if string(b) != want || filepath.Clean(path) != path {
		t.Fatalf("prompt=%q path=%s", b, path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("scratch mode=%v err=%v", info, err)
	}
}
