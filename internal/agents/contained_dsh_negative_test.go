package agents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainedDshLaunchReportsAFailedScopedInputCreation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root test user can create the scoped input in an execute-only directory")
	}
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	// The run directory is searchable but not writable: the scoped input target
	// is provably absent, and creating it is refused by the filesystem.
	if err := os.Chmod(runDir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o700) })
	_, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir))
	if err == nil || !strings.Contains(err.Error(), "create scoped input directory") {
		t.Fatalf("error = %v, want the scoped input creation refusal", err)
	}
}

func TestContainedDshLaunchReportsAnUninspectableScopedInputTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root test user can inspect a directory with no permission bits")
	}
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	if err := os.Chmod(runDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o700) })
	_, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir))
	if err == nil || !strings.Contains(err.Error(), "inspect scoped input directory") {
		t.Fatalf("error = %v, want the scoped input inspection refusal", err)
	}
}

func TestContainedDshCredentialRoutingRefusesAnInvalidInjectedName(t *testing.T) {
	t.Parallel()
	routing := Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_TEST_KEY", WireAPI: WireAPIChat}
	err := containedDshCredentialMatchesRouting(routing, "not-uppercase")
	if err == nil || !strings.Contains(err.Error(), "uppercase shell variable") {
		t.Fatalf("error = %v, want the invalid credential-name refusal", err)
	}
}

func TestWriteContainedDshPatchReportsACreateRefusalWithoutTruncatingAnything(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	err := writeContainedDshPatch(directory, []byte("- id: agent-default-model\n"))
	if err == nil || !strings.Contains(err.Error(), "create overlay") {
		t.Fatalf("error = %v, want the overlay creation refusal", err)
	}
	info, statErr := os.Stat(directory)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("the refused write replaced the existing directory: %v %#v", statErr, info)
	}
}

func TestContainedDshLaunchStillRemovesItsScopedInputOnSuccess(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	prepared, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir))
	if err != nil {
		t.Fatalf("PrepareContainedDshLaunch: %v", err)
	}
	if prepared.Workspace == nil || prepared.Workspace.InputDir != filepath.Join(runDir, containedDshInputDirName) {
		t.Fatalf("prepared workspace = %#v", prepared.Workspace)
	}
}

// failingContainedDshFile is an overlay file whose write or close can fail. It
// records whether the write path closed it, so the test can prove the failure
// path does not leak an open descriptor.
type failingContainedDshFile struct {
	writeErr error
	closeErr error
	closed   bool
}

func (file *failingContainedDshFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *failingContainedDshFile) Close() error {
	file.closed = true
	return file.closeErr
}

func TestContainedDshLaunchRemovesTheScopedInputWhenTheOverlayWriteFails(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	file := &failingContainedDshFile{writeErr: errors.New("the overlay disk is full")}
	filesystem := defaultContainedDshFilesystem()
	filesystem.openFile = func(string, int, os.FileMode) (containedDshFile, error) {
		return file, nil
	}
	_, err := prepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir), filesystem)
	if err == nil || !strings.Contains(err.Error(), "write overlay") {
		t.Fatalf("error = %v, want the overlay write refusal", err)
	}
	if !file.closed {
		t.Fatal("a failed overlay write must close the file it opened")
	}
	if _, statErr := os.Stat(filepath.Join(runDir, containedDshInputDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("scoped input still exists after a refused overlay write: %v", statErr)
	}
}

func TestContainedDshLaunchRemovesTheScopedInputWhenTheOverlayCloseFails(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	file := &failingContainedDshFile{closeErr: errors.New("the overlay close was refused")}
	filesystem := defaultContainedDshFilesystem()
	filesystem.openFile = func(string, int, os.FileMode) (containedDshFile, error) {
		return file, nil
	}
	_, err := prepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir), filesystem)
	if err == nil || !strings.Contains(err.Error(), "close overlay") {
		t.Fatalf("error = %v, want the overlay close refusal", err)
	}
	if !file.closed {
		t.Fatal("the overlay close failure must still be reported from a closed file")
	}
	if _, statErr := os.Stat(filepath.Join(runDir, containedDshInputDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("scoped input still exists after a refused overlay close: %v", statErr)
	}
}

func TestContainedDshLaunchReportsARunDirectoryThatCannotBeResolved(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	resolveErr := errors.New("the run directory resolution was refused")
	filesystem := defaultContainedDshFilesystem()
	filesystem.evalSymlinks = func(string) (string, error) { return "", resolveErr }
	_, err := prepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir), filesystem)
	if err == nil || !strings.Contains(err.Error(), "resolve run directory") || !errors.Is(err, resolveErr) {
		t.Fatalf("error = %v, want the run-directory resolution refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, containedDshInputDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused run directory still created the scoped input: %v", statErr)
	}
}
