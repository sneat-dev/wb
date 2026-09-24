package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tailCovWriteFile writes one fixture file, creating its parent directory.
func tailCovWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestTailCovSetRemoteHubRejectsUnparseableConfig covers the read/parse half of
// the failure contract: a config file that is not valid YAML must be reported
// as a parse failure naming the file, and must not be rewritten.
func TestTailCovSetRemoteHubRejectsUnparseableConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	// A duplicated mapping key is not a YAML error, so use a hard syntax
	// error instead: an unclosed flow sequence.
	original := "remote: [unclosed\n"
	tailCovWriteFile(t, path, original)

	err := SetRemoteHub(path, "https://hub.example", "machine", "/tmp/token")
	if err == nil {
		t.Fatal("SetRemoteHub accepted an unparseable config")
	}
	if !strings.Contains(err.Error(), "parse config") || !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v, want it to name the unparseable file", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != original {
		t.Fatalf("a rejected config was rewritten: %q", raw)
	}
}

// TestTailCovSetRemoteHubReportsReadFailure covers a read error that is not
// "does not exist": a config path that exists but cannot be read as a file
// (here, a directory) must surface as a read failure, not be treated as a
// fresh config and overwritten.
func TestTailCovSetRemoteHubReportsReadFailure(t *testing.T) {
	t.Parallel()
	path := t.TempDir() // an existing directory, not a config file

	err := SetRemoteHub(path, "https://hub.example", "machine", "/tmp/token")
	if err == nil {
		t.Fatal("SetRemoteHub accepted a config path that is a directory")
	}
	if !strings.Contains(err.Error(), "read config") || !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v, want a read failure naming %s", err, path)
	}
}

// TestTailCovSetRemoteHubRejectsNonMappingTopLevel pins the shape guard: a
// config whose top level is a scalar or sequence cannot carry a remote
// section, so it is refused rather than silently reshaped.
func TestTailCovSetRemoteHubRejectsNonMappingTopLevel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, content string }{
		{"scalar", "just-a-string\n"},
		{"sequence", "- parallel\n- remote\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "wb.yaml")
			tailCovWriteFile(t, path, tc.content)

			err := SetRemoteHub(path, "https://hub.example", "machine", "/tmp/token")
			if err == nil {
				t.Fatalf("SetRemoteHub accepted a %s top level", tc.name)
			}
			if !strings.Contains(err.Error(), "top level must be a mapping") {
				t.Fatalf("error = %v, want the top-level mapping refusal", err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(raw) != tc.content {
				t.Fatalf("a rejected config was rewritten: %q", raw)
			}
		})
	}
}

// TestTailCovSetRemoteHubRejectsNonMappingRemote pins the remote-section shape
// guard: `remote:` holding a scalar cannot be updated in place without
// destroying the operator's other remote settings, so it is refused.
func TestTailCovSetRemoteHubRejectsNonMappingRemote(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	tailCovWriteFile(t, path, "parallel: 3\nremote: git\n")

	err := SetRemoteHub(path, "https://hub.example", "machine", "/tmp/token")
	if err == nil {
		t.Fatal("SetRemoteHub accepted a non-mapping remote section")
	}
	if !strings.Contains(err.Error(), "remote must be a mapping") {
		t.Fatalf("error = %v, want the remote mapping refusal", err)
	}
}

// TestTailCovSetRemoteHubReportsUncreatableDirectory covers the directory
// creation failure: a config directory that cannot be created (here, an
// intermediate path component that is a dangling symlink) must not be
// mistaken for a missing config.
func TestTailCovSetRemoteHubReportsUncreatableDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "no-such-target"), dangling); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(dangling, "wb.yaml")

	err := SetRemoteHub(path, "https://hub.example", "machine", "/tmp/token")
	if err == nil {
		t.Fatal("SetRemoteHub created a config under an uncreatable directory")
	}
	if !strings.Contains(err.Error(), "create config directory") {
		t.Fatalf("error = %v, want a directory creation failure", err)
	}
}

// TestTailCovSetRemoteHubReportsUnstageableConfig covers the staging
// failure: when the config directory exists but cannot be written to, the
// update must fail loudly instead of leaving the config half-written (or
// silently reporting success).
func TestTailCovSetRemoteHubReportsUnstageableConfig(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission bits, so the staging write cannot fail")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "read-only")
	if err := os.Mkdir(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	// Restore write permission so t.TempDir's own cleanup can remove it.
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	path := filepath.Join(directory, "wb.yaml")

	err := SetRemoteHub(path, "https://hub.example", "machine", "/tmp/token")
	if err == nil {
		t.Fatal("SetRemoteHub reported success against an unwritable directory")
	}
	if !strings.Contains(err.Error(), "stage config") {
		t.Fatalf("error = %v, want a staging failure", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("a failed update left a config file behind")
	}
}
