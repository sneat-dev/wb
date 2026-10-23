package lifecyclehooks

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file is the read side of the indexer receipts that
// code-index-freshness#req:freshness-in-fleet-status describes. It reads the
// receipt stream, the queue and the hooks configuration and nothing else, and
// it writes nothing: no lock file is taken (a receipt is appended whole by a
// worker that holds the stream lock, and a partial last line is left unread
// until it is complete), no directory is created, and no executor or artifact
// is opened. Classifying a checkout as fresh, stale or diverged needs Git and
// is left to the caller, which owns a hardened Git helper.
//
// Every file it reads is untrusted input in one respect: its size. The receipt
// stream is read through a bounded buffer (a line over maxReceiptLine is
// skipped), at most maxReceiptBytes of its newest part are parsed, and a queue
// entry is opened without blocking, must be a regular file and is read through
// a size limit.

// Receipt statuses a worker writes.
const (
	ReceiptSucceeded = "succeeded"
	ReceiptFailed    = "failed"
)

const (
	maxReceiptLine  = 64 * 1024
	maxReceiptBytes = 8 << 20
	maxJobBytes     = 64 * 1024
)

// ReceiptRef is the part of a receipt the freshness read keeps: its id, the
// SHA the executor ran for, its status and when it finished. Nothing else of a
// receipt, and no path, is carried.
type ReceiptRef struct {
	ID     string
	SHA    string
	Status string
	At     time.Time
}

// Record is what the receipts and the queue say of one executor on one
// checkout: whether a run is queued or running, the latest receipt, and the
// latest successful receipt. Last and Success are nil when there is none.
type Record struct {
	Pending bool
	Last    *ReceiptRef
	Success *ReceiptRef
}

// receiptEntry is what the receipts say of one executor on one checkout of one
// repository. An entry is never changed once built, so views can share it.
type receiptEntry struct {
	path          string
	last, success *ReceiptRef
}

// receiptIndex maps executor and repository to the checkouts they have
// receipts for.
type receiptIndex map[string][]*receiptEntry

// receiptCache is what a read of the receipt stream left: the file it read, how
// big and how new it was, how far into it the last complete line ended, and
// the index of what that part holds.
type receiptCache struct {
	info     os.FileInfo
	size     int64
	modified time.Time
	offset   int64
	index    receiptIndex
}

// FreshnessReader reads indexer receipts without writing. A stream that has
// not changed is not parsed again, and one that only grew is parsed from where
// the last read ended, so a new receipt costs its own line. It is safe for
// concurrent use.
type FreshnessReader struct {
	ConfigPath  string
	StateDir    string
	ReceiptPath string

	open     func(string) (*os.File, error)
	maxBytes int64

	mu    sync.Mutex
	cache *receiptCache
}

// NewFreshnessReader is a reader of dispatcher's configuration, queue and
// receipt stream, with the paths resolved as the lifecycle-hook worker
// resolves them: the same defaulting function (Dispatcher.defaults, from
// DefaultDispatcher) that `wb hooks lifecycle` runs with, so a process started
// with an XDG_STATE_HOME override reads the stream that override names. It
// does not start the dispatcher or create any of them.
func NewFreshnessReader(dispatcher Dispatcher) *FreshnessReader {
	dispatcher = dispatcher.defaults()
	return &FreshnessReader{ConfigPath: dispatcher.ConfigPath, StateDir: dispatcher.StateDir, ReceiptPath: dispatcher.ReceiptPath}
}

// FreshnessView is one consistent read: the configured executors with the
// repositories each is bound to, the receipts, and the queued runs.
type FreshnessView struct {
	config   Config
	receipts receiptIndex
	queued   map[string][]string
}

// Read loads the hooks configuration, the receipts and the queue. A missing
// configuration, receipt stream or queue is an empty one, not an error; an
// invalid configuration, a receipt stream or queue directory that is not
// trusted (a symbolic link, or not owned by the current user or writable by
// others) and an unreadable one are errors.
func (r *FreshnessReader) Read() (*FreshnessView, error) {
	config, _, err := Load(r.ConfigPath)
	if err != nil {
		return nil, err
	}
	receipts, err := r.readReceipts()
	if err != nil {
		return nil, err
	}
	queued, err := r.readQueued()
	if err != nil {
		return nil, err
	}
	return &FreshnessView{config: config, receipts: receipts, queued: queued}, nil
}

