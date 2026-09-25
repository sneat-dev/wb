package lifecyclehooks

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/sneat-dev/wb/internal/filewrite"
)

type GCOptions struct {
	Apply     bool
	Keep      int
	OlderThan time.Duration
	Now       time.Time
}

type GCReport struct {
	Apply              bool     `json:"apply"`
	Receipts           int      `json:"receipts"`
	Candidates         []string `json:"candidates,omitempty"`
	RemovedReceipts    int      `json:"removed_receipts"`
	RemovedDiagnostics int      `json:"removed_diagnostics"`
	Findings           []string `json:"findings,omitempty"`
}

type receiptRecord struct {
	raw     []byte
	receipt Receipt
	valid   bool
	remove  bool
}

func (dispatcher Dispatcher) GC(options GCOptions) (GCReport, error) {
	dispatcher = dispatcher.defaults()
	if err := dispatcher.ensureState(); err != nil {
		return GCReport{}, err
	}
	if err := ensureTrustedParent(dispatcher.ReceiptPath, "lifecycle hook receipt parent"); err != nil {
		return GCReport{}, err
	}
	if options.Keep < 0 {
		return GCReport{}, errors.New("receipt retention count must not be negative")
	}
	if options.OlderThan < 0 {
		return GCReport{}, errors.New("diagnostic retention age must not be negative")
	}
	if options.Now.IsZero() {
		options.Now = dispatcher.Now().UTC()
	}
	report := GCReport{Apply: options.Apply}
	lock := flock.New(dispatcher.ReceiptPath + ".lock")
	if err := lock.Lock(); err != nil {
		return report, err
	}
	defer func() { _ = lock.Unlock() }()
	if _, _, err := validateTrustedDataFile(dispatcher.ReceiptPath, "lifecycle hook receipt stream"); err != nil {
		return report, err
	}
	records, findings, err := readReceiptRecords(dispatcher.ReceiptPath)
	if err != nil {
		return report, err
	}
	report.Findings = append(report.Findings, findings...)
	validIndexes := make([]int, 0, len(records))
	for index := range records {
		if records[index].valid {
			validIndexes = append(validIndexes, index)
		}
	}
	report.Receipts = len(validIndexes)
	protectedFrom := len(validIndexes) - options.Keep
	if protectedFrom < 0 {
		protectedFrom = 0
	}
	cutoff := options.Now.Add(-options.OlderThan)
	for ordinal, index := range validIndexes {
		receipt := records[index].receipt
		if _, err := os.Stat(filepath.Join(dispatcher.unseenDir(), receipt.ID+".json")); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
		when := receipt.FinishedAt
		if when.IsZero() {
			when = receipt.StartedAt
		}
		if ordinal >= protectedFrom || when.IsZero() || when.After(cutoff) {
			continue
		}
		records[index].remove = true
		report.Candidates = append(report.Candidates, receipt.ID)
	}
	if !options.Apply || len(report.Candidates) == 0 {
		return report, nil
	}
	if err := rewriteReceiptRecords(dispatcher.ReceiptPath, records); err != nil {
		return report, err
	}
	for _, record := range records {
		if !record.remove {
			continue
		}
		if err := os.Remove(filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), record.receipt.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
		if removed, err := dispatcher.removeReceiptDiagnostics(record.receipt); err != nil {
			return report, err
		} else if removed {
			report.RemovedDiagnostics++
		}
	}
	report.RemovedReceipts = len(report.Candidates)
	return report, nil
}

func readReceiptRecords(path string) ([]receiptRecord, []string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	var records []receiptRecord
	var findings []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := append([]byte(nil), scanner.Bytes()...)
		record := receiptRecord{raw: raw}
		record.receipt, err = decodeReceipt(raw)
		if err != nil {
			findings = append(findings, fmt.Sprintf("preserved invalid lifecycle hook receipt line %d: %v", line, err))
		} else {
			record.valid = true
		}
		records = append(records, record)
	}
	return records, findings, scanner.Err()
}

func rewriteReceiptRecords(path string, records []receiptRecord) error {
	return rewriteReceiptRecordsInjected(path, records, nil)
}

// rewriteReceiptRecordsInjected is rewriteReceiptRecords's test seam (task-9
// PR-8): every production call site reaches it only through
// rewriteReceiptRecords, which always passes a nil *filewrite.Injector, so
// production behaviour is unchanged. A test passes its own Injector to reach
// the create/chmod/write/sync/close/rename/dir-sync failure branches
// deterministically.
func rewriteReceiptRecordsInjected(path string, records []receiptRecord, inj *filewrite.Injector) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := filewrite.CreateTemp(filepath.Dir(path), ".lifecycle-receipts-*", inj)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryPath, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryPath, inj)
		return err
	}
	for _, record := range records {
		if record.remove {
			continue
		}
		if err := filewrite.Write(temporary, append(record.raw, '\n'), temporaryPath, inj); err != nil {
			_ = filewrite.Close(temporary, temporaryPath, inj)
			return err
		}
	}
	if err := filewrite.Sync(temporary, temporaryPath, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryPath, inj)
		return err
	}
	if err := filewrite.Close(temporary, temporaryPath, inj); err != nil {
		return err
	}
	if err := filewrite.Rename(temporaryPath, path, inj); err != nil {
		return err
	}
	return syncDirectoryInjected(filepath.Dir(path), inj)
}

func (dispatcher Dispatcher) removeReceiptDiagnostics(receipt Receipt) (bool, error) {
	directory := filepath.Join(dispatcher.diagnosticsDir(), receipt.ID)
	for _, diagnostic := range []string{receipt.StdoutPath, receipt.StderrPath} {
		if diagnostic == "" {
			continue
		}
		if !pathWithin(directory, diagnostic) {
			return false, fmt.Errorf("refuse diagnostic path outside receipt directory: %s", diagnostic)
		}
	}
	if _, err := os.Stat(directory); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := os.RemoveAll(directory); err != nil {
		return false, err
	}
	return true, syncDirectory(dispatcher.diagnosticsDir())
}
