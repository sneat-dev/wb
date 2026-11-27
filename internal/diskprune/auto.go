package diskprune

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// autoRateLimit is the least time between two daemon-triggered prunes.
const autoRateLimit = time.Hour

// Receipt records one --auto run (disk-prune#req:daemon-auto-prune).
type Receipt struct {
	Time        time.Time          `json:"time"`
	Trigger     string             `json:"trigger"`
	Outcome     string             `json:"outcome"`
	Reasons     []string           `json:"reasons"`
	Filesystems []FilesystemReport `json:"filesystems"`
	Unlinked    map[string]int64   `json:"unlinked_by_category"`
	Skips       map[string]string  `json:"skips,omitempty"`
	Roots       map[string]string  `json:"roots"`
	Error       string             `json:"error,omitempty"`
}

// ReceiptsDir is where receipts live under a state directory.
func ReceiptsDir(stateDir string) string { return filepath.Join(stateDir, "disk", "receipts") }

// ReceiptFor summarises a report.
func ReceiptFor(trigger string, at time.Time, w *World, report Report) Receipt {
	receipt := Receipt{
		Time: at, Trigger: trigger, Outcome: report.Outcome, Reasons: report.Reasons,
		Filesystems: report.Filesystems, Unlinked: map[string]int64{}, Skips: map[string]string{},
		Roots: map[string]string{"GOCACHE": w.Go.GOCACHE},
	}
	for i, dir := range w.TempDirs {
		receipt.Roots["TMPDIR"+strconv.Itoa(i)] = dir
	}
	for _, category := range report.Categories {
		receipt.Unlinked[category.Name] = category.UnlinkedBytes
		if category.Status == StatusSkipped {
			receipt.Skips[category.Name] = category.Reason
		}
	}
	return receipt
}

// WriteReceipt appends a receipt, best-effort: on a full disk it may fail,
// and the failure is the caller's to report, never fatal to the prune.
func WriteReceipt(stateDir string, receipt Receipt) error {
	dir := ReceiptsDir(stateDir)
	// Neither the state directory, nor disk/, nor receipts/ may be reached
	// through a symlink, or a receipt (or a directory for it) could be
	// created inside a checkout. Each component is checked before anything
	// is created beneath it.
	for _, path := range []string{stateDir, filepath.Join(stateDir, "disk"), dir} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			mkdir := os.Mkdir
			if path == stateDir {
				// A fresh machine's state directory; its parents are the
				// projects root's.
				mkdir = os.MkdirAll
			}
			if err := mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(path)
		}
		if err != nil || !info.IsDir() {
			return fmt.Errorf("receipt directory %s is not a real directory", path)
		}
	}
	// A Receipt holds only strings, numbers, times and maps of them, which
	// always marshal.
	raw, _ := json.MarshalIndent(receipt, "", "  ")
	name := receipt.Time.UTC().Format("20060102T150405.000000000Z") + "-" + receipt.Trigger + ".json"
	return writeNewFile(filepath.Join(dir, name), raw)
}

// newestAutomaticReceipt is the newest receipt of an unattended-subset run,
// by the daemon or a manual --auto: a manual run to its own target says
// nothing about whether the floor is being held.
func newestAutomaticReceipt(stateDir string) (Receipt, string, bool) {
	daemon, daemonPath, daemonOK := NewestReceipt(stateDir, "daemon")
	manual, manualPath, manualOK := NewestReceipt(stateDir, "manual-auto")
	if manualOK && (!daemonOK || manual.Time.After(daemon.Time)) {
		return manual, manualPath, true
	}
	return daemon, daemonPath, daemonOK
}

// NewestReceipt returns the newest receipt with the trigger ("" for any),
// with its path.
func NewestReceipt(stateDir, trigger string) (Receipt, string, bool) {
	return newestReceipt(stateDir, trigger, false)
}

// newestReceipt is NewestReceipt; withLocked also counts a locked attempt,
// which the hourly rate limit must (an attempt is an attempt), while the
// floor finding must not (it measured nothing).
func newestReceipt(stateDir, trigger string, withLocked bool) (Receipt, string, bool) {
	entries, err := os.ReadDir(ReceiptsDir(stateDir))
	if err != nil {
		return Receipt{}, "", false
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		path := filepath.Join(ReceiptsDir(stateDir), name)
		// A receipt is a small regular file; anything else under the name,
		// a FIFO above all, is skipped rather than allowed to block.
		raw, err := readRegularFile(path, 1<<20)
		if err != nil {
			continue
		}
		var receipt Receipt
		if json.Unmarshal(raw, &receipt) != nil {
			continue
		}
		// A run that never took the lock measured nothing: it neither
		// clears nor replaces the last real result.
		if receipt.Outcome == OutcomeLocked && !withLocked {
			continue
		}
		if trigger == "" || receipt.Trigger == trigger {
			return receipt, path, true
		}
	}
	return Receipt{}, "", false
}

// receiptFreshness is how long an unsuccessful receipt stays a finding: a
// later successful run of any trigger replaces it sooner, and a stale one
// no longer describes the machine.
const receiptFreshness = 24 * time.Hour

