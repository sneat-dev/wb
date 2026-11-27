package diskprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type autoHarness struct {
	pruner  *AutoPruner
	config  wbconfig.DiskConfig
	free    float64
	now     time.Time
	prunes  int
	report  Report
	failure error
	logs    []string
	ticks   chan time.Time
}

func newAutoHarness(t *testing.T) *autoHarness {
	t.Helper()
	h := &autoHarness{config: wbconfig.DiskConfig{AutoPrune: true, MinimumAvailable: 0.99, CheckInterval: 5 * time.Second},
		free: 0.5, now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC), report: Report{Outcome: OutcomeTargetUnmet}, ticks: make(chan time.Time)}
	h.pruner = &AutoPruner{
		StateDir: t.TempDir(),
		Config:   func() (wbconfig.DiskConfig, error) { return h.config, nil },
		Roots:    func() []string { return []string{"/tmp", "/cache"} },
		Probe: func(path string) (disk.Filesystem, error) {
			if path == "/tmp" {
				return disk.Filesystem{}, errors.New("statfs")
			}
			return disk.Filesystem{TotalBytes: 100, AvailableBytes: int64(h.free * 100)}, nil
		},
		Prune: func(context.Context, float64) (*World, Report, error) {
			h.prunes++
			return &World{TempDirs: []string{"/tmp"}}, Report{Outcome: h.report.Outcome, Categories: []CategoryReport{{Name: CategoryScratch, UnlinkedBytes: 9}, {Name: CategoryBuild, Status: StatusSkipped, Reason: "busy"}}}, h.failure
		},
		Now:    func() time.Time { return h.now },
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return h.ticks, func() {} },
		Log:    func(line string) { h.logs = append(h.logs, line) },
	}
	return h
}

// AC daemon-prunes-below-floor: below the floor the daemon prunes once,
// writes a receipt, and is rate-limited for an hour; above the floor, off,
// or with an unreadable configuration it does nothing.
func TestAutoPrunerRunsBelowTheFloorAtMostHourly(t *testing.T) {
	t.Parallel()
	h := newAutoHarness(t)
	h.pruner.Tick(context.Background())
	h.now = h.now.Add(30 * time.Minute)
	h.pruner.Tick(context.Background())
	if h.prunes != 1 {
		t.Fatalf("prunes = %d, want 1 inside the hour", h.prunes)
	}
	receipt, path, ok := NewestReceipt(h.pruner.StateDir, "daemon")
	if !ok || receipt.Outcome != OutcomeTargetUnmet || receipt.Unlinked[CategoryScratch] != 9 || receipt.Skips[CategoryBuild] != "busy" || receipt.Roots["TMPDIR0"] != "/tmp" {
		t.Fatalf("receipt = %#v at %s", receipt, path)
	}
	if finding := ReceiptFinding(h.pruner.StateDir); !strings.Contains(finding, "ended target-unmet") || !strings.Contains(finding, path) {
		t.Fatalf("finding = %q", finding)
	}
	h.now = h.now.Add(time.Hour)
	h.report.Outcome = OutcomeDone
	h.pruner.Tick(context.Background())
	if h.prunes != 2 || ReceiptFinding(h.pruner.StateDir) != "" {
		t.Fatalf("after an hour: prunes %d finding %q", h.prunes, ReceiptFinding(h.pruner.StateDir))
	}
	h.now = h.now.Add(2 * time.Hour)
	for name, mutate := range map[string]func(){
		"off":   func() { h.config.AutoPrune = false },
		"above": func() { h.config.AutoPrune = true; h.free = 0.999 },
		"config": func() {
			h.pruner.Config = func() (wbconfig.DiskConfig, error) { return wbconfig.DiskConfig{}, errors.New("bad") }
		},
	} {
		mutate()
		h.pruner.Tick(context.Background())
		if h.prunes != 2 {
			t.Fatalf("%s: pruned", name)
		}
	}
}

