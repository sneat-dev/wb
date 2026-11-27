//go:build linux

package diskprune

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePnpm answers pnpm's three queries and records every call.
type fakePnpm struct {
	version, store, cacheDir string
	after                    string
	fail                     map[string]error
	calls                    []string
}

func (f *fakePnpm) run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if err := f.fail[call]; err != nil {
		return "", err
	}
	switch call {
	case "--version":
		return f.version + "\n", nil
	case "store path":
		if f.after != "" && strings.Contains(strings.Join(f.calls, ";"), "store prune") {
			return f.after, nil
		}
		return f.store + "\n", nil
	case "config get cache-dir":
		return f.cacheDir, nil
	}
	return "", nil
}

func pnpmWorld(t *testing.T) (*testWorld, *fakePnpm) {
	t.Helper()
	tw := newTestWorld(t)
	store := filepath.Join(t.TempDir(), "pnpm", "store")
	mustWrite(t, filepath.Join(store, "v10", "files", "00", "abc"), "lonely", 0o644)
	tw.PnpmStores = []string{store, filepath.Join(t.TempDir(), "absent-store")}
	fake := &fakePnpm{version: "10.20.0", store: filepath.Join(store, "v10"), cacheDir: filepath.Join(t.TempDir(), "pnpm-cache"), fail: map[string]error{}}
	tw.Pnpm = fake.run
	tw.categories = []category{pnpmCategory{}}
	return tw, fake
}

// The plan never runs pnpm and has no estimate; apply prunes through pnpm
// itself, from a fixed environment, with no --store-dir.
func TestPnpmPlanNeverRunsPnpmAndApplyDelegates(t *testing.T) {
	t.Parallel()
	tw, fake := pnpmWorld(t)
	plan := tw.run(Options{Floor: 0.01})
	cat := categoryByName(t, plan, CategoryPnpm)
	if len(fake.calls) != 0 || cat.EstimateBytes != nil || len(cat.Items) != 1 || cat.Items[0].ProjectedAvailableBytes != nil {
		t.Fatalf("plan ran pnpm %v or estimated: %#v", fake.calls, cat)
	}
	if !strings.Contains(cat.Items[0].Reason, "single-link") || cat.Items[0].Bytes <= 0 {
		t.Fatalf("plan reason = %q bytes %d", cat.Items[0].Reason, cat.Items[0].Bytes)
	}
	report := tw.run(Options{Apply: true, Floor: 0.01})
	if got := strings.Join(fake.calls, ";"); got != "--version;store path;config get cache-dir;store prune;store path" {
		t.Fatalf("calls = %s", got)
	}
	if cat := categoryByName(t, report, CategoryPnpm); cat.Items[0].Status != StatusDeleted {
		t.Fatalf("applied = %#v", cat)
	}
	for _, setting := range []string{"COREPACK_ENABLE_NETWORK=0", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0"} {
		found := false
		for _, value := range pnpmEnv {
			found = found || value == setting
		}
		if !found {
			t.Errorf("pnpm environment lacks %s", setting)
		}
	}
}

