//go:build e2e

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/periodic"
)

// TestE2EPeriodicPublishToAGitStoreCommitsOnlyWhatChanged runs the periodic
// publisher against the git provider and a temporary bare repository (no
// network, no real store): an idle machine adds no commit at each interval,
// a changed one adds exactly one, and the keepalive adds one after six hours.
func TestE2EPeriodicPublishToAGitStoreCommitsOnlyWhatChanged(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	deps := f.deps("alice", clock)
	deps.now = func() time.Time { return clock }
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state", Machine: "laptop", Publish: remotestate.PublishConfig{Interval: 5 * time.Minute, Unpushed: remotestate.RedactNone}}
	publisher := newPeriodicPublisher(deps, cfg, f.projectsRoot, nil)
	commits := func() int {
		count, err := strconv.Atoi(strings.TrimSpace(remoteGit(t, f.origin, "rev-list", "--count", "main")))
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	attempt := func(advance time.Duration) {
		clock = clock.Add(advance)
		publisher.Publish(t.Context(), nil)
	}
	base := commits() // the seed commit
	attempt(0)
	if got := commits(); got != base+1 {
		t.Fatalf("first publish: %d commits, want %d (status %+v)", got, base+1, publisher.Status())
	}
	stored := remoteGit(t, f.origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if !strings.Contains(stored, "os: ") || !strings.Contains(stored, "cpu_count:") || strings.Contains(stored, "agents:") || strings.Contains(stored, "metrics:") {
		t.Fatalf("stored snapshot should carry the hardware and neither agents nor metrics:\n%s", stored)
	}
	// The first publish created the clone of the store under the projects root,
	// which the next scan lists as a repository: one more commit, once.
	attempt(6 * time.Minute)
	if got := commits(); got != base+2 {
		t.Fatalf("the store's own clone appearing in the scan: %d commits, want %d", got, base+2)
	}
	base++
	// An idle machine: five more intervals, no commit.
	for range 5 {
		attempt(6 * time.Minute)
	}
	if got := commits(); got != base+1 || publisher.Status().Skipped != 5 {
		t.Fatalf("idle machine: %d commits, status %+v", got, publisher.Status())
	}
	// A change is one commit.
	if err := os.WriteFile(filepath.Join(f.projectsRoot, "acme", "widgets", "another.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt(6 * time.Minute)
	if got := commits(); got != base+2 {
		t.Fatalf("changed machine: %d commits, want %d", got, base+2)
	}
	// Idle for seven hours: nothing at five hours, the keepalive at seven.
	attempt(5 * time.Hour)
	if got := commits(); got != base+2 {
		t.Fatalf("idle for five hours: %d commits, want %d", got, base+2)
	}
	attempt(2 * time.Hour)
	if got := commits(); got != base+3 || publisher.Status().Diagnostic != periodic.DiagnosticNone {
		t.Fatalf("keepalive: %d commits, status %+v", got, publisher.Status())
	}
}
