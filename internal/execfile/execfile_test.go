package execfile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteExecutableFileProducesAnExecutableAtomicallyAtFinalPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake")
	if err := WriteExecutableFile(path, []byte("#!/bin/sh\nprintf 'ok'\n"), 0o755); err != nil {
		t.Fatalf("WriteExecutableFile: %v", err)
	}
	out, err := exec.Command(path).CombinedOutput()
	if err != nil || string(out) != "ok" {
		t.Fatalf("output = %q err = %v", out, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0o755", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "fake" {
		t.Fatalf("directory entries = %#v, want only the final path (no leftover temp file)", entries)
	}
}

func TestWriteExecutableFileNeverExposesAWriteOpenFDDuringAConcurrentFork(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// This reproduces the ETXTBSY race directly: while one goroutine
	// repeatedly overwrites the same path with WriteExecutableFile,
	// another goroutine repeatedly execs it. os.WriteFile in place of
	// WriteExecutableFile below makes this test fail with "text file
	// busy" within a handful of iterations on a loaded machine.
	path := filepath.Join(dir, "racer")
	if err := WriteExecutableFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("seed WriteExecutableFile: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	errs := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ctx.Err() == nil; i++ {
			body := []byte("#!/bin/sh\nexit 0\n# " + strings.Repeat("x", i%7) + "\n")
			if err := WriteExecutableFile(path, body, 0o755); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()
	for ctx.Err() == nil {
		if err := exec.CommandContext(ctx, path).Run(); err != nil && ctx.Err() == nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				break
			}
			select {
			case errs <- err:
			default:
			}
			break
		}
	}
	<-done
	select {
	case err := <-errs:
		t.Fatalf("concurrent write/exec raced: %v", err)
	default:
	}
}

//nolint:paralleltest // mutates the package-level createTempExecutableFile/renameExecutableFile seams
func TestWriteExecutableFileSurfacesCreateTempFailure(t *testing.T) {
	original := createTempExecutableFile
	t.Cleanup(func() { createTempExecutableFile = original })
	wantErr := errors.New("injected create failure")
	createTempExecutableFile = func(string) (tempExecutableFile, error) { return nil, wantErr }

	if err := WriteExecutableFile(filepath.Join(t.TempDir(), "fake"), nil, 0o755); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

//nolint:paralleltest // mutates the package-level createTempExecutableFile/renameExecutableFile seams
func TestWriteExecutableFileSurfacesWriteFailureAndCleansUpTemp(t *testing.T) {
	original := createTempExecutableFile
	t.Cleanup(func() { createTempExecutableFile = original })
	dir := t.TempDir()
	fake := &fakeTempExecutableFile{name: filepath.Join(dir, ".wexec-fake"), writeErr: errors.New("injected write failure")}
	createTempExecutableFile = func(string) (tempExecutableFile, error) { return fake, nil }

	if err := WriteExecutableFile(filepath.Join(dir, "fake"), []byte("x"), 0o755); !errors.Is(err, fake.writeErr) {
		t.Fatalf("error = %v, want %v", err, fake.writeErr)
	}
	if !fake.closed {
		t.Fatal("temp file was not closed after a write failure")
	}
}

//nolint:paralleltest // mutates the package-level createTempExecutableFile/renameExecutableFile seams
func TestWriteExecutableFileSurfacesChmodFailureAndCleansUpTemp(t *testing.T) {
	original := createTempExecutableFile
	t.Cleanup(func() { createTempExecutableFile = original })
	dir := t.TempDir()
	fake := &fakeTempExecutableFile{name: filepath.Join(dir, ".wexec-fake"), chmodErr: errors.New("injected chmod failure")}
	createTempExecutableFile = func(string) (tempExecutableFile, error) { return fake, nil }

	if err := WriteExecutableFile(filepath.Join(dir, "fake"), []byte("x"), 0o755); !errors.Is(err, fake.chmodErr) {
		t.Fatalf("error = %v, want %v", err, fake.chmodErr)
	}
	if !fake.closed {
		t.Fatal("temp file was not closed after a chmod failure")
	}
}

//nolint:paralleltest // mutates the package-level createTempExecutableFile/renameExecutableFile seams
func TestWriteExecutableFileSurfacesCloseFailure(t *testing.T) {
	original := createTempExecutableFile
	t.Cleanup(func() { createTempExecutableFile = original })
	dir := t.TempDir()
	fake := &fakeTempExecutableFile{name: filepath.Join(dir, ".wexec-fake"), closeErr: errors.New("injected close failure")}
	createTempExecutableFile = func(string) (tempExecutableFile, error) { return fake, nil }

	if err := WriteExecutableFile(filepath.Join(dir, "fake"), []byte("x"), 0o755); !errors.Is(err, fake.closeErr) {
		t.Fatalf("error = %v, want %v", err, fake.closeErr)
	}
}

//nolint:paralleltest // mutates the package-level createTempExecutableFile/renameExecutableFile seams
func TestWriteExecutableFileSurfacesRenameFailureAndRemovesTemp(t *testing.T) {
	originalCreate, originalRename := createTempExecutableFile, renameExecutableFile
	t.Cleanup(func() { createTempExecutableFile, renameExecutableFile = originalCreate, originalRename })
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, ".wexec-real")
	if err := os.WriteFile(tmpPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed temp file: %v", err)
	}
	createTempExecutableFile = func(string) (tempExecutableFile, error) {
		f, err := os.OpenFile(tmpPath, os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	wantErr := errors.New("injected rename failure")
	renameExecutableFile = func(string, string) error { return wantErr }

	if err := WriteExecutableFile(filepath.Join(dir, "fake"), []byte("x"), 0o755); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if _, statErr := os.Stat(tmpPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temp file %s still present after a failed rename", tmpPath)
	}
}

type fakeTempExecutableFile struct {
	name     string
	writeErr error
	chmodErr error
	closeErr error
	closed   bool
}

func (f *fakeTempExecutableFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *fakeTempExecutableFile) Chmod(os.FileMode) error { return f.chmodErr }

func (f *fakeTempExecutableFile) Close() error {
	f.closed = true
	return f.closeErr
}

func (f *fakeTempExecutableFile) Name() string { return f.name }
