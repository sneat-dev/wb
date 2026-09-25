package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR8 is task-9 PR-8's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally matches
// a different test's error by coincidence.
var errBoomPR8 = errors.New("pr8 boom")

func TestRewriteReceiptRecordsInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
		filewrite.StepDirSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "receipts.jsonl")
			records := []receiptRecord{{raw: []byte(`{"a":1}`)}}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			err := rewriteReceiptRecordsInjected(path, records, inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("rewriteReceiptRecordsInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".lifecycle-receipts-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if step != filewrite.StepDirSync && len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			if step == filewrite.StepDirSync {
				// The rename already succeeded by the time the directory
				// sync is injected to fail, so the temp name is gone and
				// the destination file is (correctly) already published.
				if _, statErr := os.Stat(path); statErr != nil {
					t.Fatalf("dir-sync failure unexpectedly lost the published file: %v", statErr)
				}
				return
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed write published a visible receipts file: %v", statErr)
			}
		})
	}
}

// TestRewriteReceiptRecordsInjectedPublishesAt0600 is review-763's
// chmod-preset-Hook lesson applied to this site: os.CreateTemp already
// creates its temp file at 0600, the same as this site's own final published
// mode, so a plain end-to-end 0600 assertion cannot tell a real chmod call
// from a deleted one.
func TestRewriteReceiptRecordsInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "receipts.jsonl")
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(dir, ".lifecycle-receipts-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary file before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if err := rewriteReceiptRecordsInjected(path, nil, inj); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("Hook did not run before the real chmod")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published receipts file mode = %o, want 0600", perm)
	}
}

func TestRewriteReceiptRecordsInjectedSkipsRemovedRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "receipts.jsonl")
	records := []receiptRecord{
		{raw: []byte(`{"a":1}`)},
		{raw: []byte(`{"a":2}`), remove: true},
		{raw: []byte(`{"a":3}`)},
	}
	if err := rewriteReceiptRecordsInjected(path, records, nil); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":1}\n{\"a\":3}\n"
	if string(contents) != want {
		t.Fatalf("rewritten receipts = %q, want %q", contents, want)
	}
}