func (r *FreshnessReader) readReceipts() (receiptIndex, error) {
	info, exists, err := validateTrustedDataFile(r.ReceiptPath, "lifecycle hook receipt stream")
	if err != nil {
		return nil, err
	}
	if !exists {
		return receiptIndex{}, nil
	}
	open := r.open
	if open == nil {
		open = os.Open
	}
	file, err := open(r.ReceiptPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("lifecycle hook receipt stream changed while opening")
	}
	limit := r.maxBytes
	if limit <= 0 {
		limit = maxReceiptBytes
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cached := r.cache
	same := cached != nil && os.SameFile(cached.info, opened)
	grown := same && opened.Size() > cached.size
	if same && opened.Size() == cached.size && opened.ModTime().Equal(cached.modified) {
		return cached.index, nil
	}
	start, base, skipFirst := int64(0), receiptIndex{}, false
	if grown && opened.Size()-cached.offset <= limit && endsLineBefore(file, cached.offset) {
		start, base = cached.offset, cached.index
	} else if opened.Size() > limit {
		start, skipFirst = opened.Size()-limit, true
	}
	section := io.NewSectionReader(file, start, opened.Size()-start+limit)
	index, consumed, err := parseReceipts(bufio.NewReaderSize(section, maxReceiptLine), base, skipFirst)
	if err != nil {
		return nil, err
	}
	r.cache = &receiptCache{info: opened, size: opened.Size(), modified: opened.ModTime(), offset: start + consumed, index: index}
	return index, nil
}

// endsLineBefore reports whether offset is where a line starts: the start of
// the file, or just after a newline. A stream rewritten in place and grown past
// its old size can leave the old offset in the middle of a line.
func endsLineBefore(file *os.File, offset int64) bool {
	if offset == 0 {
		return true
	}
	var previous [1]byte
	n, err := file.ReadAt(previous[:], offset-1)
	return n == 1 && err == nil && previous[0] == '\n'
}

// parseReceipts adds the receipts reader holds to a copy of base. It returns
// how many bytes of complete lines it consumed: a line without its newline yet
// is left for the next read, a line longer than the buffer is skipped, and a
// line that does not decode is ignored. With skipFirst the reader starts in
// the middle of a line, which is dropped.
func parseReceipts(reader *bufio.Reader, base receiptIndex, skipFirst bool) (receiptIndex, int64, error) {
	index := make(receiptIndex, len(base))
	for key, entries := range base {
		index[key] = entries
	}
	cloned := map[string]bool{}
	var consumed int64
	if skipFirst {
		skipped, _, err := drainLine(reader, 0)
		if err != nil {
			return nil, 0, err
		}
		consumed += skipped
	}
	for {
		line, err := reader.ReadSlice('\n')
		switch {
		case err == nil:
			consumed += int64(len(line))
			if receipt, decodeErr := decodeReceipt(line); decodeErr == nil && receipt.Executor != "" && receipt.Checkout != "" {
				addReceipt(index, cloned, receipt)
			}
		case errors.Is(err, bufio.ErrBufferFull):
			skipped, complete, drainErr := drainLine(reader, len(line))
			if drainErr != nil {
				return nil, 0, drainErr
			}
			if !complete {
				return index, consumed, nil
			}
			consumed += skipped
		case errors.Is(err, io.EOF):
			return index, consumed, nil
		default:
			return nil, 0, err
		}
	}
}

// drainLine reads to the end of the current line, which already had buffered
// bytes read; it returns the line's whole length and whether its newline was
// found.
func drainLine(reader *bufio.Reader, buffered int) (int64, bool, error) {
	total := int64(buffered)
	for {
		chunk, err := reader.ReadSlice('\n')
		total += int64(len(chunk))
		switch {
		case err == nil:
			return total, true, nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return total, false, nil
		default:
			return 0, false, err
		}
	}
}

// addReceipt records receipt as the latest for its executor, repository and
// checkout. A key's slice is copied the first time a parse touches it, because
// the index it started from is shared with earlier views.
func addReceipt(index receiptIndex, cloned map[string]bool, receipt Receipt) {
	key := receiptKey(receipt.Executor, receipt.Repository)
	if !cloned[key] {
		cloned[key] = true
		index[key] = append([]*receiptEntry(nil), index[key]...)
	}
	ref := &ReceiptRef{ID: receipt.ID, SHA: receipt.NewSHA, Status: receipt.Status, At: receipt.FinishedAt}
	path := filepath.Clean(receipt.Checkout)
	entry := &receiptEntry{path: path, last: ref}
	if receipt.Status == ReceiptSucceeded {
		entry.success = ref
	}
	for position, existing := range index[key] {
		if existing.path != path {
			continue
		}
		if entry.success == nil {
			entry.success = existing.success
		}
		index[key][position] = entry
		return
	}
	index[key] = append(index[key], entry)
}

// readQueued lists the executor, repository and checkout of every queued or
// running job. The state directory and each queue directory are checked as the
// worker's own are (not a symbolic link, owned by the current user, not
// writable by others); an entry must be a regular file, is opened without
// blocking so a named pipe cannot hold the read, and is read through a size
// limit. An entry that fails any of that, or does not decode, is skipped.
func (r *FreshnessReader) readQueued() (map[string][]string, error) {
	queued := map[string][]string{}
	for _, directory := range []string{r.StateDir, filepath.Join(r.StateDir, "pending"), filepath.Join(r.StateDir, "running")} {
		err := validatePrivateDirectory(directory, "lifecycle hook state directory")
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"pending", "running"} {
		directory := filepath.Join(r.StateDir, name)
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
				if job, ok := readBoundedJob(filepath.Join(directory, entry.Name()), entry); ok {
					key := receiptKey(job.Executor, job.Event.Repository)
					queued[key] = append(queued[key], filepath.Clean(job.Event.Checkout))
				}
			}
		}
	}
	return queued, nil
}

