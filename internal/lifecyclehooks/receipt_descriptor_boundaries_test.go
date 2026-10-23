package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestReceiptAppendRetainsOwnedDescriptorFailureContracts(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"open", "stat", "identity", "chmod", "write", "sync"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "receipts.jsonl")
			before := []byte("existing receipt evidence\n")
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			receipt := Receipt{SchemaVersion: receiptSchemaVersion, ID: "next-receipt"}
			failure := errors.New("owned receipt operation failed")
			var inj *filewrite.Injector
			for _, step := range []filewrite.Step{filewrite.StepChmod, filewrite.StepWrite, filewrite.StepSync} {
				if phase == string(step) {
					inj = &filewrite.Injector{Step: step, Err: failure}
				}
			}
			var held *os.File
			open := func(name string, mode os.FileMode, inj *filewrite.Injector) (*os.File, error) {
				var err error
				switch phase {
				case "open":
					return os.OpenFile(filepath.Join(t.TempDir(), "missing", "receipt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
				case "identity":
					held, err = filewrite.OpenAppend(filepath.Join(t.TempDir(), "other-receipt"), mode, inj)
				default:
					held, err = filewrite.OpenAppend(name, mode, inj)
				}
				if err == nil && phase == "stat" {
					if err := held.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return held, err
			}
			err := appendReceiptInjected(path, receipt, inj, open)
			switch phase {
			case "open":
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("open cause=%v", err)
				}
			case "stat":
				if !errors.Is(err, os.ErrClosed) {
					t.Fatalf("stat cause=%v", err)
				}
			case "identity":
				if err == nil || !strings.Contains(err.Error(), "changed while opening") {
					t.Fatalf("identity accepted: %v", err)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatalf("%s cause=%v", phase, err)
				}
			}
			if held != nil {
				if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("owned descriptor left open: %v", err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "sync" {
				if !strings.HasPrefix(string(after), string(before)) || !strings.Contains(string(after), `"id":"next-receipt"`) {
					t.Fatalf("written receipt lost at sync failure: %q", after)
				}
			} else if string(after) != string(before) {
				t.Fatalf("prior evidence changed at %s: %q", phase, after)
			}
			if _, err := os.Stat(filepath.Join(receiptIndexDir(path), receipt.ID+".json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed receipt indexed: %v", err)
			}
		})
	}
}
