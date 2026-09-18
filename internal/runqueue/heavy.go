package runqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

// This file implements the adaptive heavy-job queue for a large machine
// (numCPU >= smallMachineThreshold). Founder 2026-09-18 (sneat-dev/wb#621),
// after watching three agent lanes test concurrently at a one-minute load of
// 4.86 on an 18-core Mac (86% idle): "Can we make it smarter? If no queue we
// can start 100%. If something is running and queue has more than 2 items
// start 2 with 2/3. If all finished and in queue only one item start with
// 100%. If 3 items in queue start 3 each 50% of cores" — formalized as:
//
//	"A 'heavy' job is a broad Go/Node test or build, or any coverage or race
//	run. Share: when a heavy job is admitted, its allocation depends on k,
//	the number of heavy jobs that will be running or waiting once it is
//	admitted (itself included). With N = NumCPU: k=1: N (100%); k=2: N*2/3;
//	k>=3: N/2. Concurrency: at most 3 heavy jobs run at once. A 4th waits in
//	FIFO order, and when a slot frees it is admitted with the share for the k
//	at that moment. Allocation is fixed at start."
//
// Then, refined once more after considering total oversubscription:
//
//	"Should we allow 2nd runner in parallel with 50% cpu? So 1st 100% and
//	total 150%." "Add a total-allocation cap of 150% of N for heavy jobs,
//	when N >= 8. A newly admitted heavy job gets
//	min(share(k), 1.5*N - allocated_heavy) ... If that result is below N/4,
//	the job waits in FIFO order instead. This subsumes the 3-job slot limit.
//	Keep the limit only as a defensive bound."
//
// Backfill-with-aging (an earlier design for the small-machine budget-sum
// pool) does not apply here: "The backfill-with-aging item from the brief is
// replaced by this: FIFO among heavy waiters, and focused jobs never wait
// behind heavy ones." A focused job (see Kind.IsHeavy, Admit) never touches
// any of this — it is always admitted immediately and is invisible to k.
//
// Allocation is fixed at admission and never revised for a job already
// running: "A running process's GOMAXPROCS cannot be changed, so a job
// admitted alone at 100% keeps it. Jobs that arrive later are still admitted
// by the rule above. That means a burst can briefly oversubscribe the CPU,
// and that is acceptable: tests here mostly wait on I/O and subprocesses."

// heavyShareFloorDivisor: a computed candidate share below N/4 is not worth
// admitting — the job keeps waiting instead of running starved. Founder:
// "If that result is below N/4, the job waits in FIFO order instead."
const heavyShareFloorDivisor = 4

// heavyCapNumerator/heavyCapDenominator: the total CPU allocation summed
// across concurrently running heavy jobs may not exceed 1.5x N. Founder:
// "Add a total-allocation cap of 150% of N for heavy jobs, when N >= 8."
const heavyCapNumerator, heavyCapDenominator = 3, 2

// heavyDefensiveMax bounds concurrently running heavy jobs even if the
// share/room arithmetic were ever wrong. It is mathematically redundant —
// the share floor (N/2 once k>=3) and the 1.5N cap already make a 4th
// concurrent heavy job impossible (4 * N/2 = 2N > 1.5N) — and kept "only as
// a defensive bound" per the founder, never as the primary gate: "This
// subsumes the 3-job slot limit. Keep the limit only as a defensive bound."
const heavyDefensiveMax = 3

// heavyShare is a heavy job's allocation when k heavy jobs (itself included)
// are alive (running or still waiting) at the moment it is admitted: k=1 ->
// N (100%); k=2 -> N*2/3; k>=3 -> N/2. Fixed at admission time and never
// recomputed for a job already running.
func heavyShare(k int) int {
	n := numCPU()
	switch {
	case k <= 1:
		return n
	case k == 2:
		return n * 2 / 3
	default:
		return n / 2
	}
}

// heavyAdmissionCandidate computes, from currently live heavy state, the
// share a newly arriving heavy job would get: min(share(k), room), where k
// counts every heavy job alive (running + still waiting, this one included)
// and room is the 1.5N cap minus the sum of already-running heavy jobs' own
// fixed allocations. runningHeavy and waitingCount must be read together,
// under the admission lock, so the two never disagree about a job that just
// transitioned from waiting to running.
func heavyAdmissionCandidate(runningHeavy []Holder, waitingCount int) (share, k int) {
	allocated := 0
	for _, holder := range runningHeavy {
		allocated += holder.Units
	}
	k = len(runningHeavy) + waitingCount
	room := numCPU()*heavyCapNumerator/heavyCapDenominator - allocated
	share = heavyShare(k)
	if share > room {
		share = room
	}
	return share, k
}

func heavyRoot(projectsRoot string) string { return filepath.Join(queueRoot(projectsRoot), "heavy") }
func heavyWaitingDir(projectsRoot string) string {
	return filepath.Join(heavyRoot(projectsRoot), "waiting")
}
func heavyRunningDir(projectsRoot string) string {
	return filepath.Join(heavyRoot(projectsRoot), "running")
}
func heavyLockPath(projectsRoot string) string {
	return filepath.Join(heavyRoot(projectsRoot), "admission.lock")
}

