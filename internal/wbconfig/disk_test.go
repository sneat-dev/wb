package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskConfigDefaultsWhenAbsent(t *testing.T) {
	t.Parallel()
	config, err := LoadDisk(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if config.MinimumAvailable != 0.10 {
		t.Fatalf("defaults = %#v", config)
	}
	for _, raw := range []string{"", "other: 1\n", "disk:\n"} {
		config, err := parseDisk([]byte(raw))
		if err != nil || config.MinimumAvailable != 0.10 {
			t.Fatalf("%q: %#v %v", raw, config, err)
		}
	}
}

// Every key is applied as written.
func TestDiskConfigReadsEveryKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	raw := "disk:\n  minimum_available: 0.2\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinimumAvailable != 0.2 {
		t.Fatalf("config = %#v", config)
	}
}

// An invalid value is an error naming the key, never a silent default.
func TestDiskConfigRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"disk: [\n":                                    "parse config",
		"disk:\n  unknown: 1\n":                        "parse disk section",
		"disk:\n  minimum_available: 0\n":              "disk.minimum_available",
		"disk:\n  minimum_available: 1\n":              "disk.minimum_available",
		"disk:\n  prune:\n    ignore_sum_files: [x]\n": "parse disk section",
	} {
		if _, err := parseDisk([]byte(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", raw, err, want)
		}
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "wb.yaml")
	if err := os.WriteFile(bad, []byte("disk:\n  minimum_available: maybe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDisk(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("load error %v does not name the file", err)
	}
	// Only a regular file is read: a directory, or a FIFO that would
	// block, is refused; a path through a file is a read error.
	if _, err := LoadDisk(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("reading a directory: %v", err)
	}
	if _, err := LoadDisk(filepath.Join(bad, "x")); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("reading through a file: %v", err)
	}
}
