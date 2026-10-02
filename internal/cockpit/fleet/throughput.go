package fleet

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// The sealed-work throughput block (cockpit-views#req:throughput-block): how
// many tasks this machine finished and dropped on each of the last 30 UTC days,
// the five finished tasks that took longest and the median and 90th percentile
// of the finished durations. Its source is this machine's sealed terminal
// records (worktreeclaims.TerminalRecord): a record counts at its `sealed_at`,
// by its `worktree_disposition` (the table in dispositionFinished and
// dispositionDropped), and its duration is `sealed_at` minus the claim's
// `recorded_at`. The block reports what the records prove and never guesses a
// merge. It is local only: it is never exported, and an envelope that carries
// one is refused.

// The throughput window and the bounds of the collector.
const (
	// ThroughputWindowDays is the window of the block: today and the 29 UTC days
	// before it.
	ThroughputWindowDays = 30
	// throughputSlowest is the most slowest tasks the block lists.
	throughputSlowest = 5
	// DefaultThroughputInterval is how long the collector goes between scans of
	// the terminal records when nothing asks for one sooner.
	DefaultThroughputInterval = 10 * time.Minute
	// DefaultTerminalReadLimit is the most records read in one scan; it is a
	// safety bound, because a scan reads the newest records first and stops at
	// the window. A scan that hits it reads the rest on the next refresh.
	DefaultTerminalReadLimit = 5000
	// DefaultTerminalTotalLimit is the most terminal records the collector knows
	// of at once.
	DefaultTerminalTotalLimit = 20000
	// maxLandingDuration bounds a claim-to-seal duration: a record claiming more
	// is not a usable one.
	maxLandingDuration = 3650 * 24 * time.Hour
	// readHorizon is how far behind the window a record's file time may be and
	// the record still be read: older files are not read at all. A record's file
	// is written when it is sealed, so its time is its seal time or later.
	readHorizon = (ThroughputWindowDays + 1) * 24 * time.Hour
)

// The dispositions that count as finished work and as dropped work. `handoff`
// is neither (the work continues elsewhere), and a disposition outside both is
// not counted.
var (
	dispositionFinished = []string{"landed", "removed", "retired", "recycled"}
	dispositionDropped  = []string{"discarded", "superseded", "not_landed", "orphaned"}
)

// Throughput is the sealed-work throughput block. PerDay lists only the days
// with a sealing, oldest first; Slowest at most five finished tasks, longest
// first. Both are lists, never null. MedianSeconds and P90Seconds are the median
// and 90th percentile (nearest rank) of the finished tasks' durations in the
// window, absent when none finished. Capped says the collector hit one of its
// bounds, so the numbers cover part of the records.
type Throughput struct {
	WindowDays    int              `json:"window_days"`
	PerDay        []ThroughputDay  `json:"per_day"`
	Slowest       []ThroughputTask `json:"slowest"`
	MedianSeconds *int             `json:"median_seconds,omitempty"`
	P90Seconds    *int             `json:"p90_seconds,omitempty"`
	Capped        bool             `json:"capped,omitempty"`
}

// ThroughputDay is the number of tasks sealed on one UTC date (YYYY-MM-DD): the
// finished ones and the dropped ones. A task counts once a day, as finished when
// any of its records that day is. Landed is how many of the finished were
// sealed `landed`, absent when none.
type ThroughputDay struct {
	Date     string `json:"date"`
	Finished int    `json:"finished"`
	Dropped  int    `json:"dropped"`
	Landed   int    `json:"landed,omitempty"`
}

// ThroughputTask is one of the slowest finished tasks: its name, the seconds
// from its claim to its sealing and the sealing time. A task finished in several
// repositories appears once, with its longest duration.
type ThroughputTask struct {
	Task            string    `json:"task"`
	DurationSeconds int       `json:"duration_seconds"`
	LandedAt        time.Time `json:"landed_at"`
}

// TerminalFile is one terminal record as a listing sees it. Key names it to the
// source and stays inside the daemon; Size and ModTime are its identity: an
// immutable record whose size and time are unchanged is not read again.
type TerminalFile struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// TerminalRecords is the collector's only way to this machine's terminal
// records, so a test replaces it. List returns at most limit files and says
// whether more existed; Read decodes one. Both are read-only.
type TerminalRecords interface {
	List(ctx context.Context, limit int) (files []TerminalFile, truncated bool, err error)
	Read(key string) (worktreeclaims.TerminalRecord, error)
}

