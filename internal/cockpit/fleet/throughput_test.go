package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// fakeTerminals is a TerminalRecords with the calls it received counted.
type fakeTerminals struct {
	mu      sync.Mutex
	files   []TerminalFile
	records map[string]worktreeclaims.TerminalRecord
	readErr map[string]error
	listErr error
	panicIn string
	lists   int
	reads   []string
}

func newFakeTerminals() *fakeTerminals {
	return &fakeTerminals{records: map[string]worktreeclaims.TerminalRecord{}, readErr: map[string]error{}}
}

// put stores a record under key with an identity of mtime.
func (f *fakeTerminals) put(key string, mtime time.Time, record worktreeclaims.TerminalRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file := TerminalFile{Key: key, Size: 100, ModTime: mtime}
	f.records[key] = record
	for index := range f.files {
		if f.files[index].Key == key {
			f.files[index] = file
			return
		}
	}
	f.files = append(f.files, file)
}

func (f *fakeTerminals) remove(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.files {
		if f.files[index].Key == key {
			f.files = append(f.files[:index], f.files[index+1:]...)
			return
		}
	}
}

func (f *fakeTerminals) List(_ context.Context, limit int) ([]TerminalFile, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.panicIn == "list" {
		panic("list")
	}
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	if len(f.files) > limit {
		return append([]TerminalFile(nil), f.files[:limit]...), true, nil
	}
	return append([]TerminalFile(nil), f.files...), false, nil
}

func (f *fakeTerminals) Read(key string) (worktreeclaims.TerminalRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, key)
	return f.records[key], f.readErr[key]
}

func (f *fakeTerminals) counts() (lists, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists, len(f.reads)
}

// terminal is a sealed terminal record of task with the given disposition,
// claimed at claimed and sealed at sealed.
func terminal(task, disposition string, claimed, sealed time.Time) worktreeclaims.TerminalRecord {
	record := worktreeclaims.TerminalRecord{Disposition: disposition, SealedAt: sealed}
	record.Task, record.RecordedAt, record.Repository = task, claimed, "acme/widgets"
	return record
}

func landedTerminal(task string, claimed, sealed time.Time) worktreeclaims.TerminalRecord {
	return terminal(task, "landed", claimed, sealed)
}

// throughputSnapshotter is a snapshotter over no repository that reads source.
func throughputSnapshotter(source TerminalRecords, change func(*Options)) (*Snapshotter, *manualClock) {
	return newSnapshotter(oneRepoSources("/repo/widgets").collectors(), func(options *Options) {
		options.Terminals = source
		if change != nil {
			change(options)
		}
	})
}

func throughputOf(t *testing.T, snapshotter *Snapshotter) *Throughput {
	t.Helper()
	return snapshotter.Document().Throughput
}

