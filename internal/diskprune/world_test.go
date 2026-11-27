//go:build linux

package diskprune

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/disk"
)

// testWorld is a World over real temporary directories, with a fake clock,
// process table, filesystem and admission queue.
type testWorld struct {
	*World
	t         *testing.T
	tmp       string
	mu        sync.Mutex
	total     int64
	available int64
	// returned is the share of unlinked bytes that comes back as free space;
	// 1 normally, 0 to simulate open deleted files.
	returned  float64
	processes []Process
	evidence  Evidence
	admitted  int
	removed   []string
}

func newTestWorld(t *testing.T) *testWorld {
	t.Helper()
	tw := &testWorld{t: t, tmp: t.TempDir(), total: 100 << 30, available: 1 << 30, returned: 1}
	home := t.TempDir()
	now := time.Now().Add(5 * 24 * time.Hour)
	tw.World = &World{
		Now: func() time.Time { return now }, UID: os.Getuid(), Home: home,
		ProjectsRoot: filepath.Join(home, "projects"), StateDir: filepath.Join(home, "projects", ".wb"),
		TempDirs:  []string{tw.tmp},
		Processes: func() Table { return Table{Processes: tw.processes, Evidence: tw.evidence} },
		Probe: func(path string) (disk.Filesystem, error) {
			tw.mu.Lock()
			defer tw.mu.Unlock()
			return disk.Filesystem{Path: path, TotalBytes: tw.total, AvailableBytes: tw.available}, nil
		},
		Identity:           StatIdentity,
		BindOfSubdirectory: func(string) (bool, error) { return false, nil },
		RemoveFile:         RemoveFileAt,
		Scan:               scanTree,
		LockDirs:           LockDirs,
		Admit: func(context.Context) (string, time.Duration, func()) {
			tw.admitted++
			return "admitted", 3 * time.Millisecond, func() {}
		},
		RemoveTree: func(parent, name string, id Identity) (TreeOutcome, error) {
			outcome, err := RemoveTree(parent, name, id)
			tw.mu.Lock()
			tw.available += int64(float64(outcome.Bytes) * tw.returned)
			tw.removed = append(tw.removed, name)
			tw.mu.Unlock()
			return outcome, err
		},
		categories: []category{scratchCategory{}},
	}
	return tw
}

// entry makes a scratch entry holding size bytes, aged by the fake clock.
func (tw *testWorld) entry(name string, size int) string {
	tw.t.Helper()
	path := filepath.Join(tw.tmp, name)
	mustWrite(tw.t, filepath.Join(path, "001", "data"), strings.Repeat("x", size), 0o644)
	return path
}

func (tw *testWorld) run(o Options) Report {
	tw.t.Helper()
	if o.Target.Kind == "" {
		o.Target = Target{Kind: TargetMax}
	}
	return Run(context.Background(), tw.World, o)
}

func itemsByName(report Report, category string) map[string]ItemReport {
	out := map[string]ItemReport{}
	for _, c := range report.Categories {
		if c.Name != category {
			continue
		}
		for _, it := range c.Items {
			out[filepath.Base(it.Path)] = it
		}
	}
	return out
}

func categoryByName(t *testing.T, report Report, name string) CategoryReport {
	t.Helper()
	for _, c := range report.Categories {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no category %s in %#v", name, report.Categories)
	return CategoryReport{}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// diskFilesystem shortens the probe's type in fixtures.
type diskFilesystem = disk.Filesystem

// cacheName turns a short fixture name such as "0a01-a" into a real Go
// build-cache entry name: a 64-digit hex digest in its own shard.
func cacheName(short string) string {
	digest, suffix := short[:len(short)-2], short[len(short)-2:]
	return digest + strings.Repeat("0", 64-len(digest)) + suffix
}
