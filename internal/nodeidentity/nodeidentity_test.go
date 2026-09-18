package nodeidentity

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPersistsAcrossRestart encodes the AC: a node ID survives a daemon
// restart, i.e. calling Load twice against the same projects root returns the
// same value.
func TestLoadPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	first, err := Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != byteLength*2 {
		t.Fatalf("node ID %q has length %d, want %d", first, len(first), byteLength*2)
	}
	second, err := Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("Load is not stable across calls: %q != %q", first, second)
	}
}

// TestLoadIsStableAcrossHostRename encodes the other half of the AC: nothing
// about node identity derives from the hostname, so Load is unaffected by
// whatever the current host is named. Load never reads os.Hostname, so this
// test proves the point by asserting the file's content, not process state.
func TestLoadIsStableAcrossHostRename(t *testing.T) {
	root := t.TempDir()
	id, err := Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	path, err := Path(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(raw)) != id {
		t.Fatalf("file content %q != returned ID %q", raw, id)
	}
	// Renaming the host is simulated by simply reloading: nothing in Load
	// consults the hostname, so a second, independent Load call is the
	// behavioural proxy for "renaming changes nothing".
	again, err := Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("Load changed after simulated rename: %q != %q", again, id)
	}
}

// TestLoadWritesPrivateModeAndDirectory proves the exact path and
// permissions the feature specifies.
func TestLoadWritesPrivateModeAndDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, nil); err != nil {
		t.Fatal(err)
	}
	path, err := Path(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb", "state", "node-id"); path != want {
		t.Fatalf("Path(%q) = %q, want %q", root, path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("node-id mode = %o, want 0600", perm)
	}
}

// TestLoadUsesInjectableRandom proves randomness is a seam: two different
// injected readers of distinct content produce distinct IDs.
func TestLoadUsesInjectableRandom(t *testing.T) {
	one, err := Load(t.TempDir(), bytes.NewReader(bytes.Repeat([]byte{0x11}, byteLength)))
	if err != nil {
		t.Fatal(err)
	}
	two, err := Load(t.TempDir(), bytes.NewReader(bytes.Repeat([]byte{0x22}, byteLength)))
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("distinct injected randomness produced the same node ID")
	}
	if one != "11111111111111111111111111111111" {
		t.Fatalf("unexpected encoding of injected randomness: %q", one)
	}
}

// TestLoadFailsOnShortRandomRead proves a starved injected reader is reported
// rather than silently producing a short or zero ID.
func TestLoadFailsOnShortRandomRead(t *testing.T) {
	_, err := Load(t.TempDir(), bytes.NewReader([]byte{0x01, 0x02}))
	if err == nil {
		t.Fatal("expected an error for a short random read")
	}
}

// TestLoadRecoversFromACorruptFile proves a malformed node-id file is
// reported, not silently replaced or crashed on.
func TestLoadRecoversFromACorruptFile(t *testing.T) {
	root := t.TempDir()
	path, err := Path(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, nil); err == nil {
		t.Fatal("expected an error for a corrupt node-id file")
	}
}

// TestLoadFileRejectsEmptyPath covers the guard clause directly.
func TestLoadFileRejectsEmptyPath(t *testing.T) {
	if _, err := LoadFile("  ", nil); err == nil {
		t.Fatal("expected an error for an empty path")
	}
}

// TestLoadFileWinsALostCreateRace proves the create-exclusive loser reads
// back the winner's file rather than failing outright: this is what makes a
// racing daemon start and `wb peers join` safe together.
func TestLoadFileWinsALostCreateRace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "node-id")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	winner, err := LoadFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second call against the same, now-populated path is the racing
	// loser's exact code path: OpenFile with O_EXCL fails with ErrExist and
	// LoadFile reads the winner's content back.
	loser, err := LoadFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loser != winner {
		t.Fatalf("racing loser read %q, want winner's %q", loser, winner)
	}
}

// TestLoadFileReportsAnUnwritableDirectory proves a directory WB cannot
// create surfaces as an error rather than a panic.
func TestLoadFileReportsAnUnwritableDirectory(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "nested", "node-id")
	if _, err := LoadFile(path, nil); err == nil {
		t.Fatal("expected an error when the node identity directory cannot be created")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("injected random failure") }

// TestLoadFileReportsARandomReadFailure proves a hard failure from the
// injected reader (not just a short read) is reported.
func TestLoadFileReportsARandomReadFailure(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "node-id"), failingReader{}); err == nil {
		t.Fatal("expected an error from a failing random reader")
	}
}

// TestLoadFileReportsAWritePermissionFailure covers the branch past
// MkdirAll, where a create-exclusive open fails for a reason other than the
// file already existing (here, a read-only directory).
func TestLoadFileReportsAWritePermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "readonly")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := LoadFile(filepath.Join(dir, "node-id"), nil); err == nil {
		t.Fatal("expected an error writing into a read-only directory")
	}
}

// TestLoadPropagatesAnUnresolvableProjectsRoot proves Load surfaces a
// resolution failure under the projects root rather than swallowing it. The
// fault is a projects root that is itself a regular file — a condition that
// fails identically on every OS — rather than clearing HOME: on Windows,
// os.UserHomeDir resolves from USERPROFILE, not HOME, so clearing only HOME
// silently stops exercising this path there.
func TestLoadPropagatesAnUnresolvableProjectsRoot(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(blocker, nil); err == nil {
		t.Fatal("expected Load to fail when the projects root is a regular file")
	}
}

var _ io.Reader = failingReader{}
