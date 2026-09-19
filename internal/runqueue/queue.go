// Package runqueue coordinates CPU-heavy commands across WB processes.
package runqueue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

const retryInterval = 100 * time.Millisecond

// numCPU is the machine's logical CPU count (N in the admission formulas
// below). A var, not a direct runtime.NumCPU() call, so tests can drive
// every N this package's table of cases covers (sneat-dev/wb#621) without
// depending on the machine the test happens to run on; production code
// never assigns it.
var numCPU = runtime.NumCPU

// smallMachineThreshold: below this NumCPU, admission keeps the original
// spec table (budget = N-1; focused 1, broad 2, coverage/race the whole
// budget) with no adaptive heavy-job sharing at all — lead design, so a
// small machine (the spec's four-vCPU default included) is unaffected by
// this change; one core stays free for interactive work there, as before.
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
// model — lead design (sneat-dev/wb#621): a heavy job is a broad Go/Node
// test or build, or any coverage or race run.
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
		return classifyGo(arguments)
	case "golangci-lint":
		// Review finding (PR #628, M7): an explicit broad scope
		// (`run ./...`, or no target at all as golangci-lint's own
		// default) is a whole-repository lint and should be heavy; a
		// scoped `run ./path/to/one/package` stays focused (light lint).
		if hasBroadScope(arguments) {
			return KindBroad
		}
		return KindFocused
	case "staticcheck", "pytest":
		return KindFocused
	case "vitest", "jest", "mocha":
		return classifyNodeTestRunner(arguments)
	case "nx":
		return classifyNx(arguments)
	case "npm", "pnpm", "yarn", "bun", "npx":
		return classifyNodePackageManager(tool, arguments)
	case "cargo":
		if hasAny(arguments, "test", "build", "check", "clippy") {
			return KindBroad
		}
	}
	return KindNone
}

// classifyGo classifies a `go` invocation. Review finding (PR #628, M7):
// -race/-cover given through the GOFLAGS environment variable (not just
// argv) must be caught too, and an explicit list of 2+ package paths is
// broad even without a "..." wildcard.
func classifyGo(arguments []string) Kind {
	if !hasAny(arguments, "test", "vet", "build") {
		return KindNone
	}
	if hasPrefix(arguments, "-race") || hasPrefix(arguments, "-cover") || goflagsRaceOrCover() {
		return KindRaceOrCover
	}
	if hasBroadScope(arguments) || countGoPackagePaths(arguments) >= 2 {
		return KindBroad
	}
	return KindFocused
}

// goflagsRaceOrCover reports whether the effective GOFLAGS — the GOFLAGS
// environment variable, or (review finding, PR #628 re-review, Minor 3)
// the `go env -w GOFLAGS` persisted default when the environment variable
// itself is unset — carries -race or -cover, so a caller that sets it
// ambiently either way still gets the heavy classification.
func goflagsRaceOrCover() bool {
	fields := strings.Fields(EffectiveGOFLAGS())
	return hasPrefix(fields, "-race") || hasPrefix(fields, "-cover")
}

// countGoPackagePaths counts argv entries that look like an explicit Go
// package path ("./..." handling is hasBroadScope's job; this counts
// ordinary paths like "./internal/foo") rather than a flag or its value.
func countGoPackagePaths(arguments []string) int {
	count := 0
	for _, argument := range arguments {
		if argument == "." || strings.HasPrefix(argument, "./") {
			count++
		}
	}
	return count
}

// classifyNx classifies a direct `nx` invocation (also reused by
// classifyNodePackageManager for `pnpm nx …`/`npx nx …`). Review finding
// (PR #628, S3): a run-many or affected invocation always spans multiple
// projects and is heavy regardless of its target; a single-verb command
// with no explicit project (or project:target) argument — "pnpm nx
// run-many" with no target included — is unscoped and therefore also
// heavy; only a verb with an explicit project argument is focused.
func classifyNx(arguments []string) Kind {
	if hasAny(arguments, "run-many", "affected") {
		return KindBroad
	}
	if hasAny(arguments, "build") {
		return KindBroad
	}
	if hasAny(arguments, "test", "lint", "e2e", "run") {
		if hasExplicitNxTarget(arguments) {
			return KindFocused
		}
		return KindBroad
	}
	return KindNone
}

// hasExplicitNxTarget reports whether an nx verb (test/lint/e2e/run) is
// followed by a positional, non-flag argument naming the project (or
// project:target) it scopes to.
func hasExplicitNxTarget(arguments []string) bool {
	sawVerb := false
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if !sawVerb {
			sawVerb = true
			continue
		}
		return true
	}
	return false
}

