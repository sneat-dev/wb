package lifecyclehooks

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const freshnessRepository = "github.com/acme/app"

func freshnessReceipt(t *testing.T, dispatcher Dispatcher, id, repository, checkout, sha, status string, finished time.Time) {
	t.Helper()
	receipt := Receipt{
		SchemaVersion: receiptSchemaVersion, ID: id, Event: EventCheckoutUpdated, Repository: repository,
		Checkout: checkout, NewSHA: sha, Cause: "pull", Executor: "index", QueuedAt: finished, StartedAt: finished, FinishedAt: finished, Status: status,
	}
	if err := appendReceipt(dispatcher.ReceiptPath, receipt); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func readView(t *testing.T, reader *FreshnessReader) *FreshnessView {
	t.Helper()
	view, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestFreshnessViewReportsLatestAndSuccessfulReceiptPerCheckout(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	other := filepath.Join(filepath.Dir(checkout), "other")
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, at)
	freshnessReceipt(t, dispatcher, "r2", freshnessRepository, checkout, "bbb", ReceiptFailed, at.Add(time.Minute))
	freshnessReceipt(t, dispatcher, "r3", freshnessRepository, other, "ccc", ReceiptFailed, at)
	freshnessReceipt(t, dispatcher, "r4", freshnessRepository, checkout, "ddd", ReceiptSucceeded, at.Add(-time.Hour))
	view := readView(t, NewFreshnessReader(dispatcher))
	record := view.Record("index", freshnessRepository, checkout)
	if record.Pending || record.Last == nil || record.Last.SHA != "ddd" || record.Success == nil || record.Success.SHA != "ddd" {
		t.Fatalf("the latest in the stream wins: %+v / %+v / %+v", record, record.Last, record.Success)
	}
	if only := view.Record("index", freshnessRepository, other); only.Success != nil || only.Last == nil || only.Last.SHA != "ccc" {
		t.Fatalf("a checkout with only a failed receipt has no success: %+v", only)
	}
	if none := view.Record("index", freshnessRepository, filepath.Join(checkout, "missing")); none.Last != nil || none.Success != nil || none.Pending {
		t.Fatalf("a checkout with no receipt has none: %+v", none)
	}
	if got := view.Executors("GitHub.com/Acme/App"); !slices.Equal(got, []string{"index"}) {
		t.Fatalf("executors = %v", got)
	}
	if got := view.Executors("gitlab.com/acme/app"); len(got) != 0 {
		t.Fatalf("an unbound repository has no executors: %v", got)
	}
	if view.Signature(freshnessRepository, []string{checkout}) == view.Signature(freshnessRepository, []string{filepath.Join(checkout, "missing")}) {
		t.Fatal("a checkout with receipts and one without must not share a signature")
	}
}

func TestAFailureAfterASuccessKeepsTheSuccess(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, at)
	freshnessReceipt(t, dispatcher, "r2", freshnessRepository, checkout, "bbb", ReceiptFailed, at.Add(time.Minute))
	record := readView(t, NewFreshnessReader(dispatcher)).Record("index", freshnessRepository, checkout)
	if record.Last.SHA != "bbb" || record.Success.SHA != "aaa" {
		t.Fatalf("last %+v success %+v", record.Last, record.Success)
	}
}

func TestAReceiptDoesNotCountForAnotherRepositoryAtTheSamePath(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	freshnessReceipt(t, dispatcher, "r1", "github.com/acme/old", checkout, "aaa", ReceiptSucceeded, time.Now())
	view := readView(t, NewFreshnessReader(dispatcher))
	if record := view.Record("index", freshnessRepository, checkout); record.Last != nil {
		t.Fatalf("a path another repository used lent its receipt: %+v", record.Last)
	}
	if record := view.Record("index", "GitHub.com/Acme/Old", checkout); record.Last == nil {
		t.Fatal("the repository is matched case-insensitively")
	}
}

