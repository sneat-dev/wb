package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetStatsCountsLocalRepositories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initTestRepository(t, filepath.Join(root, "acme", "clean"))
	initTestRepository(t, filepath.Join(root, "acme", "dirty"))
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runWB(t, "fleet", "stats", "--projects-root", root, "--format", "json")
	if result.exitCode != exitOK {
		t.Fatalf("exit code = %d; stderr: %s", result.exitCode, result.stderr)
	}
	var report fleetStatsReport
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, result.stdout)
	}
	if report.Inventory.Organizations != 1 || report.Inventory.Repositories != 2 {
		t.Errorf("inventory = %+v, want 1 org / 2 repos", report.Inventory)
	}
	if report.Git.Inspected != 2 || report.Git.Attention != 1 || report.Git.Clean != 1 || report.Git.Error != 0 {
		t.Errorf("git stats = %+v, want inspected=2 attention=1 clean=1 error=0", report.Git)
	}
}

func TestFleetStatusMatchesHistoricalFleetWorklist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initTestRepository(t, filepath.Join(root, "acme", "clean"))
	initTestRepository(t, filepath.Join(root, "acme", "dirty"))
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	legacy := decodeStatusIndex(t, runWB(t, "status", "--projects-root", root, "--format", "json"))
	fleet := decodeStatusIndex(t, runWB(t, "fleet", "status", "--projects-root", root, "--format", "json"))
	if fleet.HiddenClean != legacy.HiddenClean || len(fleet.Repositories) != len(legacy.Repositories) {
		t.Fatalf("fleet status diverged from wb status: fleet=%+v legacy=%+v", fleet, legacy)
	}
	if len(fleet.Repositories) != 1 || fleet.Repositories[0].Repository != "acme/dirty" {
		t.Errorf("fleet status = %+v, want only acme/dirty", fleet.Repositories)
	}
}

func TestFleetOverviewIncludesStatsAndAttention(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initTestRepository(t, filepath.Join(root, "acme", "clean"))
	initTestRepository(t, filepath.Join(root, "acme", "dirty"))
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"fleet", "--projects-root", root, "--format", "json"},
		{"fleet", "overview", "--projects-root", root, "--format", "json"},
	} {
		result := runWB(t, args...)
		if result.exitCode != exitOK {
			t.Fatalf("%v exit code = %d; stderr: %s", args, result.exitCode, result.stderr)
		}
		var report fleetOverviewReport
		if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
			t.Fatalf("%v decode: %v\n%s", args, err, result.stdout)
		}
		if report.Stats.Inventory.Repositories != 2 || report.Stats.Git.Attention != 1 {
			t.Errorf("%v stats = %+v", args, report.Stats)
		}
		if len(report.Status.Repositories) != 1 || report.Status.HiddenClean != 1 {
			t.Errorf("%v status = %+v", args, report.Status)
		}
	}

	markdown := runWB(t, "fleet", "--projects-root", root)
	if markdown.exitCode != exitOK {
		t.Fatalf("markdown overview failed: %s", markdown.stderr)
	}
	for _, want := range []string{"# WB fleet overview", "## Stats", "## Attention", "acme/dirty"} {
		if !strings.Contains(markdown.stdout, want) {
			t.Errorf("overview markdown missing %q:\n%s", want, markdown.stdout)
		}
	}
}

func TestRepoStatusReportsOneCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clean := initTestRepository(t, filepath.Join(root, "acme", "clean"))
	initTestRepository(t, filepath.Join(root, "acme", "dirty"))
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	single := decodeStatusIndex(t, runWB(t, "repo", "status", clean, "--format", "json"))
	if len(single.Repositories) != 1 || single.Repositories[0].Status != "clean" {
		t.Errorf("repo status = %+v, want one clean repository", single.Repositories)
	}

	rejected := runWB(t, "--filter", "acme", "repo", "status", clean)
	if rejected.exitCode != exitUsage {
		t.Fatalf("repo status accepted --filter; exit=%d stderr=%s", rejected.exitCode, rejected.stderr)
	}
	if !strings.Contains(rejected.stderr, "repo status") {
		t.Errorf("rejection should name repo status: %q", rejected.stderr)
	}
}
