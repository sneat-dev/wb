package filewrite

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// WriteJSONImmutableAt encodes value as indented JSON followed by a newline
// and publishes it immutably below directory. beforeRename is an optional
// test seam that runs after the temporary file has been durably closed and
// before its no-replace publication.
func WriteJSONImmutableAt(directory *os.File, name string, value any, idempotent bool, beforeRename func(*os.File, string)) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteBytesImmutableAt(directory, name, append(content, '\n'), 0o600, idempotent, beforeRename)
}

// WriteBytesImmutableAt publishes content below directory without replacing an
// existing entry. An idempotent write accepts an existing entry only when its
// bytes match exactly.
func WriteBytesImmutableAt(directory *os.File, name string, content []byte, mode os.FileMode, idempotent bool, beforeRename func(*os.File, string)) error {
	return WriteBytesImmutableAtInjected(directory, name, content, mode, idempotent, nil, beforeRename)
}

// WriteBytesImmutableAtInjected is WriteBytesImmutableAt with one explicit
// filewrite failure injector for tests.
func WriteBytesImmutableAtInjected(directory *os.File, name string, content []byte, mode os.FileMode, idempotent bool, inj *Injector, beforeRename func(*os.File, string)) error {
	if unsafeFileName(name) {
		return fmt.Errorf("unsafe immutable filename %q", name)
	}
	if existing, err := ReadAt(directory, name); err == nil {
		if idempotent && bytes.Equal(existing, content) {
			return nil
		}
		return fmt.Errorf("immutable file already exists: %s", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeBytesWithTemporaryAtInjected(directory, name, content, mode, inj, func(temporary string) (bool, error) {
		if beforeRename != nil {
			beforeRename(directory, name)
		}
		if err := RenameNoReplace(int(directory.Fd()), temporary, int(directory.Fd()), name, inj); err != nil {
			if existing, readErr := ReadAt(directory, name); idempotent && readErr == nil && bytes.Equal(existing, content) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// ReadAt reads one non-symlinked entry below an already-open directory.
func ReadAt(directory *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

// ReadJSONAt decodes one non-symlinked JSON entry below directory.
func ReadJSONAt(directory *os.File, name string, target any) error {
	content, err := ReadAt(directory, name)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

// WriteJSONAtomic encodes value as indented JSON followed by a newline and
// atomically replaces path, creating its parent directory when necessary.
func WriteJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return WriteBytesAtomic(filepath.Dir(path), filepath.Base(path), append(content, '\n'), mode)
}

// WriteJSONAtomicAt encodes value as indented JSON followed by a newline and
// atomically replaces name below directory.
func WriteJSONAtomicAt(directory *os.File, name string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteBytesAtomicAt(directory, name, append(content, '\n'), mode)
}

// WriteBytesAtomicAt atomically replaces name below directory.
func WriteBytesAtomicAt(directory *os.File, name string, content []byte, mode os.FileMode) error {
	return WriteBytesAtomicAtInjected(directory, name, content, mode, nil)
}

// WriteBytesAtomicAtInjected is WriteBytesAtomicAt with one explicit
// filewrite failure injector for tests.
func WriteBytesAtomicAtInjected(directory *os.File, name string, content []byte, mode os.FileMode, inj *Injector) error {
	if directory == nil || unsafeFileName(name) {
		return fmt.Errorf("unsafe atomic filename %q", name)
	}
	return writeBytesWithTemporaryAtInjected(directory, name, content, mode, inj, func(temporary string) (bool, error) {
		if err := RenameAt(int(directory.Fd()), temporary, int(directory.Fd()), name, inj); err != nil {
			return false, err
		}
		return true, nil
	})
}

// WriteBytesAtomic atomically replaces name in directory, creating directory
// with private permissions when it does not yet exist.
func WriteBytesAtomic(directory, name string, content []byte, mode os.FileMode) error {
	return WriteBytesAtomicInjected(directory, name, content, mode, nil)
}

// WriteBytesAtomicInjected is WriteBytesAtomic with one explicit filewrite
// failure injector for tests.
func WriteBytesAtomicInjected(directory, name string, content []byte, mode os.FileMode, inj *Injector) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := CreateTemp(directory, "."+name+".tmp-*", inj)
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := ChmodFile(temporary, mode, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := Write(temporary, content, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := Sync(temporary, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := Close(temporary, temporaryName, inj); err != nil {
		return err
	}
	if err := Rename(temporaryName, filepath.Join(directory, name), inj); err != nil {
		return err
	}
	if err := inj.run(StepOpenDirectory, directory); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return SyncDir(dir, inj)
}

func writeBytesWithTemporaryAtInjected(directory *os.File, name string, content []byte, mode os.FileMode, inj *Injector, publish func(string) (bool, error)) error {
	temporary := "." + name + ".tmp-" + randomHexToken(12)
	fd, err := CreateExclusive(int(directory.Fd()), temporary, uint32(mode.Perm()), inj)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		}
	}()
	if err := Write(file, content, temporary, inj); err != nil {
		return err
	}
	if err := Sync(file, temporary, inj); err != nil {
		return err
	}
	if err := Close(file, temporary, inj); err != nil {
		return err
	}
	published, err := publish(temporary)
	if err != nil {
		return err
	}
	if !published {
		return nil
	}
	cleanup = false
	return SyncDir(directory, inj)
}

func unsafeFileName(name string) bool {
	return strings.Contains(name, "/") || name == "" || name == "." || name == ".."
}

func randomHexToken(byteCount int) string {
	token := make([]byte, byteCount)
	_, _ = rand.Read(token)
	return hex.EncodeToString(token)
}
