// Package runqueue coordinates CPU-heavy commands across WB processes.
package runqueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

const retryInterval = 100 * time.Millisecond

// numCPU is the machine's logical CPU count (N in the admission formulas
// below). A var, not a direct runtime.NumCPU() call, so tests can drive
// every N in the founder's table (sneat-dev/wb#621) without depending on the
// machine the test happens to run on; production code never assigns it.
var numCPU = runtime.NumCPU

// smallMachineThreshold: below this NumCPU, admission keeps the original
// spec table (budget = N-1; focused 1, broad 2, coverage/race the whole
// budget) with no adaptive heavy-job sharing at all. Founder 2026-09-18
// (sneat-dev/wb#621): "Small machines. When N < 8, keep today's spec table
// exactly (budget = N-1; focused 1, broad 2, coverage 3; no adaptive
// shares). One core stays free for interactive work there."
const smallMachineThreshold = 8

func smallMachine() bool { return numCPU() < smallMachineThreshold }

// SetNumCPUForTest overrides the machine's logical CPU count every
// admission formula in this package reads, returning a restore func. It
// exists only so tests outside this package (cmd/wb, internal/daemon) can
// drive a specific N deterministically, the same way this package's own
// tests assign numCPU directly; production code must never call it.
func SetNumCPUForTest(n int) (restore func()) {
	previous := numCPU
	numCPU = func() int { return n }
	return func() { numCPU = previous }
}

// Budget leaves one logical CPU available for the harness and operating
// system. Even a single-core machine retains one execution slot. It sizes
// the original small-machine spec table and is reported informationally
// elsewhere; the adaptive heavy-job share (see heavyShare) is computed from
// numCPU directly, not from this budget.
func Budget() int {
	if numCPU() <= 1 {
		return 1
	}
	return numCPU() - 1
}

// Kind classifies a governed command for admission purposes.
type Kind int

const (
	// KindNone is not CPU-governed at all: no admission, no share.
	KindNone Kind = iota
	// KindFocused is a single-package Go test/vet or a light lint
	// (golangci-lint, staticcheck, pytest, vitest, jest, mocha, or an
	// nx/npm/pnpm/yarn/bun/npx test or lint script). Always admitted
	// immediately; never waits behind a heavy job and never takes a heavy
	// slot.
	KindFocused
	// KindBroad is a broad-scope Go/Node test or build (or an Angular/Nx
	// production build, or cargo test/build/check/clippy).
	KindBroad
	// KindRaceOrCover is any -race or -cover* Go run.
	KindRaceOrCover
)

// IsHeavy reports whether kind is "heavy" under the adaptive (N >= 8)
// model. Founder 2026-09-18 (sneat-dev/wb#621): "A 'heavy' job is a broad
// Go/Node test or build, or any coverage or race run."
func (kind Kind) IsHeavy() bool { return kind == KindBroad || kind == KindRaceOrCover }

// Classify reports what kind of governed work argv is. KindNone means argv
// is not CPU-governed at all.
func Classify(argv []string) Kind {
	if len(argv) == 0 {
		return KindNone
	}
	tool := strings.ToLower(filepath.Base(argv[0]))
	arguments := argv[1:]
	switch tool {
	case "go":
		if !hasAny(arguments, "test", "vet", "build") {
			return KindNone
		}
		if hasPrefix(arguments, "-race") || hasPrefix(arguments, "-cover") {
			return KindRaceOrCover
		}
		if hasBroadScope(arguments) {
			return KindBroad
		}
		return KindFocused
	case "golangci-lint", "staticcheck", "pytest", "vitest", "jest", "mocha":
		return KindFocused
	case "nx":
		if hasAny(arguments, "build", "run-many", "affected") {
			return KindBroad
		}
		if hasAny(arguments, "test", "lint", "e2e") {
			return KindFocused
		}
	case "npm", "pnpm", "yarn", "bun", "npx":
		joined := strings.ToLower(strings.Join(arguments, " "))
		if strings.Contains(joined, "build") || strings.Contains(joined, "e2e") || strings.Contains(joined, "affected") {
			return KindBroad
		}
		if strings.Contains(joined, "test") || strings.Contains(joined, "lint") {
			return KindFocused
		}
	case "cargo":
		if hasAny(arguments, "test", "build", "check", "clippy") {
			return KindBroad
		}
	}
	return KindNone
}

// Units is a stateless CPU-share estimate for argv: the small-machine
// table's exact number on a machine with numCPU < 8, or the share a large
// machine's job would get if admitted alone (k=1) otherwise. It exists for
// informational sizing (e.g. a durable operation record's initial estimate)
// where no live queue state is available; the real, state-dependent number
// for a heavy job on a large machine is decided by Admit at admission time
// and can differ from this estimate.
func Units(argv []string, budget int) int {
	kind := Classify(argv)
	if kind == KindNone || budget < 1 {
		return 0
	}
	if smallMachine() {
		return smallMachineUnits(kind, budget)
	}
	if kind == KindFocused {
		return clampToCapacity(focusedShare(), budget)
	}
	return clampToCapacity(heavyShare(1), budget)
}

