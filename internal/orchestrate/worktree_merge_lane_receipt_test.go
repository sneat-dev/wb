package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestMergeLaneClaimFindsCandidateAfterMalformedSibling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reports, "000-malformed.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	lane := worktreeMergeLaneID("acme/repo", "main")
	path := filepath.Join(reports, lane+".json")
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path,
		Repository: "acme/repo", Target: "main", Status: WorktreeMergePrepared,
		Candidate: WorktreeMergeCandidate{Branch: "wb/candidate"},
		Sources:   []WorktreeMergeSource{{Branch: "wb/source"}}}
	writeMergeLaneReceipt(t, path, receipt)
	claim, err := ActiveMergeLaneClaim(root, "acme/repo", "wb/candidate")
	if err != nil || claim == nil || claim.Lane != lane || claim.ReceiptPath != path {
		t.Fatalf("candidate claim = %+v, %v", claim, err)
	}
	claim, err = ActiveMergeLaneClaim(root, "other/repo", "wb/candidate")
	if err != nil || claim != nil {
		t.Fatalf("unrelated repository claim = %+v, %v", claim, err)
	}
	active, err := activeWorktreeMergeLaneReceipt(context.Background(), root, reports, lane)
	if err != nil || active == nil || active.ReceiptPath != path {
		t.Fatalf("active lane = %+v, %v", active, err)
	}
	active, err = activeWorktreeMergeLaneReceipt(context.Background(), root, reports, lane, path)
	if err != nil || active != nil {
		t.Fatalf("excluded lane = %+v, %v", active, err)
	}
	receipt.Status = WorktreeMergeComplete
	writeMergeLaneReceipt(t, path, receipt)
	claim, err = ActiveMergeLaneClaim(root, "acme/repo", "wb/source")
	if err != nil || claim != nil {
		t.Fatalf("complete receipt claim = %+v, %v", claim, err)
	}
}

func TestMergeLaneClaimIncludesRebatchedCandidate(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{Sources: []WorktreeMergeSource{{Branch: "wb/source"}},
		Candidate:           WorktreeMergeCandidate{Branch: "wb/candidate"},
		RebatchedCandidates: []WorktreeMergeCandidate{{Branch: "wb/rebatched"}}}
	for _, branch := range []string{"wb/source", "wb/candidate", "wb/rebatched"} {
		if !worktreeMergeReceiptClaimsBranch(receipt, branch) {
			t.Fatalf("branch %q lost its lane claim", branch)
		}
	}
	if worktreeMergeReceiptClaimsBranch(receipt, "wb/other") {
		t.Fatal("unrelated branch inherited a lane claim")
	}
}

func writeMergeLaneReceipt(t *testing.T, path string, receipt WorktreeMergeReceipt) {
	t.Helper()
	contents, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
