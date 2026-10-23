package sessionpark

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/session"
)

// Helpers kept for tests only: no production caller remains.

// ContinuationPathUnderLock returns the deterministic private local-resume
// artifact only after re-reading it through the retained aggregate descriptor.
// Callers must pass it through WB_SESSION_CONTINUATION_FILE, never stdout or
// harness argv.
func (s Store) ContinuationPathUnderLock(lock *SourceLock) (string, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return "", fmt.Errorf("read parked continuation requires retained source authority")
	}
	raw, err := readPrivateRegularAt(lock.aggregate, sourceContinuationFileName, MaxContinuationBytes)
	if err != nil || !bytes.Equal(raw, []byte(lock.bundle.Continuation)) {
		return "", fmt.Errorf("private parked continuation conflicts with exact source bundle")
	}
	return filepath.Join(s.Root, lock.parkID, sourceContinuationFileName), nil
}

func (s Store) Resume(id string, successor session.Record, now time.Time) (State, error) {
	lock, err := s.Acquire(context.Background(), id)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = lock.Close() }()
	if _, _, err := s.PrepareLocalUnderLock(lock, now); err != nil {
		return State{}, err
	}
	return s.ResumeUnderLock(lock, successor, now)
}
