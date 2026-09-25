package migrate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR8 is task-9 PR-8's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally matches
// a different test's error by coincidence.
var errBoomPR8 = errors.New("pr8 boom")

func hashHexPR8(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum)
}

func TestApplyInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepChmod,
		filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "file.go")
			original := []byte("package a\n")
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatal(err)
			}
			plan := Plan{Changes: []FileChange{{
				Path: path, OriginalSHA256: hashHexPR8(original), Updated: []byte("package a\n// updated\n"),
			}}}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			if err := applyInjected(plan, inj); !errors.Is(err, errBoomPR8) {
				t.Fatalf("applyInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".wb-migrate-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != string(original) {
				t.Fatalf("failed apply modified the original file: %q", contents)
			}
		})
	}
}

func TestApplyInjectedPreservesTheOriginalFileMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.go")
	original := []byte("package a\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Changes: []FileChange{{
		Path: path, OriginalSHA256: hashHexPR8(original), Updated: []byte("package a\n// updated\n"),
	}}}
	if err := applyInjected(plan, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("applied file mode = %o, want 0640 (preserved from the original)", perm)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "package a\n// updated\n" {
		t.Fatalf("applied contents = %q", contents)
	}
}