// sealing is what the collector keeps of a counted record.
type sealing struct {
	task     string
	at       time.Time
	duration time.Duration
	finished bool
	landed   bool
}

// terminalEntry is what the collector keeps of one record: its identity and,
// when it is a usable counted record, its sealing. A record that failed to read
// or that is too old to read is kept without one, so it is not looked at again
// until it changes.
type terminalEntry struct {
	file    TerminalFile
	sealing *sealing
}

// throughputCollector scans the terminal records, caches what it learns by the
// records' identity and computes the block. It is used by one goroutine at a
// time (the snapshotter's busy flag).
type throughputCollector struct {
	source     TerminalRecords
	interval   time.Duration
	readLimit  int
	totalLimit int
	logf       func(string, ...any)

	entries   map[string]terminalEntry
	scanned   time.Time
	day       string
	pending   bool
	truncated bool
	block     *Throughput
}

func newThroughputCollector(source TerminalRecords, interval time.Duration, readLimit, totalLimit int, logf func(string, ...any)) *throughputCollector {
	if interval <= 0 {
		interval = DefaultThroughputInterval
	}
	if readLimit <= 0 {
		readLimit = DefaultTerminalReadLimit
	}
	if totalLimit <= 0 {
		totalLimit = DefaultTerminalTotalLimit
	}
	return &throughputCollector{source: source, interval: interval, readLimit: readLimit, totalLimit: totalLimit, logf: logf, entries: map[string]terminalEntry{}}
}

func dayOf(when time.Time) string { return when.UTC().Format(time.DateOnly) }

// scanDue says a scan of the records is due: none has run, a capped one left
// records unread, or the interval has passed.
func (c *throughputCollector) scanDue(now time.Time) bool {
	return c.scanned.IsZero() || c.pending || now.Sub(c.scanned) >= c.interval
}

// due says the block needs computing again: a scan is due, or the UTC day
// changed, which moves the window.
func (c *throughputCollector) due(now time.Time) bool {
	return c.scanDue(now) || dayOf(now) != c.day
}

// collect scans when a scan is due and returns the block as of now, nil when
// no record is usable. A scan that cannot list the records keeps what was
// known and is tried again after the interval.
func (c *throughputCollector) collect(ctx context.Context, now time.Time) *Throughput {
	if c.scanDue(now) {
		c.scanned = now
		if err := catch(func() error { return c.scan(ctx, now) }); err != nil {
			c.logf("cockpit fleet: scan terminal records: %v", err)
		}
	}
	c.day = dayOf(now)
	c.block = c.compute(now)
	return c.block
}

// scan lists the records and reads those whose identity is new or changed,
// newest file first and only files within the window and a day, up to the read
// limit; what it did not read is read on the next refresh, and a list cut at the
// total limit is reported. A scan cut off by its context changes nothing.
func (c *throughputCollector) scan(ctx context.Context, now time.Time) error {
	files, truncated, err := c.source.List(ctx, c.totalLimit)
	if err != nil {
		return err
	}
	next := make(map[string]terminalEntry, len(files))
	var due []TerminalFile
	for _, file := range files {
		old, known := c.entries[file.Key]
		switch {
		case known && old.file == file:
			next[file.Key] = old
		case file.ModTime.Before(now.Add(-readHorizon)):
			next[file.Key] = terminalEntry{file: file}
		default:
			if known {
				next[file.Key] = old
			}
			due = append(due, file)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].ModTime.After(due[j].ModTime) })
	deferred := len(due) > c.readLimit
	if deferred {
		due = due[:c.readLimit]
	}
	for _, file := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, readErr := c.source.Read(file.Key)
		entry := terminalEntry{file: file}
		if readErr == nil {
			entry.sealing = sealingOf(record)
		}
		next[file.Key] = entry
	}
	c.entries, c.pending = next, deferred
	if truncated || deferred {
		c.logf("cockpit fleet: terminal record scan is capped (listed %d, read %d, list truncated %t, reads deferred %t)", len(files), len(due), truncated, deferred)
	}
	c.truncated = truncated || deferred
	return nil
}

