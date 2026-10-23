package sessionlaunch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestAttemptCreationPreservesFilesystemFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"mkdir", "root open", "ready open", "exec open", "directory sync"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			state, _ := slCovOpenState(t)
			opens := 0
			var inj *filewrite.Injector
			mkdir := func(fd int, name string, mode uint32) error {
				if stage == "mkdir" {
					return syscall.EROFS
				}
				return unix.Mkdirat(fd, name, mode)
			}
			open := func(fd int, name string, create bool) (int, error) {
				opens++
				if stage == "root open" && opens == 1 || stage == "ready open" && opens == 2 || stage == "exec open" && opens == 3 {
					return -1, syscall.EROFS
				}
				return openPrivateDirectoryAt(fd, name, create)
			}
			if stage == "directory sync" {
				inj = &filewrite.Injector{Step: filewrite.StepDirSync, Err: syscall.EROFS}
			}
			attempt, err := state.createAttemptWithOperations(newLauncherAttemptID, mkdir, open, inj)
			if attempt != nil || !errors.Is(err, syscall.EROFS) {
				t.Fatalf("create = %v, %v", attempt, err)
			}
		})
	}
}

func TestAttemptCreationBoundsRealCompetingCollisions(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	calls := 0
	newID := func(index uint64) string {
		calls++
		id := fmt.Sprintf("%06d-%s", index, strings.Repeat("a", 32))
		if calls == 1 {
			if err := os.Mkdir(slCovAttemptDir(root, id), 0700); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	attempt, err := state.createAttemptWithOperations(newID, unix.Mkdirat, openPrivateDirectoryAt, nil)
	if attempt != nil || err == nil || !strings.Contains(err.Error(), "too many random ID collisions") || calls != 100 {
		t.Fatalf("collision limit = %v, %v, calls=%d", attempt, err, calls)
	}
}

func TestClaimedAttemptRecoveryPreservesObservationFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"root read", "child read", "child open", "child create", "sync"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			state, root := slCovOpenState(t)
			id := "000001-" + strings.Repeat("a", 32)
			path := slCovAttemptDir(root, id)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if stage == "child read" || stage == "child open" {
				if err := os.Mkdir(filepath.Join(path, readyDirectoryName), 0700); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			var observationErr error
			read := func(directory *os.File) ([]os.DirEntry, error) {
				reads++
				if stage == "root read" && reads == 1 || stage == "child read" && reads == 2 {
					if err := directory.Close(); err != nil {
						t.Fatal(err)
					}
					entries, err := directory.ReadDir(-1)
					if err != nil {
						observationErr = err
					}
					return entries, err
				}
				return readAttemptDirectory(directory)
			}
			open := func(fd int, name string, create bool) (int, error) {
				if stage == "child open" && name == readyDirectoryName || stage == "child create" && create {
					return -1, syscall.EROFS
				}
				return openPrivateDirectoryAt(fd, name, create)
			}
			var inj *filewrite.Injector
			if stage == "sync" {
				inj = &filewrite.Injector{Step: filewrite.StepDirSync, Err: syscall.EROFS}
			}
			attempt, err := state.recoverClaimedAttempt(id, open, read, inj)
			if attempt != nil || err == nil {
				t.Fatalf("recover = %v, %v", attempt, err)
			}
			if stage == "root read" || stage == "child read" {
				if observationErr == nil || !errors.Is(err, observationErr) {
					t.Fatalf("native directory error = %v", err)
				}
			} else if !errors.Is(err, syscall.EROFS) {
				t.Fatalf("filesystem error = %v", err)
			}
		})
	}
}

func TestAttemptCreationRefusesClosedAttemptDirectory(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	if err := state.attempts.Close(); err != nil {
		t.Fatal(err)
	}
	attempt, err := state.createAttempt()
	if attempt != nil || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("attempt = %v, %v", attempt, err)
	}
}

func TestProcessEvidenceRefusesNativeDirectoryReadErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{execDirectoryName, readyDirectoryName} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			state, _ := slCovOpenState(t)
			attempt, err := state.createAttempt()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = attempt.Close() })
			var observationErr error
			pid, found, err := attempt.preReleaseProcessEvidenceWithEntries(func(directory *os.File) ([]os.DirEntry, error) {
				if stage == execDirectoryName && directory == attempt.exec || stage == readyDirectoryName && directory == attempt.ready {
					if err := directory.Close(); err != nil {
						t.Fatal(err)
					}
				}
				entries, err := directory.ReadDir(-1)
				if err != nil {
					observationErr = err
					if _, statErr := directory.Stat(); !errors.Is(statErr, os.ErrClosed) {
						t.Fatalf("owned directory still open: %v", statErr)
					}
				}
				return entries, err
			})
			if pid != 0 || found || observationErr == nil || !errors.Is(err, observationErr) {
				t.Fatalf("evidence = %d, %t, %v", pid, found, err)
			}
		})
	}
}

func TestPrivateDirectoryStatRefusalClosesAcquisition(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	fd, err := openPrivateDirectoryWithStat(int(state.launch.Fd()), attemptsDirectoryName, false, func(fd int, stat *unix.Stat_t) error { return syscall.EIO })
	if fd != -1 || !errors.Is(err, syscall.EIO) {
		t.Fatalf("directory = %d, %v", fd, err)
	}
}

func TestExecFenceProbeRetainsNativeLockAndUnlockErrors(t *testing.T) {
	t.Parallel()
	for _, failCall := range []int{1, 2} {
		t.Run(fmt.Sprint(failCall), func(t *testing.T) {
			t.Parallel()
			state, _ := slCovOpenState(t)
			attempt, err := state.createAttempt()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = attempt.Close() })
			fence, err := attempt.acquireExecFence(42)
			if err != nil {
				t.Fatal(err)
			}
			if err := fence.Close(); err != nil {
				t.Fatal(err)
			}
			calls := 0
			held, err := attempt.execFenceHeldWithFlock(42, func(fd, flags int) error {
				calls++
				if calls == failCall {
					return syscall.EIO
				}
				return unix.Flock(fd, flags)
			})
			if held || !errors.Is(err, syscall.EIO) {
				t.Fatalf("fence = %t, %v", held, err)
			}
		})
	}
}
