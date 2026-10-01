package worktreejournal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

const (
	JournalRootDirectory  = ".wb"
	JournalLocalDirectory = "local"
)

// DirectoryStore scopes descriptor operations used by secure journal traversal.
type DirectoryStore struct {
	OpenRoot      func(path string, create bool) (*os.File, error)
	OpenComponent func(parentFD int, name string, create bool) (int, error)
	Chmod         func(fd int, mode uint32) error
	Matches       func(path string, directory *os.File) bool
}

func (s DirectoryStore) root(path string, create bool) (*os.File, error) {
	if s.OpenRoot != nil {
		return s.OpenRoot(path, create)
	}
	return worktreesecure.OpenAbsoluteDirectoryNoFollow(path, create)
}
func (s DirectoryStore) component(parentFD int, name string, create bool) (int, error) {
	if s.OpenComponent != nil {
		return s.OpenComponent(parentFD, name, create)
	}
	return s.OpenJournalComponent(parentFD, name, create)
}
func (s DirectoryStore) chmod(fd int, mode uint32) error {
	if s.Chmod != nil {
		return s.Chmod(fd, mode)
	}
	return unix.Fchmod(fd, mode)
}
func (s DirectoryStore) matches(path string, directory *os.File) bool {
	if s.Matches != nil {
		return s.Matches(path, directory)
	}
	return worktreesecure.DirectoryStillMatches(path, directory)
}
func OpenJournalDirectory(worktree string, create bool) (*os.File, error) {
	return (DirectoryStore{}).OpenJournalDirectory(worktree, create)
}
func OpenJournalComponent(parentFD int, name string, create bool) (int, error) {
	return (DirectoryStore{}).OpenJournalComponent(parentFD, name, create)
}
func OpenJournalSubdirectory(worktree, name string, create bool) (*os.File, error) {
	return (DirectoryStore{}).OpenJournalSubdirectory(worktree, name, create)
}

func (s DirectoryStore) OpenJournalDirectory(worktree string, create bool) (*os.File, error) {
	root, err := s.root(worktree, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	wbFD, err := s.component(int(root.Fd()), JournalRootDirectory, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(wbFD) }()

	localFD, err := s.component(wbFD, JournalLocalDirectory, create)
	if err != nil {
		return nil, err
	}
	// Only the WB-owned directory is tightened, and only when this call
	// created it: fchmod is a metadata write, and the read path opened the
	// descriptor O_RDONLY, so a sandbox that denies writes outside the
	// workspace would report a read as a denied write. The repository's own
	// .wb holds tracked policy files whose mode belongs to the repository, not
	// to WB either way.
	if create {
		if err := s.chmod(localFD, 0o700); err != nil {
			_ = unix.Close(localFD)
			return nil, err
		}
	}
	directory := os.NewFile(uintptr(localFD), "wb-journal")
	path := filepath.Join(worktree, JournalRootDirectory, JournalLocalDirectory)
	if !s.matches(path, directory) {
		_ = directory.Close()
		return nil, fmt.Errorf("work-log journal directory path changed: %s", path)
	}
	return directory, nil
}

func (s DirectoryStore) OpenJournalComponent(parentFD int, name string, create bool) (int, error) {
	var fd int
	var err error
	if create {
		fd, err = worktreesecure.OpenOrCreateNoFollowDirectory(parentFD, name)
	} else {
		fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	if errors.Is(err, unix.ENOENT) {
		return 0, os.ErrNotExist
	}
	if err != nil {
		return 0, fmt.Errorf("open work-log journal component %s: %w", name, err)
	}
	return fd, nil
}

func (s DirectoryStore) OpenJournalSubdirectory(worktree, name string, create bool) (*os.File, error) {
	journal, err := s.OpenJournalDirectory(worktree, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = journal.Close() }()

	fd, err := s.component(int(journal.Fd()), name, create)
	if err != nil {
		return nil, err
	}
	// Creating path only, for the same reason as openJournalDirectory: the read
	// path's descriptor is O_RDONLY and fchmod on it is a metadata write.
	if create {
		if err := s.chmod(fd, 0o700); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
	}
	directory := os.NewFile(uintptr(fd), "wb-journal-"+name)
	return directory, nil
}
