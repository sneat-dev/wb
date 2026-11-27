//go:build linux

package diskprune

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildCacheFixture lays out a GOCACHE with entries of the given ages, Go's
// bookkeeping files, a fuzz corpus, and a foreign directory inside a shard.
func buildCacheFixture(t *testing.T, tw *testWorld, ages map[string]time.Duration) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "go-build")
	mustWrite(t, filepath.Join(root, "README"), "This directory holds cached build artifacts from the Go build system.", 0o644)
	mustWrite(t, filepath.Join(root, "trim.txt"), "1", 0o644)
	mustWrite(t, filepath.Join(root, "testexpire.txt"), "1", 0o644)
	mustWrite(t, filepath.Join(root, "fuzz", "pkg", "FuzzX", "corpus"), "seed", 0o644)
	mustWrite(t, filepath.Join(root, "ab", "backup", "notes"), "mine", 0o644)
	mustWrite(t, filepath.Join(root, "ab", "notanentry"), "x", 0o644)
	for name, age := range ages {
		path := filepath.Join(root, name[:2], name)
		mustWrite(t, path, strings.Repeat("x", 4096), 0o644)
		when := tw.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	tw.Go.GOCACHE = root
	tw.categories = []category{buildCacheCategory{}, fullCleanCategory{}}
	return root
}

func keptFiles(t *testing.T, root string) []string {
	t.Helper()
	var kept []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			kept = append(kept, rel)
		}
		return nil
	})
	return kept
}

// AC go-cache-trimmed-by-last-use: oldest entries go first, nothing younger
// than the floor (raised by a long-running build tool, not by gopls) goes,
// and Go's bookkeeping, the fuzz corpus and foreign files stay.
func TestBuildCacheTrimRespectsTheFloor(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{
		"0a01-a": 100 * time.Hour, "0a02-d": 40 * time.Hour, "1b01-a": 32 * time.Hour, "1b02-a": 30 * time.Hour, "2c01-a": time.Hour,
	})
	tw.processes = []Process{
		{PID: 501, Comm: "go", Readable: true, Age: 30 * time.Hour},
		{PID: 502, Comm: "gopls", Readable: true, Age: 40 * 24 * time.Hour},
	}
	report := tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	cat := categoryByName(t, report, CategoryBuild)
	if cat.EffectiveFloor != "31h0m0s" || cat.FloorSetByPID != 501 || len(cat.Items) != 0 || len(cat.Shards) != 2 {
		t.Fatalf("category = %#v", cat)
	}
	kept := strings.Join(keptFiles(t, root), " ")
	for _, gone := range []string{"0a01-a", "0a02-d", "1b01-a"} {
		if strings.Contains(kept, gone) {
			t.Errorf("%s survived: %s", gone, kept)
		}
	}
	for _, stays := range []string{"1b02-a", "2c01-a", "README", "trim.txt", "testexpire.txt", "corpus", "notes", "notanentry"} {
		if !strings.Contains(kept, stays) {
			t.Errorf("%s was removed: %s", stays, kept)
		}
	}
	if report.Outcome != OutcomeDone {
		t.Fatalf("outcome %q %v", report.Outcome, report.Reasons)
	}
}

// AC full-clean-needs-its-flag: without the flag nothing young goes; with it
// every entry goes but fuzz and foreign files stay and go is never run; a Go
// tool or --older-than skips it as a requested step.
func TestBuildCacheFullClean(t *testing.T) {
	t.Parallel()
	ages := map[string]time.Duration{"0a01-a": 100 * time.Hour, "1b01-a": time.Hour}
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, ages)
	tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	if !strings.Contains(strings.Join(keptFiles(t, root), " "), "1b01-a") {
		t.Fatal("a young entry went without --allow-full-clean")
	}
	report := tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, AllowFullClean: true, Floor: 0.01})
	kept := strings.Join(keptFiles(t, root), " ")
	if strings.Contains(kept, "1b01-a") || !strings.Contains(kept, "corpus") || !strings.Contains(kept, "notes") {
		t.Fatalf("after full clean: %s (%v)", kept, report.Reasons)
	}
	if categoryByName(t, report, categoryFullClean).Status != StatusApplied {
		t.Fatalf("full clean = %#v", categoryByName(t, report, categoryFullClean))
	}

	busy := newTestWorld(t)
	buildCacheFixture(t, busy, ages)
	busy.processes = []Process{{PID: 77, Comm: "gopls", Readable: true}}
	got := busy.run(Options{Apply: true, Categories: []string{CategoryBuild}, AllowFullClean: true, Floor: 0.01})
	if cat := categoryByName(t, got, categoryFullClean); !strings.HasPrefix(cat.Reason, "requested-step-skipped") || len(cat.PIDs) != 1 || got.Outcome != OutcomeFailClosed {
		t.Fatalf("busy full clean = %#v outcome %q", cat, got.Outcome)
	}
	aged := newTestWorld(t)
	buildCacheFixture(t, aged, ages)
	got = aged.run(Options{Apply: true, Categories: []string{CategoryBuild}, AllowFullClean: true, OlderThan: 48 * time.Hour, Floor: 0.01})
	if cat := categoryByName(t, got, categoryFullClean); cat.Reason != "age-filter-unsupported" {
		t.Fatalf("aged full clean = %#v", cat)
	}
	started := newTestWorld(t)
	buildCacheFixture(t, started, ages)
	started.categories = []category{buildCacheCategory{}, hookedCategory{inner: fullCleanCategory{}, after: func() {
		started.processes = []Process{{PID: 88, Comm: "go", Readable: true}}
	}}}
	got = started.run(Options{Apply: true, Categories: []string{CategoryBuild}, AllowFullClean: true, Floor: 0.01})
	if cat := categoryByName(t, got, categoryFullClean); len(cat.Items) != 1 || !strings.HasPrefix(cat.Items[0].Reason, "requested-step-skipped") || got.Outcome != OutcomeFailClosed {
		t.Fatalf("go started before the full clean applied: %#v", cat)
	}
}

