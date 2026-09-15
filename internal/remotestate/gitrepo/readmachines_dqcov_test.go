package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDQCovStatusReportsMachinesReadError proves Status surfaces a filesystem
// failure while walking machines/ instead of returning a partial snapshot.
func TestDQCovStatusReportsMachinesReadError(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	dqCovChmodDir(t, filepath.Join(p.opts.ClonePath, "machines"), 0o000)

	if _, err := p.Status(context.Background()); err == nil {
		t.Fatal("Status with an unreadable machines directory succeeded, want an error")
	}
	if _, err := p.List(context.Background()); err == nil {
		t.Fatal("List with an unreadable machines directory succeeded, want an error")
	}
}

// TestDQCovStatusReportsClaimsReadError proves Status surfaces a filesystem
// failure while reading claims/ rather than returning machines only.
func TestDQCovStatusReportsClaimsReadError(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, "claims"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Status(context.Background()); err == nil {
		t.Fatal("Status with a file where claims/ belongs succeeded, want an error")
	}
}

// TestDQCovListReportsUnreadableSnapshotFile proves an existing snapshot path
// that cannot be read becomes an error entry rather than a silent skip.
func TestDQCovListReportsUnreadableSnapshotFile(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(p.opts.ClonePath, "machines", "carol", "desk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dqCovSymlink(t, filepath.Join(t.TempDir(), "gone-snapshot.yaml"), filepath.Join(dir, "snapshot.yaml"))

	entries, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, entry := range entries {
		if entry.Snapshot.Login == "carol" && entry.Snapshot.Machine == "desk" {
			found = true
			if entry.Error == "" {
				t.Fatalf("entry = %+v, want a read error", entry)
			}
		}
	}
	if !found {
		t.Fatalf("entries = %+v, want carol/desk with a read error", entries)
	}
}

// TestDQCovReadMachinesReportsMissingRootAsEmpty proves an absent machines/
// directory is an empty projection, not an error.
func TestDQCovReadMachinesReportsMissingRootAsEmpty(t *testing.T) {
	p := New(Options{ClonePath: filepath.Join(t.TempDir(), "p", "wb-state"), CloneURL: "file:///nowhere"})
	if err := os.MkdirAll(p.opts.ClonePath, 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := p.readMachines()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none", entries)
	}
}
