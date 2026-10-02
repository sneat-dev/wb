package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestRuntimePathProtectionReportsNativeBoundaryFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	layout := ExecutionLayout{Root: filepath.Join(root, "runtime"), ReportRoot: filepath.Join(root, "reports"), PendingMetricsRoot: filepath.Join(root, "pending")}
	refused := errors.New("mode protection refused")
	err := ensureExecutionLayoutInjected(layout, &filewrite.Injector{Step: filewrite.StepChmod, Name: layout.ReportRoot, Err: refused})
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), "protect hook runtime path "+layout.ReportRoot) {
		t.Fatalf("protection=%v", err)
	}
	if _, err := os.Stat(layout.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(layout.PendingMetricsRoot); !os.IsNotExist(err) {
		t.Fatalf("later directory created after refusal: %v", err)
	}
}

func TestPendingMetricsPublicationFailureCreatesNoReceipt(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepChmod, filewrite.StepWrite} {
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			refused := errors.New("receipt publication refused")
			path, err := persistPendingMetricsReceiptInjected(root, "events.jsonl", []Event{{Hook: "pre-push"}}, errors.New("append failed"), time.Unix(10, 0), &filewrite.Injector{Step: step, Err: refused})
			if path != "" || !errors.Is(err, refused) {
				t.Fatalf("path=%q error=%v", path, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unpublished receipts=%v error=%v", entries, err)
			}
		})
	}
}
