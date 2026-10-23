package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredStageInventoryRetainsNativeEntryRefusals(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"info", "content"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			stage := t.TempDir()
			path := filepath.Join(stage, "evidence")
			saved := path + "-retained"
			if err := os.WriteFile(path, []byte("owned evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			inventory, err := inventoryStageObserved(stage, func(current, at string) {
				if current == path && at == boundary {
					called = true
					if err := os.Rename(path, saved); err != nil {
						t.Fatal(err)
					}
				}
			})
			if !called || !errors.Is(err, os.ErrNotExist) || inventory != (stageContentInventory{}) {
				t.Fatalf("inventory=%+v called=%v error=%v", inventory, called, err)
			}
			if content, err := os.ReadFile(saved); err != nil || string(content) != "owned evidence" {
				t.Fatalf("retained=%q %v", content, err)
			}
		})
	}
}

func TestRetiredStageReceiptDiscoveryKeepsUnreadableAndForeignEvidence(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, "reports", "worktree-stage-recovery")
	unreadable := filepath.Join(root, "other", "receipt-directory.json")
	if err := os.MkdirAll(unreadable, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreadable, "evidence"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	// A directory is filtered before ReadFile; a malformed receipt remains evidence.
	malformed := filepath.Join(root, "other", "receipt-invalid.json")
	if err := os.WriteFile(malformed, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, "task", "receipt-applied.json")
	outcome := RetiredStageRecoveryOutcome{Results: []RetiredStageRecoveryResult{{Task: "foreign", Stage: "stage", Applied: true}, {Task: "task", Stage: "stage", Applied: true}}}
	if err := writeRetiredStageReceiptInjected(receipt, outcome, nil); err != nil {
		t.Fatal(err)
	}
	if got := findRetiredStageReceipt(home, "task", "stage"); got != receipt {
		t.Fatalf("receipt=%q want=%q", got, receipt)
	}
	if got := findRetiredStageReceipt(home, "absent", "stage"); got != "" {
		t.Fatalf("foreign receipt accepted=%q", got)
	}
	if raw, err := os.ReadFile(malformed); err != nil || string(raw) != "{" {
		t.Fatalf("malformed evidence=%q %v", raw, err)
	}
}

func TestRetiredStageReceiptPublicationPreservesPriorBytes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte("prior"), 0600); err != nil {
		t.Fatal(err)
	}
	// MkdirAll must refuse a regular-file parent without replacing it.
	err := writeRetiredStageReceiptInjected(filepath.Join(path, "receipt.json"), RetiredStageRecoveryOutcome{}, nil)
	if err == nil || !strings.Contains(err.Error(), "create private stage recovery receipt directory") {
		t.Fatalf("parent refusal=%v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "prior" {
		t.Fatalf("prior=%q %v", raw, err)
	}
}