// Configurations prune cannot age, a protected cache, untrusted process
// evidence (fixed 72 h floor), unreadable shards and removal failures.
func TestBuildCacheEdgeCases(t *testing.T) {
	t.Parallel()
	for name, env := range map[string]GoEnv{
		"off": {GOCACHE: "off"}, "relative": {GOCACHE: "cache"}, "prog": {GOCACHE: "/tmp", GOCACHEPROG: "cacheprog"},
		"missing": {GOCACHE: "/nonexistent/go-build"},
	} {
		tw := newTestWorld(t)
		tw.Go = env
		tw.categories = []category{buildCacheCategory{}, fullCleanCategory{}}
		report := tw.run(Options{Categories: []string{CategoryBuild}, AllowFullClean: true})
		for _, cat := range report.Categories {
			if !strings.HasPrefix(cat.Reason, "unsupported-cache") {
				t.Errorf("%s: %s = %#v", name, cat.Name, cat)
			}
		}
	}
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 50 * time.Hour, "0a02-a": 100 * time.Hour})
	tw.Protected = []string{filepath.Dir(root)}
	report := tw.run(Options{Categories: []string{CategoryBuild}, AllowFullClean: true})
	for _, cat := range report.Categories {
		if !strings.HasPrefix(cat.Reason, "protected") {
			t.Errorf("protected %s = %#v", cat.Name, cat)
		}
	}
	tw.Protected = nil
	tw.evidence = Evidence{Unknown: "container"}
	report = tw.run(Options{Categories: []string{CategoryBuild}, AllowFullClean: true})
	if cat := categoryByName(t, report, CategoryBuild); cat.EffectiveFloor != "72h0m0s" || len(cat.Shards) != 1 || cat.Shards[0].Entries != 1 {
		t.Fatalf("untrusted evidence floor = %#v", cat)
	}
	if cat := categoryByName(t, report, categoryFullClean); !strings.HasPrefix(cat.Reason, "unknown") {
		t.Fatalf("full clean under unknown evidence = %#v", cat)
	}
	tw.evidence = Evidence{}
	if err := os.Chmod(filepath.Join(root, "0a"), 0o000); err != nil {
		t.Fatal(err)
	}
	report = tw.run(Options{Categories: []string{CategoryBuild}})
	_ = os.Chmod(filepath.Join(root, "0a"), 0o755)
	if os.Geteuid() != 0 && !categoryByName(t, report, CategoryBuild).Partial {
		t.Fatal("an unreadable shard must make the category partial")
	}
	tw.Identity = func(string) (Identity, error) { return Identity{}, errors.New("statx") }
	if !categoryByName(t, tw.run(Options{Categories: []string{CategoryBuild}}), CategoryBuild).Partial {
		t.Fatal("an identity failure must make the category partial")
	}
	tw.Identity = StatIdentity
	calls := 0
	tw.RemoveFile = func(dir, name string, id Identity) (int64, error) {
		calls++
		if calls == 1 {
			return 0, ErrChanged
		}
		return 0, errors.New("EIO")
	}
	report = tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	if report.Outcome != OutcomeFailed || calls != 2 {
		t.Fatalf("removal failure outcome %q calls %d", report.Outcome, calls)
	}
	if _, partial := readCacheFiles(tw.World, filepath.Join(t.TempDir(), "absent")); !partial {
		t.Fatal("a missing cache must read as partial")
	}
}