// nodeTestRunnerValueFlags lists jest/mocha/vitest flags that consume a
// following value — a config path, project name, reporter, pattern, or
// similar — never a positional file/path argument that would scope the
// run to one area. Review finding (PR #628 re-review, Minor 2): without
// this, `jest -c jest.config.js`, `mocha --config .mocharc.yml`, and
// `vitest --project core` were wrongly classified as focused, because
// their flag's own value looked like a scoping file/path argument.
var nodeTestRunnerValueFlags = map[string]bool{
	"-c": true, "--config": true,
	"-t": true, "--testnamepattern": true,
	"--project": true, "--reporter": true, "--pool": true,
	"--maxworkers": true, "--rootdir": true, "--testpathpattern": true,
	"--grep": true, "--fgrep": true,
	"-r": true, "--require": true,
	"--timeout": true,
}

// classifyNodeTestRunner classifies a direct vitest/jest/mocha invocation.
// Review finding (PR #628, S3): a bare `jest`, `mocha`, or `vitest run`
// with no file or path argument runs the whole suite and is heavy; an
// explicit file or path argument scopes it to one area and is focused.
func classifyNodeTestRunner(arguments []string) Kind {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if strings.HasPrefix(argument, "-") {
			base, _, hasValueInline := strings.Cut(argument, "=")
			if !hasValueInline && nodeTestRunnerValueFlags[strings.ToLower(base)] && index+1 < len(arguments) {
				index++ // skip the flag's own value, not a scoping path
			}
			continue
		}
		if argument == "run" || argument == "test" {
			continue
		}
		return KindFocused
	}
	return KindBroad
}

// classifyNodePackageManager classifies npm/pnpm/yarn/bun/npx, including
// the common "<package manager> nx …" and "<package manager> <runner> …"
// delegation shapes. Review finding (PR #628, S3): a bare `pnpm test` /
// `npx vitest run` with no workspace filter or file argument runs across
// the whole workspace and is heavy; only an explicit --filter/-w/--scope
// flag or a file/path argument narrows it to one package.
func classifyNodePackageManager(tool string, arguments []string) Kind {
	if len(arguments) == 0 {
		return KindNone
	}
	switch strings.ToLower(arguments[0]) {
	case "nx":
		return classifyNx(arguments[1:])
	case "vitest", "jest", "mocha":
		return classifyNodeTestRunner(arguments[1:])
	}
	if !hasAnyScriptToken(arguments, "test", "run", "build", "lint", "e2e", "affected") {
		return KindNone
	}
	// Review finding (PR #628 re-review round 3, Minor 2): match whole
	// tokens, not substrings of a joined command line — "pnpm test --
	// src/builder.spec.ts" must not classify broad merely because
	// "builder.spec.ts" contains the substring "build". hasAnyScriptToken
	// (review finding, round 4, Serious 2) additionally recognizes an
	// npm/pnpm/yarn-style scoped script name like "build:prod" or
	// "test:unit"/"test:ci" as matching its "build"/"test" keyword, so
	// `npm run build:prod`, `pnpm run test:unit`, and `pnpm test:ci` are
	// governed instead of falling through to KindNone.
	if hasAnyScriptToken(arguments, "build", "e2e", "affected") {
		return KindBroad
	}
	if hasAnyScriptToken(arguments, "test", "lint") {
		if hasNodeWorkspaceScope(tool, arguments) {
			return KindFocused
		}
		return KindBroad
	}
	return KindNone
}

// hasAnyScriptToken reports whether arguments contains a token that is
// exactly one of targets, or an npm/pnpm/yarn-style scoped script name
// beginning with "target:" (e.g. "build:prod", "test:unit", "test:ci").
// Review finding (PR #628 re-review round 4, Serious 2): plain token
// equality (hasAny) left `npm run build:prod`, `pnpm run test:unit`, and
// `pnpm test:ci` classified as KindNone (ungoverned), since none of
// "build:prod"/"test:unit"/"test:ci" equals "build"/"test" exactly.
func hasAnyScriptToken(arguments []string, targets ...string) bool {
	for _, argument := range arguments {
		for _, target := range targets {
			if argument == target || strings.HasPrefix(argument, target+":") {
				return true
			}
		}
	}
	return false
}

