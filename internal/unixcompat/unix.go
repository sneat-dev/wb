//go:build !windows

package unix

import "golang.org/x/sys/unix"

const (
	O_RDONLY            = unix.O_RDONLY
	O_WRONLY            = unix.O_WRONLY
	O_RDWR              = unix.O_RDWR
	O_CREAT             = unix.O_CREAT
	O_EXCL              = unix.O_EXCL
	O_DIRECTORY         = unix.O_DIRECTORY
	O_NOFOLLOW          = unix.O_NOFOLLOW
	O_CLOEXEC           = unix.O_CLOEXEC
	O_NONBLOCK          = unix.O_NONBLOCK
	S_IFMT              = unix.S_IFMT
	S_IFREG             = unix.S_IFREG
	S_IFDIR             = unix.S_IFDIR
	LOCK_EX             = unix.LOCK_EX
	LOCK_NB             = unix.LOCK_NB
	LOCK_UN             = unix.LOCK_UN
	AT_SYMLINK_NOFOLLOW = unix.AT_SYMLINK_NOFOLLOW
	AT_REMOVEDIR        = unix.AT_REMOVEDIR
	S_IFLNK             = unix.S_IFLNK
	F_GETFD             = unix.F_GETFD
	F_SETFD             = unix.F_SETFD
	FD_CLOEXEC          = unix.FD_CLOEXEC
)

type Stat_t = unix.Stat_t

var EEXIST = unix.EEXIST
var ENOENT = unix.ENOENT
var EWOULDBLOCK = unix.EWOULDBLOCK
var EAGAIN = unix.EAGAIN
var EACCES = unix.EACCES
var EPERM = unix.EPERM
var ENOTEMPTY = unix.ENOTEMPTY
var Open = unix.Open
var Openat = unix.Openat
var Fstatat = unix.Fstatat
var Fchdir = unix.Fchdir
var FcntlInt = unix.FcntlInt
var Renameat = unix.Renameat
var Close = unix.Close
var Fstat = unix.Fstat
var Lstat = unix.Lstat
var Fchmod = unix.Fchmod
var Fsync = unix.Fsync
var Flock = unix.Flock
var Dup = unix.Dup
var CloseOnExec = unix.CloseOnExec
var Mkdirat = unix.Mkdirat
var Unlinkat = unix.Unlinkat
var Linkat = unix.Linkat
