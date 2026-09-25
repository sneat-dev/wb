//go:build e2e

// Package envguard's contract tests run TracksOwnGoWork and GoEnvOverrides
// against real git, mirroring internal/gitcli's contract tests: task-8's
// plan calls this shape a contract test, proving the production seam
// (internal/runner.Real, wired through [realRunner]) drives real git
// exactly the way the default-tier fake-backed tests in envguard_test.go
// and tailcov_test.go assert the branching logic does. These tests never
// run in the default `go test ./...` tier -- only under the e2e tag -- so
// they carry no unit_tier.pending entry.
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

// contractWriteFile writes a fixture file inside dir, creating dir first.
func contractWriteFile(t *testing.T, dir, name, content string) string {
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

// contractWriteExecutable stages a fake external binary in its own
// directory and returns the directory, so a caller can prepend it to PATH.
func contractWriteExecutable(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := execfile.WriteExecutableFile(filepath.Join(dir, "git"), []byte(content), 0o755); err != nil {
		t.Fatalf("stage fake git: %v", err)
	}
	return dir
}

// contractRealGit resolves the real git binary so a fake one can forward to
// it, or skips when the machine has no git at all.
func contractRealGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	return git
}

// contractScratchRepo creates a git repository whose HEAD commits a
// go.work, i.e. a repository that owns multi-module workspace mode
// intrinsically.
func contractScratchRepo(t *testing.T) string {
	t.Helper()
	contractRealGit(t) // contractRunGit shells out; skip cleanly with no git
	repository := t.TempDir()
	contractWriteFile(t, repository, "go.work", "go 1.27\n")
	contractRunGit(t, repository, "init")
	contractRunGit(t, repository, "config", "user.email", "test@example.com")
	contractRunGit(t, repository, "config", "user.name", "Test")
	contractRunGit(t, repository, "add", "go.work")
	contractRunGit(t, repository, "commit", "-m", "add go.work")
	return repository
}

func TestContractTracksOwnGoWorkTrueOnlyWhenCommittedAndUnchanged(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contractRunGit(t, repository, "init")
	contractRunGit(t, repository, "config", "user.email", "test@example.com")
	contractRunGit(t, repository, "config", "user.name", "Test")

	// Untracked go.work: not the repository's own.
	tracked, err := TracksOwnGoWork(repository)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for an untracked go.work")
	}

	contractRunGit(t, repository, "add", "go.work")
	contractRunGit(t, repository, "commit", "-m", "add go.work")

	tracked, err = TracksOwnGoWork(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("TracksOwnGoWork = false for a committed, unchanged go.work")
	}

	// Dirty working-tree edit: no longer "its own" until committed again.
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.26\n\nuse ./extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracked, err = TracksOwnGoWork(repository)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for a go.work that differs from HEAD")
	}
}

// TestContractTracksOwnGoWorkResolvesGitTopLevelAboveGoWorkDirectory covers
// a go.work that does not sit at the git repository's top level -- e.g. a
// committed nested/go.work with the git root one directory above nested/.
// TracksOwnGoWork must resolve the true top level with `git rev-parse
// --show-toplevel` and check nested/go.work relative to it, not assume
// go.work's own directory is the git root.
func TestContractTracksOwnGoWorkResolvesGitTopLevelAboveGoWorkDirectory(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	contractRunGit(t, root, "init")
	contractRunGit(t, root, "config", "user.email", "test@example.com")
	contractRunGit(t, root, "config", "user.name", "Test")

	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contractRunGit(t, root, "add", "nested/go.work")
	contractRunGit(t, root, "commit", "-m", "add nested go.work")

	// Checks run "under nested/" -- repoRoot is the go.work's own
	// directory, one level below the git top level.
	tracked, err := TracksOwnGoWork(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("TracksOwnGoWork = false for a committed, unchanged nested/go.work with the git root above it")
	}

	// A dirty edit to the nested go.work is no longer "its own" until
	// committed again -- proves the diff check also resolves the correct
	// relative path, not just the existence check.
	if err := os.WriteFile(filepath.Join(nested, "go.work"), []byte("go 1.26\n\nuse ./extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracked, err = TracksOwnGoWork(nested)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for a nested go.work that differs from HEAD")
	}
}

