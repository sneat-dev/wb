//go:build linux

package diskprune

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AC scratch-producers-only: only directories whose producer is known, that
// nothing uses, that hold no checkout, runtime state or cache data, and that
// are old enough, are removed; everything else is kept with its reason.
func TestScratchRemovesOnlyKnownUnusedProducers(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	for _, name := range []string{"go-build222", "wb-index-77", "TestFoo123", "go-build111", "wb-pr1-test-cache-x", "wb-notes", "wb-rescue-1", "wb-sock-1", "wb-wt-x", "wb-index-backup"} {
		tw.entry(name, 64)
	}
	cache := filepath.Join(tw.tmp, "wb-pr2-lint-go-cache")
	mustWrite(t, filepath.Join(cache, "README"), "This directory holds cached build artifacts from the Go build system.", 0o644)
	mustWrite(t, filepath.Join(cache, "0a", "0a1b-a"), "x", 0o644)
	mustWrite(t, filepath.Join(cache, "trim.txt"), "1", 0o644)
	notCache := filepath.Join(tw.tmp, "wb-x-cache")
	mustWrite(t, filepath.Join(notCache, "README"), "", 0o644)
	mustWrite(t, filepath.Join(notCache, "notes.txt"), "mine", 0o644)
	held := filepath.Join(tw.tmp, "wb-pr1-test-cache")
	mustWrite(t, filepath.Join(held, "README"), "", 0o644)
	mustWrite(t, filepath.Join(tw.tmp, "wb-index-9", "repo", ".git", "HEAD"), "ref", 0o644)
	mustWrite(t, filepath.Join(tw.tmp, "wb-wt-x", ".git"), "gitdir: /x", 0o644)
	mustWrite(t, filepath.Join(tw.tmp, "wb", "old-task", "f"), "x", 0o644)
	mustWrite(t, filepath.Join(tw.tmp, "go-build555", "sub", "f"), "x", 0o644)
	if err := os.Chmod(filepath.Join(tw.tmp, "go-build555", "sub"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(tw.tmp, "go-build555", "sub"), 0o755) })
	mustWrite(t, filepath.Join(tw.tmp, "unrelated-dir", "f"), "x", 0o644)
	mustWrite(t, filepath.Join(tw.tmp, "wb-loose-file"), "x", 0o644)
	if err := os.Symlink(tw.tmp, filepath.Join(tw.tmp, "go-build999")); err != nil {
		t.Fatal(err)
	}
	tw.processes = []Process{
		{PID: 4242, Comm: "compile", Readable: true, Files: []string{filepath.Join(tw.tmp, "go-build111", "001", "data")}},
		{PID: 4243, Comm: "bash", Readable: true, Environ: []string{"GOCACHE=" + held}},
		{PID: 7, Comm: "sshd", Argv: []string{"sshd: u@pts/0"}},
	}

	report := tw.run(Options{Apply: true, Floor: 0.01})
	items := itemsByName(report, CategoryScratch)
	for name, want := range map[string]string{
		"go-build222": StatusDeleted, "wb-index-77": StatusDeleted, "TestFoo123": StatusDeleted, "wb-pr2-lint-go-cache": StatusDeleted,
	} {
		if items[name].Status != want || exists(filepath.Join(tw.tmp, name)) {
			t.Errorf("%s: status %q (%s), still exists %t", name, items[name].Status, items[name].Reason, exists(filepath.Join(tw.tmp, name)))
		}
	}
	for name, reason := range map[string]string{
		"go-build111": "held-open", "wb-pr1-test-cache": "held-open", "wb-notes": "not-a-known-producer",
		"wb-rescue-1": "recovery-material", "wb-sock-1": "runtime", "wb-wt-x": "checkout-in-scratch",
		"wb-index-9": "checkout-in-scratch", "wb": "owner-unverifiable", "go-build555": "partial-scan",
		"wb-index-backup": "not-a-known-producer", "wb-x-cache": "not-a-known-producer",
		"wb-loose-file": "not-a-directory", "go-build999": "not-a-directory", "wb-pr1-test-cache-x": "not-a-known-producer",
	} {
		if it := items[name]; it.Status != StatusKeep || !strings.HasPrefix(it.Reason, reason) {
			t.Errorf("%s: status %q reason %q, want keep %q", name, it.Status, it.Reason, reason)
		}
		if !exists(filepath.Join(tw.tmp, name)) {
			t.Errorf("%s was removed", name)
		}
	}
	if pids := items["go-build111"].PIDs; len(pids) != 1 || pids[0].PID != 4242 {
		t.Errorf("held-open pids = %#v", pids)
	}
	if _, listed := items["unrelated-dir"]; listed {
		t.Error("an entry prune has no opinion about was listed")
	}
	// go-build555 is partial-scan, a fail-closed finding.
	if report.Outcome != OutcomeFailClosed || !report.Lock.Held || tw.admitted != 1 {
		t.Fatalf("outcome %q reasons %v lock %#v admitted %d", report.Outcome, report.Reasons, report.Lock, tw.admitted)
	}
}

