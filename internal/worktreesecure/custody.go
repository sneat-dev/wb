// Package worktreesecure provides descriptor-held filesystem custody helpers.
// It owns no worktree policy, record format, Git behavior, or transaction flow.
package worktreesecure

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/secureopen"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// RenameNoReplace publishes one directory entry without replacing another.
type RenameNoReplace func(fromDirectoryFD int, fromName string, toDirectoryFD int, toName string) error

// ValidSegment decides whether a caller-owned path component is admissible.
type ValidSegment func(string) bool

// DirectoryIdentity is the device/inode identity of a directory entry.
type DirectoryIdentity struct{ device, inode uint64 }

// Device returns the identity device number.
func (identity DirectoryIdentity) Device() uint64 { return identity.device }

// Inode returns the identity inode number.
func (identity DirectoryIdentity) Inode() uint64 { return identity.inode }

// ErrDirectoryMoveIdentityChanged reports a no-replace move whose identity no longer matches.
var ErrDirectoryMoveIdentityChanged = errors.New("directory move identity changed")

// CloseIncompleteFiles releases a partially acquired descriptor set.
func CloseIncompleteFiles(files ...*os.File) bool {
	for _, file := range files {
		if file == nil {
			for _, acquired := range files {
				if acquired != nil {
					_ = acquired.Close()
				}
			}
			return true
		}
	}
	return false
}

// DuplicateDirectoryDescriptor duplicates an owned directory descriptor.
func DuplicateDirectoryDescriptor(directory *os.File, name string) (*os.File, error) {
	if directory == nil {
		return nil, fmt.Errorf("directory descriptor is unavailable")
	}
	fd, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name), nil
}

// PathWithin reports whether path is lexical root or a descendant of root.
func PathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// DirectoryExistsNoFollow classifies a directory without accepting a symlink.
func DirectoryExistsNoFollow(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect worktree destination %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing symlinked worktree destination %s", path)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("worktree destination is not a directory: %s", path)
	}
	return true, nil
}

// OpenAbsoluteDirectoryNoFollow walks an absolute path through real no-follow opens.
func OpenAbsoluteDirectoryNoFollow(path string, create bool) (*os.File, error) {
	return OpenAbsoluteDirectoryNoFollowWith(secureopen.Real{}, path, create)
}

// OpenAbsoluteDirectoryNoFollowWith walks an absolute path through opener-held descriptors.
func OpenAbsoluteDirectoryNoFollowWith(opener secureopen.Opener, path string, create bool) (*os.File, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("secure directory path must be absolute: %s", path)
	}
	fd, err := opener.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, fmt.Errorf("open filesystem root for secure directory %s: %w", path, err)
	}
	if path == string(filepath.Separator) {
		return os.NewFile(uintptr(fd), "wb-secure-directory"), nil
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		var next int
		if create {
			next, err = OpenOrCreateNoFollowDirectoryWith(opener, fd, segment)
		} else {
			next, err = opener.OpenDir(fd, segment)
			if err != nil {
				if opener.IsSymlink(fd, segment) {
					err = fmt.Errorf("refusing symlinked secure worktree directory %s", segment)
				} else {
					err = fmt.Errorf("open secure worktree directory %s: %w", segment, err)
				}
			}
		}
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "wb-secure-directory"), nil
}

// OpenDirectoryAtNoFollow opens a single child directory under parentFD.
func OpenDirectoryAtNoFollow(parentFD int, name, descriptorName, openContext string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", openContext, err)
	}
	return os.NewFile(uintptr(fd), descriptorName), nil
}

// OpenOrCreateNoFollowDirectory opens or creates one child directory through real no-follow opens.
func OpenOrCreateNoFollowDirectory(parentFD int, name string) (int, error) {
	return OpenOrCreateNoFollowDirectoryWith(secureopen.Real{}, parentFD, name)
}

// OpenOrCreateNoFollowDirectoryWith opens or creates one child directory through opener.
func OpenOrCreateNoFollowDirectoryWith(opener secureopen.Opener, parentFD int, name string) (int, error) {
	if err := opener.Mkdir(parentFD, name); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create secure worktree directory %s: %w", name, err)
	}
	fd, err := opener.OpenDir(parentFD, name)
	if err != nil {
		if opener.IsSymlink(parentFD, name) {
			return -1, fmt.Errorf("refusing symlinked secure worktree directory %s", name)
		}
		return -1, fmt.Errorf("open secure worktree directory %s: %w", name, err)
	}
	return fd, nil
}

// DirectoryEmpty reports whether a held directory has no entries.
func DirectoryEmpty(directory *os.File) (bool, error) {
	if _, err := directory.Seek(0, 0); err != nil {
		return false, err
	}
	entries, err := directory.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return len(entries) == 0, nil
}

// RequireAbsentNoFollowChild rejects every existing child without following links.
func RequireAbsentNoFollowChild(parentFD int, name string) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		_ = unix.Close(fd)
	}
	if err != nil {
		return fmt.Errorf("inspect secure worktree destination %s: %w", name, err)
	}
	return fmt.Errorf("secure worktree destination already exists: %s", name)
}

// DirectoryStillMatches checks a lexical path against a held directory descriptor.
func DirectoryStillMatches(path string, directory *os.File) bool {
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() {
		return false
	}
	held, err := directory.Stat()
	return err == nil && os.SameFile(current, held)
}

