package cmddisk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/diskprune"
)

// pruneHarness records what the command passed to the engine and returns a
// canned report.
type pruneHarness struct {
	t        *testing.T
	config   string
	report   diskprune.Report
	options  diskprune.Options
	ignore   []string
	runs     int
	worldErr error
	ticks    chan time.Time
	stopped  bool
	events   []diskprune.Event
}

func newPruneHarness(t *testing.T) *pruneHarness {
	t.Helper()
	return &pruneHarness{t: t, config: filepath.Join(t.TempDir(), "wb.yaml"), report: diskprune.Report{Mode: "plan", Outcome: diskprune.OutcomeDone}, ticks: make(chan time.Time)}
}

func (h *pruneHarness) deps(interactive bool) PruneDependencies {
	return PruneDependencies{
		World: func(_ context.Context, root string, ignore []string) (*diskprune.World, error) {
			h.ignore = ignore
			return &diskprune.World{ProjectsRoot: root}, h.worldErr
		},
		Run: func(_ context.Context, w *diskprune.World, o diskprune.Options) diskprune.Report {
			h.runs++
			h.options = o
			for _, event := range h.events {
				w.Progress(event)
			}
			return h.report
		},
		ConfigPath:  func() string { return h.config },
		Ticker:      func(time.Duration) (<-chan time.Time, func()) { return h.ticks, func() { h.stopped = true } },
		Interactive: func(bool) bool { return interactive },
	}
}

func (h *pruneHarness) execute(flags shared.Flags, interactive bool, args ...string) (string, string, error) {
	h.t.Helper()
	runtime := shared.Runtime{
		Flags:     func() shared.Flags { return flags },
		ExitError: func(code int, message string) error { return &codedError{code, message} },
	}
	command := New(runtime, Dependencies{Prune: h.deps(interactive)})
	var out, errOut bytes.Buffer
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs(append([]string{"prune"}, args...))
	err := command.Execute()
	return out.String(), errOut.String(), err
}

func exitCode(err error) int {
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	if err != nil {
		return -1
	}
	return 0
}

// AC flags-validated-first: every invalid invocation exits 2 before the
// world is read or the engine runs.
func TestPruneRejectsInvalidFlagsBeforeReading(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--category", "docker"}, {"--category", "worktrees"}, {"--category", "wb-state"},
		{"--until-free", "12XB"}, {"--older-than", "soon"}, {"--older-than", "-1h"},
		{"--auto", "--category", "scratch"}, {"--auto", "--until-free", "5%"}, {"--auto", "--older-than", "1h"},
		{"--auto", "--allow-full-clean"}, {"--allow-full-clean", "--category", "scratch"}, {"--format", "yaml"},
	} {
		h := newPruneHarness(t)
		h.worldErr = errors.New("the world must not be read")
		_, _, err := h.execute(shared.Flags{}, false, args...)
		if exitCode(err) != shared.ExitUsage || h.runs != 0 {
			t.Errorf("%v: exit %d (%v), runs %d", args, exitCode(err), err, h.runs)
		}
	}
	h := newPruneHarness(t)
	if err := os.WriteFile(h.config, []byte("disk:\n  minimum_available: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.execute(shared.Flags{}, false); exitCode(err) != shared.ExitUsage {
		t.Fatalf("invalid config: %v", err)
	}
}