// Young entries, entries containing protected data or cache data, a foreign
// owner, and a nested socket are all kept.
func TestScratchKeepsYoungProtectedAndCacheData(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	tw.Now = time.Now
	clone := filepath.Join(tw.tmp, "go-build2", "acme", "app")
	mustWrite(t, filepath.Join(clone, "main.go"), "package main", 0o644)
	tw.Protected = []string{clone}
	mustWrite(t, filepath.Join(tw.tmp, "wb-coverage-baseline-3", "mod", "cache", "download", "x", "@v", "list"), "", 0o644)
	sock := filepath.Join(tw.tmp, "wb-index-5")
	if err := os.MkdirAll(sock, 0o755); err != nil {
		t.Fatal(err)
	}
	if len(sock) < 100 {
		if listener, err := net.Listen("unix", filepath.Join(sock, "s")); err == nil {
			t.Cleanup(func() { _ = listener.Close() })
		}
	}
	tw.UID = os.Getuid() + 1
	mustWrite(t, filepath.Join(tw.tmp, "go-build333", "f"), "x", 0o644)
	foreign := tw.run(Options{Apply: true, Floor: 0.01})
	if it := itemsByName(foreign, CategoryScratch)["go-build333"]; it.Reason != "foreign-owner" {
		t.Fatalf("foreign = %#v", it)
	}
	tw.UID = os.Getuid()

	report := tw.run(Options{Apply: true, Floor: 0.01, OlderThan: time.Hour})
	items := itemsByName(report, CategoryScratch)
	if it := items["go-build1"]; it.Status != StatusKeep || !strings.HasPrefix(it.Reason, "too-young") {
		t.Errorf("young = %#v", it)
	}
	if it := items["go-build2"]; !strings.HasPrefix(it.Reason, "protected") {
		t.Errorf("protected = %#v", it)
	}
	if it := items["wb-coverage-baseline-3"]; it.Reason != "contains-cache-data" {
		t.Errorf("cache data = %#v", it)
	}
	if it := items["wb-index-5"]; exists(filepath.Join(sock, "s")) && it.Reason != "runtime" {
		t.Errorf("socket = %#v", it)
	}
	if cat := categoryByName(t, report, CategoryScratch); cat.EffectiveFloor != "24h0m0s" {
		t.Errorf("floor = %q", cat.EffectiveFloor)
	}
	if insideTmp := tw.tmp; true {
		tw.Protected = []string{insideTmp}
		around := tw.run(Options{Floor: 0.01})
		if it := itemsByName(around, CategoryScratch)["go-build1"]; !strings.HasPrefix(it.Reason, "protected") {
			t.Errorf("a candidate inside a protected root = %#v", it)
		}
	}
}

// A plan changes nothing and reports the work as planned; the target stops
// deletion once it is met, and later candidates are beyond-target.
func TestScratchPlanChangesNothingAndTargetStops(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 600<<10)
	tw.entry("go-build2", 600<<10)
	tw.available = tw.total/10 - 500<<10
	plan := tw.run(Options{Target: Target{Kind: TargetRatio, Ratio: 0.1}})
	items := itemsByName(plan, CategoryScratch)
	if items["go-build1"].Status != StatusDelete || items["go-build2"].Status != StatusBeyondTarget || items["go-build1"].ProjectedAvailableBytes == nil {
		t.Fatalf("plan items = %#v", items)
	}
	if plan.Outcome != OutcomePlanned || plan.Lock.Held || plan.Admission.Status != "not-needed" || len(tw.removed) != 0 {
		t.Fatalf("plan outcome %q lock %#v admission %#v removed %v", plan.Outcome, plan.Lock, plan.Admission, tw.removed)
	}
	if !exists(filepath.Join(tw.tmp, "go-build1")) || !exists(filepath.Join(tw.tmp, "go-build2")) {
		t.Fatal("a plan deleted something")
	}

	applied := tw.run(Options{Apply: true, Target: Target{Kind: TargetRatio, Ratio: 0.1}, Floor: 0.01})
	items = itemsByName(applied, CategoryScratch)
	if len(tw.removed) != 1 || applied.Outcome != OutcomeDone {
		t.Fatalf("removed %v outcome %q reasons %v items %#v", tw.removed, applied.Outcome, applied.Reasons, items)
	}
	fs := applied.Filesystems[0]
	if !fs.TargetMet || fs.UnlinkedBytes <= 0 || fs.ObservedGainBytes != fs.UnlinkedBytes || fs.EstimatedBytes <= 0 {
		t.Fatalf("filesystem = %#v", fs)
	}
}