func TestAPathSpelledAsASymbolicLinkMatchesTheSameDirectory(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	link := filepath.Join(filepath.Dir(checkout), "link")
	if err := os.Symlink(checkout, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, link, "aaa", ReceiptSucceeded, time.Now())
	view := readView(t, NewFreshnessReader(dispatcher))
	if record := view.Record("index", freshnessRepository, checkout); record.Success == nil {
		t.Fatal("the same directory under another spelling must match")
	}
	if record := view.Record("index", freshnessRepository, filepath.Join(checkout, "absent")); record.Success != nil {
		t.Fatal("a directory that does not exist matches nothing but its own spelling")
	}
}

func TestTwoSpellingsOfOneDirectoryMergeToTheNewestReceipts(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	link := filepath.Join(filepath.Dir(checkout), "link")
	if err := os.Symlink(checkout, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "old", ReceiptSucceeded, at)
	freshnessReceipt(t, dispatcher, "r2", freshnessRepository, link, "new", ReceiptSucceeded, at.Add(time.Hour))
	freshnessReceipt(t, dispatcher, "r3", freshnessRepository, link, "newest", ReceiptFailed, at.Add(2*time.Hour))
	record := readView(t, NewFreshnessReader(dispatcher)).Record("index", freshnessRepository, checkout)
	if record.Success.SHA != "new" || record.Last.SHA != "newest" {
		t.Fatalf("merged = last %+v success %+v", record.Last, record.Success)
	}
}

func TestFreshnessReaderParsesOnlyWhatIsNewAndKeepsEarlierViewsIntact(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, at)
	reader := NewFreshnessReader(dispatcher)
	first := readView(t, reader)
	cached := reader.cache
	if again := readView(t, reader); reader.cache != cached || again.Record("index", freshnessRepository, checkout).Success.SHA != "aaa" {
		t.Fatal("an unchanged stream was parsed again")
	}
	before := first.Signature(freshnessRepository, []string{checkout})
	offset := cached.offset
	freshnessReceipt(t, dispatcher, "r2", freshnessRepository, checkout, "bbb", ReceiptSucceeded, at.Add(time.Hour))
	third := readView(t, reader)
	if reader.cache.offset <= offset || reader.cache.info == nil {
		t.Fatalf("the read did not advance: %d -> %d", offset, reader.cache.offset)
	}
	if got := third.Record("index", freshnessRepository, checkout).Success; got == nil || got.SHA != "bbb" {
		t.Fatalf("a new receipt was not read: %+v", got)
	}
	if got := first.Record("index", freshnessRepository, checkout).Success; got.SHA != "aaa" {
		t.Fatalf("an earlier view changed under its reader: %+v", got)
	}
	if third.Signature(freshnessRepository, []string{checkout}) == before {
		t.Fatal("a new receipt did not change the signature")
	}
}

func TestFreshnessReaderLeavesAnUnfinishedLineUntilItIsComplete(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, at)
	reader := NewFreshnessReader(dispatcher)
	readView(t, reader)
	line := `{"schema_version":2,"id":"r2","repository":"` + freshnessRepository + `","checkout":"` + checkout + `","new_sha":"bbb","executor":"index","status":"succeeded","finished_at":"2026-10-01T10:00:00Z"}`
	appendRaw(t, dispatcher.ReceiptPath, line[:60])
	if got := readView(t, reader).Record("index", freshnessRepository, checkout).Success.SHA; got != "aaa" {
		t.Fatalf("a half-written line was used: %s", got)
	}
	appendRaw(t, dispatcher.ReceiptPath, line[60:]+"\n")
	if got := readView(t, reader).Record("index", freshnessRepository, checkout).Success.SHA; got != "bbb" {
		t.Fatalf("the completed line was not read: %s", got)
	}
}

