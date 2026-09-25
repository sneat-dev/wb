package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/disk"
)

// cwDiskAmpleFreeSpace is a fixed, plentiful filesystem reading: the disk
// tests must not depend on the host's own free space at test time (review
// B4). A VM whose disk happens to be under the default 10% headroom floor
// would otherwise turn "an empty fleet has no findings" into a real finding
// no test fixture caused.
func cwDiskAmpleFreeSpace(path string) (disk.Filesystem, error) {
	const gigabyte = 1 << 30
	return disk.Filesystem{Path: path, TotalBytes: 100 * gigabyte, UsedBytes: 20 * gigabyte, AvailableBytes: 80 * gigabyte}, nil
}

func TestDiskReportsAnEmptyFleetWithNoFindings(t *testing.T) {
	root := t.TempDir()
	// disk.Collect falls back to the real $HOME/.wb when WBHome is unset, and
	// wb disk never wires one from --projects-root; isolate HOME too, or this
	// walks the host's own live fleet state instead of the fixture.
	t.Setenv("HOME", t.TempDir())
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command {
		return newDiskCmdWithProbe(&invocation{projectsRoot: root}, cwDiskAmpleFreeSpace)
	}, "--skip-sizes")
	if err != nil {
		t.Fatalf("wb disk on an empty projects root: %v", err)
	}
	if !strings.Contains(stdout, "RECLAIM") && !strings.Contains(stdout, "APPARENT") {
		t.Fatalf("wb disk text report missing its own categories: %q", stdout)
	}
	if strings.Contains(stdout, "only ") && strings.Contains(stdout, "available") {
		t.Fatalf("wb disk raised a low-headroom finding despite the injected ample-space probe: %q", stdout)
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
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command {
		return newDiskCmdWithProbe(&invocation{projectsRoot: root}, cwDiskAmpleFreeSpace)
	}, "--format", "json", "--skip-sizes")
	if err != nil {
		t.Fatalf("wb disk --format json on an empty projects root: %v", err)
	}
	if !strings.Contains(stdout, "\"") {
		t.Fatalf("wb disk json report looks empty: %q", stdout)
	}
	if strings.Contains(stdout, `"findings"`) {
		t.Fatalf("wb disk json raised a finding despite the injected ample-space probe: %q", stdout)
	}
}
