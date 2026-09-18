package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// migrationLockName is a single lock file under <root>/.wb/layout-migrations/
// that serializes every `--apply` run (migrate or undo) against one projects
// root. Two runs interleaving their moves is exactly the hazard a shared
// multi-agent machine creates: one run's plan is computed against a
// filesystem state a second run then rearranges underneath it.
const migrationLockName = "migrate.lock"

type migrationLock struct {
	file *os.File
}

// acquireMigrationLock takes an exclusive, non-blocking lock on the single
// migration lock file for root. A second concurrent `--apply` fails
// immediately with a clear message instead of blocking or interleaving.
// flock releases automatically if the holding process dies, so a crashed run
// never strands the lock.
func acquireMigrationLock(root string) (*migrationLock, error) {
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(home, migrationsDirName)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("prepare migration lock directory: %w", err)
	}
	path := filepath.Join(directory, migrationLockName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open migration lock %s: %w", path, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another `wb layout migrate --apply` is already running against %s (lock: %s); wait for it to finish", root, path)
		}
		return nil, fmt.Errorf("hold migration lock %s: %w", path, err)
	}
	return &migrationLock{file: file}, nil
}

func (lock *migrationLock) release() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	_ = lock.file.Close()
}