// smallMachineUnits is the exact original spec table (unchanged since
// before sneat-dev/wb#621): focused 1, broad 2 (clamped to budget), and
// coverage/race the whole budget.
func smallMachineUnits(kind Kind, budget int) int {
	switch kind {
	case KindFocused:
		return clampToCapacity(1, budget)
	case KindBroad:
		return clampToCapacity(2, budget)
	case KindRaceOrCover:
		return budget
	}
	return 0
}

// focusedShare is a focused job's fixed allocation on a large machine
// (numCPU >= 8): max(1, N/8). It never depends on other running work, and a
// focused job is always admitted immediately (see Admit) — it is "outside
// the cap" and never waits behind a heavy job.
func focusedShare() int { return max(1, numCPU()/8) }

func clampToCapacity(units, capacity int) int {
	if units > capacity {
		return capacity
	}
	return units
}

func hasBroadScope(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "..." || argument == "./..." || strings.HasSuffix(argument, "/...") {
			return true
		}
	}
	return false
}

// Lease holds units machine-wide until Release. Slot files live below the
// projects root so harnesses already permitted to write repositories can join
// the same budget without requiring access to the user's home directory.
type Lease struct {
	files []*os.File
	// extraRelease runs once, after any flock release above, for cleanup a
	// Lease carries beyond its own slot files — e.g. removing a legacy or
	// heavy holder record. nil when there is none.
	extraRelease func()
	// heartbeat refreshes this Lease's holder record(s) so they do not age
	// past staleAfter while the command they were granted for keeps
	// running. nil when there is no holder record to refresh (KindNone,
	// KindFocused, or a Lease that failed before announcing).
	heartbeat func()
}

func (lease *Lease) Release() {
	if lease == nil {
		return
	}
	for index := len(lease.files) - 1; index >= 0; index-- {
		_ = unix.Flock(int(lease.files[index].Fd()), unix.LOCK_UN)
		_ = lease.files[index].Close()
	}
	lease.files = nil
	if lease.extraRelease != nil {
		lease.extraRelease()
		lease.extraRelease = nil
	}
}

// Heartbeat refreshes this Lease's holder record(s), the same way callers
// already refresh a long-running command's queue-visibility record on a
// ~10s cadence. Safe to call on a nil Lease or one with nothing to refresh.
func (lease *Lease) Heartbeat() {
	if lease == nil || lease.heartbeat == nil {
		return
	}
	lease.heartbeat()
}

// Acquire waits for units from one projects-root budget-sum lease pool
// (the small-machine table, and any other caller — e.g.
// internal/repositoryevents — sharing plain unit-weighted capacity). Each
// attempt either acquires every requested slot or releases all partial
// locks before waiting, preventing two multi-unit commands from
// deadlocking one another. There is no backfill or fairness ordering here:
// a flock race is fine for this pool because every caller today requests
// either 1 unit or a small, budget-bounded count — see Admit and admitHeavy
// for the FIFO, share-computing pool a heavy job on a large machine uses
// instead.
func Acquire(ctx context.Context, projectsRoot string, units, budget int) (*Lease, time.Duration, error) {
	if units <= 0 {
		return &Lease{}, 0, nil
	}
	if budget < 1 {
		budget = 1
	}
	if units > budget {
		units = budget
	}
	directory := queueRoot(projectsRoot)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, 0, fmt.Errorf("create WB CPU lease directory: %w", err)
	}
	started := time.Now()
	for {
		lease := &Lease{}
		for slot := 0; slot < budget && len(lease.files) < units; slot++ {
			path := filepath.Join(directory, fmt.Sprintf("slot-%02d.lock", slot))
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				lease.Release()
				return nil, time.Since(started), fmt.Errorf("open WB CPU slot: %w", err)
			}
			if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				_ = file.Close()
				continue
			}
			lease.files = append(lease.files, file)
		}
		if len(lease.files) == units {
			return lease, time.Since(started), nil
		}
		lease.Release()
		select {
		case <-ctx.Done():
			return nil, time.Since(started), ctx.Err()
		case <-time.After(retryInterval):
		}
	}
}

// Admission is the result of requesting to run one governed command.
type Admission struct {
	// Lease must be released (Release is always safe, including on a nil
	// Lease or one that never held anything) once the command finishes.
	Lease *Lease
	// Units is the CPU share (GOMAXPROCS / Go -p) the command should run
	// with. Zero for KindNone.
	Units int
	// Waited is how long this call spent waiting before admission.
	Waited time.Duration
}

