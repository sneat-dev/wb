package sessionlaunch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR7 is task-9 PR-7's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR7 = errors.New("pr7 boom")

func openPR7LaunchDir(t *testing.T) *os.File {
	t.Helper()
	dir := t.TempDir()
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

// TestPublishLaunchArtifactInjectedHonoursInjectedFailures covers every
// write-path step. A dir-sync failure is asserted separately below: by the
// time directory.Sync runs, the link has already published the artifact
// (the original code's own "linked = true" happens strictly before the
// final directory.Sync call), so the artifact legitimately exists on disk
// despite the reported error -- that is this call shape's pre-existing
// behaviour, unchanged by this migration. Every other injected step fails
// before the link, so no artifact and no leftover temp file exist
// afterward.
func TestPublishLaunchArtifactInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepLink,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			directory := openPR7LaunchDir(t)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR7}
			published, err := publishLaunchArtifactInjected(directory, "artifact.json", []byte("hi"), inj)
			if !errors.Is(err, errBoomPR7) {
				t.Fatalf("publishLaunchArtifactInjected(%s failure) = (%v, %v), want errBoomPR7", step, published, err)
			}
			if published {
				t.Fatalf("publishLaunchArtifactInjected(%s failure) reported published=true", step)
			}
			matches, globErr := filepath.Glob(filepath.Join(directory.Name(), ".pending-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			if _, statErr := os.Stat(filepath.Join(directory.Name(), "artifact.json")); !os.IsNotExist(statErr) {
				t.Fatalf("failed publish left a visible artifact: %v", statErr)
			}
		})
	}
}

func TestPublishLaunchArtifactInjectedReportsAnInjectedDirSyncFailureAfterAlreadyPublishing(t *testing.T) {
	t.Parallel()
	directory := openPR7LaunchDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepDirSync, Err: errBoomPR7}
	published, err := publishLaunchArtifactInjected(directory, "artifact.json", []byte("hi"), inj)
	if !errors.Is(err, errBoomPR7) {
		t.Fatalf("publishLaunchArtifactInjected(dir sync failure) = (%v, %v), want errBoomPR7", published, err)
	}
	if published {
		t.Fatal("publishLaunchArtifactInjected(dir sync failure) reported published=true")
	}
	if _, statErr := os.Stat(filepath.Join(directory.Name(), "artifact.json")); statErr != nil {
		t.Fatalf("expected the artifact to already be published on disk despite the dir-sync error: %v", statErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(directory.Name(), ".pending-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp file(s) after dir-sync failure: %v", matches)
	}
}

// TestPublishLaunchArtifactInjectedTranslatesAShortWriteToIoErrShortWrite
// covers review-t9-pr6's byte-identical-error-text lesson applied here: the
// original code compared temporary.Write's own (written, err) pair against
// len(raw) and returned the stdlib io.ErrShortWrite sentinel on a short
// write with no error. filewrite.Write instead returns its own
// *filewrite.ShortWriteError on a short write, so
// publishLaunchArtifactInjected translates that back with errors.As to keep
// every caller's io.ErrShortWrite check working unchanged.
func TestPublishLaunchArtifactInjectedTranslatesAShortWriteToIoErrShortWrite(t *testing.T) {
	t.Parallel()
	directory := openPR7LaunchDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepShortWrite, ShortBytes: 1}
	_, err := publishLaunchArtifactInjected(directory, "artifact.json", []byte("hi"), inj)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("publishLaunchArtifactInjected(short write) = %v, want io.ErrShortWrite", err)
	}
	var short *filewrite.ShortWriteError
	if errors.As(err, &short) {
		t.Fatalf("publishLaunchArtifactInjected leaked a raw *filewrite.ShortWriteError: %v", err)
	}
}

func TestPublishLaunchArtifactInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	directory := openPR7LaunchDir(t)
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(directory.Name(), ".pending-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary file before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	published, err := publishLaunchArtifactInjected(directory, "artifact.json", []byte("hi"), inj)
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("expected publishLaunchArtifactInjected to report published=true")
	}
	if !hookRan {
		t.Fatal("Hook did not run before the real chmod")
	}
	info, err := os.Stat(filepath.Join(directory.Name(), "artifact.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published artifact mode = %o, want 0600", perm)
	}
}

func TestPublishLaunchArtifactInjectedReportsFalseWhenAlreadyPublished(t *testing.T) {
	t.Parallel()
	directory := openPR7LaunchDir(t)
	first, err := publishLaunchArtifactInjected(directory, "artifact.json", []byte("hi"), nil)
	if err != nil || !first {
		t.Fatalf("first publish = (%v, %v), want (true, nil)", first, err)
	}
	second, err := publishLaunchArtifactInjected(directory, "artifact.json", []byte("bye"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Fatal("expected the second publish to report published=false")
	}
}
