package sessionmove

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmCovReadAdmittedRequestFileRejectsClosedAndNonRegularDescriptors(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)

	closed, err := os.Open(filepath.Join(smCovHandoffDir(fixture), requestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdmittedRequestFile(closed, fixture.request.HandoffID, fixture.digest); err == nil || !strings.Contains(err.Error(), "inspect admitted handoff request") {
		t.Fatalf("closed request descriptor error = %v", err)
	}

	directory, err := os.Open(smCovHandoffDir(fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if _, err := readAdmittedRequestFile(directory, fixture.request.HandoffID, fixture.digest); err == nil || !strings.Contains(err.Error(), "bounded immutable file") {
		t.Fatalf("directory request descriptor error = %v", err)
	}
}

func TestSmCovSecureDirectoriesRejectUnsafeExistingModes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	for _, name := range []string{"unsafe-message", successorAddressesDirName} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if directory, err := openSecureDirectoryAt(parent, "unsafe-message", false, "message test"); err == nil {
		_ = directory.Close()
		t.Fatal("openSecureDirectoryAt accepted mode 0755")
	}
	if directory, err := openSuccessorAddressesAt(parent, false); err == nil {
		_ = directory.Close()
		t.Fatal("openSuccessorAddressesAt accepted mode 0755")
	}
}

func TestSmCovReadImmutableRejectsMutableAndOversizedArtifacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authority, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()

	mutable := filepath.Join(dir, "mutable")
	if err := os.WriteFile(mutable, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(authority, "mutable", 32, "test artifact"); err == nil || !strings.Contains(err.Error(), "single-link bounded") {
		t.Fatalf("mutable artifact error = %v", err)
	}

	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(authority, "oversized", 4, "test artifact"); err == nil || !strings.Contains(err.Error(), "single-link bounded") {
		t.Fatalf("oversized artifact error = %v", err)
	}

	linked := filepath.Join(dir, "linked")
	if err := os.WriteFile(linked, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(linked, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(linked, filepath.Join(dir, "linked-copy")); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(authority, "linked", 32, "test artifact"); err == nil || !strings.Contains(err.Error(), "single-link bounded") {
		t.Fatalf("hard-linked artifact error = %v", err)
	}
}

func TestSmCovOpenEventsAtRejectsUnsafeExistingDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, eventsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, eventsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	handoff, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handoff.Close() }()
	if events, err := openEventsAt(handoff, false); err == nil {
		_ = events.Close()
		t.Fatal("openEventsAt accepted mode 0755")
	}
}

func TestSmCovOpenRootReportsCreateFailureBelowARegularFile(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(blocker, "handoffs"))
	if root, err := store.openRoot(true); err == nil {
		_ = root.Close()
		t.Fatal("openRoot created a directory below a regular file")
	} else if !strings.Contains(err.Error(), "create handoff store root") {
		t.Fatalf("openRoot create failure = %v", err)
	}
}
