package worktreeclaims

import (
	"errors"
	"fmt"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"os"
	"time"
)

const RepositoryRegistrationLockName = "wb-worktree-registration.lock"

type RepositoryRegistrationLock struct{ file *os.File }

type RepositoryRegistrationPorts struct {
	Openat func(int, string, int, uint32) (int, error)
	Flock  func(int, int) error
}

func AcquireRepositoryRegistrationLock(common *os.File, validate func() error, now func() time.Time, sleep func(time.Duration), timeout time.Duration, options ...RepositoryRegistrationPorts) (*RepositoryRegistrationLock, error) {
	ports := RepositoryRegistrationPorts{Openat: unix.Openat, Flock: unix.Flock}
	if len(options) > 0 {
		if options[0].Openat != nil {
			ports.Openat = options[0].Openat
		}
		if options[0].Flock != nil {
			ports.Flock = options[0].Flock
		}
	}
	if common == nil {
		return nil, fmt.Errorf("repository registration lock requires canonical Git descriptor")
	}
	deadline := now().Add(timeout)
	var file *os.File
	var err error
	for {
		fd, openErr := ports.Openat(
			int(common.Fd()), RepositoryRegistrationLockName,
			unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		err = openErr
		if err == nil {
			file = os.NewFile(uintptr(fd), "wb-repository-registration-lock")
			break
		}
		if !errors.Is(err, unix.ENOENT) {
			return nil, fmt.Errorf("open repository registration lock: %w", err)
		}
		// Darwin can report a transient ENOENT when two descriptors create this
		// new repository-local file at the same time. Retry only while the held
		// canonical descriptor still proves that the .git path is unchanged;
		// a substituted or removed Git directory remains a hard failure.
		if validateErr := validate(); validateErr != nil {
			return nil, fmt.Errorf("open repository registration lock after canonical validation: %w", validateErr)
		}
		if now().After(deadline) {
			return nil, fmt.Errorf("open repository registration lock after %s: %w", timeout, err)
		}
		sleep(20 * time.Millisecond)
	}
	for {
		if now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another WB Git mutation held the repository registration lock for %s", timeout)
		}
		fd := int(file.Fd())
		err = ports.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("hold repository registration lock: %w", err)
		}
		sleep(20 * time.Millisecond)
	}
	return &RepositoryRegistrationLock{file: file}, nil
}

func (lock *RepositoryRegistrationLock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := lock.file.Close()
	lock.file = nil
	return err
}
