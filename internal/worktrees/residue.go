package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

// Residue is what a failed `git worktree remove` leaves on disk.
//
// A non-zero exit from that command is not necessarily a refusal. Git deletes
// the working tree first and the registration second, and it deletes the
// registration even when the tree delete failed partway: it records the error,
// continues, and exits non-zero. A single unwritable directory inside an
// ignored build tree — node_modules, dist, .venv — is enough, and the bigger
// the tree the likelier it is. What survives is a checkout no registration
// owns: invisible to every enumeration that reads Git's worktree registry, and
// read back by WB's own directory walk as a candidate that "is not a Git
// worktree root", which blocks the task instead of finishing it.
//
// WB is the only thing that can finish it. By the time cleanup reaches removal
// the tree is clean by Git's own definition, its head is integrated into the
// exact origin target, and the path is one WB created under its own worktrees
// root and still holds a validated descriptor for. That is what authorizes the
// removal — not the name of any directory inside it. There is deliberately no
// list of disposable directory names: such a list would be a second, weaker
// gate in front of the real one, always one entry short of the next build tool,
// and it would authorize deleting node_modules inside a checkout that failed
// the gate that matters.

// residueRemovalMaxDepth bounds the descent. Real dependency trees nest far
// below this; anything deeper is a loop or a filesystem WB should not be
// walking, and refusing is better than never returning.
const residueRemovalMaxDepth = 128

// worktreeRemovalLeftResidue separates the two failures a non-zero Git removal
// can mean. A worktree Git still lists was refused, and a refusal is never
// WB's to override behind Git's back. One Git no longer lists was unregistered
// by the command that then failed to finish deleting it.
func worktreeRemovalLeftResidue(ctx context.Context, canonical *canonicalRepository, worktreePath string) (bool, error) {
	registrations, err := registeredWorktreePathsCanonical(ctx, canonical)
	if err != nil {
		return false, err
	}
	return !registrations[filepath.Clean(worktreePath)], nil
}

// worktreeStillRegistered answers the same question for a caller that holds a
// backlog record rather than an open canonical repository.
func worktreeStillRegistered(ctx context.Context, canonicalDir, worktreePath string) (bool, error) {
	canonical, err := openCanonicalRepository(canonicalDir)
	if err != nil {
		return false, err
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return false, err
	}
	residue, err := worktreeRemovalLeftResidue(ctx, canonical, worktreePath)
	if err != nil {
		return false, err
	}
	return !residue, nil
}

// removeWorktreeResidue deletes the unregistered checkout through the
// descriptors cleanup already holds, so every step stays anchored to the
// worktree WB validated rather than to a name that could be replaced under it.
// The caller must have established that the path still exists and that Git no
// longer registers it.
func removeWorktreeResidue(handle *cleanupWorktreeHandle) error {
	if handle == nil || handle.parent == nil || handle.worktree == nil {
		return fmt.Errorf("cleanup worktree descriptor is unavailable")
	}
	if err := handle.validate(); err != nil {
		return err
	}
	if err := removeDirectoryContentsAt(handle.worktree, handle.worktreePath, 0); err != nil {
		return err
	}
	name := filepath.Base(handle.worktreePath)
	if err := unix.Unlinkat(int(handle.parent.Fd()), name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove residual worktree %s: %w", handle.worktreePath, err)
	}
	return nil
}

// removeUnregisteredWorktreeResidue removes the residue when there is any, and
// reports whether it did. A path Git already finished deleting before it failed
// for some other reason leaves nothing to own and is not an error.
func removeUnregisteredWorktreeResidue(handle *cleanupWorktreeHandle, worktreePath string) (bool, error) {
	if _, err := os.Lstat(worktreePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect residual worktree %s: %w", worktreePath, err)
	}
	if err := removeWorktreeResidue(handle); err != nil {
		return false, err
	}
	return true, nil
}

// removeDirectoryContentsAt empties directory, which the caller owns and holds
// open. path names it for diagnostics only; every operation goes through the
// descriptor.
func removeDirectoryContentsAt(directory *os.File, path string, depth int) error {
	if depth >= residueRemovalMaxDepth {
		return fmt.Errorf("residue %s nests more than %d directories deep", path, residueRemovalMaxDepth)
	}
	names, err := directoryEntryNames(directory, path)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := removeResidueEntry(directory, path, name, depth); err != nil {
			return err
		}
	}
	return nil
}