// sealingOf is the sealing a record stands for, nil when it is not a usable
// one: a disposition that is neither finished nor dropped, missing either
// timestamp, a time before 2000, a sealing before its claim, a duration over ten
// years, or no task name.
func sealingOf(record worktreeclaims.TerminalRecord) *sealing {
	finished := slices.Contains(dispositionFinished, record.Disposition)
	if !finished && !slices.Contains(dispositionDropped, record.Disposition) {
		return nil
	}
	if record.SealedAt.IsZero() || record.RecordedAt.IsZero() ||
		record.SealedAt.Before(earliestBootTime) || record.RecordedAt.Before(earliestBootTime) || record.SealedAt.Before(record.RecordedAt) {
		return nil
	}
	duration := record.SealedAt.Sub(record.RecordedAt)
	task := plainText(record.Task)
	if task == "" {
		task = plainText(record.EffortID)
	}
	if duration > maxLandingDuration || task == "" {
		return nil
	}
	return &sealing{task: task, at: record.SealedAt.UTC(), duration: duration, finished: finished, landed: record.Disposition == "landed"}
}

// dayTasks is what one task did on one day.
type dayTasks struct{ finished, landed bool }

// compute builds the block from the cache for the window ending today (UTC). A
// task counts once on each day it was sealed, finished winning over dropped; it
// appears once among the slowest and in the percentiles, with its longest
// finished duration.
func (c *throughputCollector) compute(now time.Time) *Throughput {
	first := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1-ThroughputWindowDays)
	usable := false
	days := map[string]map[string]dayTasks{}
	longest := map[string]sealing{}
	for _, entry := range c.entries {
		found := entry.sealing
		if found == nil {
			continue
		}
		usable = true
		if found.at.Before(first) || found.at.After(now.Add(maxSkew)) {
			continue
		}
		date := dayOf(found.at)
		if days[date] == nil {
			days[date] = map[string]dayTasks{}
		}
		did := days[date][found.task]
		did.finished = did.finished || found.finished
		did.landed = did.landed || found.landed
		days[date][found.task] = did
		if !found.finished {
			continue
		}
		if best, seen := longest[found.task]; !seen || found.duration > best.duration || (found.duration == best.duration && found.at.After(best.at)) {
			longest[found.task] = *found
		}
	}
	if !usable {
		return nil
	}
	block := &Throughput{WindowDays: ThroughputWindowDays, PerDay: []ThroughputDay{}, Slowest: []ThroughputTask{}, Capped: c.truncated}
	for date, tasks := range days {
		day := ThroughputDay{Date: date}
		for _, did := range tasks {
			switch {
			case did.finished:
				day.Finished++
			default:
				day.Dropped++
			}
			if did.landed {
				day.Landed++
			}
		}
		block.PerDay = append(block.PerDay, day)
	}
	sort.Slice(block.PerDay, func(i, j int) bool { return block.PerDay[i].Date < block.PerDay[j].Date })
	ranked := make([]sealing, 0, len(longest))
	for _, item := range longest {
		ranked = append(ranked, item)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.duration != b.duration {
			return a.duration > b.duration
		}
		if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		return a.task < b.task
	})
	for _, item := range ranked[:min(len(ranked), throughputSlowest)] {
		block.Slowest = append(block.Slowest, ThroughputTask{Task: item.task, DurationSeconds: int(item.duration / time.Second), LandedAt: item.at})
	}
	if len(ranked) > 0 {
		median, p90 := nearestRank(ranked, 50), nearestRank(ranked, 90)
		block.MedianSeconds, block.P90Seconds = &median, &p90
	}
	return block
}

// nearestRank is the percentile (nearest rank) of the durations of ranked,
// which is sorted longest first, in whole seconds.
func nearestRank(ranked []sealing, percent int) int {
	rank := (percent*len(ranked) + 99) / 100 // ceil(percent*n/100), at least 1 for n > 0
	return int(ranked[len(ranked)-rank].duration / time.Second)
}

// startThroughput computes the block in a goroutine of its own when it is due
// (a scan is due, or the UTC day changed), unless one is still running. The
// refresh does not wait for it: the result is published when it arrives. A
// refresh that finds nothing due does no work at all.
func (s *Snapshotter) startThroughput(ctx context.Context) {
	if s.throughputs == nil || !s.throughputBusy.CompareAndSwap(false, true) {
		return
	}
	if !s.throughputs.due(s.now()) {
		s.throughputBusy.Store(false)
		return
	}
	s.side.Add(1)
	go func() {
		defer s.side.Done()
		defer s.throughputBusy.Store(false)
		block := s.throughputs.collect(ctx, s.now())
		s.mu.Lock()
		defer s.mu.Unlock()
		s.throughput = block
		s.publishMaybeLocked()
	}()
}
