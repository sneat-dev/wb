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
// (numCPU >= smallMachineThreshold), sneat-dev/wb#621. The founder's own
// words, verbatim, relayed during implementation on 2026-09-18 (everything
// else below —
// the k-tiered share rule, the 150%-cap formula, the N/4 floor, the 3-job
// bound, the backfill-with-aging replacement, the "fixed at start"
// behavior, and the N<8 rule — is the lead's design, not a founder quote,
// even where it is written as a rule statement):
//
//   - "Feels wrong. This is MacBook Pro with m5 max with lots of CPU and
//     36gb memory"
//   - "It should not throttle so aggressively on powerful machines."
//   - "Can we make it smarter? If no queue we can start 100%. If something
//     is running and queue has more than 2 items start 2 with 2/3. If all
//     finished and in queue only one item start with 100%. If 3 items in
//     queue start 3 each 50% of cores"
//   - "I think that should work as tests also for io ops"
//   - "Should we allow 2nd runner in parallel with 50% cpu? So 1st 100%
//     and total 150%"
//
// Lead design, formalizing the founder's messages above into the rule this
// file implements: a "heavy" job is a broad Go/Node test or build, or any
// coverage or race run. When a heavy job is admitted, its allocation
// depends on k, the number of heavy jobs that will be running or waiting
// once it is admitted (itself included): with N = NumCPU, k=1 gives N
// (100%), k=2 gives N*2/3, k>=3 gives N/2. The total allocation across
// concurrently running heavy jobs may not exceed 1.5*N: a newly admitted
// heavy job actually gets min(share(k), 1.5*N - allocated_heavy), and if
// that is below N/4, it waits instead, in strict FIFO order among heavy
// waiters — replacing this design's earlier backfill-with-aging admission
// for the small-machine budget-sum pool entirely; a focused job (see
// Kind.IsHeavy, Admit) never touches any of this — it is always admitted
// immediately and is invisible to k. Allocation is fixed at admission and
// never revised for a job already running, because a running process's
// GOMAXPROCS cannot be changed: a job admitted alone at 100% keeps that
// share even as others are admitted at a smaller one later, so a burst can
// briefly leave the machine oversubscribed — accepted because these
// commands mostly wait on I/O and subprocesses, not pure CPU.

// heavyShareFloorDivisor: lead design — a computed candidate share below
// N/4 is not worth admitting; the job keeps waiting instead of running
// starved.
const heavyShareFloorDivisor = 4

// heavyCapNumerator/heavyCapDenominator: lead design — the total CPU
// allocation summed across concurrently running heavy jobs may not exceed
// 1.5x N, formalizing the founder's "Should we allow 2nd runner in
// parallel with 50% cpu? So 1st 100% and total 150%".
const heavyCapNumerator, heavyCapDenominator = 3, 2

// heavyDefensiveMax bounds concurrently running heavy jobs: lead design, a
// fallback once the 1.5N cap does the real work. The share floor (N/2 once
// k>=3) and the 1.5N cap do make a 4th concurrent job impossible
// (4 * N/2 = 2N > 1.5N) whenever the running-holder count both checks read
// is accurate — but that shared premise is exactly what a bug like PR #628's
// B1 (a holder aged out and vanished from accounting while still genuinely
// running) can break: with holders undercounted, the room check alone was
// fooled into admitting an 18+18+9 against a 27 cap in review. This bound is
// a second, independent read of the same live state, not a separate signal
// — it does not protect against B1-class undercounting on its own, but it
// is still load-bearing, not "mathematically redundant," against any bug
// that inflates k/room from a different angle than accounting for
// allocated units.
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
	if err := atomicWriteFile(path, payload, 0o600); err != nil {
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
	_ = atomicWriteFile(path, payload, 0o600)
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

// admitHeavy waits for this heavy job's turn under the lead's adaptive
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
		headTickets := readTicketsIn(heavyWaitingDir(projectsRoot))
		if len(headTickets) == 0 || headTickets[0].path != ticket.pathLocked() {
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
		// Review finding (PR #628, M3): re-read the waiting queue under the
		// same lock the running-holder read below uses, so k and the
		// head-of-queue check reflect one consistent, atomic snapshot
		// instead of the pre-lock read above (which another process could
		// have changed in the meantime).
		waitingTickets := readTicketsIn(heavyWaitingDir(projectsRoot))
		if len(waitingTickets) == 0 || waitingTickets[0].path != ticket.pathLocked() {
			unlockHeavyAdmission(lockFile)
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
		holderPath, ok := announceHeavyHolder(projectsRoot, self, share, startedAt)
		if !ok {
			// Review finding (PR #628, M2): never admit a job invisible to
			// the cap. A failed announce must not hand out a Lease that no
			// accounting sees — retry the whole decision instead, still
			// holding our place at the head of the FIFO queue.
			unlockHeavyAdmission(lockFile)
			if !sleepOrDone(ctx, retryInterval) {
				return nil, 0, time.Since(started), ctx.Err()
			}
			continue
		}
		ticket.Forget()
		unlockHeavyAdmission(lockFile)
		lease := &Lease{extraRelease: func() { removeHeavyHolder(holderPath) }}
		lease.armHeartbeat(func() { heartbeatHeavyHolder(holderPath, self, share, startedAt) })
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
