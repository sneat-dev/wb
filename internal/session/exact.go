package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

const maxExactSessionRecordBytes = 64 << 10

// LookupExact reads one live registration through no-follow descriptors and
// proves that the filename, payload PID, and current process all agree. It is
// used at the tmux delivery boundary where a path-following convenience read
// would let a swapped record redirect message bytes.
func LookupExact(dir string, pid int) (Record, bool, error) {
	if pid <= 0 {
		return Record{}, false, fmt.Errorf("exact session lookup requires a positive PID")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil || absolute != dir || filepath.Clean(absolute) != absolute {
		return Record{}, false, fmt.Errorf("session directory must be one clean absolute path")
	}
	directoryFD, err := unix.Open(absolute, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Record{}, false, fmt.Errorf("open exact session directory: %w", err)
	}
	directory := os.NewFile(uintptr(directoryFD), "wb-session-exact-directory")
	if directory == nil {
		_ = unix.Close(directoryFD)
		return Record{}, false, fmt.Errorf("wrap exact session directory")
	}
	defer func() { _ = directory.Close() }()
	var directoryStat unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryStat); err != nil || directoryStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		if err != nil {
			return Record{}, false, fmt.Errorf("inspect exact session directory: %w", err)
		}
		return Record{}, false, fmt.Errorf("exact session directory is not a directory")
	}

	name := strconv.Itoa(pid) + ".json"
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Record{}, false, fmt.Errorf("open exact session record: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wb-session-exact-record")
	if file == nil {
		_ = unix.Close(fd)
		return Record{}, false, fmt.Errorf("wrap exact session record")
	}
	defer func() { _ = file.Close() }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG ||
		before.Mode&0o777 != 0o644 || before.Nlink != 1 || before.Size < 0 || before.Size > maxExactSessionRecordBytes {
		if err != nil {
			return Record{}, false, fmt.Errorf("inspect exact session record: %w", err)
		}
		return Record{}, false, fmt.Errorf("exact session record is not one single-link bounded regular mode 0644 file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxExactSessionRecordBytes+1))
	if err != nil || len(raw) > maxExactSessionRecordBytes {
		if err != nil {
			return Record{}, false, fmt.Errorf("read exact session record: %w", err)
		}
		return Record{}, false, fmt.Errorf("exact session record exceeds %d bytes", maxExactSessionRecordBytes)
	}
	if int64(len(raw)) != before.Size {
		return Record{}, false, fmt.Errorf("exact session record size changed while read")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Record{}, false, fmt.Errorf("rewind exact session record: %w", err)
	}
	verification, err := io.ReadAll(io.LimitReader(file, maxExactSessionRecordBytes+1))
	if err != nil {
		return Record{}, false, fmt.Errorf("verify exact session record: %w", err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino ||
		before.Mode != after.Mode || before.Nlink != after.Nlink || before.Size != after.Size || !bytes.Equal(raw, verification) {
		if err != nil {
			return Record{}, false, fmt.Errorf("reinspect exact session record: %w", err)
		}
		return Record{}, false, fmt.Errorf("exact session record changed while verified")
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil || record.PID != pid {
		if err != nil {
			return Record{}, false, fmt.Errorf("decode exact session record: %w", err)
		}
		return Record{}, false, fmt.Errorf("exact session record PID %d does not match filename PID %d", record.PID, pid)
	}
	if record.NativeHarnessID == "" {
		record.NativeHarnessID = record.AgentID
	}
	return record, state(pid) == StateLive, nil
}
