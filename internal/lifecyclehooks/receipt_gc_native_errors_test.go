//go:build !windows

package lifecyclehooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReceiptGCRefusesUnreadableUnseenCoordinateBeforeRewriting(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{SchemaVersion: receiptSchemaVersion, ID: "receipt", FinishedAt: time.Now().Add(-time.Hour)}
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, receipt)
	before, err := os.ReadFile(dispatcher.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	unseen := filepath.Join(dispatcher.unseenDir(), receipt.ID+".json")
	if err := os.Symlink(filepath.Base(unseen), unseen); err != nil {
		t.Fatal(err)
	}
	report, err := dispatcher.GC(GCOptions{Apply: true, OlderThan: time.Minute, Now: time.Now()})
	if err == nil || len(report.Candidates) != 0 || report.RemovedReceipts != 0 {
		t.Fatalf("unreadable unseen receipt discarded=%+v %v", report, err)
	}
	after, err := os.ReadFile(dispatcher.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("stream changed=%q %v", after, err)
	}
	if target, err := os.Readlink(unseen); err != nil || target != filepath.Base(unseen) {
		t.Fatalf("unseen evidence changed=%q %v", target, err)
	}
}
