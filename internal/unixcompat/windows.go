//go:build windows

package unix

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	O_RDONLY    = 0
	O_WRONLY    = 1
	O_RDWR      = 2
	O_CREAT     = 0x40
	O_EXCL      = 0x80
	O_DIRECTORY = 0
	// These flags are interpreted by the compatibility adapters. Keep
	// O_NOFOLLOW non-zero so Openat can enforce it with Lstat before opening.
	O_NOFOLLOW          = 0x200000
	O_CLOEXEC           = 0
	O_NONBLOCK          = 0
	S_IFMT              = 0o170000
	S_IFREG             = 0o100000
	S_IFDIR             = 0o040000
	S_IFLNK             = 0o120000
	LOCK_EX             = 2
	LOCK_NB             = 4
	LOCK_UN             = 8
	AT_SYMLINK_NOFOLLOW = 0
	AT_REMOVEDIR        = 0
	F_GETFD             = 1
	F_SETFD             = 2
	FD_CLOEXEC          = 1
)

var EEXIST = os.ErrExist
var ENOENT = os.ErrNotExist
var EWOULDBLOCK = errors.New("operation would block")
var EAGAIN = EWOULDBLOCK
var EACCES = os.ErrPermission
var EPERM = os.ErrPermission
var ENOTEMPTY = errors.New("directory not empty")

type Stat_t struct {
	Dev, Ino, Nlink uint64
	Mode            uint32
	Size, Blocks    int64
}

var files struct {
	sync.Mutex
	paths map[int]string
}

func remember(f *os.File, path string) int {
	files.Lock()
	defer files.Unlock()
	if files.paths == nil {
		files.paths = map[int]string{}
	}
	fd := int(f.Fd())
	files.paths[fd] = path
	return fd
}
func pathOf(fd int) string { files.Lock(); defer files.Unlock(); return files.paths[fd] }
func Open(path string, flags, mode int) (int, error) {
	f, err := os.OpenFile(path, openFlags(flags), os.FileMode(mode))
	if err != nil {
		return -1, err
	}
	return remember(f, path), nil
}
func Openat(dirfd int, name string, flags, mode uint32) (int, error) {
	dir := pathOf(dirfd)
	if dir == "" {
		return -1, errors.New("unknown directory handle")
	}
	path := filepath.Join(dir, name)
	if flags&O_NOFOLLOW != 0 {
		info, err := os.Lstat(path)
		if err != nil {
			return -1, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return -1, errors.New("symbolic link refused")
		}
	}
	return Open(path, int(flags), int(mode))
}
func openFlags(flags int) int {
	result := os.O_RDONLY
	switch flags & 3 {
	case O_WRONLY:
		result = os.O_WRONLY
	case O_RDWR:
		result = os.O_RDWR
	}
	if flags&O_CREAT != 0 {
		result |= os.O_CREATE
	}
	if flags&O_EXCL != 0 {
		result |= os.O_EXCL
	}
	return result
}
func Close(fd int) error {
	files.Lock()
	delete(files.paths, fd)
	files.Unlock()
	return os.NewFile(uintptr(fd), "").Close()
}
func Fstat(fd int, stat *Stat_t) error {
	f := os.NewFile(uintptr(fd), "")
	if f == nil {
		return os.ErrInvalid
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat.Size = info.Size()
	stat.Mode = uint32(info.Mode().Perm())
	if info.IsDir() {
		stat.Mode |= S_IFDIR
	} else {
		stat.Mode |= S_IFREG
		stat.Mode = (stat.Mode & S_IFMT) | 0o644
	}
	stat.Nlink = 1
	return nil
}
func Lstat(path string, stat *Stat_t) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat.Size = info.Size()
	stat.Mode = uint32(info.Mode().Perm())
	if info.Mode()&os.ModeSymlink != 0 {
		stat.Mode |= S_IFLNK
	} else if info.Mode().IsRegular() {
		stat.Mode |= S_IFREG
		stat.Mode = (stat.Mode & S_IFMT) | 0o644
	}
	stat.Nlink = 1
	return nil
}
func Fstatat(dirfd int, name string, stat *Stat_t, flags int) error {
	return Lstat(filepath.Join(pathOf(dirfd), name), stat)
}
func Fchdir(fd int) error                            { return os.Chdir(pathOf(fd)) }
func FcntlInt(fd uintptr, cmd, arg int) (int, error) { return 0, nil }
func Renameat(olddirfd int, oldname string, newdirfd int, newname string) error {
	return os.Rename(filepath.Join(pathOf(olddirfd), oldname), filepath.Join(pathOf(newdirfd), newname))
}
func Fchmod(fd int, mode uint32) error { return nil }
func Fsync(fd int) error               { return os.NewFile(uintptr(fd), "").Sync() }
func Flock(fd, op int) error           { return nil }
func Dup(fd int) (int, error) {
	f := os.NewFile(uintptr(fd), "")
	if f == nil {
		return -1, os.ErrInvalid
	}
	dup, err := os.OpenFile(pathOf(fd), os.O_RDWR, 0)
	if err != nil {
		return -1, err
	}
	return remember(dup, pathOf(fd)), nil
}
func CloseOnExec(fd int) {}
func Mkdirat(dirfd int, name string, mode uint32) error {
	return os.Mkdir(filepath.Join(pathOf(dirfd), name), os.FileMode(mode))
}
func Unlinkat(dirfd int, name string, flags int) error {
	return os.Remove(filepath.Join(pathOf(dirfd), name))
}
func Linkat(olddirfd int, oldname string, newdirfd int, newname string, flags int) error {
	return os.Link(filepath.Join(pathOf(olddirfd), oldname), filepath.Join(pathOf(newdirfd), newname))
}

func SyncDirectory(file *os.File) error { return nil }
