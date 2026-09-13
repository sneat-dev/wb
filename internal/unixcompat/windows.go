//go:build windows

package unix

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	win "golang.org/x/sys/windows"
)

const (
	O_RDONLY    = 0
	O_WRONLY    = 1
	O_RDWR      = 2
	O_CREAT     = 0x40
	O_EXCL      = 0x80
	O_DIRECTORY = 0x100000
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
	LOCK_SH             = 1
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

func rememberHandle(handle win.Handle, path string) int {
	files.Lock()
	defer files.Unlock()
	if files.paths == nil {
		files.paths = map[int]string{}
	}
	fd := int(handle)
	files.paths[fd] = path
	return fd
}
func pathOf(fd int) string { files.Lock(); defer files.Unlock(); return files.paths[fd] }
func Open(path string, flags, _ int) (int, error) {
	return openWindows(path, flags, flags&O_NOFOLLOW != 0)
}
func Openat(dirfd int, name string, flags, mode uint32) (int, error) {
	dir := pathOf(dirfd)
	if dir == "" {
		return -1, errors.New("unknown directory handle")
	}
	path := filepath.Join(dir, name)
	return Open(path, int(flags), int(mode))
}

func openNoFollow(path string, flags int) (int, error) {
	return openWindows(path, flags, true)
}

func openWindows(path string, flags int, noFollow bool) (int, error) {
	name, err := win.UTF16PtrFromString(path)
	if err != nil {
		return -1, err
	}
	access := uint32(win.GENERIC_READ)
	switch flags & (O_WRONLY | O_RDWR) {
	case O_WRONLY:
		access = win.GENERIC_WRITE
	case O_RDWR:
		access = win.GENERIC_READ | win.GENERIC_WRITE
	}
	if flags&O_CREAT != 0 {
		access |= win.GENERIC_WRITE
	}
	creation := uint32(win.OPEN_EXISTING)
	switch {
	case flags&(O_CREAT|O_EXCL) == O_CREAT|O_EXCL:
		creation = win.CREATE_NEW
	case flags&O_CREAT != 0:
		creation = win.OPEN_ALWAYS
	}
	attributes := uint32(win.FILE_ATTRIBUTE_NORMAL)
	if noFollow {
		attributes |= win.FILE_FLAG_OPEN_REPARSE_POINT
	}
	if flags&O_DIRECTORY != 0 {
		attributes |= win.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := win.CreateFile(name, access, win.FILE_SHARE_READ|win.FILE_SHARE_WRITE, nil, creation, attributes, 0)
	if err != nil {
		return -1, err
	}
	var info win.ByHandleFileInformation
	if err := win.GetFileInformationByHandle(handle, &info); err != nil {
		_ = win.CloseHandle(handle)
		return -1, err
	}
	if noFollow && info.FileAttributes&win.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = win.CloseHandle(handle)
		return -1, errors.New("symbolic link or reparse point refused")
	}
	return rememberHandle(handle, path), nil
}
func Close(fd int) error {
	files.Lock()
	delete(files.paths, fd)
	files.Unlock()
	return win.CloseHandle(win.Handle(fd))
}
func Fstat(fd int, stat *Stat_t) error {
	var info win.ByHandleFileInformation
	if err := win.GetFileInformationByHandle(win.Handle(fd), &info); err != nil {
		return err
	}
	// Keep identity and link-count semantics aligned with Lstat/Fstatat. The
	// Windows compatibility adapter historically reports zero identities and a
	// single link, and callers compare path and handle results.
	stat.Dev = 0
	stat.Ino = 0
	stat.Nlink = 1
	stat.Size = int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow))
	if info.FileAttributes&win.FILE_ATTRIBUTE_DIRECTORY != 0 {
		stat.Mode = S_IFDIR | 0o777
	} else {
		stat.Mode = S_IFREG | 0o644
	}
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
func Fsync(fd int) error               { return win.FlushFileBuffers(win.Handle(fd)) }
func Flock(fd, op int) error           { return nil }
func Dup(fd int) (int, error) {
	process, err := win.GetCurrentProcess()
	if err != nil {
		return -1, err
	}
	var duplicate win.Handle
	if err := win.DuplicateHandle(process, win.Handle(fd), process, &duplicate, 0, false, win.DUPLICATE_SAME_ACCESS); err != nil {
		return -1, err
	}
	return rememberHandle(duplicate, pathOf(fd)), nil
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
