package orchestrate

import (
	"os"
	"strings"
	"testing"
)

func landValidationAssertAuditReplay(t *testing.T, f engineFixture, r WorktreeMergeReceipt, path, want string) {
	t.Helper()
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LandWorktreeMerge(t.Context(), WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath})
	if err == nil || !strings.Contains(err.Error(), want) || got.Candidate != r.Candidate {
		t.Fatalf("actual append-only replay refusal=%+v, %v", got, err)
	}
	after, readErr := os.ReadFile(r.ReceiptPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("replay rewrote receipt: %v", readErr)
	}
	retained, readErr := os.ReadFile(path)
	if readErr != nil || string(retained) != string(audit) {
		t.Fatalf("replay rewrote immutable audit: %v", readErr)
	}
}