// A failed prune and an unwritable receipt are both recorded or logged; the
// loop picks up the newest daemon receipt as its rate limit and stops with
// its context.
func TestAutoPrunerFailuresAndLoop(t *testing.T) {
	t.Parallel()
	h := newAutoHarness(t)
	h.failure = errors.New("resolve failed")
	h.pruner.Tick(context.Background())
	if receipt, _, _ := NewestReceipt(h.pruner.StateDir, ""); receipt.Outcome != OutcomeFailed || receipt.Error != "resolve failed" {
		t.Fatalf("failed receipt = %#v", receipt)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.pruner.StateDir = blocked
	h.now = h.now.Add(2 * time.Hour)
	h.failure = nil
	h.pruner.Tick(context.Background())
	if !strings.Contains(strings.Join(h.logs, "\n"), "receipt not written") {
		t.Fatalf("logs = %v", h.logs)
	}

	loop := newAutoHarness(t)
	if err := WriteReceipt(loop.pruner.StateDir, Receipt{Time: loop.now, Trigger: "daemon", Outcome: OutcomeDone}); err != nil {
		t.Fatal(err)
	}
	if err := WriteReceipt(loop.pruner.StateDir, Receipt{Time: loop.now.Add(time.Second), Trigger: "manual", Outcome: OutcomeDone}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { loop.pruner.Run(ctx); close(done) }()
	loop.ticks <- loop.now
	cancel()
	<-done
	if loop.prunes != 0 {
		t.Fatal("the loop ignored the newest daemon receipt's rate limit")
	}
}

// Receipts that cannot be read or parsed are skipped; a missing directory
// has none.
func TestNewestReceiptSkipsUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, ok := NewestReceipt(dir, ""); ok {
		t.Fatal("found a receipt in an empty state directory")
	}
	if err := os.MkdirAll(filepath.Join(ReceiptsDir(dir), "zz-not-a-file"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ReceiptsDir(dir), "zy-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteReceipt(dir, Receipt{Time: time.Unix(1, 0), Trigger: "manual", Outcome: OutcomeDone}); err != nil {
		t.Fatal(err)
	}
	if receipt, _, ok := NewestReceipt(dir, "manual"); !ok || receipt.Trigger != "manual" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if _, _, ok := NewestReceipt(dir, "daemon"); ok {
		t.Fatal("a manual receipt matched the daemon trigger")
	}
	if NewAutoPruner("/p", dir, func(string) {}) == nil {
		t.Fatal("no real auto-pruner")
	}
}

// The real wiring reads the configuration, lists the roots --auto may prune
// and hands the engine --auto at the floor; the engine is a recorder here, so
// nothing on this machine is deleted.
func TestAutoPrunerRealWiring(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("disk:\n  auto_prune: true\n  check_interval: 2s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got Options
	pruner := newAutoPruner(t.TempDir(), t.TempDir(), func(string) {}, func() string { return config }, func(_ context.Context, _ *World, o Options) Report {
		got = o
		return Report{Outcome: OutcomeDone}
	})
	if loaded, err := pruner.Config(); err != nil || !loaded.AutoPrune || loaded.CheckInterval != 2*time.Second {
		t.Fatalf("config = %#v, %v", loaded, err)
	}
	if roots := pruner.Roots(); len(roots) < 2 {
		t.Fatalf("roots = %v", roots)
	}
	if _, report, err := pruner.Prune(context.Background(), 0.1); err != nil || report.Outcome != OutcomeDone || !got.Auto || got.Floor != 0.1 {
		t.Fatalf("prune = %#v, %v (options %#v)", report, err, got)
	}
	ticks, stop := pruner.Ticker(time.Millisecond)
	<-ticks
	stop()
	if pruner.Now().IsZero() {
		t.Fatal("no clock")
	}
	if err := os.WriteFile(config, []byte("disk: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pruner.Config(); err == nil {
		t.Fatal("an invalid configuration was ignored")
	}
}

// A machine that cannot be resolved is a failed run, recorded as such.
//
//nolint:paralleltest // clears HOME
func TestAutoPrunerReportsAnUnresolvableMachine(t *testing.T) {
	config := filepath.Join(t.TempDir(), "absent.yaml")
	pruner := newAutoPruner(t.TempDir(), t.TempDir(), func(string) {}, func() string { return config }, Run)
	t.Setenv("HOME", "")
	if _, _, err := pruner.Prune(context.Background(), 0.1); err == nil {
		t.Fatal("an unresolvable machine was pruned")
	}
}