// hasNodeWorkspaceScope reports whether arguments carry an explicit
// workspace/filter flag or a file/path argument that narrows an npm/pnpm/
// yarn/bun/npx invocation to less than the whole workspace. tool
// disambiguates bare "-w", whose meaning is package-manager-specific
// (review finding, PR #628 re-review round 4, Minor 1): npm's `-w <name>`
// (equivalent to `--workspace <name>`/`--workspace=<name>`) names one
// specific workspace and narrows scope, so `npm -w foo test` is focused —
// but pnpm's bare `-w` (short for `--workspace-root`, taking no value)
// does the opposite: `pnpm -w test` runs the whole suite from the
// workspace root. Treating every package manager's `-w` the same way
// wrongly classified `npm -w foo test` as broad.
func hasNodeWorkspaceScope(tool string, arguments []string) bool {
	for index, argument := range arguments {
		lower := strings.ToLower(argument)
		switch {
		case lower == "--filter", lower == "--workspace", lower == "--scope",
			strings.HasPrefix(lower, "--filter="), strings.HasPrefix(lower, "--workspace="), strings.HasPrefix(lower, "--scope="):
			return true
		case lower == "-w" && tool == "npm" && index+1 < len(arguments):
			return true
		case strings.Contains(argument, "/"), strings.HasSuffix(argument, ".ts"), strings.HasSuffix(argument, ".js"):
			return true
		}
	}
	return false
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
	// stopHeartbeat, when non-nil, is closed by Release to stop the
	// self-heartbeat goroutine armHeartbeat started. Review finding (PR
	// #628, B1): a caller-driven heartbeat loop is easy to forget — the
	// worker and daemon executors never called one, so a holder aged past
	// staleAfter and was reaped mid-run, making the 150% cap accounting
	// blind to it (a reproduced 18+18+9=45 against a 27 cap). A Lease now
	// heartbeats itself for as long as it is held, so no caller can forget.
	stopHeartbeat chan struct{}
	// heartbeatDone is closed by the armHeartbeat goroutine right before it
	// returns, once it has actually observed stopHeartbeat closed — never
	// merely because Release asked it to. Release waits on this before
	// running extraRelease. Review finding (PR #628 re-review, Serious 1):
	// closing stopHeartbeat alone only asks the goroutine to stop on its
	// next select iteration; it does not wait for an already-in-flight
	// fn() call to finish first. Without this wait, Release could run
	// extraRelease — which frees or nils exactly what fn() reads/writes —
	// concurrently with a heartbeat write already underway when Release
	// was called: a genuine data race for the legacy pool (Cleanup nils
	// Announcement.paths while Heartbeat reads it), and for the heavy pool
	// a heartbeat's atomicWriteFile can rename a holder file back into
	// existence just after removeHeavyHolder deleted it, resurrecting a
	// "ghost" holder that then counts against the 150% cap and the 3-job
	// bound for up to staleAfter (30s).
	heartbeatDone chan struct{}
}

// leaseHeartbeatInterval is how often armHeartbeat refreshes a held Lease's
// holder record(s). It must stay well under staleAfter; a var, not a const,
// so tests can shrink both to observe the effect without a real 30s wait.
// Production code never assigns it.
var leaseHeartbeatInterval = 10 * time.Second

// armHeartbeat starts self-heartbeating fn on leaseHeartbeatInterval until
// Release. Only one heartbeat loop may be armed per Lease.
func (lease *Lease) armHeartbeat(fn func()) {
	lease.heartbeat = fn
	stop := make(chan struct{})
	done := make(chan struct{})
	lease.stopHeartbeat = stop
	lease.heartbeatDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(leaseHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fn()
			}
		}
	}()
}

