package orchestrate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPRUpdateReceiptPublicationRetainsPreviousEvidenceOnRefusal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, want string }{
		{"missing path", "PR update receipt path is required"},
		{"unsupported timestamp", "year outside of range"},
		{"parent is a file", "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			original := []byte("previous receipt evidence\n")
			existing := filepath.Join(directory, "existing.json")
			if err := os.WriteFile(existing, original, 0o600); err != nil {
				t.Fatal(err)
			}
			receipt := PullRequestUpdateResult{SchemaVersion: 1, ReceiptPath: existing}
			switch tc.name {
			case "missing path":
				receipt.ReceiptPath = ""
			case "unsupported timestamp":
				receipt.RecordedAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
			case "parent is a file":
				receipt.ReceiptPath = filepath.Join(existing, "receipt.json")
			}
			err := persistPullRequestUpdateReceipt(receipt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("publication error=%v,want %q", err, tc.want)
			}
			after, readErr := os.ReadFile(existing)
			if readErr != nil || !bytes.Equal(after, original) {
				t.Fatalf("previous evidence=%q,read error=%v", after, readErr)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "existing.json" {
				t.Fatalf("unexpected publication debris: %v", entries)
			}
		})
	}
}

func TestPRUpdateReceiptPublicationReplacesPrivateBytesAndRecoversMissingParent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"replace existing receipt", "recreate missing parent"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			directory := filepath.Join(root, "reports", "pr-update")
			path := filepath.Join(directory, "receipt.json")
			if name == "replace existing receipt" {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("old receipt\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			} else if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("parent must start missing: %v", err)
			}
			receipt := PullRequestUpdateResult{SchemaVersion: 1, Repository: "acme/app", PullRequest: "7", Target: "main", Branch: "feature", BeforeSHA: "before", TargetBeforeSHA: "target-before", AfterSHA: "after", TargetParentSHA: "target-parent", TargetCurrentSHA: "target-current", Status: "updated_partial", LocalSync: "local checkout not fast-forwarded", Reason: "recorded refusal", ReceiptPath: path, RecordedAt: time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)}
			if err := persistPullRequestUpdateReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			want, err := json.MarshalIndent(receipt, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, '\n')
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("receipt bytes=%q,read error=%v,want=%q", got, err, want)
			}
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if stat.Mode().Perm() != 0o600 {
				t.Fatalf("receipt mode=%o,want600", stat.Mode().Perm())
			}
			for _, parent := range []string{filepath.Join(root, "reports"), directory} {
				stat, err := os.Stat(parent)
				if err != nil {
					t.Fatal(err)
				}
				if !stat.IsDir() || stat.Mode().Perm() != 0o700 {
					t.Fatalf("parent %q mode=%v,want directory700", parent, stat.Mode())
				}
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "receipt.json" {
				t.Fatalf("publication debris=%v", entries)
			}
		})
	}
}