// readBoundedJob reads one queue entry that the directory listing showed as a
// regular file. It opens it without blocking, requires the opened file to be
// that same regular file, and reads at most maxJobBytes.
func readBoundedJob(path string, entry os.DirEntry) (queuedJob, bool) {
	listed, _ := entry.Info()
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return queuedJob{}, false
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err == nil && opened.Mode().IsRegular() && os.SameFile(listed, opened) {
		raw, readErr := io.ReadAll(io.LimitReader(file, maxJobBytes+1))
		if readErr == nil && len(raw) <= maxJobBytes {
			job, decodeErr := decodeJob(raw, path)
			return job, decodeErr == nil
		}
	}
	return queuedJob{}, false
}

func receiptKey(executor, repository string) string {
	return executor + "\x00" + strings.ToLower(strings.TrimSpace(repository))
}

// samePath reports whether candidate is checkout: the same cleaned path, or,
// when both exist, the same directory by identity, so a spelling that differs
// only in case on a case-insensitive file system, or by a symbolic link,
// matches. target is checkout's own stat, taken once by the caller.
func samePath(candidate, checkout string, target func() os.FileInfo) bool {
	if candidate == checkout {
		return true
	}
	other, err := os.Stat(candidate)
	return err == nil && target() != nil && os.SameFile(other, target())
}

// Executors lists, sorted, the executors bound to a checkout-updated event of
// repository, which is the lower-case host/owner/name identity the dispatcher
// matches bindings against.
func (v *FreshnessView) Executors(repository string) []string {
	event := Event{Name: EventCheckoutUpdated, Repository: strings.ToLower(repository)}
	seen := map[string]bool{}
	var names []string
	for _, binding := range v.config.Bindings {
		if !binding.matches(event) {
			continue
		}
		for _, name := range binding.Execute {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// Record is what the view holds of executor on checkout of repository. Receipts
// and queued jobs are matched on all three, so a path another repository once
// used does not lend its receipts.
func (v *FreshnessView) Record(executor, repository, checkout string) Record {
	key := receiptKey(executor, repository)
	checkout = filepath.Clean(checkout)
	var stat os.FileInfo
	statted := false
	target := func() os.FileInfo {
		if !statted {
			stat, _ = os.Stat(checkout)
			statted = true
		}
		return stat
	}
	var record Record
	for _, queued := range v.queued[key] {
		if samePath(queued, checkout, target) {
			record.Pending = true
		}
	}
	for _, entry := range v.receipts[key] {
		if !samePath(entry.path, checkout, target) {
			continue
		}
		if record.Last == nil || !entry.last.At.Before(record.Last.At) {
			record.Last = entry.last
		}
		if entry.success != nil && (record.Success == nil || !entry.success.At.Before(record.Success.At)) {
			record.Success = entry.success
		}
	}
	return record
}

// Signature is a digest of everything Record and Executors say for repository
// on checkouts, so a caller can tell, without Git, that nothing a code-index
// state depends on besides HEAD has changed.
func (v *FreshnessView) Signature(repository string, checkouts []string) string {
	sum := sha256.New()
	for _, executor := range v.Executors(repository) {
		_, _ = fmt.Fprintf(sum, "executor\x00%s\n", executor)
		for _, checkout := range checkouts {
			record := v.Record(executor, repository, checkout)
			_, _ = fmt.Fprintf(sum, "%s\x00%t", filepath.Clean(checkout), record.Pending)
			for _, ref := range []*ReceiptRef{record.Last, record.Success} {
				if ref == nil {
					_, _ = fmt.Fprint(sum, "\x00-")
					continue
				}
				_, _ = fmt.Fprintf(sum, "\x00%s", ref.ID)
			}
			_, _ = fmt.Fprintln(sum)
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}
