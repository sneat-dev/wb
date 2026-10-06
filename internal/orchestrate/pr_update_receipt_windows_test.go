//go:build windows

package orchestrate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsPRUpdateReceiptPublication(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "reports", "pr-update")
	path := filepath.Join(directory, "receipt.json")
	receipt := PullRequestUpdateResult{SchemaVersion: 1, Repository: "acme/app", PullRequest: "7", Target: "main", Branch: "feature", BeforeSHA: "before", TargetBeforeSHA: "target-before", AfterSHA: "after", TargetParentSHA: "target-parent", Status: "updated", ReceiptPath: path, RecordedAt: time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)}
	for _, status := range []string{"requested", "updated"} {
		receipt.Status = status
		if err := persistPullRequestUpdateReceipt(receipt); err != nil {
			t.Fatalf("native Windows receipt %s: %v", status, err)
		}
		want, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, '\n')
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("receipt bytes=%q,error=%v,want=%q", got, err, want)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "receipt.json" {
			t.Fatalf("publication debris=%v", entries)
		}
	}
}
