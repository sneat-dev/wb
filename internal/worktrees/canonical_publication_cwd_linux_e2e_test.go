//go:build linux && e2e

package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestE2ECanonicalPublicationRejectsDeletedLinuxCwd(t *testing.T) {
	const marker = "WB_CANONICAL_PUBLICATION_DELETED_CWD_CHILD"
	const rootVariable = "WB_CANONICAL_PUBLICATION_DELETED_CWD_ROOT"
	if os.Getenv(marker) == "1" {
		assertCanonicalPublicationDeletedLinuxCwd(t, os.Getenv(rootVariable))
		return
	}
	t.Parallel()
	// Only the disposable child enters and removes the empty stage. The
	// parent owns the containing directory and its eventual cleanup.
	root := t.TempDir()
	deadline := time.Now().Add(time.Minute)
	if parent, ok := t.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2ECanonicalPublicationRejectsDeletedLinuxCwd$")
	command.Env = append(omitEnv(os.Environ(), []string{marker, rootVariable, "PWD"}), marker+"=1", rootVariable+"="+root)
	if dir := wtLifeCovCoverDir(); testing.CoverMode() != "" && dir != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+dir)
		command.Env = append(command.Env, "GOCOVERDIR="+dir)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("deleted-cwd child = %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
	}
	wantDiagnostic := fmt.Sprintf("wb secure stage helper: determine staging directory: %v\n", os.NewSyscallError("getwd", syscall.ENOENT))
	if got := stderr.String(); got != wantDiagnostic {
		t.Fatalf("deleted-cwd diagnostic = %q, want %q", got, wantDiagnostic)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("deleted-cwd fixture left stage entries: %v, %v", entries, err)
	}
}

// This runs only before the child test's return, with no parallel tests in
// that process. Restoring cwd by its retained descriptor precedes all testing
// cleanup and the normal coverage flush, including assertion failure paths.
func assertCanonicalPublicationDeletedLinuxCwd(t *testing.T, root string) {
	t.Helper()
	if !filepath.IsAbs(root) {
		t.Fatalf("child operation root is not absolute: %q", root)
	}
	original, err := os.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Fchdir(int(original.Fd())); err != nil {
			t.Errorf("restore original cwd: %v", err)
		}
		if err := original.Close(); err != nil {
			t.Error(err)
		}
	}()
	// A matching PWD could bypass Linux getcwd through Go's same-file check.
	t.Setenv("PWD", "")
	absoluteRoot, err := absoluteProjectsRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitExecutable, err = filepath.Abs(gitExecutable)
	if err != nil {
		t.Fatal(err)
	}
	gitIdentity, err := os.Stat(gitExecutable)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "empty-stage")
	relativeGitDirectory, err := filepath.Rel(stage, filepath.Dir(gitExecutable))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(stage); err != nil {
		t.Fatal(err)
	}
	if code := verifySecureStageContainment(absoluteRoot); code != 0 {
		t.Fatalf("live stage containment = %d", code)
	}
	// Remove only this empty fixture directory, while its cwd object remains
	// held by the kernel. No permissions, mounts or syscall results are faked.
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("unlinked Linux cwd prerequisite = %v, want ENOENT", err)
	}
	if got, err := absoluteProjectsRoot(root); err != nil || got != absoluteRoot {
		t.Fatalf("absolute configured root after cwd deletion = %q, %v; want %q", got, err, absoluteRoot)
	}
	if got, err := absoluteProjectsRoot("relative"); got != "" || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("relative project root after cwd deletion = %q, %v", got, err)
	}
	got, err := Guard(context.Background(), "unused-checkout", GuardOptions{ProjectsRoot: "relative"})
	if !reflect.DeepEqual(got, GuardResult{}) || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("guard after cwd deletion = %+v, %v", got, err)
	}
	if code := verifySecureStageContainment(absoluteRoot); code != 1 {
		t.Fatalf("deleted-cwd stage containment = %d, want 1", code)
	}
	// Go documents execerrdot=0 for callers retaining relative PATH search.
	// Resolve the captured real Git through Linux's still-reachable parent;
	// only its later absolute-spelling step must fail on the unlinked cwd.
	t.Setenv("PATH", relativeGitDirectory)
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",execerrdot=0")
	foundGit, err := exec.LookPath("git")
	if err != nil || filepath.IsAbs(foundGit) {
		t.Fatalf("real Git relative lookup prerequisite = %q, %v", foundGit, err)
	}
	foundIdentity, err := os.Stat(foundGit)
	if err != nil || !os.SameFile(gitIdentity, foundIdentity) {
		t.Fatalf("relative lookup did not retain captured real Git identity: %q, %v", foundGit, err)
	}
	gotExecutable, err := platformTrustedGitExecutable()
	wantError := fmt.Sprintf("make Git path absolute before secure staging handoff: %v", os.NewSyscallError("getwd", syscall.ENOENT))
	if gotExecutable != "" || !errors.Is(err, syscall.ENOENT) || err.Error() != wantError {
		t.Fatalf("relative real Git absolute-spelling refusal = %q, %v; want %q", gotExecutable, err, wantError)
	}
}
