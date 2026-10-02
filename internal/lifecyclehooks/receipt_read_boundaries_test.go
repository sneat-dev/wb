package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReceiptScanRetainsOpenAndOwnedStatFailuresWithoutVisiting(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"open", "stat", "identity"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "receipts.jsonl")
			if err := os.WriteFile(path, []byte(`{"schema_version":1,"id":"receipt"}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var held *os.File
			visits := 0
			_, err := scanReceiptsWithOpen(path, func(Receipt) { visits++ }, func(path string) (*os.File, error) {
				if phase == "open" {
					return os.Open(path + "\x00")
				}
				var err error
				if phase == "identity" {
					path = hkCovWriteFile(t, filepath.Join(t.TempDir(), "other-receipt"), "other", 0600)
				}
				held, err = os.Open(path)
				if err == nil && phase == "stat" {
					if err := held.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return held, err
			})
			if err == nil || visits != 0 {
				t.Fatalf("failed scan visited=%d error=%v", visits, err)
			}
			if phase == "stat" && !errors.Is(err, os.ErrClosed) {
				t.Fatalf("closed descriptor cause=%v", err)
			}
		})
	}
}