// Deletions that free nothing stop escalation on that filesystem; a byte
// target larger than the filesystem is reported, not pursued.
func TestScratchSpaceNotReturnedAndUnreachableTarget(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 300<<20)
	tw.returned = 0
	report := tw.run(Options{Apply: true, Floor: 0.01})
	if report.Outcome != OutcomeSpaceNotReturned || report.Filesystems[0].Note != "space-not-returned" {
		t.Fatalf("outcome %q fs %#v", report.Outcome, report.Filesystems)
	}

	unreachable := newTestWorld(t)
	unreachable.entry("go-build1", 8)
	got := unreachable.run(Options{Apply: true, Target: Target{Kind: TargetBytes, Bytes: unreachable.total * 2}, Floor: 0.01})
	if it := itemsByName(got, CategoryScratch)["go-build1"]; it.Reason != "target-unreachable" || !exists(filepath.Join(unreachable.tmp, "go-build1")) {
		t.Fatalf("unreachable item = %#v", it)
	}
	if got.Outcome != OutcomeFailClosed {
		t.Fatalf("unreachable outcome %q", got.Outcome)
	}
}

// AC one-prune-per-user: a held run lock deletes nothing and names the
// holder; a held resource lock skips only that category.
func TestScratchRespectsLocks(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	home, err := LockDir(tw.Home)
	if err != nil {
		t.Fatal(err)
	}
	report := tw.run(Options{Apply: true})
	home.Release()
	if report.Outcome != OutcomeLocked || report.Lock.Held || !exists(filepath.Join(tw.tmp, "go-build1")) {
		t.Fatalf("locked run = %#v", report)
	}

	resource, err := LockDir(tw.tmp)
	if err != nil {
		t.Fatal(err)
	}
	report = tw.run(Options{Apply: true, Floor: 0.01})
	resource.Release()
	if cat := categoryByName(t, report, CategoryScratch); cat.Status != StatusSkipped || !strings.HasPrefix(cat.Reason, "locked") {
		t.Fatalf("resource-locked category = %#v", cat)
	}

	tw.LockDirs = func(paths []string) (*Lock, error) {
		if len(paths) == 1 && paths[0] == tw.Home {
			return &Lock{}, nil
		}
		return nil, errors.New("flock unsupported")
	}
	report = tw.run(Options{Apply: true, Floor: 0.01})
	if cat := categoryByName(t, report, CategoryScratch); !strings.HasPrefix(cat.Reason, "lock-unavailable") {
		t.Fatalf("lock failure = %#v", cat)
	}
}

// Untrusted process evidence and an incomplete protected inventory skip the
// whole category as fail-closed; an unreadable Go tool makes entries
// busy-unverified.
func TestScratchFailsClosedOnUntrustedEvidence(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*testWorld){
		"unsupported": func(tw *testWorld) { tw.evidence = Evidence{Unsupported: true} },
		"unknown":     func(tw *testWorld) { tw.evidence = Evidence{Unknown: "nested PID namespace"} },
		"hidepid": func(tw *testWorld) {
			tw.evidence = Evidence{HidePID: true}
			if err := os.Chmod(tw.tmp, 0o1777); err != nil {
				t.Fatal(err)
			}
		},
		"protected gap": func(tw *testWorld) { tw.ProtectedGap = "git worktree list failed in acme/app" },
	} {
		tw := newTestWorld(t)
		tw.entry("go-build1", 8)
		mutate(tw)
		report := tw.run(Options{Apply: true, Floor: 0.01})
		if cat := categoryByName(t, report, CategoryScratch); cat.Status != StatusSkipped || report.Outcome != OutcomeFailClosed || !exists(filepath.Join(tw.tmp, "go-build1")) {
			t.Errorf("%s: category %#v outcome %q", name, cat, report.Outcome)
		}
	}
	hidden := newTestWorld(t)
	hidden.entry("go-build1", 8)
	hidden.evidence = Evidence{HidePID: true}
	if err := os.Chmod(hidden.tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if report := hidden.run(Options{Floor: 0.01}); itemsByName(report, CategoryScratch)["go-build1"].Status != StatusDelete {
		t.Fatal("hidepid with a private temporary directory must still plan")
	}

	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	tw.processes = []Process{{PID: 8, Comm: "go", Argv: []string{"go", "build"}}}
	report := tw.run(Options{Apply: true, Floor: 0.01})
	if it := itemsByName(report, CategoryScratch)["go-build1"]; it.Reason != "busy-unverified" || report.Outcome != OutcomeFailClosed {
		t.Fatalf("unverified = %#v, outcome %q", it, report.Outcome)
	}
}

