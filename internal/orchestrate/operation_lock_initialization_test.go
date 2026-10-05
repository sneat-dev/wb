package orchestrate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"testing"
)

// This models only the metadata IO protocol. Real acquisition and retirement
// remain native in the caller-cleanup tests below.
type operationMetadataIO struct {
	failAt   string
	failure  error
	calls    []string
	contents []byte
}

func (file *operationMetadataIO) Truncate(size int64) error {
	file.calls = append(file.calls, fmt.Sprintf("truncate(%d)", size))
	if file.failAt == "truncate" {
		return file.failure
	}
	file.contents = file.contents[:size]
	return nil
}

func (file *operationMetadataIO) Seek(offset int64, whence int) (int64, error) {
	file.calls = append(file.calls, fmt.Sprintf("seek(%d,%d)", offset, whence))
	if file.failAt == "seek" {
		return 0, file.failure
	}
	return offset, nil
}

func (file *operationMetadataIO) Write(contents []byte) (int, error) {
	file.calls = append(file.calls, "write")
	if file.failAt == "write" {
		return 0, file.failure
	}
	file.contents = append(file.contents, contents...)
	return len(contents), nil
}

func TestOperationLockMetadataInitializationPreservesIOContract(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"", "truncate", "seek", "write"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("metadata IO refused")
			file := &operationMetadataIO{failAt: stage, failure: failure, contents: []byte("old metadata that must be truncated")}
			err := writeOperationLockMetadata(file, "fresh-operation")
			wantCalls := []string{"truncate(0)"}
			if stage != "truncate" {
				wantCalls = append(wantCalls, "seek(0,0)")
			}
			if stage != "truncate" && stage != "seek" {
				wantCalls = append(wantCalls, "write")
			}
			if !reflect.DeepEqual(file.calls, wantCalls) {
				t.Fatalf("IO order = %v, want %v", file.calls, wantCalls)
			}
			if stage == "" {
				want := "operation=fresh-operation\npid=" + strconv.Itoa(os.Getpid()) + "\n"
				if err != nil || string(file.contents) != want {
					t.Fatalf("metadata = %q, error = %v, want %q", file.contents, err, want)
				}
			} else if stage == "truncate" {
				if !errors.Is(err, failure) || err.Error() != `initialize operation "fresh-operation" lock: metadata IO refused` {
					t.Fatalf("truncate error = %v", err)
				}
			} else if err != failure {
				t.Fatalf("%s error = %v, want original sentinel", stage, err)
			}
		})
	}
}

func TestOperationLockInitializationFailureReleasesNativeCustody(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"truncate", "seek", "write", "unavailable descriptor"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			githubDir := t.TempDir()
			const operation = "failed-initialization"
			path := operationLockTestPath(t, githubDir, operation)
			failure := errors.New("metadata IO refused")
			var heldFile, heldDirectory *os.File
			var initializerErr error
			got, err := acquireOperationLockWithMetadataInitializer(githubDir, operation, false, func(candidate OperationLock, gotOperation string) error {
				t.Cleanup(func() { _ = candidate.Release() })
				if gotOperation != operation {
					t.Fatalf("initializer operation = %q", gotOperation)
				}
				heldFile, heldDirectory = candidate.lock.File(), candidate.directory
				if _, err := heldFile.Stat(); err != nil {
					t.Fatalf("initializer has no real held file: %v", err)
				}
				if _, err := heldDirectory.Stat(); err != nil {
					t.Fatalf("initializer has no real directory: %v", err)
				}
				if stage == "unavailable descriptor" {
					initializerErr = initializeOperationLockMetadata(OperationLock{}, gotOperation)
				} else {
					initializerErr = writeOperationLockMetadata(&operationMetadataIO{failAt: stage, failure: failure}, gotOperation)
				}
				return initializerErr
			})
			if got.lock != nil || got.directory != nil || err == nil || err != initializerErr {
				t.Fatalf("failed acquisition = %#v, %v; initializer error = %v", got, err, initializerErr)
			}
			if stage == "unavailable descriptor" {
				if err.Error() != `initialize operation "failed-initialization" lock: descriptor is unavailable` {
					t.Fatalf("descriptor error = %v", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("original IO error lost: %v", err)
			}
			assertOperationDescriptorClosed(t, heldFile)
			assertOperationDescriptorClosed(t, heldDirectory)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("failed initialization left lock entry: %v", err)
			}
			next, err := AcquireOperationLock(githubDir, operation, false)
			if err != nil {
				t.Fatalf("failed initialization retained native custody: %v", err)
			}
			if err := next.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOperationLockMetadataInitializationTruncatesRealHeldFile(t *testing.T) {
	t.Parallel()
	githubDir := t.TempDir()
	const operation = "real-initialization"
	path := operationLockTestPath(t, githubDir, operation)
	lock, err := AcquireOperationLock(githubDir, operation, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	file := lock.lock.File()
	if _, err := file.WriteString("stale suffix that must not survive initialization"); err != nil {
		t.Fatal(err)
	}
	if err := initializeOperationLockMetadata(lock, operation); err != nil {
		t.Fatal(err)
	}
	want := "operation=" + operation + "\npid=" + strconv.Itoa(os.Getpid()) + "\n"
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("real metadata = %q, %v; want %q", got, err, want)
	}
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil || position != int64(len(want)) {
		t.Fatalf("initialized cursor = %d, %v", position, err)
	}
}

func TestOperationLockResumeKeepsExactMetadataWithoutInitializing(t *testing.T) {
	t.Parallel()
	githubDir := t.TempDir()
	const operation = "resume-keeps-metadata"
	path := operationLockTestPath(t, githubDir, operation)
	contents := "operation=" + operation + "\npid=" + strconv.Itoa(killedProcessPID(t)) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireOperationLockWithMetadataInitializer(githubDir, operation, true, func(OperationLock, string) error {
		t.Fatal("resume initialized interrupted metadata")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("resume replaced interrupted inode: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != contents {
		t.Fatalf("resume metadata = %q, %v; want %q", got, err, contents)
	}
}

func TestOperationLockReleaseClosesNativeDescriptorsAndAcceptsPartialOwner(t *testing.T) {
	t.Parallel()
	if err := (OperationLock{}).Release(); err != nil {
		t.Fatalf("empty owner release = %v", err)
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := (OperationLock{directory: directory}).Release(); err != nil {
		t.Fatal(err)
	}
	assertOperationDescriptorClosed(t, directory)
	lock, err := AcquireOperationLock(t.TempDir(), "release-closes-handles", false)
	if err != nil {
		t.Fatal(err)
	}
	file, directory := lock.lock.File(), lock.directory
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	assertOperationDescriptorClosed(t, file)
	assertOperationDescriptorClosed(t, directory)
	if err := lock.Release(); err != nil {
		t.Fatalf("repeated release = %v", err)
	}
}

func assertOperationDescriptorClosed(t *testing.T, file *os.File) {
	t.Helper()
	if file == nil {
		t.Fatal("no real descriptor captured")
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("descriptor %q is not closed: %v", file.Name(), err)
	}
}
