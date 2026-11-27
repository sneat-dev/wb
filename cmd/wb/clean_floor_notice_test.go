package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// AC findings-point-to-the-verb: below the floor, wb clean names wb disk
// prune; at or above it, or when anything cannot be measured, it says
// nothing.
func TestCleanFloorNoticeNamesDiskPrune(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	resolve := func(string) (wbhome.Resolution, error) { return wbhome.Resolution{Root: "/p"}, nil }
	low := func(string) (disk.Filesystem, error) { return disk.Filesystem{TotalBytes: 100, AvailableBytes: 5}, nil }
	if notice := diskFloorNotice("", config, resolve, low); !strings.Contains(notice, "wb disk prune") || !strings.Contains(notice, "10% free-space floor") {
		t.Fatalf("notice = %q", notice)
	}
	ample := func(string) (disk.Filesystem, error) {
		return disk.Filesystem{TotalBytes: 100, AvailableBytes: 50}, nil
	}
	failing := func(string) (disk.Filesystem, error) { return disk.Filesystem{}, errors.New("statfs") }
	for name, notice := range map[string]string{
		"ample":   diskFloorNotice("", config, resolve, ample),
		"probe":   diskFloorNotice("", config, resolve, failing),
		"unknown": diskFloorNotice("", config, resolve, func(string) (disk.Filesystem, error) { return disk.Filesystem{}, nil }),
		"resolve": diskFloorNotice("", config, func(string) (wbhome.Resolution, error) { return wbhome.Resolution{}, errors.New("x") }, low),
	} {
		if notice != "" {
			t.Errorf("%s: notice %q", name, notice)
		}
	}
	if err := os.WriteFile(config, []byte("disk:\n  minimum_available: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if notice := diskFloorNotice("", config, resolve, low); notice != "" {
		t.Fatalf("invalid config notice = %q", notice)
	}
}