func TestFreshnessReaderSkipsMalformedAndOverlongLines(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, time.Now())
	for _, line := range []string{"not json\n", `{"schema_version":2,"id":"r2","executor":"","checkout":"/x"}` + "\n", `{"schema_version":1,"id":"r3"}` + "\n", strings.Repeat("x", 3*maxReceiptLine) + "\n"} {
		appendRaw(t, dispatcher.ReceiptPath, line)
	}
	freshnessReceipt(t, dispatcher, "r5", freshnessRepository, checkout, "eee", ReceiptSucceeded, time.Now())
	appendRaw(t, dispatcher.ReceiptPath, strings.Repeat("y", 3*maxReceiptLine))
	reader := NewFreshnessReader(dispatcher)
	if got := readView(t, reader).Record("index", freshnessRepository, checkout).Success.SHA; got != "eee" {
		t.Fatalf("an overlong or malformed line disturbed the stream: %s", got)
	}
}

func TestFreshnessReaderParsesOnlyTheNewestPartOfALargeStream(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	other := filepath.Join(filepath.Dir(checkout), "other")
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, other, "old", ReceiptSucceeded, at)
	for i := range 6 {
		freshnessReceipt(t, dispatcher, fmt.Sprintf("n%d", i), freshnessRepository, checkout, fmt.Sprintf("sha%d", i), ReceiptSucceeded, at)
	}
	reader := NewFreshnessReader(dispatcher)
	reader.maxBytes = 700
	view := readView(t, reader)
	if view.Record("index", freshnessRepository, other).Last != nil {
		t.Fatal("the oldest receipt was parsed past the cap")
	}
	if got := view.Record("index", freshnessRepository, checkout).Last.SHA; got != "sha5" {
		t.Fatalf("the newest receipt = %s", got)
	}
	// Growth beyond the cap since the last read falls back to the newest part.
	for i := range 6 {
		freshnessReceipt(t, dispatcher, fmt.Sprintf("m%d", i), freshnessRepository, checkout, fmt.Sprintf("late%d", i), ReceiptSucceeded, at)
	}
	if got := readView(t, reader).Record("index", freshnessRepository, checkout).Last.SHA; got != "late5" {
		t.Fatalf("after growth = %s", got)
	}
}

func TestFreshnessReaderWithOneUnfinishedLineBeyondTheCapReadsNothingAndFails(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	hkCovWriteFile(t, dispatcher.ReceiptPath, strings.Repeat("x", 50), 0o600)
	reader := NewFreshnessReader(dispatcher)
	reader.maxBytes = 5
	view := readView(t, reader)
	if len(view.receipts) != 0 {
		t.Fatal("a line that never ends yields nothing")
	}
}

// writeOnly opens a file that can be statted but not read.
func writeOnly(path string) (*os.File, error) { return os.OpenFile(path, os.O_WRONLY, 0) }

func TestFreshnessReaderReportsAReadFailure(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, time.Now())
	plain := NewFreshnessReader(dispatcher)
	plain.open = writeOnly
	if _, err := plain.Read(); err == nil {
		t.Fatal("a stream that cannot be read is an error")
	}
	skipping := NewFreshnessReader(dispatcher)
	skipping.open, skipping.maxBytes = writeOnly, 10
	if _, err := skipping.Read(); err == nil {
		t.Fatal("a read failure while skipping into a large stream is an error")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk gone") }

func TestParseReceiptsReportsAFailureInTheMiddleOfAnOverlongLine(t *testing.T) {
	t.Parallel()
	source := io.MultiReader(strings.NewReader(strings.Repeat("x", 3*maxReceiptLine)), failingReader{})
	if _, _, err := parseReceipts(bufio.NewReaderSize(source, maxReceiptLine), receiptIndex{}, false); err == nil {
		t.Fatal("a failing read inside a skipped line is an error")
	}
}

func TestFreshnessReaderChecksTheOpenedFile(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, time.Now())
	swapped := NewFreshnessReader(dispatcher)
	swapped.open = func(string) (*os.File, error) { return os.Open(dispatcher.ConfigPath) }
	if _, err := swapped.Read(); err == nil {
		t.Fatal("a file other than the one validated is an error")
	}
	closed := NewFreshnessReader(dispatcher)
	closed.open = func(path string) (*os.File, error) {
		file, err := os.Open(path)
		_ = file.Close()
		return file, err
	}
	if _, err := closed.Read(); err == nil {
		t.Fatal("a handle that cannot be statted is an error")
	}
}

