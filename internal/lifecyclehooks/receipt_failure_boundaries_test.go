package lifecyclehooks

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestReceiptAppendRejectsUnencodableTimestampWithoutChangingStream(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	before := []byte("existing receipt evidence\n")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{SchemaVersion: receiptSchemaVersion, ID: "invalid-time", StartedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := appendReceipt(path, receipt); err == nil || !strings.Contains(err.Error(), "year outside of range") {
		t.Fatalf("invalid receipt accepted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("prior receipt evidence changed: %q %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(receiptIndexDir(path), receipt.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid receipt indexed: %v", err)
	}
}

func TestQueuePublicationRejectsUnencodableClockBeforeCreatingJob(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	dispatcher.Now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
	if _, err := dispatcher.enqueue(pending{name: "index", event: hkCovEvent(checkout), count: 1}); err == nil || !strings.Contains(err.Error(), "write lifecycle hook queue item") {
		t.Fatalf("invalid queue clock accepted: %v", err)
	}
	entries, err := os.ReadDir(dispatcher.pendingDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid queued job published: %v %v", entries, err)
	}
}

func TestReceiptGCPreservesEvidenceWhenNativeRewriteFails(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	receipt := hkCovReceipt("receipt")
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, receipt)
	before, err := os.ReadFile(dispatcher.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("receipt rewrite publication failed")
	report, err := dispatcher.gcWithRewrite(GCOptions{Apply: true, OlderThan: time.Minute, Now: receipt.FinishedAt.Add(time.Hour)}, func(path string, records []receiptRecord) error {
		return rewriteReceiptRecordsInjected(path, records, &filewrite.Injector{Step: filewrite.StepRename, Err: failure})
	})
	if !errors.Is(err, failure) || report.RemovedReceipts != 0 || len(report.Candidates) != 1 {
		t.Fatalf("rewrite failure lost=%+v %v", report, err)
	}
	after, err := os.ReadFile(dispatcher.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("receipt evidence changed=%q %v", after, err)
	}
}

func TestReceiptIDRetainsTimestampAndRandomSuffixShape(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 123).UTC()
	prefix := now.Format("20060102T150405.000000000Z") + "-"
	id := receiptID(now)
	raw, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	if !strings.HasPrefix(id, prefix) || len(raw) != 8 || err != nil {
		t.Fatalf("receipt identity=%q random bytes=%d %v", id, len(raw), err)
	}
}