// TestThroughputBlockFromSealedRecords is
// cockpit-views#ac:throughput-block-from-sealed-records: sealed records of every
// disposition on two days, an old one, through two runs of the collector and a
// request for the document.
func TestThroughputBlockFromSealedRecords(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	now := clock.Now() // 2026-10-01 09:00 UTC
	yesterday := now.AddDate(0, 0, -1)
	source.put("a", now, landedTerminal("task-a", now.Add(-5*time.Hour), now.Add(-2*time.Hour)))
	source.put("b", now, landedTerminal("task-b", yesterday.Add(-48*time.Hour), yesterday))
	source.put("c", now, terminal("task-c", "recycled", yesterday.Add(-time.Hour), yesterday.Add(-30*time.Minute)))
	source.put("r", now, terminal("task-r", "removed", now.Add(-6*time.Hour), now.Add(-90*time.Minute)))
	source.put("a-dropped", now, terminal("task-a", "discarded", now.Add(-5*time.Hour), now.Add(-time.Hour)))
	source.put("orphaned", now, terminal("task-o", "orphaned", now.Add(-3*time.Hour), now.Add(-time.Hour)))
	source.put("handoff", now, terminal("task-h", "handoff", now.Add(-3*time.Hour), now.Add(-time.Hour)))
	source.put("old", now, landedTerminal("task-old", now.AddDate(0, 0, -41), now.AddDate(0, 0, -40)))
	server := newCockpitServer(t, snapshotter)

	refreshAndSettle(t, snapshotter)
	_, firstReads := source.counts()
	refreshAndSettle(t, snapshotter)
	if lists, reads := source.counts(); reads != firstReads || lists != 1 || firstReads != 8 {
		t.Fatalf("lists %d, reads %d then %d: the second run must read nothing", lists, firstReads, reads)
	}

	var document struct {
		Throughput *Throughput `json:"throughput"`
	}
	if err := json.Unmarshal(server.get("/api/v1/cockpit/fleet", nil).Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	got := document.Throughput
	if got == nil || got.WindowDays != 30 || got.Capped {
		t.Fatalf("throughput = %+v", got)
	}
	// 09-30: b (landed) and c (recycled) finished. 10-01: a (landed, and dropped
	// the same day: finished wins) and r (removed) finished, o (orphaned)
	// dropped, h (handoff) neither.
	wantDays := []ThroughputDay{{Date: "2026-09-30", Finished: 2, Landed: 1}, {Date: "2026-10-01", Finished: 2, Dropped: 1, Landed: 1}}
	if fmt.Sprint(got.PerDay) != fmt.Sprint(wantDays) {
		t.Errorf("per_day = %+v, want %+v", got.PerDay, wantDays)
	}
	want := []ThroughputTask{
		{Task: "task-b", DurationSeconds: 48 * 3600, LandedAt: yesterday},
		{Task: "task-r", DurationSeconds: 4*3600 + 1800, LandedAt: now.Add(-90 * time.Minute)},
		{Task: "task-a", DurationSeconds: 3 * 3600, LandedAt: now.Add(-2 * time.Hour)},
		{Task: "task-c", DurationSeconds: 1800, LandedAt: yesterday.Add(-30 * time.Minute)},
	}
	if len(got.Slowest) != len(want) {
		t.Fatalf("slowest = %+v, want %+v", got.Slowest, want)
	}
	for index, task := range want {
		if got.Slowest[index].Task != task.Task || got.Slowest[index].DurationSeconds != task.DurationSeconds || !got.Slowest[index].LandedAt.Equal(task.LandedAt) {
			t.Errorf("slowest[%d] = %+v, want %+v", index, got.Slowest[index], task)
		}
	}
	// Nearest rank over 1800, 10800, 16200 and 172800 seconds.
	if got.MedianSeconds == nil || *got.MedianSeconds != 10800 || got.P90Seconds == nil || *got.P90Seconds != 172800 {
		t.Errorf("median %v, p90 %v, want 10800 and 172800", got.MedianSeconds, got.P90Seconds)
	}
	// The export never carries it.
	if snapshotter.Export(false).Fleet.Throughput != nil {
		t.Error("the export carries throughput")
	}
}

// TestThroughputIsOmittedWithoutTimestamps is
// cockpit-views#ac:throughput-is-omitted-without-timestamps: no sealed record
// with both timestamps and a counted disposition means no block and no invented
// value.
func TestThroughputIsOmittedWithoutTimestamps(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	server := newCockpitServer(t, snapshotter)
	now := clock.Now()
	source.put("no-claim-time", now, landedTerminal("a", time.Time{}, now))
	source.put("no-seal-time", now, landedTerminal("b", now.Add(-time.Hour), time.Time{}))
	source.put("handoff", now, terminal("c", "handoff", now.Add(-time.Hour), now))
	source.put("unknown", now, terminal("d", "mystery", now.Add(-time.Hour), now))
	source.put("unreadable", now, worktreeclaims.TerminalRecord{})
	source.readErr["unreadable"] = errors.New("denied")
	refreshAndSettle(t, snapshotter)

	body := server.get("/api/v1/cockpit/fleet", nil).Body.String()
	if strings.Contains(body, "throughput") || throughputOf(t, snapshotter) != nil {
		t.Fatalf("a document with no usable record carries throughput: %s", body)
	}
}

// TestThroughputWithNoSourceHasNoBlock covers a daemon with no terminal source.
func TestThroughputWithNoSourceHasNoBlock(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(oneRepoSources("/repo/widgets").collectors(), nil)
	refreshAndSettle(t, snapshotter)
	if throughputOf(t, snapshotter) != nil {
		t.Fatal("throughput without a source")
	}
}

// TestThroughputCountsATaskOncePerDayAcrossRepositories pins the rule: a task
// sealed in several repositories counts once on a day (finished winning over
// dropped) and appears once among the slowest, with its longest duration.
func TestThroughputCountsATaskOncePerDayAcrossRepositories(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	now := clock.Now()
	day := now.Add(-2 * time.Hour)
	source.put("repo-1", now, landedTerminal("shared", day.Add(-time.Hour), day))
	source.put("repo-2", now, terminal("shared", "removed", day.Add(-3*time.Hour), day))
	source.put("repo-3", now, terminal("shared", "discarded", day.Add(-2*time.Hour), day.Add(time.Minute)))
	source.put("other", now, landedTerminal("other", day.Add(-time.Hour), day))
	source.put("dropped-only", now, terminal("dropped-only", "superseded", day.Add(-time.Hour), day))
	source.put("dropped-only-2", now, terminal("dropped-only", "not_landed", day.Add(-time.Hour), day))
	source.put("next-day", now, terminal("shared", "retired", now.AddDate(0, 0, -3), now.AddDate(0, 0, -2)))
	refreshAndSettle(t, snapshotter)

	got := throughputOf(t, snapshotter)
	if fmt.Sprint(got.PerDay) != fmt.Sprint([]ThroughputDay{{Date: "2026-09-29", Finished: 1}, {Date: "2026-10-01", Finished: 2, Dropped: 1, Landed: 2}}) {
		t.Errorf("per_day = %+v", got.PerDay)
	}
	if len(got.Slowest) != 2 || got.Slowest[0].Task != "shared" || got.Slowest[0].DurationSeconds != 24*3600 || got.Slowest[1].Task != "other" {
		t.Errorf("slowest = %+v, want shared at its longest (the one-day sealing), then other", got.Slowest)
	}
}

// TestThroughputSlowestIsFiveOrderedAndDeterministic covers the cap of five, the
// order and the tie-break.
func TestThroughputSlowestIsFiveOrderedAndDeterministic(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	now := clock.Now()
	for index, hours := range []int{1, 7, 3, 7, 5, 9, 2} {
		sealed := now.Add(-time.Duration(index) * time.Minute)
		source.put(fmt.Sprint("k", index), now, landedTerminal(fmt.Sprint("task-", index), sealed.Add(-time.Duration(hours)*time.Hour), sealed))
	}
	refreshAndSettle(t, snapshotter)

	var names []string
	for _, task := range throughputOf(t, snapshotter).Slowest {
		names = append(names, fmt.Sprint(task.Task, ":", task.DurationSeconds/3600))
	}
	// 9h, then 7h newest first (task-1 sealed 1 minute ago, task-3 3 minutes ago), then 5h, then 3h.
	if want := "task-5:9 task-1:7 task-3:7 task-4:5 task-2:3"; strings.Join(names, " ") != want {
		t.Errorf("slowest = %v, want %s", names, want)
	}
	source.put("k0", now.Add(time.Second), landedTerminal("task-0", now.Add(-time.Hour), now.Add(-time.Minute)))
	refreshAndSettle(t, snapshotter) // no rescan yet: the interval has not passed
	if lists, _ := source.counts(); lists != 1 {
		t.Errorf("lists = %d", lists)
	}
}

// TestThroughputTiesBreakByName covers the name tie-break of equal durations
// landed at one instant.
func TestThroughputTiesBreakByName(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	now := clock.Now()
	tie := now.Add(-time.Hour)
	source.put("tie-b", now, landedTerminal("b-tie", tie.Add(-time.Hour), tie))
	source.put("tie-a", now, landedTerminal("a-tie", tie.Add(-time.Hour), tie))
	refreshAndSettle(t, snapshotter)
	got := throughputOf(t, snapshotter).Slowest
	if len(got) != 2 || got[0].Task != "a-tie" || got[1].Task != "b-tie" {
		t.Errorf("slowest = %+v", got)
	}
}

// TestThroughputRecordsAreCachedByIdentity: an unchanged record is not read
// again, a changed one is, a deleted one drops out, and one that failed to read
// is not retried until it changes.
func TestThroughputRecordsAreCachedByIdentity(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	now := clock.Now()
	source.put("a", now, landedTerminal("task-a", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	source.put("b", now, landedTerminal("task-b", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	source.put("bad", now, worktreeclaims.TerminalRecord{})
	source.readErr["bad"] = errors.New("denied")
	refreshAndSettle(t, snapshotter)
	if _, reads := source.counts(); reads != 3 {
		t.Fatalf("reads = %d, want 3", reads)
	}

	clock.advance(DefaultThroughputInterval)
	source.put("a", now.Add(time.Second), landedTerminal("task-a2", now.Add(-10*time.Hour), now.Add(-2*time.Hour)))
	source.remove("b")
	refreshAndSettle(t, snapshotter)
	lists, reads := source.counts()
	if lists != 2 || reads != 4 || source.reads[3] != "a" {
		t.Errorf("lists %d reads %v: only the changed record may be read again", lists, source.reads)
	}
	got := throughputOf(t, snapshotter)
	if len(got.Slowest) != 1 || got.Slowest[0].Task != "task-a2" {
		t.Errorf("slowest = %+v, want only the changed record's task", got.Slowest)
	}

	source.readErr["bad"] = nil
	source.put("bad", now.Add(time.Minute), landedTerminal("task-bad", now.Add(-time.Hour), now.Add(-time.Minute)))
	clock.advance(DefaultThroughputInterval)
	refreshAndSettle(t, snapshotter)
	if got := throughputOf(t, snapshotter); len(got.Slowest) != 2 {
		t.Errorf("slowest = %+v, want the repaired record", got.Slowest)
	}
}

// TestThroughputScansOnItsOwnCadence: a refresh before the interval does no
// work, and the UTC day changing recomputes the window without a scan.
func TestThroughputScansOnItsOwnCadence(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, func(options *Options) { options.ThroughputInterval = 24 * 365 * time.Hour })
	now := clock.Now()
	source.put("a", now, landedTerminal("task-a", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	source.put("edge", now, landedTerminal("task-edge", now.AddDate(0, 0, -40), now.AddDate(0, 0, -29)))
	refreshAndSettle(t, snapshotter)
	if got := throughputOf(t, snapshotter); fmt.Sprint(got.PerDay) != fmt.Sprint([]ThroughputDay{{Date: "2026-09-02", Finished: 1, Landed: 1}, {Date: "2026-10-01", Finished: 1, Landed: 1}}) {
		t.Fatalf("per_day = %+v: the record landed 29 days ago is the oldest of the window", got.PerDay)
	}
	clock.advance(time.Hour) // same day
	refreshAndSettle(t, snapshotter)
	clock.advance(24 * time.Hour) // the next day: 2026-09-02 leaves the window
	refreshAndSettle(t, snapshotter)
	got := throughputOf(t, snapshotter)
	if len(got.PerDay) != 1 || got.PerDay[0].Date != "2026-10-01" {
		t.Errorf("per_day after the day changed = %+v", got.PerDay)
	}
	if lists, reads := source.counts(); lists != 1 || reads != 2 {
		t.Errorf("lists %d reads %d: the day change must recompute without scanning", lists, reads)
	}
}

// TestThroughputReadsAreCappedAndResumed: a scan reads at most the read limit,
// says it is capped, and the next refresh (not the next interval) reads more.
func TestThroughputReadsAreCappedAndResumed(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	var logged []string
	snapshotter, _ := throughputSnapshotter(source, func(options *Options) {
		options.TerminalReadLimit = 2
		options.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	})
	now := newClock().Now()
	for index := range 5 {
		source.put(fmt.Sprint("k", index), now, landedTerminal(fmt.Sprint("task-", index), now.Add(-5*time.Hour), now.Add(-time.Duration(index+1)*time.Minute)))
	}
	refreshAndSettle(t, snapshotter)
	got := throughputOf(t, snapshotter)
	if _, reads := source.counts(); reads != 2 || !got.Capped || got.PerDay[0].Finished != 2 {
		t.Fatalf("reads %d, throughput %+v: the first scan must read 2 and say it is capped", reads, got)
	}
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	got = throughputOf(t, snapshotter)
	if _, reads := source.counts(); reads != 5 || got.Capped || got.PerDay[0].Finished != 5 {
		t.Errorf("reads %d, throughput %+v: the cap must resolve in three refreshes without the interval passing", reads, got)
	}
	capped := 0
	for _, line := range logged {
		if strings.Contains(line, "capped") {
			capped++
		}
	}
	if capped != 2 {
		t.Errorf("%d capped diagnostics, want 2: %v", capped, logged)
	}
}

// TestThroughputTotalIsCapped: more records than the total limit are not all
// known, and the block says so.
func TestThroughputTotalIsCapped(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, _ := throughputSnapshotter(source, func(options *Options) { options.TerminalTotalLimit = 3 })
	now := newClock().Now()
	for index := range 5 {
		source.put(fmt.Sprint("k", index), now, landedTerminal(fmt.Sprint("task-", index), now.Add(-5*time.Hour), now.Add(-time.Hour)))
	}
	refreshAndSettle(t, snapshotter)
	got := throughputOf(t, snapshotter)
	if !got.Capped || got.PerDay[0].Finished != 3 {
		t.Errorf("throughput = %+v, want 3 known and capped", got)
	}
	// The same capped scan is not read again.
	refreshAndSettle(t, snapshotter)
	if lists, reads := source.counts(); lists != 1 || reads != 3 {
		t.Errorf("lists %d reads %d", lists, reads)
	}
}

// TestThroughputKeepsWhatItKnewWhenTheScanFails: a listing error or a panic is
// logged and the previous block stays; the scan is tried again after the
// interval.
func TestThroughputKeepsWhatItKnewWhenTheScanFails(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	var logged []string
	snapshotter, clock := throughputSnapshotter(source, func(options *Options) {
		options.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	})
	now := clock.Now()
	source.put("a", now, landedTerminal("task-a", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	source.listErr = errors.New("unreadable")
	refreshAndSettle(t, snapshotter)
	if throughputOf(t, snapshotter) != nil {
		t.Fatal("a block from a failed first scan")
	}
	source.listErr = nil
	refreshAndSettle(t, snapshotter) // inside the interval: not tried again
	if throughputOf(t, snapshotter) != nil {
		t.Fatal("the failed scan was retried before the interval")
	}
	clock.advance(DefaultThroughputInterval)
	refreshAndSettle(t, snapshotter)
	if throughputOf(t, snapshotter) == nil {
		t.Fatal("no block after a good scan")
	}
	source.panicIn = "list"
	clock.advance(DefaultThroughputInterval)
	refreshAndSettle(t, snapshotter)
	if throughputOf(t, snapshotter) == nil {
		t.Error("a panicking scan dropped the block")
	}
	if len(logged) != 2 {
		t.Errorf("logged %v, want the two failures", logged)
	}
}

// TestThroughputAContextCutOffScanChangesNothing: a scan whose context ends
// keeps the cache it had.
func TestThroughputAContextCutOffScanChangesNothing(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	now := newClock().Now()
	source.put("a", now, landedTerminal("task-a", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	collector := newThroughputCollector(source, 0, 0, 0, func(string, ...any) {})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if block := collector.collect(ctx, now); block != nil || len(collector.entries) != 0 {
		t.Errorf("a cancelled scan produced %+v with %d entries", block, len(collector.entries))
	}
}

// TestThroughputRecordsAreValidated: names are plain text and capped, durations
// bounded and non-negative, dates and times plausible.
func TestThroughputRecordsAreValidated(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	sealed := now.Add(-time.Hour)
	long := strings.Repeat("x", 300)
	effort := landedTerminal("", sealed.Add(-time.Hour), sealed)
	effort.EffortID = "effort-1"
	for name, test := range map[string]struct {
		record worktreeclaims.TerminalRecord
		task   string // the name of the landing, empty when none is usable
	}{
		"a plain name":              {landedTerminal("task-a", sealed.Add(-time.Hour), sealed), "task-a"},
		"controls are removed":      {landedTerminal("ta\x1b[31msk\u202e", sealed.Add(-time.Hour), sealed), "ta[31msk"},
		"a long name is cut":        {landedTerminal(long, sealed.Add(-time.Hour), sealed), strings.Repeat("x", 200)},
		"no task uses the effort":   {effort, "effort-1"},
		"no name at all":            {landedTerminal("\x01", sealed.Add(-time.Hour), sealed), ""},
		"handoff is not counted":    {terminal("t", "handoff", sealed.Add(-time.Hour), sealed), ""},
		"no sealed time":            {landedTerminal("t", sealed.Add(-time.Hour), time.Time{}), ""},
		"no claim time":             {landedTerminal("t", time.Time{}, sealed), ""},
		"sealed before claimed":     {landedTerminal("t", sealed, sealed.Add(-time.Second)), ""},
		"a sealed time before 2000": {landedTerminal("t", time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)), ""},
		"a claim time before 2000":  {landedTerminal("t", time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC), sealed), ""},
		"a duration over ten years": {landedTerminal("t", sealed.AddDate(-11, 0, 0), sealed), ""},
		"zero duration is valid":    {landedTerminal("t", sealed, sealed), "t"},
	} {
		got := sealingOf(test.record)
		switch {
		case test.task == "" && got != nil:
			t.Errorf("%s: landing %+v, want none", name, got)
		case test.task != "" && (got == nil || got.task != test.task):
			t.Errorf("%s: landing %+v, want task %q", name, got, test.task)
		}
	}
	// A landing in the future is usable (so the block exists) but is not counted.
	source := newFakeTerminals()
	collector := newThroughputCollector(source, 0, 0, 0, func(string, ...any) {})
	source.put("future", now, landedTerminal("soon", now, now.Add(time.Hour)))
	block := collector.collect(t.Context(), now)
	if block == nil || len(block.PerDay) != 0 || len(block.Slowest) != 0 || block.PerDay == nil || block.Slowest == nil {
		t.Errorf("a landing in the future gave %+v, want empty lists, never null", block)
	}
}

// TestThroughputRequestsReadNoRecord: a request for the document never reaches
// the terminal source.
func TestThroughputRequestsReadNoRecord(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	now := clock.Now()
	source.put("a", now, landedTerminal("task-a", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	lists, reads := source.counts()
	for range 5 {
		server.get("/api/v1/cockpit/fleet", nil)
	}
	if l, r := source.counts(); l != lists || r != reads {
		t.Errorf("a request listed or read terminal records: %d,%d -> %d,%d", lists, reads, l, r)
	}
}

// TestThroughputIsRefusedInARemoteEnvelope: the block is local only, so an
// envelope that carries one is refused, and one with a bad date or name is
// refused by the field rules first.
func TestThroughputIsRefusedInARemoteEnvelope(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	if full.Fleet.Throughput != nil {
		t.Fatal("an export carries throughput")
	}
	block := func() *Throughput {
		return &Throughput{WindowDays: 30, PerDay: []ThroughputDay{{Date: "2026-10-01", Finished: 1}}, Slowest: []ThroughputTask{{Task: "task-a", DurationSeconds: 5, LandedAt: now.Add(-time.Hour)}}}
	}
	for name, test := range map[string]struct {
		change func(*Throughput)
		reason string
	}{
		"a valid block":       {func(*Throughput) {}, "throughput is local only"},
		"a date that is text": {func(b *Throughput) { b.PerDay[0].Date = "yesterday" }, "date is not valid"},
		"an empty date":       {func(b *Throughput) { b.PerDay[0].Date = "" }, "date is not valid"},
		"a task with control": {func(b *Throughput) { b.Slowest[0].Task = "a\x00" }, "task is not valid"},
		"an empty task":       {func(b *Throughput) { b.Slowest[0].Task = "" }, "task is not valid"},
		"a negative count":    {func(b *Throughput) { b.PerDay[0].Finished = -1 }, "finished is negative"},
		"a future landing":    {func(b *Throughput) { b.Slowest[0].LandedAt = now.Add(time.Hour) }, "landed_at is before 2000"},
		"too many days":       {func(b *Throughput) { b.PerDay = make([]ThroughputDay, 31) }, "per_day has more than 30"},
		"too many slowest":    {func(b *Throughput) { b.Slowest = make([]ThroughputTask, 6) }, "slowest has more than 5"},
	} {
		envelope := copyEnvelope(t, full)
		envelope.Fleet.Throughput = block()
		test.change(envelope.Fleet.Throughput)
		if reason := refusalOf(t, envelope.Validate(false, now)); !strings.Contains(reason, test.reason) {
			t.Errorf("%s: refused as %q, want %q", name, reason, test.reason)
		}
	}
}

// TestThroughputAChangedRecordBeyondTheCapKeepsItsOldValue: when a scan's read
// limit is reached, a record that changed but was not read yet keeps what was
// known of it until a later refresh reads it.
func TestThroughputAChangedRecordBeyondTheCapKeepsItsOldValue(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, func(options *Options) { options.TerminalReadLimit = 1 })
	now := clock.Now()
	source.put("a", now, landedTerminal("task-a", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	source.put("b", now, landedTerminal("task-b", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	source.put("a", now.Add(time.Second), landedTerminal("task-a2", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	source.put("b", now.Add(time.Second), landedTerminal("task-b2", now.Add(-4*time.Hour), now.Add(-2*time.Hour)))
	clock.advance(DefaultThroughputInterval)
	refreshAndSettle(t, snapshotter)
	names := func() string {
		var names []string
		for _, task := range throughputOf(t, snapshotter).Slowest {
			names = append(names, task.Task)
		}
		return strings.Join(names, " ")
	}
	if got := names(); got != "task-a2 task-b" {
		t.Errorf("after the capped scan = %q, want the read record new and the unread one as it was", got)
	}
	refreshAndSettle(t, snapshotter)
	if got := names(); got != "task-a2 task-b2" {
		t.Errorf("after the next refresh = %q", got)
	}
}

// TestThroughputDispositionTable pins the mapping of dispositions to the
// finished and dropped categories.
func TestThroughputDispositionTable(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	for disposition, want := range map[string]string{
		"landed": "finished landed", "removed": "finished", "retired": "finished", "recycled": "finished",
		"discarded": "dropped", "superseded": "dropped", "not_landed": "dropped", "orphaned": "dropped",
		"handoff": "", "mystery": "", "": "",
	} {
		got := sealingOf(terminal("t", disposition, now.Add(-time.Hour), now))
		category := ""
		switch {
		case got == nil:
		case got.finished && got.landed:
			category = "finished landed"
		case got.finished:
			category = "finished"
		default:
			category = "dropped"
		}
		if category != want {
			t.Errorf("%q counts as %q, want %q", disposition, category, want)
		}
	}
}

// TestThroughputReadsNewestFirstAndStopsAtTheWindow: the files within the window
// and a day are read newest first, so a capped scan holds the newest records,
// and older files are never read at all.
func TestThroughputReadsNewestFirstAndStopsAtTheWindow(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, func(options *Options) { options.TerminalReadLimit = 2 })
	now := clock.Now()
	for index := range 4 {
		sealed := now.Add(-time.Duration(index) * time.Hour)
		source.put(fmt.Sprint("k", index), sealed, landedTerminal(fmt.Sprint("task-", index), sealed.Add(-time.Hour), sealed))
	}
	for index := range 3 {
		old := now.AddDate(0, 0, -32-index)
		source.put(fmt.Sprint("old", index), old, landedTerminal(fmt.Sprint("old-", index), old.Add(-time.Hour), old))
	}
	refreshAndSettle(t, snapshotter)
	if got := fmt.Sprint(source.reads); got != "[k0 k1]" {
		t.Fatalf("the capped scan read %s, want the two newest", got)
	}
	refreshAndSettle(t, snapshotter)
	if got := fmt.Sprint(source.reads); got != "[k0 k1 k2 k3]" {
		t.Errorf("reads = %s: the next refresh reads the rest and never an old file", got)
	}
	if got := throughputOf(t, snapshotter); got.Capped || got.PerDay[0].Finished != 4 {
		t.Errorf("throughput = %+v", got)
	}
}

// TestThroughputWithOnlyDroppedWorkHasNoPercentiles: a block with no finished
// task has no median, no p90 and no slowest, and a single finished task is both.
func TestThroughputWithOnlyDroppedWorkHasNoPercentiles(t *testing.T) {
	t.Parallel()
	source := newFakeTerminals()
	snapshotter, clock := throughputSnapshotter(source, nil)
	now := clock.Now()
	source.put("d", now, terminal("task-d", "discarded", now.Add(-time.Hour), now))
	refreshAndSettle(t, snapshotter)
	got := throughputOf(t, snapshotter)
	if got == nil || got.MedianSeconds != nil || got.P90Seconds != nil || len(got.Slowest) != 0 || fmt.Sprint(got.PerDay) != fmt.Sprint([]ThroughputDay{{Date: "2026-10-01", Dropped: 1}}) {
		t.Fatalf("throughput = %+v", got)
	}
	source.put("f", now, terminal("task-f", "removed", now.Add(-2*time.Hour), now))
	clock.advance(DefaultThroughputInterval)
	refreshAndSettle(t, snapshotter)
	got = throughputOf(t, snapshotter)
	if *got.MedianSeconds != 7200 || *got.P90Seconds != 7200 {
		t.Errorf("one finished task: median %d p90 %d, want 7200 both", *got.MedianSeconds, *got.P90Seconds)
	}
}
