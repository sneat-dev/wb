package repopath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Owner.Relative: both branches (legacy two-level vs host-qualified).
func TestRWI08OwnerRelative(t *testing.T) {
	t.Parallel()

	legacy := Owner{Name: "sneat-dev"}
	if got, want := legacy.Relative(), "sneat-dev"; got != want {
		t.Fatalf("Relative() = %q, want %q", got, want)
	}

	hosted := Owner{Host: "github.com", Name: "sneat-dev"}
	if got, want := hosted.Relative(), "github.com/sneat-dev"; got != want {
		t.Fatalf("Relative() = %q, want %q", got, want)
	}
}

// Owners: root missing (errors.Is ErrNotExist branch) and a non-directory
// entry inside a forge-host directory (organization.IsDir() false branch).
func TestRWI08OwnersRootMissing(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "does-not-exist")
	owners, unreadable := Owners(root)
	if owners != nil || unreadable != nil {
		t.Fatalf("Owners(missing) = %v, %v; want nil, nil", owners, unreadable)
	}
}

func TestRWI08OwnersSkipsNonDirOrganization(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	hostDir := filepath.Join(root, "github.com")
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A regular file masquerading as an org entry must be skipped via the
	// organization.IsDir() branch, not appended.
	if err := os.WriteFile(filepath.Join(hostDir, "notadir"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostDir, "sneat-dev"), 0o700); err != nil {
		t.Fatalf("MkdirAll org: %v", err)
	}

	owners, unreadable := Owners(root)
	if len(unreadable) != 0 {
		t.Fatalf("unreadable = %v, want empty", unreadable)
	}
	if len(owners) != 1 || owners[0].Name != "sneat-dev" || owners[0].Host != "github.com" {
		t.Fatalf("owners = %+v, want exactly one github.com/sneat-dev entry", owners)
	}
}

// Locate exercises every branch: missing root, a real I/O error, no match,
// exactly one match, and an ambiguous multi-host match.
func TestRWI08LocateRootMissing(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "missing")
	got, err := Locate(root, "acme", "widgets")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != (Address{Org: "acme", Repo: "widgets"}) {
		t.Fatalf("Locate(missing root) = %+v, want legacy address", got)
	}
}

func TestRWI08LocateReadDirError(t *testing.T) {
	t.Parallel()

	// A regular file in place of root makes os.ReadDir fail with an error
	// that is not ErrNotExist (ENOTDIR), reaching the propagated-error path.
	root := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Locate(root, "acme", "widgets")
	if err == nil {
		t.Fatalf("Locate(file root) = nil error, want an error")
	}
}

func TestRWI08LocateNoMatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// A forge-host directory exists but has no cloned .git for this repo.
	if err := os.MkdirAll(filepath.Join(root, "github.com"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A non-forge, non-dir entry must be skipped by the range/continue guard.
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := Locate(root, "acme", "widgets")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != (Address{Org: "acme", Repo: "widgets"}) {
		t.Fatalf("Locate(no clone) = %+v, want legacy address", got)
	}
}

func TestRWI08LocateOneMatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	gitDir := filepath.Join(root, "github.com", "acme", "widgets", ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	got, err := Locate(root, "acme", "widgets")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	want := Address{Host: "github.com", Org: "acme", Repo: "widgets"}
	if got != want {
		t.Fatalf("Locate() = %+v, want %+v", got, want)
	}
}

func TestRWI08LocateAmbiguousMatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, host := range []string{"github.com", "gitlab.example.com"} {
		gitDir := filepath.Join(root, host, "acme", "widgets", ".git")
		if err := os.MkdirAll(gitDir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	_, err := Locate(root, "acme", "widgets")
	if err == nil {
		t.Fatalf("Locate(ambiguous) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("Locate error = %q, want mention of multiple hosts", err.Error())
	}
	if !strings.Contains(err.Error(), "github.com") || !strings.Contains(err.Error(), "gitlab.example.com") {
		t.Fatalf("Locate error = %q, want both hosts listed", err.Error())
	}
}

// ClonePathForURL: existing clone found, Locate error propagated, a real
// stat error other than ErrNotExist, and the no-clone-yet fallback.
func TestRWI08ClonePathForURLExistingClone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	gitDir := filepath.Join(root, "github.com", "acme", "widgets", ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	got, err := ClonePathForURL(root, "acme", "widgets", "https://github.com/acme/widgets.git")
	if err != nil {
		t.Fatalf("ClonePathForURL: %v", err)
	}
	want := filepath.Join(root, "github.com", "acme", "widgets")
	if got != want {
		t.Fatalf("ClonePathForURL() = %q, want %q", got, want)
	}
}

func TestRWI08ClonePathForURLLocateError(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ClonePathForURL(root, "acme", "widgets", "https://github.com/acme/widgets.git")
	if err == nil {
		t.Fatalf("ClonePathForURL(bad root) = nil error, want an error")
	}
}

func TestRWI08ClonePathForURLStatError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// No forge-host directory exists, so Locate returns the legacy address
	// whose resolved path is <root>/acme/widgets. Make that path a regular
	// file so stat(<path>/.git) fails with ENOTDIR rather than ErrNotExist.
	legacyPath := filepath.Join(root, "acme", "widgets")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ClonePathForURL(root, "acme", "widgets", "https://github.com/acme/widgets.git")
	if err == nil {
		t.Fatalf("ClonePathForURL(non-ENOENT stat error) = nil error, want an error")
	}
}

func TestRWI08ClonePathForURLFallback(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	got, err := ClonePathForURL(root, "acme", "widgets", "https://github.com/acme/widgets.git")
	if err != nil {
		t.Fatalf("ClonePathForURL: %v", err)
	}
	want := filepath.Join(root, "github.com", "acme", "widgets")
	if got != want {
		t.Fatalf("ClonePathForURL() = %q, want %q", got, want)
	}
}