func TestFreshnessReaderStartsOverWhenTheStreamIsReplacedOrShrinks(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, at)
	freshnessReceipt(t, dispatcher, "r2", freshnessRepository, checkout, "bbb", ReceiptSucceeded, at)
	reader := NewFreshnessReader(dispatcher)
	readView(t, reader)
	if err := os.Remove(dispatcher.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	freshnessReceipt(t, dispatcher, "r3", freshnessRepository, checkout, "ccc", ReceiptSucceeded, at)
	if got := readView(t, reader).Record("index", freshnessRepository, checkout).Last.SHA; got != "ccc" {
		t.Fatalf("a replaced stream = %s", got)
	}
	// Truncated in place: same file, smaller.
	if err := os.Truncate(dispatcher.ReceiptPath, 0); err != nil {
		t.Fatal(err)
	}
	if view := readView(t, reader); view.Record("index", freshnessRepository, checkout).Last != nil {
		t.Fatal("a truncated stream still reports receipts")
	}
}

func TestFreshnessViewSeesQueuedAndRunningJobsAndWritesNothing(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	running := filepath.Join(filepath.Dir(checkout), "running")
	for _, directory := range []string{checkout, running} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hkCovEnqueueOne(t, dispatcher, checkout)
	hkCovWriteJob(t, dispatcher.runningDir(), queuedJob{SchemaVersion: jobSchemaVersion, Key: queueKey("index", running), Executor: "index", Event: hkCovEvent(running)})
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), "garbage.json"), "{", 0o600)
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), "note.txt"), "x", 0o600)
	if err := os.Mkdir(filepath.Join(dispatcher.pendingDir(), "dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := treeListing(t, filepath.Dir(dispatcher.StateDir))
	view := readView(t, NewFreshnessReader(dispatcher))
	if !view.Record("index", freshnessRepository, checkout).Pending || !view.Record("index", freshnessRepository, running).Pending {
		t.Fatal("a queued and a running job both make the checkout pending")
	}
	if view.Record("index", "github.com/acme/other", checkout).Pending {
		t.Fatal("a job of another repository does not make this one pending")
	}
	if !slices.Equal(before, treeListing(t, filepath.Dir(dispatcher.StateDir))) {
		t.Fatal("reading wrote to the state directory")
	}
}

func treeListing(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		info, _ := entry.Info()
		names = append(names, path+"|"+info.ModTime().String())
		return nil
	})
	return names
}

func TestFreshnessReaderWithNothingConfiguredIsEmpty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	reader := &FreshnessReader{ConfigPath: filepath.Join(root, "absent.yaml"), StateDir: filepath.Join(root, "state"), ReceiptPath: filepath.Join(root, "receipts.jsonl")}
	view := readView(t, reader)
	if len(view.Executors(freshnessRepository)) != 0 || view.Signature(freshnessRepository, []string{root}) == "" {
		t.Fatal("no configuration means no executors")
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("reading created the state directory")
	}
}

func TestFreshnessReaderRejectsWhatItCannotTrust(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, time.Now())

	badConfig := *NewFreshnessReader(dispatcher)
	badConfig.ConfigPath = hkCovWriteFile(t, filepath.Join(t.TempDir(), "wb.yaml"), "hooks: [", 0o600)
	if _, err := badConfig.Read(); err == nil {
		t.Fatal("an invalid configuration is an error")
	}

	badStream := *NewFreshnessReader(dispatcher)
	if err := os.Chmod(badStream.ReceiptPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := badStream.Read(); err == nil {
		t.Fatal("a world-writable receipt stream is not trusted")
	}
	if err := os.Chmod(badStream.ReceiptPath, 0o600); err != nil {
		t.Fatal(err)
	}

	unreadable := *NewFreshnessReader(dispatcher)
	if err := os.Chmod(unreadable.ReceiptPath, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := unreadable.Read(); err == nil {
			t.Fatal("an unreadable receipt stream is an error")
		}
	}
	if err := os.Chmod(unreadable.ReceiptPath, 0o600); err != nil {
		t.Fatal(err)
	}

	directory := *NewFreshnessReader(dispatcher)
	directory.ReceiptPath = t.TempDir()
	if _, err := directory.Read(); err == nil {
		t.Fatal("a directory is not a receipt stream")
	}
}