// AC recheck-before-delete: something that starts using a candidate after the
// plan, or makes it unsafe, keeps it; a removal error is a failure.
func TestScratchRechecksBeforeEachDeletion(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		mutate func(tw *testWorld, path string)
		status string
		reason string
	}{
		"held after plan": {func(tw *testWorld, path string) {
			tw.processes = []Process{{PID: 9, Comm: "cat", Readable: true, Cwd: path}}
		}, StatusSkipped, "held-open"},
		"unverified after plan": {func(tw *testWorld, path string) {
			tw.processes = []Process{{PID: 9, Comm: "go"}}
		}, StatusSkipped, "busy-unverified"},
		"checkout after plan": {func(tw *testWorld, path string) {
			mustWrite(tw.t, filepath.Join(path, ".git"), "gitdir: x", 0o644)
		}, StatusSkipped, "checkout-in-scratch"},
		"young after plan": {func(tw *testWorld, path string) {
			now := time.Now()
			tw.Now = func() time.Time { return now }
		}, StatusSkipped, "too-young"},
		"changed": {func(tw *testWorld, path string) {
			tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{}, ErrChanged }
		}, StatusSkipped, "changed"},
		"replaced": {func(tw *testWorld, path string) {
			tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{Bytes: 4096}, ErrReplaced }
		}, StatusReplaced, "replaced"},
		"mount": {func(tw *testWorld, path string) {
			tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{}, ErrContainsMount }
		}, StatusSkipped, "contains-mount"},
		"moved": {func(tw *testWorld, path string) {
			tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{}, ErrMoved }
		}, StatusSkipped, "moved"},
		"unsupported": {func(tw *testWorld, path string) {
			tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{}, ErrUnsupported }
		}, StatusSkipped, "unsupported-platform"},
		"failed": {func(tw *testWorld, path string) {
			tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{}, errors.New("EIO") }
		}, StatusFailed, ""},
	} {
		tw := newTestWorld(t)
		path := tw.entry("go-build1", 8)
		categories := tw.categories
		tw.categories = []category{hookedCategory{inner: categories[0], after: func() { c.mutate(tw, path) }}}
		report := tw.run(Options{Apply: true, Floor: 0.01})
		if it := itemsByName(report, CategoryScratch)["go-build1"]; it.Status != c.status || it.Reason != c.reason {
			t.Errorf("%s: item %#v", name, it)
		}
	}
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{}, errors.New("EIO") }
	if report := tw.run(Options{Apply: true, Floor: 0.01}); report.Outcome != OutcomeFailed {
		t.Fatalf("failed outcome = %q", report.Outcome)
	}
}

// hookedCategory runs a hook after its inner category planned, to change the
// world between plan and apply.
type hookedCategory struct {
	inner category
	after func()
}

func (h hookedCategory) name() string { return h.inner.name() }

func (h hookedCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	plan := h.inner.plan(ctx, w, o)
	h.after()
	return plan
}

// Planning failures degrade per entry: an unreadable temporary directory is
// partial, an identity failure keeps the entry, a cancelled context stops.
func TestScratchPlanningFailures(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.TempDirs = append(tw.TempDirs, filepath.Join(tw.tmp, "absent"))
	tw.entry("go-build1", 8)
	tw.Identity = func(string) (Identity, error) { return Identity{}, errors.New("statx failed") }
	report := tw.run(Options{Floor: 0.01})
	cat := categoryByName(t, report, CategoryScratch)
	if !cat.Partial || !strings.HasPrefix(itemsByName(report, CategoryScratch)["go-build1"].Reason, "partial-scan") {
		t.Fatalf("category %#v", cat)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := Run(ctx, newTestWorld(t).World, Options{Target: Target{Kind: TargetMax}})
	if cancelled.Outcome != OutcomeCancelled {
		t.Fatalf("cancelled outcome %q", cancelled.Outcome)
	}
	probe := newTestWorld(t)
	probe.entry("go-build1", 8)
	probe.Probe = func(string) (diskFilesystem, error) { return diskFilesystem{}, errors.New("statfs failed") }
	if it := itemsByName(probe.run(Options{Floor: 0.01}), CategoryScratch)["go-build1"]; it.Status != StatusFailed {
		t.Fatalf("probe failure item = %#v", it)
	}
}