// Every refusal: pnpm running (as node), too old, unresolvable, a store or
// cache directory outside what was measured or inside protected data, a
// write set on a filesystem already at its target, a held lock, and a prune
// that fails or changes the store.
func TestPnpmRefusals(t *testing.T) {
	t.Parallel()
	failure := errors.New("exit 1")
	cases := map[string]struct {
		mutate func(tw *testWorld, f *fakePnpm)
		status string
		reason string
	}{
		"busy": {func(tw *testWorld, f *fakePnpm) {
			tw.processes = []Process{{PID: 3, Comm: "node", Argv: []string{"node", "/usr/lib/node_modules/pnpm/bin/pnpm.cjs", "install"}, Readable: true}}
		}, StatusSkipped, "busy"},
		"old":         {func(tw *testWorld, f *fakePnpm) { f.version = "7.33.0" }, StatusSkipped, "unavailable"},
		"no version":  {func(tw *testWorld, f *fakePnpm) { f.fail["--version"] = failure }, StatusSkipped, "unavailable"},
		"no path":     {func(tw *testWorld, f *fakePnpm) { f.fail["store path"] = failure }, StatusSkipped, "unavailable"},
		"no cachedir": {func(tw *testWorld, f *fakePnpm) { f.fail["config get cache-dir"] = failure }, StatusSkipped, "unavailable"},
		"elsewhere":   {func(tw *testWorld, f *fakePnpm) { f.store = "/srv/other-store/v10" }, StatusSkipped, "store-not-measured"},
		"protected store": {func(tw *testWorld, f *fakePnpm) {
			tw.Protected = []string{filepath.Dir(f.store)}
		}, StatusSkipped, "protected"},
		"protected cache": {func(tw *testWorld, f *fakePnpm) {
			f.cacheDir = filepath.Join(tw.StateDir, "pnpm")
			tw.Protected = []string{tw.StateDir}
		}, StatusSkipped, "protected"},
		"cache at target": {func(tw *testWorld, f *fakePnpm) {
			probe := tw.Probe
			tw.Probe = func(path string) (diskFilesystem, error) {
				fs, err := probe(path)
				if strings.HasPrefix(path, filepath.Dir(f.cacheDir)) {
					fs.AvailableBytes = fs.TotalBytes
				}
				return fs, err
			}
		}, StatusSkipped, "write-set-outside-target"},
		"cache locked": {func(tw *testWorld, f *fakePnpm) {
			mustWrite(tw.t, filepath.Join(f.cacheDir, "metadata", "x"), "x", 0o644)
			lock, err := LockDir(f.cacheDir)
			if err != nil {
				tw.t.Fatal(err)
			}
			tw.t.Cleanup(lock.Release)
		}, StatusSkipped, "locked"},
		"prune fails":     {func(tw *testWorld, f *fakePnpm) { f.fail["store prune"] = failure }, StatusFailed, ""},
		"path changes":    {func(tw *testWorld, f *fakePnpm) { f.after = "/elsewhere/v10" }, StatusFailed, ""},
		"undefined cache": {func(tw *testWorld, f *fakePnpm) { f.cacheDir = "undefined" }, StatusDeleted, ""},
	}
	for name, c := range cases {
		tw, fake := pnpmWorld(t)
		c.mutate(tw, fake)
		report := tw.run(Options{Target: Target{Kind: TargetRatio, Ratio: 0.5}, Apply: true, Floor: 0.01})
		it := categoryByName(t, report, CategoryPnpm).Items[0]
		if it.Status != c.status || !strings.HasPrefix(it.Reason, c.reason) {
			t.Errorf("%s: item %#v", name, it)
		}
	}
}

// Without a store, with an age filter, with untrusted evidence or an
// unreadable store, the category is skipped before pnpm is considered.
func TestPnpmSkipsBeforeRunning(t *testing.T) {
	t.Parallel()
	tw, _ := pnpmWorld(t)
	tw.PnpmStores = nil
	if cat := categoryByName(t, tw.run(Options{}), CategoryPnpm); !strings.HasPrefix(cat.Reason, "unavailable") {
		t.Fatalf("no store = %#v", cat)
	}
	tw, _ = pnpmWorld(t)
	if cat := categoryByName(t, tw.run(Options{OlderThan: time.Hour}), CategoryPnpm); cat.Reason != "age-filter-unsupported" {
		t.Fatalf("aged = %#v", cat)
	}
	tw, _ = pnpmWorld(t)
	tw.evidence = Evidence{Unknown: "container"}
	if cat := categoryByName(t, tw.run(Options{}), CategoryPnpm); !strings.HasPrefix(cat.Reason, "unknown") {
		t.Fatalf("unknown = %#v", cat)
	}
	tw, _ = pnpmWorld(t)
	tw.Identity = func(string) (Identity, error) { return Identity{}, errors.New("statx") }
	if cat := categoryByName(t, tw.run(Options{}), CategoryPnpm); !strings.HasPrefix(cat.Reason, "partial-scan") {
		t.Fatalf("identity = %#v", cat)
	}
}
