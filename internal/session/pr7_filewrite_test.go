package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR7 is task-9 PR-7's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR7 = errors.New("pr7 boom")

// TestMarkParkedInjectedHonoursInjectedFailures covers every write-path step
// of markParkedInjected. This site creates the marker directly at its final
// path (filewrite.CreateExclusivePath, no temp-name-then-rename indirection),
// so only a create failure guarantees no marker exists afterward; a write,
// sync or close failure can leave a marker already flushed to disk with its
// content already committed -- that is this call shape's pre-existing
// behaviour (unchanged by this migration), not a leftover temp file.
func TestMarkParkedInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "pr7-park-" + string(step), Runtime: "codex"})
			if err != nil {
				t.Fatal(err)
			}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR7}
			if _, err := markParkedInjected(dir, registered.PID, "pr7-parked", inj); !errors.Is(err, errBoomPR7) {
				t.Fatalf("markParkedInjected(%s failure) = %v, want errBoomPR7", step, err)
			}
			if step == filewrite.StepOpenOrCreate {
				if _, found := readParkedMarker(dir, registered.WBSessionID); found {
					t.Fatalf("failed create published a visible marker")
				}
			}
		})
	}
}

// TestMarkResumedInjectedHonoursInjectedFailures mirrors the park test above
// for markResumedInjected; see its doc comment for why only the create
// failure asserts no marker exists.
func TestMarkResumedInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepSync, filewrite.StepClose,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "pr7-resume-" + string(step), Runtime: "codex"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := markParkedInjected(dir, registered.PID, "pr7-parked", nil); err != nil {
				t.Fatal(err)
			}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR7}
			if _, err := markResumedInjected(dir, registered.PID, "pr7-parked", "wbs-pr7-successor", inj); !errors.Is(err, errBoomPR7) {
				t.Fatalf("markResumedInjected(%s failure) = %v, want errBoomPR7", step, err)
			}
			if step == filewrite.StepOpenOrCreate {
				if _, found := readResumedMarker(dir, registered.WBSessionID); found {
					t.Fatalf("failed create published a visible marker")
				}
			}
		})
	}
}

// TestMarkParkedInjectedPublishesAt0600 covers review-756's B2 lesson: the
// no-replace marker file is created directly at its final mode via
// filewrite.CreateExclusivePath (O_WRONLY|O_CREATE|O_EXCL, 0o600), so a
// deleted chmod call is not this site's failure mode -- there is no
// separate chmod step to lose in the first place. This is a direct
// published-mode assertion, not a missing-chmod detector, because none is
// needed here.
func TestMarkParkedInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "pr7-mode", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := markParkedInjected(dir, registered.PID, "pr7-parked", nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "lifecycle", registered.WBSessionID+".parked.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("published parked marker mode = %v, want 0600", info.Mode().Perm())
	}
}