func TestContractTracksOwnGoWorkResolvesSymlinkedRepositoryRoot(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	physical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(physical, "repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	contractRunGit(t, repository, "init")
	contractRunGit(t, repository, "config", "user.email", "test@example.com")
	contractRunGit(t, repository, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contractRunGit(t, repository, "add", "go.work")
	contractRunGit(t, repository, "commit", "-m", "add go.work")
	alias := filepath.Join(physical, "repository-alias")
	if err := os.Symlink(repository, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tracked, err := TracksOwnGoWork(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("TracksOwnGoWork = false through a symlinked repository root")
	}
}

// TestContractTracksOwnGoWorkFalseOutsideAnyGitRepository covers a temp
// module (a go.work with no enclosing git repository at all): GoEnvOverrides
// must yield GOWORK=off rather than erroring or panicking when `git
// rev-parse --show-toplevel` finds no repository.
func TestContractTracksOwnGoWorkFalseOutsideAnyGitRepository(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracked, err := TracksOwnGoWork(root)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for a go.work outside any git repository")
	}

	overrides := GoEnvOverrides(root)
	if len(overrides) != 1 || overrides[0] != "GOWORK=off" {
		t.Fatalf("overrides = %v, want [GOWORK=off]", overrides)
	}
}

// TestContractGoEnvOverridesKeepsIntrinsicWorkspace covers the one case
// where the check is NOT isolated: a repository that commits its own
// go.work and leaves it unchanged keeps workspace mode, so there is no
// override at all.
func TestContractGoEnvOverridesKeepsIntrinsicWorkspace(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contractRunGit(t, repository, "init")
	contractRunGit(t, repository, "config", "user.email", "test@example.com")
	contractRunGit(t, repository, "config", "user.name", "Test")
	contractRunGit(t, repository, "add", "go.work")
	contractRunGit(t, repository, "commit", "-m", "add go.work")

	if overrides := GoEnvOverrides(repository); len(overrides) != 0 {
		t.Fatalf("GoEnvOverrides = %v, want no override for a repository that tracks its own go.work", overrides)
	}
}

func contractRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

// TestContractMissingGitBinaryIsReported covers the git-inspection failure
// that is not a git exit status: when git cannot be executed at all, the
// tracked-go.work probe reports that failure instead of guessing, and
// GoEnvOverrides still fails closed toward isolation.
func TestContractMissingGitBinaryIsReported(t *testing.T) {
	root := filepath.Dir(contractWriteFile(t, t.TempDir(), "go.work", "go 1.27\n"))
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

// TestContractRelativeGitTopLevelIsReported covers `git rev-parse
// --show-toplevel` returning something that cannot be made relative to the
// go.work path. A toplevel that is not absolute is unusable, so the caller
// must see the failure rather than a silent "not tracked".
func TestContractRelativeGitTopLevelIsReported(t *testing.T) {
	root := filepath.Dir(contractWriteFile(t, t.TempDir(), "go.work", "go 1.27\n"))
	dir := contractWriteExecutable(t, "#!/bin/sh\necho not-an-absolute-toplevel\n")
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

// TestContractUnrunnableObjectProbeIsReported covers the git object probe
// failing to run at all (as opposed to exiting non-zero, which means "not in
// HEAD"). The failure is reported, never converted into a clean "not
// tracked".
func TestContractUnrunnableObjectProbeIsReported(t *testing.T) {
	realGit := contractRealGit(t)
	chmod, err := exec.LookPath("chmod")
	if err != nil {
		t.Skip("chmod not available")
	}
	repository := contractScratchRepo(t)

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
	t.Setenv("PATH", contractWriteExecutable(t, script))

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

// TestContractUnexpectedDiffFailureIsReported covers `git diff --quiet`
// failing for a reason other than "the file differs" (exit 1). Any other
// status means the comparison never happened, so it is an error -- not an
// unchanged file.
func TestContractUnexpectedDiffFailureIsReported(t *testing.T) {
	realGit := contractRealGit(t)
	repository := contractScratchRepo(t)

	script := fmt.Sprintf(`#!/bin/sh
for argument in "$@"; do
	if [ "$argument" = "diff" ]; then
		exit 2
	fi
done
exec %q "$@"
`, realGit)
	t.Setenv("PATH", contractWriteExecutable(t, script))

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
