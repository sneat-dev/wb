//go:build e2e

package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// TestE2ECockpitFleetSnapshotThroughTheDaemonsWiringReadsOtherMachinesWithoutNetworkOrWrites
// builds the snapshotter from the daemon's own options for a remote section
// whose local state clone exists and whose origin could not be reached: it reads
// the other machine from the clone and changes nothing under it.
func TestE2ECockpitFleetSnapshotThroughTheDaemonsWiringReadsOtherMachinesWithoutNetworkOrWrites(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "github.com", "acme", "wb-state")
	published := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	data, err := remotestate.Encode(remotestate.Snapshot{SchemaVersion: remotestate.SchemaVersion, Login: "alice", Machine: "desk", PublishedAt: published, WBVersion: "v0.9.0"})
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(clone, "machines", "alice", "desk", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"remote", "add", "origin", "git@github.com:acme/wb-state.git"}, {"add", "."}, {"commit", "-m", "snapshot"}} {
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir = clone
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	states := func() map[string]string {
		found := map[string]string{}
		_ = filepath.WalkDir(clone, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr == nil {
				info, _ := entry.Info()
				found[path] = info.ModTime().String() + info.Mode().String() + strconv.FormatInt(info.Size(), 10)
			}
			return nil
		})
		return found
	}
	before := states()
	configPath := cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-1\n")()
	options := cockpitFleetOptions(root, t.TempDir(), configPath, wbconfig.DefaultCockpitConfig(), io.Discard, func() (string, error) { return "host", nil })
	snapshotter := cockpitfleet.New(options)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	machines := func() []cockpitfleet.Machine {
		body, _ := snapshotter.Body()
		var document cockpitfleet.Document
		if err := json.Unmarshal(body, &document); err != nil {
			t.Fatal(err)
		}
		return document.Machines
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(machines()) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the other machine was not read: %+v", machines())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if desk := machines()[0]; desk.Machine != "desk" || desk.Route != cockpitfleet.RouteCached || !desk.ObservedAt.Equal(published) {
		t.Errorf("machines = %+v", machines())
	}
	after := states()
	for path, state := range before {
		if after[path] != state {
			t.Errorf("%s changed during a snapshot", path)
		}
	}
	if len(after) != len(before) {
		t.Errorf("%d files appeared under the state clone", len(after)-len(before))
	}
}