// DirectoryEntryStillMatches checks a child entry against a held directory descriptor.
func DirectoryEntryStillMatches(parent *os.File, name string, directory *os.File) bool {
	if parent == nil || directory == nil {
		return false
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	candidate := os.NewFile(uintptr(fd), "wb-worktree-entry-check")
	defer func() { _ = candidate.Close() }()
	expected, expectedErr := directory.Stat()
	actual, actualErr := candidate.Stat()
	return expectedErr == nil && actualErr == nil && os.SameFile(expected, actual)
}

// NoFollowChildAbsent reports whether a child is absent without following links.
func NoFollowChildAbsent(parentFD int, name string) (bool, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, unix.Close(fd)
}

// MoveExpectedDirectoryNoReplace moves expected only after an authorization callback.
func MoveExpectedDirectoryNoReplace(fromDirectory *os.File, fromName string, toDirectory *os.File, toName string, expected *os.File, rename RenameNoReplace, afterAuthorization func(), afterMove ...func()) (*os.File, error) {
	var authorize func() error
	if afterAuthorization != nil {
		authorize = func() error { afterAuthorization(); return nil }
	}
	return MoveExpectedDirectoryNoReplaceAuthorized(fromDirectory, fromName, toDirectory, toName, expected, rename, authorize, afterMove...)
}

// MoveExpectedDirectoryNoReplaceAuthorized moves expected through a caller-owned no-replace primitive.
func MoveExpectedDirectoryNoReplaceAuthorized(fromDirectory *os.File, fromName string, toDirectory *os.File, toName string, expected *os.File, rename RenameNoReplace, authorize func() error, afterMove ...func()) (*os.File, error) {
	if !DirectoryEntryStillMatches(fromDirectory, fromName, expected) {
		return nil, fmt.Errorf("directory entry %s changed after inspection; refusing mutation", fromName)
	}
	if authorize != nil {
		if err := authorize(); err != nil {
			return nil, err
		}
	}
	if err := rename(int(fromDirectory.Fd()), fromName, int(toDirectory.Fd()), toName); err != nil {
		return nil, err
	}
	if len(afterMove) > 0 && afterMove[0] != nil {
		afterMove[0]()
	}
	moved, err := OpenDirectoryAtNoFollow(int(toDirectory.Fd()), toName, "wb-worktree-moved-directory", "open moved directory "+toName)
	if err != nil {
		return nil, err
	}
	expectedInfo, expectedErr := expected.Stat()
	movedInfo, movedErr := moved.Stat()
	expectedMoved := expectedErr == nil && movedErr == nil && os.SameFile(expectedInfo, movedInfo)
	sourceAbsent, absentErr := NoFollowChildAbsent(int(fromDirectory.Fd()), fromName)
	if absentErr != nil {
		_ = moved.Close()
		return nil, fmt.Errorf("inspect source %s after directory move: %w", fromName, absentErr)
	}
	if expectedMoved && sourceAbsent {
		return moved, nil
	}
	if !sourceAbsent {
		return moved, fmt.Errorf("%w: source %s was recreated after no-replace move", ErrDirectoryMoveIdentityChanged, fromName)
	}
	_ = moved.Close()
	if restoreErr := rename(int(toDirectory.Fd()), toName, int(fromDirectory.Fd()), fromName); restoreErr != nil {
		return nil, fmt.Errorf("%w: destination %s changed after inspection; preserve replacement: %v", ErrDirectoryMoveIdentityChanged, toName, restoreErr)
	}
	return nil, fmt.Errorf("%w: destination %s was not the expected directory", ErrDirectoryMoveIdentityChanged, toName)
}

// IdentityAt returns the no-follow device/inode identity of a directory child.
func IdentityAt(parentFD int, name string) (DirectoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return DirectoryIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return DirectoryIdentity{}, fmt.Errorf("%s is not a directory", name)
	}
	return DirectoryIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

// ReadPrivateRecordAt opens one private child and decodes its immutable JSON record.
func ReadPrivateRecordAt[T any](runDir *os.File, child, name string, valid ValidSegment) (T, error) {
	var zero T
	directory, err := OpenPrivateChild(runDir, child, false, valid)
	if err != nil {
		return zero, err
	}
	defer func() { _ = directory.Close() }()
	var record T
	if err := filewrite.ReadJSONAt(directory, name, &record); err != nil {
		return zero, err
	}
	return record, nil
}

// OpenPrivateChild opens a caller-validated child and hardens only create paths.
func OpenPrivateChild(parent *os.File, name string, create bool, valid ValidSegment) (*os.File, error) {
	return OpenPrivateChildWith(secureopen.Real{}, parent, name, create, valid)
}

// OpenPrivateChildWith opens a caller-validated child through opener and hardens create paths.
func OpenPrivateChildWith(opener secureopen.Opener, parent *os.File, name string, create bool, valid ValidSegment) (*os.File, error) {
	if valid == nil || !valid(name) {
		return nil, fmt.Errorf("unsafe private directory segment %q", name)
	}
	var fd int
	var err error
	if create {
		fd, err = OpenOrCreateNoFollowDirectoryWith(opener, int(parent.Fd()), name)
	} else {
		fd, err = opener.OpenDir(int(parent.Fd()), name)
	}
	if err != nil {
		return nil, err
	}
	if create {
		if err := unix.Fchmod(fd, 0o700); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
	}
	return os.NewFile(uintptr(fd), "wb-worklog-"+name), nil
}