// directoryEntryNames lists through a fresh descriptor rather than the retained
// one, whose read offset belongs to the caller.
func directoryEntryNames(directory *os.File, path string) ([]string, error) {
	descriptor, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("list residue directory %s: %w", path, err)
	}
	listing := os.NewFile(uintptr(descriptor), path)
	if listing == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("wrap residue directory %s", path)
	}
	defer func() { _ = listing.Close() }()
	names, err := listing.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("list residue directory %s: %w", path, err)
	}
	return names, nil
}

func removeResidueEntry(parent *os.File, parentPath, name string, depth int) error {
	entryPath := filepath.Join(parentPath, name)
	var status unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &status, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("inspect residue %s: %w", entryPath, err)
	}
	// A symlink is unlinked, never followed: residue removal stays inside the
	// worktree even when a dependency tree links out of it.
	if status.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unlinkResidueEntry(parent, parentPath, name, 0)
	}
	child, err := openResidueDirectory(parent, entryPath, name, status)
	if err != nil {
		return err
	}
	contentsErr := removeDirectoryContentsAt(child, entryPath, depth+1)
	_ = child.Close()
	if contentsErr != nil {
		return contentsErr
	}
	return unlinkResidueEntry(parent, parentPath, name, unix.AT_REMOVEDIR)
}

// openResidueDirectory descends without following a link and proves the
// descriptor it returns is the inode it just inspected, so a directory swapped
// in during the walk is surfaced rather than descended into.
func openResidueDirectory(parent *os.File, path, name string, expected unix.Stat_t) (*os.File, error) {
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.EACCES) {
			// Granting this would mean a chmod by name, the one step that
			// cannot be anchored to a descriptor. Ask for it explicitly
			// instead: WB refusing here costs a command, and a by-name chmod
			// inside a tree WB is midway through deleting is a hole in the
			// containment every other step maintains.
			return nil, fmt.Errorf("residue directory %s denies WB the read and search permission its removal needs; grant it with chmod u+rx %s and run cleanup again: %w", path, path, err)
		}
		return nil, fmt.Errorf("open residue directory %s: %w", path, err)
	}
	directory := os.NewFile(uintptr(descriptor), path)
	if directory == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("wrap residue directory %s", path)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(descriptor, &opened); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect residue directory %s: %w", path, err)
	}
	if opened.Dev != expected.Dev || opened.Ino != expected.Ino {
		_ = directory.Close()
		return nil, fmt.Errorf("residue directory %s was replaced while WB removed it", path)
	}
	return directory, nil
}

// unlinkResidueEntry removes one entry, granting the containing directory owner
// write permission when that is what denies the unlink. This is the failure
// that stranded the task in the first place: the mode that stopped Git is
// carried by a directory inside WB's own residue, and WB holds it open.
func unlinkResidueEntry(parent *os.File, parentPath, name string, flags int) error {
	entryPath := filepath.Join(parentPath, name)
	err := unix.Unlinkat(int(parent.Fd()), name, flags)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("remove residue %s: %w", entryPath, err)
	}
	if grantErr := grantOwnerWriteAt(parent, parentPath); grantErr != nil {
		return fmt.Errorf("remove residue %s: %w", entryPath, errors.Join(err, grantErr))
	}
	if retryErr := unix.Unlinkat(int(parent.Fd()), name, flags); retryErr != nil && !errors.Is(retryErr, unix.ENOENT) {
		return fmt.Errorf("remove residue %s after granting %s owner write permission: %w", entryPath, parentPath, retryErr)
	}
	return nil
}

func grantOwnerWriteAt(directory *os.File, path string) error {
	var status unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &status); err != nil {
		return fmt.Errorf("inspect residue directory %s: %w", path, err)
	}
	if err := unix.Fchmod(int(directory.Fd()), uint32(status.Mode&0o7777|0o300)); err != nil {
		return fmt.Errorf("grant owner write permission on residue directory %s: %w", path, err)
	}
	return nil
}
