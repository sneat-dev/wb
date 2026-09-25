package envguard

import (
	"os"
	"path/filepath"
	"testing"
)

// tailCovWriteFile writes a fixture file inside dir, creating dir first.
func tailCovWriteFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestTailCovInspectSkipsBlankDirectoryArguments pins that a blank dir is not
// silently reinterpreted as "the current directory": it is skipped, so a
// go.work above the process's own cwd is never reported on its behalf.
func TestTailCovInspectSkipsBlankDirectoryArguments(t *testing.T) {
	outer := t.TempDir()
	tailCovWriteFile(t, outer, "go.work", "go 1.27\n")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inner) // a blank dir would resolve here and find outer/go.work

	root := t.TempDir()
	want := tailCovWriteFile(t, root, "go.work", "go 1.27\n")

	inputs := Inspect([]string{"PATH=/bin"}, "", "   ", root)
	if len(inputs.GoWorkAncestors) != 1 || inputs.GoWorkAncestors[0] != want {
		t.Fatalf("GoWorkAncestors = %v, want exactly [%s]", inputs.GoWorkAncestors, want)
	}
}

// TestTailCovInspectIgnoresEntriesWithoutEqualsSign pins that a bare name in
// the environment slice is not an environment variable: in particular a
// would-be WB_AGENT_* name with no '=' must not be reported as ambient agent
// identity.
func TestTailCovInspectIgnoresEntriesWithoutEqualsSign(t *testing.T) {
	t.Parallel()
	root := t.TempDir() // no go.work in this ancestry

	inputs := Inspect([]string{"PATH=/bin", "WB_AGENT_ID", "GOWORK=/ambient/go.work"}, root)
	if len(inputs.AgentVars) != 0 {
		t.Fatalf("AgentVars = %v, want none: an entry without '=' is not a variable", inputs.AgentVars)
	}
	if inputs.GOWORK != "/ambient/go.work" {
		t.Fatalf("GOWORK = %q", inputs.GOWORK)
	}
	if inputs.Empty() {
		t.Fatal("Empty() = true with GOWORK recorded")
	}
}

// TestTailCovSanitizeEnvDropsEntriesWithoutEqualsSign pins the same rule for
// the subprocess environment: an entry that is not NAME=VALUE cannot be an
// override and is dropped rather than passed through as a name-only entry.
func TestTailCovSanitizeEnvDropsEntriesWithoutEqualsSign(t *testing.T) {
	t.Parallel()
	result := SanitizeEnv([]string{"PATH=/bin", "BARE_WORD", "LONELY="})
	want := []string{"PATH=/bin", "LONELY="}
	if len(result) != len(want) {
		t.Fatalf("result = %v, want %v", result, want)
	}
	for index, entry := range want {
		if result[index] != entry {
			t.Fatalf("result = %v, want %v", result, want)
		}
	}
}

// TestTailCovUnresolvableWorkingDirectoryFailsClosed covers the two places
// that resolve a possibly-relative directory with filepath.Abs. When the
// process's working directory no longer exists, neither can resolve one: the
// go.work walk must report nothing and GoEnvOverrides must still isolate the
// check with GOWORK=off rather than erroring or skipping isolation.
func TestTailCovUnresolvableWorkingDirectoryFailsClosed(t *testing.T) {
	removed := filepath.Join(t.TempDir(), "removed")
	if err := os.MkdirAll(removed, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(removed)
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Abs("relative"); err == nil {
		t.Skip("this platform still resolves a relative path after its working directory was removed")
	}

	inputs := Inspect([]string{"PATH=/bin"}, "relative-dir")
	if !inputs.Empty() {
		t.Fatalf("Inspect = %+v, want no ambient inputs when the directory cannot be resolved", inputs)
	}

	overrides := GoEnvOverrides("relative-dir")
	if len(overrides) != 1 || overrides[0] != "GOWORK=off" {
		t.Fatalf("GoEnvOverrides = %v, want [GOWORK=off]", overrides)
	}
}

// TestTailCovTracksOwnGoWorkFalseForMissingRepository covers a repository
// root that does not exist at all: nothing can be tracked, and that is
// reported as "not its own" with no error.
func TestTailCovTracksOwnGoWorkFalseForMissingRepository(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "never-created")

	tracked, err := TracksOwnGoWork(missing)
	if err != nil {
		t.Fatalf("TracksOwnGoWork(%s) error = %v, want nil", missing, err)
	}
	if tracked {
		t.Fatalf("TracksOwnGoWork(%s) = true, want false", missing)
	}
}

// TestTailCovTracksOwnGoWorkReportsUnresolvableRoot covers a root that exists
// in the path namespace but cannot be resolved to a real directory (here a
// path below a regular file). That is a real failure, not "no go.work", and
// must be surfaced.
func TestTailCovTracksOwnGoWorkReportsUnresolvableRoot(t *testing.T) {
	t.Parallel()
	file := tailCovWriteFile(t, t.TempDir(), "regular-file", "not a directory\n")
	root := filepath.Join(file, "child")

	tracked, err := TracksOwnGoWork(root)
	if err == nil {
		t.Fatalf("TracksOwnGoWork(%s) error = nil, want the unresolvable root reported", root)
	}
	if tracked {
		t.Fatalf("TracksOwnGoWork(%s) = true with an error", root)
	}
}

// TestTailCovTracksOwnGoWorkFalseForNonRegularGoWork pins the documented
// fail-closed rule: only a regular go.work file counts as the repository's
// own. A symlinked go.work (what WB's own link mechanism creates) and a
// directory named go.work are both treated as not-its-own.
func TestTailCovTracksOwnGoWorkFalseForNonRegularGoWork(t *testing.T) {
	t.Parallel()
	real := tailCovWriteFile(t, t.TempDir(), "real-go.work", "go 1.27\n")

	symlinked := t.TempDir()
	if err := os.Symlink(real, filepath.Join(symlinked, "go.work")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tracked, err := TracksOwnGoWork(symlinked)
	if err != nil {
		t.Fatalf("TracksOwnGoWork(symlinked go.work) error = %v", err)
	}
	if tracked {
		t.Fatal("a symlinked go.work was treated as the repository's own")
	}

	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "go.work"), 0o755); err != nil {
		t.Fatal(err)
	}
	tracked, err = TracksOwnGoWork(directory)
	if err != nil {
		t.Fatalf("TracksOwnGoWork(go.work directory) error = %v", err)
	}
	if tracked {
		t.Fatal("a go.work directory was treated as the repository's own")
	}
}
