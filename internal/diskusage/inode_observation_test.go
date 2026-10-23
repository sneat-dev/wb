package diskusage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestMeasureUsesLatestInodeTypeAndNormalizesZeroLinks(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"changed type", "zero links"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "observed")
			if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
				t.Fatal(err)
			}
			usage, inodes, err := measureStat(context.Background(), root, func(path string, stat *unix.Stat_t) error {
				if scenario == "changed type" {
					if err := os.Remove(path); err != nil {
						return err
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						return err
					}
				}
				if err := unix.Lstat(path, stat); err != nil {
					return err
				}
				if scenario == "zero links" {
					stat.Nlink = 0
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "changed type" && (usage.Files != 0 || len(inodes) != 0) {
				t.Fatalf("nonregular latest inode counted: %+v", usage)
			}
			if scenario == "zero links" && (usage.Files != 1 || usage.ApparentBytes != 8 || len(inodes) != 1) {
				t.Fatalf("zero-link inode lost: %+v", usage)
			}
			for _, inode := range inodes {
				if inode.links != 1 {
					t.Fatalf("zero links not normalized: %+v", inode)
				}
			}
		})
	}
}