// ReceiptFinding is the line wb disk and wb daemon status show when the
// newest receipt, from the last day, ended anything but done; "" otherwise.
func ReceiptFinding(stateDir string) string {
	receipt, path, ok := newestAutomaticReceipt(stateDir)
	if !ok || receipt.Outcome == OutcomeDone || receipt.Outcome == OutcomeLocked || time.Since(receipt.Time) > receiptFreshness {
		return ""
	}
	return fmt.Sprintf("the last automatic prune (%s, %s) ended %s; see %s", receipt.Trigger, receipt.Time.UTC().Format(time.RFC3339), receipt.Outcome, path)
}

// AutoPruner is the daemon's below-floor trigger. It checks every
// filesystem holding a root --auto may prune at the configured interval and,
// below the floor, runs --auto at most once an hour.
type AutoPruner struct {
	ProjectsRoot string
	StateDir     string
	Config       func() (wbconfig.DiskConfig, error)
	// Roots lists the directories --auto may prune; their filesystems are
	// the ones checked.
	Roots  func() []string
	Probe  func(path string) (disk.Filesystem, error)
	Prune  func(ctx context.Context, floor float64) (*World, Report, error)
	Now    func() time.Time
	Ticker func(time.Duration) (<-chan time.Time, func())
	// Log receives one line per run and per failure to write a receipt.
	Log func(string)

	last time.Time
}

// Run loops until ctx ends. The configuration is re-read each tick, so
// turning disk.auto_prune on or off needs no restart.
func (a *AutoPruner) Run(ctx context.Context) {
	if receipt, _, ok := newestReceipt(a.StateDir, "daemon", true); ok {
		a.last = receipt.Time
	}
	interval := a.interval()
	ticks, stop := a.Ticker(interval)
	defer func() { stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			a.Tick(ctx)
			if next := a.interval(); next != interval {
				stop()
				interval = next
				ticks, stop = a.Ticker(interval)
			}
		}
	}
}

// interval is disk.check_interval, or the default when the configuration
// cannot be read: a broken wb.yaml must never stop the daemon.
func (a *AutoPruner) interval() time.Duration {
	config, err := a.Config()
	if err != nil || config.CheckInterval <= 0 {
		return wbconfig.DefaultDiskCheckInterval
	}
	return config.CheckInterval
}

// Tick is one check: off, above the floor, or rate-limited does nothing.
func (a *AutoPruner) Tick(ctx context.Context) {
	config, err := a.Config()
	if err != nil || !config.AutoPrune || !a.below(config.MinimumAvailable) {
		return
	}
	now := a.Now()
	if !a.last.IsZero() && now.Sub(a.last) < autoRateLimit {
		return
	}
	a.last = now
	w, report, err := a.Prune(ctx, config.MinimumAvailable)
	receipt := Receipt{Time: now, Trigger: "daemon", Outcome: OutcomeFailed, Error: fmt.Sprint(err)}
	if err == nil {
		receipt = ReceiptFor("daemon", now, w, report)
	}
	a.Log(fmt.Sprintf("disk auto-prune: outcome %s", receipt.Outcome))
	if err := WriteReceipt(a.StateDir, receipt); err != nil {
		a.Log("disk auto-prune: receipt not written: " + err.Error())
	}
}

func (a *AutoPruner) below(floor float64) bool {
	for _, root := range a.Roots() {
		fs, err := a.Probe(root)
		if err == nil && fs.TotalBytes > 0 && fs.AvailableRatio() < floor {
			return true
		}
	}
	return false
}

// NewAutoPruner wires the trigger to the real machine.
func NewAutoPruner(projectsRoot, stateDir string, log func(string)) *AutoPruner {
	return newAutoPruner(projectsRoot, stateDir, log, wbconfig.DefaultPath, Run)
}

// newAutoPruner takes the configuration path and the engine, so a test can
// exercise the real wiring without deleting anything on the machine.
func newAutoPruner(projectsRoot, stateDir string, log func(string), configPath func() string, run func(context.Context, *World, Options) Report) *AutoPruner {
	return &AutoPruner{
		ProjectsRoot: projectsRoot, StateDir: stateDir,
		Config: func() (wbconfig.DiskConfig, error) { return wbconfig.LoadDisk(configPath()) },
		Roots: func() []string {
			env, _ := ResolveGoEnv()
			dirs, _ := tempDirs(realWorldDeps())
			// Exactly the roots --auto can prune: the temporary directories,
			// and GOCACHE when it is a cache prune can trim. A low filesystem
			// holding only report-only data never triggers it.
			if unsupportedCache(env) == "" {
				dirs = append(dirs, env.GOCACHE)
			}
			return dirs
		},
		Probe: disk.Probe,
		Prune: func(ctx context.Context, floor float64) (*World, Report, error) {
			w, err := NewWorld(ctx, projectsRoot)
			if err != nil {
				return nil, Report{}, err
			}
			return w, run(ctx, w, Options{Auto: true, Floor: floor}), nil
		},
		Now: time.Now,
		Ticker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
		Log: log,
	}
}
