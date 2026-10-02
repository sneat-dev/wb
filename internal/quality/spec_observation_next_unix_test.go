//go:build !windows

package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecMetadataNativeConfigFailureAfterMissingRoot(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores search permission")
	}
	root := t.TempDir()
	config := filepath.Join(root, "specscore.yaml")
	if err := os.WriteFile(config, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	observed := false
	entry := verifySpecWithMetadata(context.Background(), RunOptions{}, root, func(path string) (os.FileInfo, error) {
		if path != filepath.Join(root, "spec") {
			t.Fatalf("spec path=%q", path)
		}
		info, err := os.Stat(path)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("root observation=%v", err)
		}
		if chmodErr := os.Chmod(root, 0); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		return info, err
	}, func(path string) (os.FileInfo, error) {
		if path != config {
			t.Fatalf("config path=%q", path)
		}
		observed = true
		info, err := os.Lstat(path)
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("native config observation=%v", err)
		}
		return info, err
	})
	if !observed || entry.Status != StatusFailed || entry.Check != CheckSpec || !strings.Contains(entry.Detail, "inspect SpecScore config") {
		t.Fatalf("entry=%+v observed=%v", entry, observed)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config)
	if err != nil || string(raw) != "retained" {
		t.Fatalf("config=%q err=%v", raw, err)
	}
}
