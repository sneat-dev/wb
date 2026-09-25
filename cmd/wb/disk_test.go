package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestDiskReportsAnEmptyFleetWithNoFindings(t *testing.T) {
	root := t.TempDir()
	// disk.Collect falls back to the real $HOME/.wb when WBHome is unset, and
	// wb disk never wires one from --projects-root; isolate HOME too, or this
	// walks the host's own live fleet state instead of the fixture.
	t.Setenv("HOME", t.TempDir())
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newDiskCmd(&invocation{projectsRoot: root}) }, "--skip-sizes")
	if err != nil {
		t.Fatalf("wb disk on an empty projects root: %v", err)
	}
	if !strings.Contains(stdout, "RECLAIM") && !strings.Contains(stdout, "APPARENT") {
		t.Fatalf("wb disk text report missing its own categories: %q", stdout)
	}
}

func TestDiskRejectsAnUnsupportedFormatAndOutOfRangeMinimum(t *testing.T) {
	root := t.TempDir()
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDiskCmd(&invocation{projectsRoot: root}) }, "--format", "toml"); err == nil || !strings.Contains(err.Error(), "unsupported --format") {
		t.Fatalf("wb disk --format toml = %v, want an unsupported-format refusal", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDiskCmd(&invocation{projectsRoot: root}) }, "--minimum-available", "2"); err == nil || !strings.Contains(err.Error(), "--minimum-available") {
		t.Fatalf("wb disk --minimum-available 2 = %v, want a range refusal", err)
	}
}

func TestDiskJSONReportsAnEmptyFleet(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newDiskCmd(&invocation{projectsRoot: root}) }, "--format", "json", "--skip-sizes")
	if err != nil {
		t.Fatalf("wb disk --format json on an empty projects root: %v", err)
	}
	if !strings.Contains(stdout, "\"") {
		t.Fatalf("wb disk json report looks empty: %q", stdout)
	}
}
