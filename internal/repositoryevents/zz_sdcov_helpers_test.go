package repositoryevents

// Shared fixtures for the sdCov* coverage tests. Every identifier introduced
// here is prefixed with sdCov to stay clear of the package's older helpers.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

// sdCovSkipIfPrivileged skips tests whose only lever is POSIX permission
// denial. Root ignores mode bits and Windows does not enforce them, so the
// expected error cannot be produced there.
func sdCovSkipIfPrivileged(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("filesystem permission denial is not enforceable for the superuser")
	}
}

func sdCovQueueEvent(id string) repositoryevent.Event {
	return repositoryevent.Event{
		Version:    repositoryevent.ContractVersion,
		ID:         id,
		Repository: "github.com/acme/app",
		Ref:        "refs/heads/main",
		Reason:     repositoryevent.ReasonDefaultBranchUpdated,
	}
}

func sdCovJobsDir(projectsRoot string) string {
	return filepath.Join(projectsRoot, ".wb", "runtime", "daemon", "repository-events", "jobs")
}

// sdCovValidJob builds a record that validateJobRecord accepts.
func sdCovValidJob(event repositoryevent.Event, sequence uint64) job {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return job{
		Schema:      queueSchema,
		Sequence:    sequence,
		Event:       event,
		EventDigest: eventDigest(event),
		State:       "queued",
		QueuedAt:    now,
		UpdatedAt:   now,
	}
}

// sdCovWriteRecord writes a job record under an arbitrary file name. The
// queue only requires the ".json" extension, not a hash-derived name.
func sdCovWriteRecord(t *testing.T, directory, name string, item job) {
	t.Helper()
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// sdCovSource is a scripted Source seam. Both halves are optional callbacks so
// a test can shape the exact poll/ack sequence the receiver observes.
type sdCovSource struct {
	mu    sync.Mutex
	waits []time.Duration
	acks  []repositoryevent.AckRequest

	poll func(cursor string, limit int, wait time.Duration) (repositoryevent.PollResponse, error)
	ack  func(repositoryevent.AckRequest) (repositoryevent.AckResponse, error)
}

func (source *sdCovSource) PollRepositoryEvents(_ context.Context, cursor string, limit int, wait time.Duration) (repositoryevent.PollResponse, error) {
	source.mu.Lock()
	source.waits = append(source.waits, wait)
	source.mu.Unlock()
	if source.poll != nil {
		return source.poll(cursor, limit, wait)
	}
	return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: cursor, NextCursor: cursor}, nil
}

func (source *sdCovSource) AckRepositoryEvents(_ context.Context, request repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
	source.mu.Lock()
	source.acks = append(source.acks, request)
	source.mu.Unlock()
	if source.ack != nil {
		return source.ack(request)
	}
	return repositoryevent.AckResponse{Version: repositoryevent.ContractVersion, Cursor: request.Cursor}, nil
}

func (source *sdCovSource) recordedWaits() []time.Duration {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]time.Duration(nil), source.waits...)
}

func (source *sdCovSource) recordedAcks() []repositoryevent.AckRequest {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]repositoryevent.AckRequest(nil), source.acks...)
}

// sdCovEmptyPoll answers a poll without events and without moving the cursor,
// which keeps ReceiveOnce in its "nothing to do" branch.
func sdCovEmptyPoll(cursor string, _ int, _ time.Duration) (repositoryevent.PollResponse, error) {
	return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: cursor, NextCursor: cursor}, nil
}
