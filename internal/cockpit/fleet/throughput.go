package fleet

import (
	"context"
	"sort"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// The landed-task throughput block (cockpit-views#req:throughput-block): how
// many tasks this machine landed on each of the last 30 days and the five that
// took longest from claim to landing. Its source is this machine's sealed
// terminal records (worktreeclaims.TerminalRecord): a record counts when its
// disposition is `landed`, at its `sealed_at`, and its duration is `sealed_at`
// minus the claim's `recorded_at`. The block is local only: it is never
// exported, and an envelope that carries one is refused.

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
	// DefaultTerminalReadLimit is the most records read in one scan; a scan that
	// hits it reads the rest on the next refresh.
	DefaultTerminalReadLimit = 1000
	// DefaultTerminalTotalLimit is the most terminal records the collector knows
	// of at once.
	DefaultTerminalTotalLimit = 20000
	// maxLandingDuration bounds a claim-to-landing duration: a record claiming
	// more is not a usable one.
	maxLandingDuration = 3650 * 24 * time.Hour
)

// Throughput is the landed-task throughput block. PerDay lists only the days
// with a landing, oldest first; Slowest at most five tasks, longest first. Both
// are lists, never null. Capped says the collector hit one of its bounds, so the
// numbers cover part of the records.
type Throughput struct {
	WindowDays int              `json:"window_days"`
	PerDay     []ThroughputDay  `json:"per_day"`
	Slowest    []ThroughputTask `json:"slowest"`
	Capped     bool             `json:"capped,omitempty"`
}

// ThroughputDay is the number of tasks landed on one UTC date (YYYY-MM-DD). A
// task landed in several repositories on a day counts once.
type ThroughputDay struct {
	Date   string `json:"date"`
	Landed int    `json:"landed"`
}

// ThroughputTask is one of the slowest landed tasks: its name, the seconds
// from its claim to its landing and the landing time. A task landed in several
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

// landing is what the collector keeps of a landed record.
type landing struct {
	task     string
	landedAt time.Time
	duration time.Duration
}

// terminalEntry is what the collector keeps of one record: its identity and,
// when it is a usable landed record, its landing. A record that failed to read
// is kept without one, so it is not read again until it changes.
type terminalEntry struct {
	file    TerminalFile
	landing *landing
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
// no landed record has both timestamps. A scan that cannot list the records
// keeps what was known and is tried again after the interval.
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

// scan lists the records and reads those whose identity is new or changed, up
// to the read limit; what it did not read is read on the next refresh, and a
// list cut at the total limit is reported. A scan cut off by its context
// changes nothing.
func (c *throughputCollector) scan(ctx context.Context, now time.Time) error {
	files, truncated, err := c.source.List(ctx, c.totalLimit)
	if err != nil {
		return err
	}
	next := make(map[string]terminalEntry, len(files))
	reads, deferred := 0, false
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		old, known := c.entries[file.Key]
		switch {
		case known && old.file == file:
			next[file.Key] = old
		case reads >= c.readLimit:
			deferred = true
			if known {
				next[file.Key] = old
			}
		default:
			reads++
			record, readErr := c.source.Read(file.Key)
			entry := terminalEntry{file: file}
			if readErr == nil {
				entry.landing = landingOf(record, now)
			}
			next[file.Key] = entry
		}
	}
	c.entries, c.pending = next, deferred
	if truncated || deferred {
		c.logf("cockpit fleet: terminal record scan is capped (listed %d, read %d, list truncated %t, reads deferred %t)", len(files), reads, truncated, deferred)
	}
	c.truncated = truncated || deferred
	return nil
}

// landingOf is the landing a record stands for, nil when it is not a usable
// one: not landed, missing either timestamp, a time before 2000, a landing
// before its claim, a duration over ten years, or no task name.
func landingOf(record worktreeclaims.TerminalRecord, now time.Time) *landing {
	if record.Disposition != "landed" || record.SealedAt.IsZero() || record.RecordedAt.IsZero() ||
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
	return &landing{task: task, landedAt: record.SealedAt.UTC(), duration: duration}
}

// compute builds the block from the cache for the window ending today (UTC).
// A task counts once on each day it landed, whatever number of repositories it
// landed in; it appears once among the slowest, with its longest duration.
func (c *throughputCollector) compute(now time.Time) *Throughput {
	today := now.UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, 1-ThroughputWindowDays)
	usable := false
	landedOn := map[string]map[string]bool{}
	slowest := map[string]landing{}
	for _, entry := range c.entries {
		found := entry.landing
		if found == nil {
			continue
		}
		usable = true
		if found.landedAt.Before(first) || found.landedAt.After(now.Add(maxSkew)) {
			continue
		}
		date := dayOf(found.landedAt)
		if landedOn[date] == nil {
			landedOn[date] = map[string]bool{}
		}
		landedOn[date][found.task] = true
		if best, seen := slowest[found.task]; !seen || found.duration > best.duration || (found.duration == best.duration && found.landedAt.After(best.landedAt)) {
			slowest[found.task] = *found
		}
	}
	if !usable {
		return nil
	}
	block := &Throughput{WindowDays: ThroughputWindowDays, PerDay: []ThroughputDay{}, Slowest: []ThroughputTask{}, Capped: c.truncated}
	for date, tasks := range landedOn {
		block.PerDay = append(block.PerDay, ThroughputDay{Date: date, Landed: len(tasks)})
	}
	sort.Slice(block.PerDay, func(i, j int) bool { return block.PerDay[i].Date < block.PerDay[j].Date })
	ranked := make([]landing, 0, len(slowest))
	for _, item := range slowest {
		ranked = append(ranked, item)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.duration != b.duration {
			return a.duration > b.duration
		}
		if !a.landedAt.Equal(b.landedAt) {
			return a.landedAt.After(b.landedAt)
		}
		return a.task < b.task
	})
	for _, item := range ranked[:min(len(ranked), throughputSlowest)] {
		block.Slowest = append(block.Slowest, ThroughputTask{Task: item.task, DurationSeconds: int(item.duration / time.Second), LandedAt: item.landedAt})
	}
	return block
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
