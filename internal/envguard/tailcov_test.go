package envguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/execfile"
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

// tailCovWriteExecutable stages a fake external binary in its own directory
// and returns the directory, so a caller can prepend it to PATH.
func tailCovWriteExecutable(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := execfile.WriteExecutableFile(filepath.Join(dir, "git"), []byte(content), 0o755); err != nil {
		t.Fatalf("stage fake git: %v", err)
	}
	return dir
}

// tailCovRealGit resolves the real git binary so a fake one can forward to
// it, or skips when the machine has no git at all (the pre-existing tests in
// this package take the same route).
func tailCovRealGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	return git
}

// tailCovScratchRepo creates a git repository whose HEAD commits a go.work,
// i.e. a repository that owns multi-module workspace mode intrinsically.
func tailCovScratchRepo(t *testing.T) string {
	t.Helper()
	tailCovRealGit(t) // runGit shells out; skip cleanly on a machine without git
	repository := t.TempDir()
	tailCovWriteFile(t, repository, "go.work", "go 1.27\n")
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.email", "test@example.com")
	runGit(t, repository, "config", "user.name", "Test")
	runGit(t, repository, "add", "go.work")
	runGit(t, repository, "commit", "-m", "add go.work")
	return repository
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

// TestTailCovMissingGitBinaryIsReported covers the git-inspection failure
// that is not a git exit status: when git cannot be executed at all, the
// tracked-go.work probe reports that failure instead of guessing, and
// GoEnvOverrides still fails closed toward isolation.
func TestTailCovMissingGitBinaryIsReported(t *testing.T) {
	root := filepath.Dir(tailCovWriteFile(t, t.TempDir(), "go.work", "go 1.27\n"))
	t.Setenv("PATH", t.TempDir()) // an empty PATH: git cannot be looked up

	tracked, err := TracksOwnGoWork(root)
	if err == nil {
		t.Fatal("TracksOwnGoWork reported success with no runnable git")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want the launch failure rather than a git exit status", err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true with no runnable git")
	}

	overrides := GoEnvOverrides(root)
	if len(overrides) != 1 || overrides[0] != "GOWORK=off" {
		t.Fatalf("GoEnvOverrides = %v, want [GOWORK=off] on an inspection failure", overrides)
	}
}

// TestTailCovRelativeGitTopLevelIsReported covers `git rev-parse
// --show-toplevel` returning something that cannot be made relative to the
// go.work path. A toplevel that is not absolute is unusable, so the caller
// must see the failure rather than a silent "not tracked".
func TestTailCovRelativeGitTopLevelIsReported(t *testing.T) {
	root := filepath.Dir(tailCovWriteFile(t, t.TempDir(), "go.work", "go 1.27\n"))
	dir := tailCovWriteExecutable(t, "#!/bin/sh\necho not-an-absolute-toplevel\n")
	t.Setenv("PATH", dir)

	tracked, err := TracksOwnGoWork(root)
	if err == nil {
		t.Fatal("a relative git toplevel was silently accepted")
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true with a relative git toplevel")
	}
	if !strings.Contains(err.Error(), "not-an-absolute-toplevel") {
		t.Fatalf("error = %v, want it to name the unusable toplevel", err)
	}
}

// TestTailCovUnrunnableObjectProbeIsReported covers the git object probe
// failing to run at all (as opposed to exiting non-zero, which means "not in
// HEAD"). The failure is reported, never converted into a clean "not
// tracked".
func TestTailCovUnrunnableObjectProbeIsReported(t *testing.T) {
	realGit := tailCovRealGit(t)
	chmod, err := exec.LookPath("chmod")
	if err != nil {
		t.Skip("chmod not available")
	}
	repository := tailCovScratchRepo(t)

	// Forward rev-parse to the real git, then make this git unexecutable so
	// the following `git cat-file` cannot even be launched.
	script := fmt.Sprintf(`#!/bin/sh
for argument in "$@"; do
	if [ "$argument" = "rev-parse" ]; then
		%q -C "$2" rev-parse --show-toplevel || exit 1
		%q -x "$0"
		exit 0
	fi
done
exit 3
`, realGit, chmod)
	t.Setenv("PATH", tailCovWriteExecutable(t, script))

	tracked, err := TracksOwnGoWork(repository)
	if err == nil {
		t.Fatal("an unrunnable git object probe was reported as success")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want the launch failure rather than a git exit status", err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true after an unrunnable object probe")
	}
}

// TestTailCovUnexpectedDiffFailureIsReported covers `git diff --quiet`
// failing for a reason other than "the file differs" (exit 1). Any other
// status means the comparison never happened, so it is an error -- not an
// unchanged file.
func TestTailCovUnexpectedDiffFailureIsReported(t *testing.T) {
	realGit := tailCovRealGit(t)
	repository := tailCovScratchRepo(t)

	script := fmt.Sprintf(`#!/bin/sh
for argument in "$@"; do
	if [ "$argument" = "diff" ]; then
		exit 2
	fi
done
exec %q "$@"
`, realGit)
	t.Setenv("PATH", tailCovWriteExecutable(t, script))

	tracked, err := TracksOwnGoWork(repository)
	if err == nil {
		t.Fatal("a failing git diff was reported as an unchanged go.work")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("error = %v, want the git diff exit status 2", err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true after a failing git diff")
	}
}

// TestTailCovGoEnvOverridesKeepsIntrinsicWorkspace covers the one case where
// the check is NOT isolated: a repository that commits its own go.work and
// leaves it unchanged keeps workspace mode, so there is no override at all.
func TestTailCovGoEnvOverridesKeepsIntrinsicWorkspace(t *testing.T) {
	t.Parallel()
	repository := tailCovScratchRepo(t)

	if overrides := GoEnvOverrides(repository); len(overrides) != 0 {
		t.Fatalf("GoEnvOverrides = %v, want no override for a repository that tracks its own go.work", overrides)
	}
}
