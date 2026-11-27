package main

import (
	"errors"
	"github.com/sneat-dev/wb/internal/diskprune"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// wb disk reads the newest receipt from the projects root's state directory.
func TestDiskReceiptFindingReadsTheStateDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if finding := diskReceiptFinding(root); finding != "" {
		t.Fatalf("no receipt = %q", finding)
	}
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := diskprune.WriteReceipt(home, diskprune.Receipt{Time: time.Unix(5, 0), Trigger: "daemon", Outcome: diskprune.OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	if finding := diskReceiptFinding(root); !strings.Contains(finding, "ended failed") {
		t.Fatalf("finding = %q", finding)
	}
	if diskReceiptFinding("\x00bad") != "" {
		t.Fatal("an unresolvable root produced a finding")
	}
}
