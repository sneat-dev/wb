package diskprune

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrLocked is returned when another prune holds a lock prune needs.
var ErrLocked = errors.New("another prune is running")

// Lock is a held flock on a directory; Release drops it.
type Lock struct {
	release func()
}

// Release drops the lock. It is safe on a nil lock.
func (l *Lock) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}

// LockedError names who holds a lock, when /proc/locks shows it.
type LockedError struct {
	Path string
	PID  int
}

func (e *LockedError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("%s: held by pid %d", e.Path, e.PID)
	}
	return e.Path + ": held by another process"
}

func (e *LockedError) Unwrap() error { return ErrLocked }

// lockHolder reads /proc/locks for the PID holding a FLOCK on the inode, so
// the refusal can name it. The PID is best-effort: zero when not visible.
func lockHolder(procLocks string, dev uint64, ino uint64) int {
	raw, err := os.ReadFile(procLocks)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		// 1: FLOCK  ADVISORY  WRITE 1234 08:01:131074 0 EOF
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[1] != "FLOCK" {
			continue
		}
		parts := strings.Split(fields[5], ":")
		if len(parts) != 3 {
			continue
		}
		major, errMajor := strconv.ParseUint(parts[0], 16, 32)
		minor, errMinor := strconv.ParseUint(parts[1], 16, 32)
		inode, errInode := strconv.ParseUint(parts[2], 10, 64)
		if errMajor != nil || errMinor != nil || errInode != nil || inode != ino || makedev(major, minor) != dev {
			continue
		}
		if pid, err := strconv.Atoi(fields[4]); err == nil {
			return pid
		}
	}
	return 0
}

// makedev composes a Linux device number from major and minor the way the
// kernel's new_encode_dev does, to compare with a stat's st_dev.
func makedev(major, minor uint64) uint64 {
	return (minor & 0xff) | (major&0xfff)<<8 | (minor&^0xff)<<12 | (major&^0xfff)<<32
}

// lockPaths is the deduplicated, cleaned set of directories to lock.
func lockPaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out
}