// RegisterHeavy registers a heavy-job waiter in the FIFO queue a large
// machine (numCPU >= 8) uses instead of the small-machine budget-sum pool.
// See Register for the general contract (Forget once, best-effort).
func RegisterHeavy(projectsRoot string, self Participant) *Ticket {
	return registerAt(projectsRoot, namespaceHeavy, heavyWaitingDir(projectsRoot), self)
}

// tryLockHeavyAdmission attempts to acquire the machine-wide lock that
// serializes heavy-admission decisions (read live state, decide, announce),
// so two concurrent heavy waiters never both admit past the 1.5N cap.
// Non-blocking: "false, nil" means someone else holds it right now, not an
// error.
func tryLockHeavyAdmission(projectsRoot string) (*os.File, bool, error) {
	path := heavyLockPath(projectsRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("create WB heavy admission lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open WB heavy admission lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, false, nil
	}
	return file, true, nil
}

func unlockHeavyAdmission(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

// announceHeavyHolder records a newly admitted heavy job's fixed share as a
// running holder, for heavyAdmissionCandidate's room accounting and for `wb
// run --queue` visibility.
func announceHeavyHolder(projectsRoot string, self Participant, units int, startedAt time.Time) (string, bool) {
	dir := heavyRunningDir(projectsRoot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false
	}
	seq := atomic.AddInt64(&ticketSeq, 1)
	path := filepath.Join(dir, fmt.Sprintf("%020d-%d-%d.json", startedAt.UnixNano(), self.PID, seq))
	payload, err := json.Marshal(Holder{Participant: self, Units: units, StartedAt: startedAt, UpdatedAt: startedAt})
	if err != nil {
		return "", false
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return "", false
	}
	return path, true
}

// heartbeatHeavyHolder refreshes a running heavy holder's UpdatedAt so
// readHeavyHolders (and therefore heavyAdmissionCandidate's room/k
// accounting, and `wb run --queue`) keeps treating it as live while the
// command it was admitted for keeps running — the same purpose
// Announcement.Heartbeat serves for the legacy pool.
func heartbeatHeavyHolder(path string, self Participant, units int, startedAt time.Time) {
	if path == "" {
		return
	}
	payload, err := json.Marshal(Holder{Participant: self, Units: units, StartedAt: startedAt, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, payload, 0o600)
}

func removeHeavyHolder(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// readHeavyHolders lists every live heavy holder, oldest first, reaping a
// dead PID's record exactly as readHolders does for the legacy pool.
func readHeavyHolders(projectsRoot string) []Holder {
	return readHolderRecords(heavyRunningDir(projectsRoot))
}

// admitHeavy waits for this heavy job's turn under the founder's adaptive
// policy: strict FIFO among heavy waiters (no backfill), admitted once
// min(share(k), room) is at least N/heavyShareFloorDivisor, else it keeps
// waiting. ticket must already be registered via RegisterHeavy; admitHeavy
// forgets it itself, atomically with announcing the new holder, so no
// window exists where a job is double-counted as both waiting and running.
func admitHeavy(ctx context.Context, projectsRoot string, self Participant, ticket *Ticket) (*Lease, int, time.Duration, error) {
	started := time.Now()
	for {
		ticket.Heartbeat()
		select {
		case <-ctx.Done():
			return nil, 0, time.Since(started), ctx.Err()
		default:
		}
		waitingTickets := readTicketsIn(heavyWaitingDir(projectsRoot))
		if len(waitingTickets) == 0 || waitingTickets[0].path != ticket.path {
			// Not the head of the heavy FIFO queue yet: strictly wait,
			// never attempt to jump ahead.
			if !sleepOrDone(ctx, retryInterval) {
				return nil, 0, time.Since(started), ctx.Err()
			}
			continue
		}
		lockFile, locked, err := tryLockHeavyAdmission(projectsRoot)
		if err != nil {
			return nil, 0, time.Since(started), err
		}
		if !locked {
			if !sleepOrDone(ctx, retryInterval) {
				return nil, 0, time.Since(started), ctx.Err()
			}
			continue
		}
		runningHeavy := readHeavyHolders(projectsRoot)
		if len(runningHeavy) >= heavyDefensiveMax {
			unlockHeavyAdmission(lockFile)
			if !sleepOrDone(ctx, retryInterval) {
				return nil, 0, time.Since(started), ctx.Err()
			}
			continue
		}
		share, _ := heavyAdmissionCandidate(runningHeavy, len(waitingTickets))
		if share < numCPU()/heavyShareFloorDivisor {
			unlockHeavyAdmission(lockFile)
			if !sleepOrDone(ctx, retryInterval) {
				return nil, 0, time.Since(started), ctx.Err()
			}
			continue
		}
		startedAt := time.Now().UTC()
		holderPath, _ := announceHeavyHolder(projectsRoot, self, share, startedAt)
		ticket.Forget()
		unlockHeavyAdmission(lockFile)
		lease := &Lease{
			extraRelease: func() { removeHeavyHolder(holderPath) },
			heartbeat:    func() { heartbeatHeavyHolder(holderPath, self, share, startedAt) },
		}
		return lease, share, time.Since(started), nil
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