// RegisterForAdmission registers argv's waiting ticket in the namespace its
// Kind uses — nil for KindNone/KindFocused, which never wait — so a caller
// that wants to render its own queued/heartbeat progress lines (only `wb
// run` does today) can call Ticket.Snapshot while Admit is in flight. The
// caller must call Forget on whatever is returned exactly once, regardless
// of outcome; Forget on a nil Ticket is a safe no-op.
func RegisterForAdmission(projectsRoot string, argv []string, self Participant) *Ticket {
	kind := Classify(argv)
	switch {
	case kind == KindNone || kind == KindFocused:
		return nil
	case kind.IsHeavy() && !smallMachine():
		return RegisterHeavy(projectsRoot, self)
	default:
		return Register(projectsRoot, self)
	}
}

// Admit requests to run argv under the founder's admission policy
// (sneat-dev/wb#621):
//
//   - KindNone is a no-op: Units 0, nothing to release, no wait.
//   - KindFocused is admitted immediately with its fixed share
//     (focusedShare, or the small-machine table's flat 1) and never takes a
//     heavy slot or waits behind one.
//   - On a small machine (numCPU < 8), a heavy kind (broad or
//     coverage/race) uses the original budget-sum Acquire pool with the
//     exact original spec-table weight (smallMachineUnits) — no adaptive
//     sharing.
//   - On a large machine (numCPU >= 8), a heavy kind is subject to
//     admitHeavy's FIFO, k-tiered, 150%-capped share.
//
// ticket, when non-nil, must already be registered via RegisterForAdmission
// against the same argv; when nil, Admit manages its own internally for a
// caller (worker/daemon executors) that does not render progress lines.
func Admit(ctx context.Context, projectsRoot string, argv []string, self Participant, ticket *Ticket) (Admission, error) {
	kind := Classify(argv)
	budget := Budget()
	switch {
	case kind == KindNone:
		return Admission{Lease: &Lease{}}, nil
	case kind == KindFocused:
		share := focusedShare()
		if smallMachine() {
			share = clampToCapacity(1, budget)
		}
		return Admission{Lease: &Lease{}, Units: share}, nil
	case smallMachine():
		units := smallMachineUnits(kind, budget)
		lease, waited, err := Acquire(ctx, projectsRoot, units, budget)
		if err == nil {
			announcement := lease.Announce(self)
			lease.extraRelease = announcement.Cleanup
			lease.heartbeat = announcement.Heartbeat
		}
		return Admission{Lease: lease, Units: units, Waited: waited}, err
	default:
		own := ticket
		if own == nil {
			own = RegisterHeavy(projectsRoot, self)
			defer own.Forget()
		}
		lease, units, waited, err := admitHeavy(ctx, projectsRoot, self, own)
		return Admission{Lease: lease, Units: units, Waited: waited}, err
	}
}

// AdmitExplicit is Admit for a caller that declares its own CPU need
// directly instead of via argv classification — the daemon's trusted raw
// execution fallback ("wb v0.105.0 raw-execution policy remains available
// only through `wb daemon operation submit`") is the one caller today.
// units acquires from the plain budget-sum pool regardless of what argv
// actually is (that pool, not the adaptive heavy-job one, is the right fit
// for an operator-declared want, since bypassing classification means WB
// cannot tell a heavy job from a focused one); self is announced for `wb
// run --queue` visibility exactly as Admit's other branches do.
func AdmitExplicit(ctx context.Context, projectsRoot string, units int, self Participant) (Admission, error) {
	budget := Budget()
	lease, waited, err := Acquire(ctx, projectsRoot, units, budget)
	if err == nil {
		announcement := lease.Announce(self)
		lease.extraRelease = announcement.Cleanup
		lease.heartbeat = announcement.Heartbeat
	}
	return Admission{Lease: lease, Units: units, Waited: waited}, err
}

// GovernGoFlags returns the GOFLAGS value a governed "go" command should run
// with: the caller's own existing value is preserved, with -p=<units>
// appended so package parallelism follows the CPU allocation instead of
// defaulting to the whole machine — unless the caller already pinned -p, in
// which case the caller's flag wins untouched. argv that does not invoke the
// go tool, or units <= 0, returns existing unchanged.
func GovernGoFlags(argv []string, existing string, units int) string {
	if units <= 0 || len(argv) == 0 || strings.ToLower(filepath.Base(argv[0])) != "go" {
		return existing
	}
	if hasGoParallelismFlag(existing) {
		return strings.TrimSpace(existing)
	}
	trimmed := strings.TrimSpace(existing)
	flag := fmt.Sprintf("-p=%d", units)
	if trimmed == "" {
		return flag
	}
	return trimmed + " " + flag
}

func hasGoParallelismFlag(goflags string) bool {
	for _, field := range strings.Fields(goflags) {
		if field == "-p" || strings.HasPrefix(field, "-p=") {
			return true
		}
	}
	return false
}

func hasAny(arguments []string, targets ...string) bool {
	for _, argument := range arguments {
		for _, target := range targets {
			if argument == target {
				return true
			}
		}
	}
	return false
}

func hasPrefix(arguments []string, prefix string) bool {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return true
		}
	}
	return false
}