func (lease *Lease) Release() {
	if lease == nil {
		return
	}
	if lease.stopHeartbeat != nil {
		close(lease.stopHeartbeat)
		lease.stopHeartbeat = nil
		// Wait for the goroutine to actually exit — including any fn()
		// call already in flight when Release was called — before running
		// extraRelease below. See heartbeatDone's doc comment.
		<-lease.heartbeatDone
		lease.heartbeatDone = nil
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

// Heartbeat refreshes this Lease's holder record(s) immediately, on top of
// the automatic background heartbeat armHeartbeat already runs. Safe to call
// on a nil Lease or one with nothing to refresh; callers no longer need to
// call this on a timer themselves.
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
// Kind uses — nil for KindNone always, and for KindFocused only on a large
// machine (numCPU >= 8), where a focused job is admitted instantly and
// never waits — so a caller that wants to render its own queued/heartbeat
// progress lines (only `wb run` does today) can call Ticket.Snapshot while
// Admit is in flight. The caller must call Forget on whatever is returned
// exactly once, regardless of outcome; Forget on a nil Ticket is a safe
// no-op.
func RegisterForAdmission(projectsRoot string, argv []string, self Participant) *Ticket {
	kind := Classify(argv)
	switch {
	case kind == KindNone:
		return nil
	case smallMachine():
		// Review finding (PR #628 re-review, Minor 1): on a small machine
		// every governed kind — KindFocused included — goes through the
		// same legacy Acquire pool as any other small-machine job (see
		// Admit's own smallMachine()-first ordering, the S2 fix); it is
		// not "outside the queue" the way a large machine's focused job
		// is. Returning nil unconditionally for KindFocused here made a
		// small-machine focused job invisible to `wb run --queue` and to
		// other waiters' reported queue positions — a regression from
		// origin/main.
		return Register(projectsRoot, self)
	case kind == KindFocused:
		return nil
	case kind.IsHeavy():
		return RegisterHeavy(projectsRoot, self)
	default:
		return Register(projectsRoot, self)
	}
}

// Admit requests to run argv under the admission policy designed for
// sneat-dev/wb#621 (lead design, formalizing the founder's messages
// relayed during implementation):
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
// Review finding (PR #628, S2): on a small machine every governed kind,
// focused included, must go through the original budget-sum Acquire and
// announcement exactly as on origin/main — the small-machine table has no
// "instant, unqueued" concept. So smallMachine() is checked first; only on
// a large machine does a focused job additionally get the new instant,
// never-queued path.
func Admit(ctx context.Context, projectsRoot string, argv []string, self Participant, ticket *Ticket) (Admission, error) {
	kind := Classify(argv)
	budget := Budget()
	switch {
	case kind == KindNone:
		return Admission{Lease: &Lease{}}, nil
	case smallMachine():
		units := smallMachineUnits(kind, budget)
		return admitLegacy(ctx, projectsRoot, units, budget, self)
	case kind == KindFocused:
		return Admission{Lease: &Lease{}, Units: focusedShare()}, nil
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

// admitLegacy acquires from the plain budget-sum pool and announces self,
// arming the Lease's self-heartbeat (B1) so the holder record never ages
// out from under a caller that forgets to refresh it. units is clamped to
// budget first (M4) so a caller-declared want beyond the machine's own
// budget-sum ceiling is reported as what was actually held, not what was
// asked for.
func admitLegacy(ctx context.Context, projectsRoot string, units, budget int, self Participant) (Admission, error) {
	if units > budget {
		units = budget
	}
	lease, waited, err := Acquire(ctx, projectsRoot, units, budget)
	if err == nil {
		announcement := lease.Announce(self)
		lease.extraRelease = announcement.Cleanup
		lease.armHeartbeat(announcement.Heartbeat)
	}
	return Admission{Lease: lease, Units: units, Waited: waited}, err
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
	return admitLegacy(ctx, projectsRoot, units, Budget(), self)
}

// GovernGoFlags returns the GOFLAGS value a governed "go" command should run
// with: the caller's own existing value is preserved, with -p=<units>
// appended so package parallelism follows the CPU allocation instead of
// defaulting to the whole machine — unless the caller already pinned -p, in
// which case the caller's flag wins untouched. argv that does not invoke the
// go tool, or units <= 0, returns existing unchanged.
// GovernGOMAXPROCS returns the GOMAXPROCS value a governed command's child
// should run with: the caller's own already-set value, preserved untouched
// (review finding, PR #628, S4: the spec says WB "sets GOMAXPROCS... from
// the allocation," but a caller that pinned its own value first must still
// win, the same way an explicit -p does for GOFLAGS), or units when the
// caller set nothing.
func GovernGOMAXPROCS(existing string, units int) string {
	if strings.TrimSpace(existing) != "" {
		return existing
	}
	return fmt.Sprint(units)
}

// EffectiveGOFLAGS returns the GOFLAGS a plain `go` invocation would use:
// the process environment's own GOFLAGS if set, else the persisted default
// from `go env -w GOFLAGS=...` (review finding, PR #628, M6 — the process
// environment alone misses a persisted default, so a caller who pinned
// -race via `go env -w` rather than an exported variable was silently
// overridden). Best-effort: an error running `go env` (no toolchain on
// PATH, e.g.) degrades to "", matching the pre-existing behavior of reading
// only the process environment.
func EffectiveGOFLAGS() string {
	if fromEnv := os.Getenv("GOFLAGS"); fromEnv != "" {
		return fromEnv
	}
	output, err := exec.Command("go", "env", "GOFLAGS").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// LookupEnv returns the value of key in environment (an os.Environ()-shaped
// slice), or "" if key is not present. Exported so every governed-
// environment builder (cmd/wb/run.go, cmd/wb/worker.go,
// internal/daemon/service.go) reads the caller's own environment
// consistently before deciding what to preserve.
func LookupEnv(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return entry[len(prefix):]
		}
	}
	return ""
}

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
