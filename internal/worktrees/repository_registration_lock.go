package worktrees

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"time"
)

const repositoryRegistrationLockName = worktreeclaims.RepositoryRegistrationLockName

var repositoryRegistrationLockTimeout = 2 * time.Minute

type repositoryRegistrationLock struct {
	held *worktreeclaims.RepositoryRegistrationLock
}

func acquireRepositoryRegistrationLock(canonical *canonicalRepository, now func() time.Time, sleep func(time.Duration)) (*repositoryRegistrationLock, error) {
	if canonical == nil {
		return nil, fmt.Errorf("repository registration lock requires canonical Git descriptor")
	}
	held, err := worktreeclaims.AcquireRepositoryRegistrationLock(canonical.common, canonical.validate, now, sleep, repositoryRegistrationLockTimeout)
	if err != nil {
		return nil, err
	}
	return &repositoryRegistrationLock{held: held}, nil
}
func (lock *repositoryRegistrationLock) release() error {
	if lock == nil {
		return nil
	}
	return lock.held.Release()
}
