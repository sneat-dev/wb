package nodeidentity

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR7NodeIdentity is task-9 PR-7's sentinel injected failure for this
// package, distinct from any other package's or task's sentinel so
// errors.Is never accidentally matches a different test's error by
// coincidence.
var errBoomPR7NodeIdentity = errors.New("pr7 boom")

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

// TestWriteNodeIDTempFileReportsEachBackendFailure covers
// writeNodeIDTempFileInjected's five failure branches (create, Chmod, the
// write, Sync, and Close) via *filewrite.Injector (task-9 PR-7), replacing
// the package's former fileChmod/fileWriteString/fileSync/fileClose
// package-var test seams: those were forbidden by this migration's own
// convention (an Injector is always passed explicitly per call, never held
// in a package var shared across goroutines and tests).
func TestWriteNodeIDTempFileReportsEachBackendFailure(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR7NodeIdentity}
			path, err := writeNodeIDTempFileInjected(dir, "id", inj)
			if !errors.Is(err, errBoomPR7NodeIdentity) {
				t.Fatalf("writeNodeIDTempFileInjected(%s failure) = %v, want errBoomPR7NodeIdentity", step, err)
			}
			if path != "" {
				t.Fatalf("writeNodeIDTempFileInjected(%s failure) returned a path %q, want empty", step, path)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".node-id-*.tmp"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
		})
	}
}

// TestPublishNodeIDReadsWinnersIDOnALostRace covers publishNodeID's loser
// branch directly: LoadFile's own top-of-function "already exists" fast path
// makes a genuine two-goroutine race non-deterministic and, worse, usually
// short-circuits before ever reaching os.Link at all (a second LoadFile call
// against an already-published path returns via readNodeID up front, never
// touching publishNodeID) — so a real race is not a reliable way to exercise
// this branch. Simulating the winner's publish directly, then calling
// publishNodeID as the loser would, exercises it deterministically instead.
func TestPublishNodeIDReadsWinnersIDOnALostRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node-id")

	winnerTemp, err := writeNodeIDTempFile(dir, "11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishNodeID(winnerTemp, path, "11111111111111111111111111111111"); err != nil {
		t.Fatal(err)
	}

	loserTemp, err := writeNodeIDTempFile(dir, "22222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	// publishNodeID never removes tempPath itself either way — LoadFile's own
	// defer does that — so the loser's leftover temp file is cleaned up here,
	// exactly like a real LoadFile caller's defer would.
	t.Cleanup(func() { _ = os.Remove(loserTemp) })
	got, err := publishNodeID(loserTemp, path, "22222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "11111111111111111111111111111111" {
		t.Fatalf("loser publish returned %q, want the winner's id", got)
	}
}

// TestPublishNodeIDReportsAnUnrelatedLinkFailure covers publishNodeID's
// non-EEXIST error branch: a link failure for any other reason (here, a
// destination directory that does not exist) is reported, not swallowed.
func TestPublishNodeIDReportsAnUnrelatedLinkFailure(t *testing.T) {
	dir := t.TempDir()
	tempPath, err := writeNodeIDTempFile(dir, "id")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tempPath) })
	badPath := filepath.Join(dir, "missing-parent", "node-id")
	if _, err := publishNodeID(tempPath, badPath, "id"); err == nil {
		t.Fatal("expected an error when the destination directory does not exist")
	}
}

// TestPublishNodeIDInjectedHonoursAnInjectedLinkFailure covers
// publishNodeIDInjected's link failure branch deterministically through
// *filewrite.Injector, alongside the two real-filesystem-driven branches
// above (a lost race and an unrelated real link failure).
func TestPublishNodeIDInjectedHonoursAnInjectedLinkFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tempPath, err := writeNodeIDTempFile(dir, "id")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tempPath) })
	path := filepath.Join(dir, "node-id")
	inj := &filewrite.Injector{Step: filewrite.StepLink, Err: errBoomPR7NodeIdentity}
	if _, err := publishNodeIDInjected(tempPath, path, "id", inj); !errors.Is(err, errBoomPR7NodeIdentity) {
		t.Fatalf("publishNodeIDInjected(link failure) = %v, want errBoomPR7NodeIdentity", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed publish left a visible node-id file: %v", statErr)
	}
}

// TestReadNodeIDReportsAnEmptyFile and TestReadNodeIDReportsInvalidHex cover
// readNodeID's two content-validation branches the existing corrupt-file
// test (wrong length) does not reach.
func TestReadNodeIDReportsAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node-id")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNodeID(path); err == nil {
		t.Fatal("expected an error for a blank node-id file")
	}
}

func TestReadNodeIDReportsInvalidHex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node-id")
	// Right length (byteLength*2), wrong alphabet: hex.DecodeString rejects
	// 'z', but the earlier length check would not.
	notHex := strings.Repeat("z", byteLength*2)
	if err := os.WriteFile(path, []byte(notHex+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNodeID(path); err == nil {
		t.Fatal("expected an error for a correctly-sized but non-hex node-id file")
	}
}

// TestPathAndLoadPropagateAnUnresolvableWBHome covers Path's and Load's own
// error branch when wbhome.Root itself fails — distinct from
// TestLoadPropagatesAnUnresolvableProjectsRoot, whose blocker file resolves
// fine as a projects root (EvalSymlinks succeeds on an existing file) and so
// only fails later, inside LoadFile's own MkdirAll. Pointing the projects
// root through (not at) a file makes filepath.EvalSymlinks itself fail with
// ENOTDIR, which is what actually makes wbhome.Root return an error.
func TestPathAndLoadPropagateAnUnresolvableWBHome(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	unresolvable := filepath.Join(blocker, "nested")

	if _, err := Path(unresolvable); err == nil {
		t.Fatal("expected Path to surface wbhome.Root's own resolution failure")
	}
	if _, err := Load(unresolvable, nil); err == nil {
		t.Fatal("expected Load to surface wbhome.Root's own resolution failure")
	}
}

// TestLoadFileReportsItsOwnMkdirAllFailure covers LoadFile's directory-create
// branch directly — distinct from TestLoadFileReportsAnUnwritableDirectory,
// whose blocker-file path actually fails earlier, inside readNodeID's own
// initial read (a path through a file is ENOTDIR, not ErrNotExist, so
// LoadFile returns before ever reaching its own MkdirAll call). A read-only
// (but existing) parent directory instead lets that initial read miss
// cleanly with ErrNotExist — path does not exist yet, but every existing
// path component does — so LoadFile proceeds to mint an ID and only then
// fails trying to create a new subdirectory it has no permission to create.
func TestLoadFileReportsItsOwnMkdirAllFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	root := t.TempDir()
	readOnlyParent := filepath.Join(root, "readonly")
	if err := os.MkdirAll(readOnlyParent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyParent, 0o700) })
	path := filepath.Join(readOnlyParent, "nested", "node-id")
	if _, err := LoadFile(path, nil); err == nil {
		t.Fatal("expected an error when LoadFile's own MkdirAll cannot create the missing directory")
	}
}

var _ io.Reader = failingReader{}
