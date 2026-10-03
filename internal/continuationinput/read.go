// Package continuationinput reads bounded continuation inputs with their existing file safety policies.
package continuationinput

import (
	"bytes"
	"fmt"
	"github.com/sneat-dev/wb/internal/unixcompat"
	"io"
	"path/filepath"
	"strings"
)

func ReadBounded(reader io.Reader, limit int, label string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s must not be empty", label)
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	return raw, nil
}

func (r *Reader) ReadRegularFile(path string, limit int) ([]byte, error) {
	absolute, err := r.effects.Abs(strings.TrimSpace(path))
	if err != nil || filepath.Clean(absolute) != absolute || absolute == string(filepath.Separator) {
		return nil, fmt.Errorf("message file must be one clean path")
	}
	// Resolve only the parent so standard aliases such as macOS /var work,
	// then walk that resolved identity with no-follow descriptors. The final
	// user-selected file is never symlink-resolved and remains O_NOFOLLOW.
	resolvedParent, err := r.effects.ResolveParent(filepath.Dir(absolute))
	if err != nil {
		return nil, fmt.Errorf("resolve message file parent: %w", err)
	}
	absolute = filepath.Join(resolvedParent, filepath.Base(absolute))
	segments := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	directoryFD, err := r.effects.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("open message file root: %w", err)
	}
	for _, segment := range segments[:len(segments)-1] {
		next, openErr := r.effects.OpenAt(directoryFD, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
		_ = r.effects.Close(directoryFD)
		if openErr != nil {
			return nil, fmt.Errorf("open message file parent: %w", openErr)
		}
		directoryFD = next
	}
	fd, err := r.effects.OpenAt(directoryFD, segments[len(segments)-1], unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC)
	_ = r.effects.Close(directoryFD)
	if err != nil {
		return nil, fmt.Errorf("open message file: %w", err)
	}
	// A successful native OpenAt provides a valid Unix descriptor or Windows
	// handle. File wraps that same identity into a nonnil owned reader.
	file := r.effects.File(uintptr(fd), "wb-session-message-input")
	defer func() { _ = file.Close() }()
	var before unix.Stat_t
	if err := r.effects.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 ||
		before.Size <= 0 || before.Size > int64(limit) {
		if err != nil {
			return nil, fmt.Errorf("inspect message file: %w", err)
		}
		return nil, fmt.Errorf("message file must be one non-empty bounded regular single-link file")
	}
	first, err := ReadBounded(file, limit, "message file")
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind message file: %w", err)
	}
	second, err := ReadBounded(file, limit, "message file verification")
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, second) {
		return nil, fmt.Errorf("message file changed while it was read")
	}
	var after unix.Stat_t
	if err := r.effects.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino ||
		before.Mode != after.Mode || before.Nlink != after.Nlink || before.Size != after.Size {
		if err != nil {
			return nil, fmt.Errorf("reinspect message file: %w", err)
		}
		return nil, fmt.Errorf("message file identity changed while it was verified")
	}
	return first, nil
}

func (r *Reader) ReadHandover(input io.Reader, path string, limit int) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("--handover-file is required; use - to read stdin")
	}
	var reader io.Reader
	var file HandoverFile
	if path == "-" {
		reader = input
	} else {
		var err error
		file, err = r.effects.OpenHandover(path)
		if err != nil {
			return nil, fmt.Errorf("open handover file %s: %w", path, err)
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect handover file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("handover file %s must be a regular file", path)
		}
		reader = file
	}
	contents, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read handover: %w", err)
	}
	if len(contents) > limit {
		return nil, fmt.Errorf("handover exceeds %d bytes", limit)
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return nil, fmt.Errorf("handover must not be empty")
	}
	return contents, nil
}