// The command passes the selection through, defaults the target to the
// configured floor, and maps the outcome to the exit code.
func TestPrunePassesTheSelectionAndMapsOutcomes(t *testing.T) {
	t.Parallel()
	h := newPruneHarness(t)
	if err := os.WriteFile(h.config, []byte("disk:\n  minimum_available: 0.2\n  prune:\n    ignore_sum_files: [testdata/go.sum]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := h.execute(shared.Flags{ProjectsRoot: "/p", Quiet: true}, false, "--apply", "--category", "go-build-cache", "--allow-full-clean", "--older-than", "100h")
	if err != nil {
		t.Fatal(err)
	}
	o := h.options
	if !o.Apply || !o.AllowFullClean || o.OlderThan != 100*time.Hour || o.Floor != 0.2 || o.Target.Ratio != 0.2 || o.Categories[0] != "go-build-cache" || h.ignore[0] != "testdata/go.sum" {
		t.Fatalf("options = %#v ignore %v", o, h.ignore)
	}
	if !strings.Contains(out, "outcome done") {
		t.Fatalf("text = %q", out)
	}
	h.report.Outcome = diskprune.OutcomePlanned
	if _, _, err := h.execute(shared.Flags{Quiet: true}, false, "--until-free", "max"); exitCode(err) != shared.ExitFindings || h.options.Target.Kind != diskprune.TargetMax {
		t.Fatalf("planned exit %d, target %#v", exitCode(err), h.options.Target)
	}
	h.worldErr = errors.New("resolve failed")
	if _, _, err := h.execute(shared.Flags{Quiet: true}, false); exitCode(err) != -1 {
		t.Fatalf("world failure = %v", err)
	}
	h.worldErr = nil
	h.report.Outcome = diskprune.OutcomeDone
	if _, _, err := h.execute(shared.Flags{Quiet: true}, false, "--auto"); err != nil || !h.options.Auto {
		t.Fatalf("auto = %v %#v", err, h.options)
	}
}

// JSON is the engine's report verbatim; text shows filesystems, every
// category, deletions in full and keeps summarised unless --verbose.
func TestPruneOutput(t *testing.T) {
	t.Parallel()
	estimate := int64(4096)
	h := newPruneHarness(t)
	h.report = diskprune.Report{
		SchemaVersion: 1, Mode: "apply", Outcome: diskprune.OutcomeFailed,
		Reasons:   []string{diskprune.OutcomeFailed, diskprune.OutcomeTargetUnmet},
		Admission: diskprune.AdmissionReport{Status: "bypassed"},
		Filesystems: []diskprune.FilesystemReport{{Path: "/tmp", Before: diskprune.SpaceReport{TotalBytes: 100 << 30, AvailableBytes: 1 << 30},
			After: diskprune.SpaceReport{TotalBytes: 100 << 30, AvailableBytes: 2 << 30}, UnlinkedBytes: 1 << 30, ObservedGainBytes: 1 << 30, Note: "space-not-returned"}},
		Categories: []diskprune.CategoryReport{
			{Name: "scratch", Order: 1, Status: "applied", EffectiveFloor: "24h0m0s", EstimateBytes: &estimate,
				Items: []diskprune.ItemReport{
					{Path: "/tmp/go-build1", Bytes: 4096, Status: "deleted", Reason: "unused"},
					{Path: "/tmp/go-build2", Status: "failed", Error: "EIO"},
					{Path: "/tmp/go-build3", Status: "keep", Reason: "too-young: 1h"},
					{Path: "/tmp/go-build4", Status: "keep", Reason: "too-young: 2h"},
					{Path: "/tmp/wb-rescue-1", Status: "keep", Reason: "recovery-material"},
				}},
			{Name: "go-build-cache", Order: 2, Status: "skipped", Reason: "busy", PIDs: []diskprune.PIDReport{{PID: 7, Command: "go build"}},
				Shards: []diskprune.ShardReport{{Path: "/c/0a", Entries: 3, Bytes: 12288, Status: "deleted"}}},
		},
		State: []diskprune.StateReport{{Root: "/home/u/.wb", Entries: []diskprune.StateEntryReport{{Path: "/home/u/.wb/reports/x", Class: "origin-not-proven", Bytes: 9}}}},
	}
	out, _, err := h.execute(shared.Flags{Quiet: true}, false, "--format", "json")
	var decoded diskprune.Report
	if exitCode(err) != shared.ExitFindings || json.Unmarshal([]byte(out), &decoded) != nil || decoded.Outcome != diskprune.OutcomeFailed {
		t.Fatalf("json = %q, %v", out, err)
	}
	text, _, _ := h.execute(shared.Flags{Quiet: true}, false)
	for _, want := range []string{
		"outcome failed (failed, target-unmet)", "admission: bypassed", "[space-not-returned]", "-> 2.0 GB free", "observed +1.0 GB",
		"1. scratch: applied, floor 24h0m0s, estimate 4.0 KB", "deleted       /tmp/go-build1  4.0 KB  unused",
		"error: EIO", "kept 2: too-young", "kept 1: recovery-material", "2. go-build-cache: skipped (busy)",
		"pid 7: go build", "/c/0a  3 entries", "report-only /home/u/.wb", "origin-not-proven",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	verbose, _, _ := h.execute(shared.Flags{Quiet: true}, false, "--verbose")
	if !strings.Contains(verbose, "/tmp/go-build3") || strings.Contains(verbose, "kept 2") {
		t.Fatalf("verbose text:\n%s", verbose)
	}
	h.report.Mode, h.report.Reasons = "plan", nil
	plan, _, _ := h.execute(shared.Flags{Quiet: true}, false)
	if !strings.Contains(plan, "plan estimates") || strings.Contains(plan, "admission:") {
		t.Fatalf("plan text:\n%s", plan)
	}
	if signedHuman(-2048) != "-2.0 KB" {
		t.Fatalf("negative change = %q", signedHuman(-2048))
	}
	if err := writePruneReport(failingWriter{err: errors.New("broken pipe")}, "json", false, h.report); err == nil {
		t.Fatal("a failed write was ignored")
	}
}

// Liveness: per-line on a terminal or with --verbose; otherwise a heartbeat
// on each tick that states its interval once and closes the run; --quiet
// silences both.
func TestPruneProgressModes(t *testing.T) {
	t.Parallel()
	h := newPruneHarness(t)
	h.events = []diskprune.Event{{Phase: "plan"}, {Phase: "plan"}, {Phase: "apply", Category: "scratch", Unlinked: 4096}}
	_, errOut, _ := h.execute(shared.Flags{}, true)
	// discover (before the world exists), plan once, then apply.
	if strings.Count(errOut, "wb disk prune: ") != 3 || !strings.HasPrefix(errOut, "wb disk prune: discover") || !strings.Contains(errOut, "apply scratch") {
		t.Fatalf("per-line progress = %q", errOut)
	}
	_, quiet, _ := h.execute(shared.Flags{Quiet: true}, true)
	if quiet != "" {
		t.Fatalf("quiet progress = %q", quiet)
	}

	var out lockedBuffer
	p := newPruneProgress(&out, PruneDependencies{
		Ticker:      func(time.Duration) (<-chan time.Time, func()) { return h.ticks, func() { h.stopped = true } },
		Interactive: func(bool) bool { return false },
	}, shared.Flags{}, false)
	p.event(diskprune.Event{Phase: "measure", Category: "go-module-cache"})
	h.ticks <- time.Now()
	h.ticks <- time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(out.String(), "measure go-module-cache") < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.finish(diskprune.Report{Outcome: diskprune.OutcomeDone})
	p.beat()
	text := out.String()
	if strings.Count(text, "heartbeat every 10s") != 1 || strings.Count(text, "measure go-module-cache") < 2 || !strings.Contains(text, "done; outcome done") || !h.stopped {
		t.Fatalf("heartbeat = %q", text)
	}
	silent := newPruneProgress(io.Discard, PruneDependencies{
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }, Interactive: func(bool) bool { return false },
	}, shared.Flags{}, false)
	silent.finish(diskprune.Report{})
	if DefaultPruneDependencies().World == nil {
		t.Fatal("default dependencies incomplete")
	}
	ticks, stop := DefaultPruneDependencies().Ticker(time.Millisecond)
	<-ticks
	stop()
	_ = DefaultPruneDependencies().Interactive(true)
}

// lockedBuffer is safe for the heartbeat goroutine and the test to share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// wb disk's help carries the clean-versus-prune paragraph, and prune is its
// subcommand.
func TestDiskHelpExplainsCleanVersusPrune(t *testing.T) {
	t.Parallel()
	command := New(runtimeFor("/p"), Dependencies{Collect: func(context.Context, disk.Options) (disk.Report, error) { return disk.Report{}, nil }})
	if !strings.Contains(command.Long, "wb disk prune empties the shared storeroom") {
		t.Fatal("wb disk help lacks the clean-versus-prune paragraph")
	}
	var prune *cobra.Command
	for _, sub := range command.Commands() {
		if sub.Name() == "prune" {
			prune = sub
		}
	}
	if prune == nil || !strings.Contains(prune.Long, "wb clean clears finished workbenches") || !strings.Contains(prune.Long, "never runs\ngo clean") {
		t.Fatal("wb disk prune help incomplete")
	}
}

// A report that cannot be written is an error, not a silent success.
func TestPruneReportsAWriteFailure(t *testing.T) {
	t.Parallel()
	h := newPruneHarness(t)
	runtime := shared.Runtime{
		Flags:     func() shared.Flags { return shared.Flags{Quiet: true} },
		ExitError: func(code int, message string) error { return &codedError{code, message} },
	}
	command := New(runtime, Dependencies{Prune: h.deps(false)})
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetOut(failingWriter{err: errors.New("broken pipe")})
	command.SetErr(io.Discard)
	command.SetArgs([]string{"prune"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("write failure = %v", err)
	}
}

// Every --apply and --auto run records a receipt; one that cannot be written
// (a full disk) is reported on stderr and never changes the exit.
func TestPruneWritesAReceiptForEveryApply(t *testing.T) {
	t.Parallel()
	h := newPruneHarness(t)
	var triggers []string
	deps := h.deps(false)
	deps.WriteReceipt = func(stateDir string, receipt diskprune.Receipt) error {
		triggers = append(triggers, receipt.Trigger)
		return nil
	}
	runtime := shared.Runtime{
		Flags:     func() shared.Flags { return shared.Flags{Quiet: true} },
		ExitError: func(code int, message string) error { return &codedError{code, message} },
	}
	run := func(deps PruneDependencies, args ...string) (string, error) {
		command := New(runtime, Dependencies{Prune: deps})
		var errOut bytes.Buffer
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(io.Discard)
		command.SetErr(&errOut)
		command.SetArgs(append([]string{"prune"}, args...))
		err := command.Execute()
		return errOut.String(), err
	}
	for _, args := range [][]string{{}, {"--apply"}, {"--auto"}} {
		if _, err := run(deps, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if strings.Join(triggers, ",") != "manual,manual-auto" {
		t.Fatalf("triggers = %v; a plan must write none", triggers)
	}
	deps.WriteReceipt = func(string, diskprune.Receipt) error { return errors.New("no space left on device") }
	if errOut, err := run(deps, "--apply"); err != nil || !strings.Contains(errOut, "receipt not written: no space left on device") {
		t.Fatalf("stderr %q err %v", errOut, err)
	}
}