func TestFreshnessReaderResolvesItsPathsAsTheWorkerDoes(t *testing.T) {
	t.Parallel()
	reader := NewFreshnessReader(Dispatcher{ConfigPath: "/c/wb.yaml"})
	worker := DefaultDispatcher()
	if reader.ReceiptPath != worker.ReceiptPath || reader.StateDir != worker.StateDir || reader.ConfigPath != "/c/wb.yaml" {
		t.Fatalf("reader %+v, worker %+v", reader, worker)
	}
}

func TestFreshnessReaderIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r0", freshnessRepository, checkout, "sha0", ReceiptSucceeded, at)
	reader := NewFreshnessReader(dispatcher)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 20 {
				view, err := reader.Read()
				if err != nil {
					t.Error(err)
					return
				}
				if view.Record("index", freshnessRepository, checkout).Success == nil {
					t.Error("a receipt vanished")
				}
			}
		}()
	}
	for i := 1; i <= 10; i++ {
		freshnessReceipt(t, dispatcher, fmt.Sprintf("r%d", i), freshnessRepository, checkout, fmt.Sprintf("sha%d", i), ReceiptSucceeded, at)
	}
	group.Wait()
}

func TestFreshnessReaderStartsOverWhenTheStreamWasRewrittenInPlaceAndRegrown(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	freshnessReceipt(t, dispatcher, "r1", freshnessRepository, checkout, "aaa", ReceiptSucceeded, at)
	reader := NewFreshnessReader(dispatcher)
	readView(t, reader)
	old := reader.cache.size
	// Same file, truncated, then regrown past its old size with lines of other
	// lengths, so the old offset lands inside a line.
	if err := os.Truncate(dispatcher.ReceiptPath, 0); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		line := fmt.Sprintf(`{"schema_version":2,"id":"n%d","repository":"%s","checkout":"%s","new_sha":"sha%d","executor":"index","status":"succeeded","finished_at":"2026-10-01T10:00:00Z","message":"%s"}`+"\n", i, freshnessRepository, checkout, i, strings.Repeat("p", 7*i+3))
		appendRaw(t, dispatcher.ReceiptPath, line)
	}
	if info, _ := os.Stat(dispatcher.ReceiptPath); info.Size() <= old {
		t.Fatal("the fixture did not regrow past the old size")
	}
	if got := readView(t, reader).Record("index", freshnessRepository, checkout).Last.SHA; got != "sha2" {
		t.Fatalf("a regrown stream was read from a stale offset: %s", got)
	}
}

func TestFreshnessReaderResumesAtTheStartWhenNothingWasCompleteYet(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	line := `{"schema_version":2,"id":"r1","repository":"` + freshnessRepository + `","checkout":"` + checkout + `","new_sha":"aaa","executor":"index","status":"succeeded","finished_at":"2026-10-01T10:00:00Z"}` + "\n"
	appendRaw(t, dispatcher.ReceiptPath, line[:40])
	reader := NewFreshnessReader(dispatcher)
	readView(t, reader)
	if reader.cache.offset != 0 {
		t.Fatalf("an unfinished first line was consumed: %d", reader.cache.offset)
	}
	appendRaw(t, dispatcher.ReceiptPath, line[40:])
	if got := readView(t, reader).Record("index", freshnessRepository, checkout).Last; got == nil || got.SHA != "aaa" {
		t.Fatalf("the completed first line was not read: %+v", got)
	}
}
