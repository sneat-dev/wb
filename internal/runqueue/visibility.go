package runqueue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// This file makes the CPU lease queue inspectable: who is waiting, in what
// order, and who currently holds a slot. Three agents ran `wb run -- go
// test` concurrently on 2026-09-07; the queue correctly serialized them, but
// each command printed nothing for 10-25 minutes while waiting, so the
// waiting agent could not tell "queued" from "hung" and stalled (lesson
// l152-a; lesson long-running-wb-operations-must-report-progress-within-ten-seconds).
// Registration here is entirely additive and best-effort: a caller that never
// registers a Ticket or announces a Lease simply stays invisible to this
// bookkeeping, exactly as before this file existed. It never gates
// admission — Acquire's flock does that — so a bug here can make the queue
// under- or over-report, never deadlock or double-admit it.

// Participant identifies who is waiting for or holding a CPU lease slot, for
// human-readable queue visibility (`wb run`'s queued/heartbeat/admitted
// lines and `wb run --queue`). Summary is a short, already-public label (the
// program name and its verb, e.g. "go test") — never full command
// arguments, paths, or flags — matching runlog's privacy-safe-telemetry
// contract even though these records are transient rather than durable.
type Participant struct {
	PID      int    `json:"pid"`
	Summary  string `json:"summary"`
	Worktree string `json:"worktree,omitempty"`
}

// Holder is a Participant currently holding one or more CPU lease slots.
// Units is the CPU share this holder was fixed at, at admission time; the
// legacy budget-sum pool leaves it zero (its QueueEntry.Units is instead
// derived by counting held slot files — see groupRunningHolders), while a
// heavy holder on a large machine always stores its real, computed share
// here, since it holds exactly one bookkeeping record regardless of how
// many CPUs that share represents.
type Holder struct {
	Participant
	Units     int       `json:"units,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// staleAfter is how long a ticket or holder record may go without a
// heartbeat before it is treated as abandoned rather than live. It is a
// multiple of the ~10s heartbeat cadence `wb run` already keeps while a
// command is queued or running (see cmd/wb's universalProgressHeartbeat and
// the ticker in runExternalCommand), giving a couple of missed beats of
// slack before a record is presumed stale. A var, not a const, so tests can
// shrink it instead of sleeping 30+ seconds; production code never assigns
// it.
var staleAfter = 30 * time.Second

// isLive reports whether a registered PID should still be trusted: the
// process must actually exist, per processAlive (a bare FindProcess is not
// enough on Unix — it always succeeds), and its record must have been
// refreshed recently enough to rule out a stale leftover, e.g. a PID that
// has since been reused by an unrelated process. A killed waiter or holder
// stops heartbeating and ages out of both checks without anyone needing to
// clean up after it.
func isLive(pid int, updatedAt time.Time) bool {
	if !processAlive(pid) {
		return false
	}
	if updatedAt.IsZero() {
		return true
	}
	return time.Since(updatedAt) < staleAfter
}

// State is a point-in-time snapshot of the CPU lease queue relevant to one
// waiter: its position among registered waiters, how many waiters are
// registered in total, and who currently holds slots.
type State struct {
	// Position is this ticket's 1-based rank among current waiters, oldest
	// first. Zero when the ticket is not registered (units <= 0) or the
	// caller asked for Peek rather than a specific Ticket's Snapshot.
	Position int
	// Total is the number of tickets currently registered as waiting.
	Total int
	// Holders are the Participants currently holding CPU lease slots,
	// oldest first. It can be shorter than the busy-slot count when a
	// holder never announced (e.g. an older WB binary, or a caller other
	// than `wb run` sharing the same budget).
	Holders []Holder
}

var ticketSeq int64

// queueRootOverrideFrom/queueRootOverrideTo, when queueRootOverrideFrom is
// non-empty, replace the projectsRoot-derived directory every admission call
// in this package (Acquire, Admit, AdmitExplicit, Register, RegisterHeavy,
// and their read-only Peek/Snapshot counterparts) resolves its queue state
// against, but ONLY when the projectsRoot a caller passes is exactly equal
// to queueRootOverrideFrom — every other projectsRoot keeps resolving to its
// own, real, unoverridden directory. It exists only so cmd/wb's own tests
// can give the one, specific projectsRoot the test binary would otherwise
// default to (see TestMain, which keys it to defaultProjectsRoot()) an
// isolated CPU admission queue, separate from the real, machine-wide one an
// outer `wb run -- go test ./cmd/wb/...` invocation already holds a slot in
// — without it, a test that itself calls into `wb run --` (e.g.
// TestRunCommandAdmitsCPUHeavyWorkBelowFloor) joins that same queue and
// waits behind its own outer holder forever (sneat-dev/wb#623's own
// deadlock on this VM). The override is keyed, rather than blanket, so a
// test that explicitly passes its own `--projects-root`/t.TempDir() (e.g.
// TestRunCommandReportsQueueVisibilityOnStderr) keeps contending for that
// root's real, unoverridden queue directory — proving `wb run` still wires
// --projects-root into CPU admission (PR #736 review finding B1). Production
// code must never assign either var; only SetQueueRootForTest, meant to be
// called from a package's TestMain or from a test itself, does.
var (
	queueRootOverrideFrom string
	queueRootOverrideTo   string
)

// SetQueueRootForTest overrides the directory queueRoot resolves to, but
// only for the exact fromProjectsRoot given — every other projectsRoot is
// unaffected. It returns a restore func. See queueRootOverrideFrom/
// queueRootOverrideTo. Production code must never call this; it exists for
// TestMain and for tests the same way SetNumCPUForTest exists for tests
// that need a specific NumCPU.
func SetQueueRootForTest(fromProjectsRoot, dir string) (restore func()) {
	previousFrom, previousTo := queueRootOverrideFrom, queueRootOverrideTo
	queueRootOverrideFrom, queueRootOverrideTo = fromProjectsRoot, dir
	return func() { queueRootOverrideFrom, queueRootOverrideTo = previousFrom, previousTo }
}

func queueRoot(projectsRoot string) string {
	if queueRootOverrideFrom != "" && projectsRoot == queueRootOverrideFrom {
		return queueRootOverrideTo
	}
	return filepath.Join(projectsRoot, ".wb", "runtime", "cpu")
}

func ticketDir(projectsRoot string) string {
	return filepath.Join(queueRoot(projectsRoot), "waiting")
}

func holderPathFor(lockPath string) string {
	return strings.TrimSuffix(lockPath, ".lock") + ".holder.json"
}

// ticketNamespace tells a Ticket, and the functions that read its queue
// state, which pool it belongs to: the legacy budget-sum pool (small
// machines, and any other plain Acquire caller such as
// internal/repositoryevents), or the adaptive heavy-job FIFO pool a large
// machine (numCPU >= smallMachineThreshold) uses instead (see heavy.go).
// The two never share directories, so a heavy job's admission never
// contends with an unrelated caller's plain Acquire on the same slot files.
type ticketNamespace int

const (
	namespaceLegacy ticketNamespace = iota
	namespaceHeavy
)

// Ticket is one caller's registered wait, used for queue visibility and, in
// the heavy namespace only, for strict FIFO ordering (see admitHeavy). The
// legacy namespace's Ticket never gates admission itself.
//
// Review finding (PR #628, S5): admitHeavy's own loop and a caller's
// external progress ticker (cmd/wb/run.go) can both call Heartbeat/Forget on
// the same *Ticket concurrently. mu guards every read and write of path so
// Forget can never race a concurrent Heartbeat into "recreating" a ticket
// (Heartbeat always re-checks path under the same lock Forget clears it
// under, so once cleared it stays cleared).
type Ticket struct {
	mu           sync.Mutex
	projectsRoot string
	namespace    ticketNamespace
	path         string
	createdAt    time.Time
	self         Participant
}

// pathLocked returns the ticket's current path under mu, for admitHeavy's
// own head-of-queue comparisons (same package, but still concurrent with
// Heartbeat/Forget from another goroutine).
func (ticket *Ticket) pathLocked() string {
	if ticket == nil {
		return ""
	}
	ticket.mu.Lock()
	defer ticket.mu.Unlock()
	return ticket.path
}

type ticketRecord struct {
	Participant
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	path      string
}

// Register records a legacy-pool waiter so State/Snapshot can report queue
// position and depth while units > 0. Callers must call Forget once they
// stop waiting, admitted or not — typically via defer immediately after
// Register. Registration is best-effort: a failure to write the ticket file
// degrades to an invisible waiter (Snapshot reports Position 0) rather than
// blocking or failing the caller, since visibility must never become a new
// way for `wb run` to hang or refuse work.
func Register(projectsRoot string, self Participant) *Ticket {
	return registerAt(projectsRoot, namespaceLegacy, ticketDir(projectsRoot), self)
}

// atomicWriteFile writes data to path by writing it to a hidden sibling
// temp file in the same directory and renaming it into place (rename is
// atomic within one directory on every filesystem this package runs on).
// A reader that lists dir with os.ReadDir at the wrong instant may still
// observe the temp file, so every directory-listing reader here (
// readTicketsIn, readHolderRecords) skips names starting with "." — never
// a real ticket or holder record. Bug found via -race on PR #628's new
// concurrent-admission tests: registerAt's and Heartbeat's/
// heartbeatHeavyHolder's plain os.WriteFile truncates a file in place, and
// a heartbeat loop rewrites the very file readTicketsIn/readHolderRecords
// list and parse every ~100ms while another heavy job's admission decision
// is being computed; a reader that opened the file mid-truncate saw
// invalid JSON, and its json.Unmarshal error silently dropped an otherwise
// live waiter or holder from the k/room count (reproduced as
// TestAdmitHeavySimultaneousArrivalsRespectTheCap's three-way case
// occasionally admitting 9+12+6 instead of 9+9+9).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Chmod(tempPath, perm); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// reapStaleTempFile removes a hidden ".tmp-*" sibling atomicWriteFile left
// behind by a process killed between CreateTemp and Rename (the normal
// path either completes the rename or cleans the temp file up inline on
// error). Review finding (PR #628 re-review, Minor 4). Anything younger
// than staleAfter might still be a write genuinely in flight from another
// process, so only an older leftover — one no live writer could still be
// producing — is removed; readTicketsIn/readHolderRecords call this for
// every dot-prefixed name they skip while listing their directory anyway,
// so the reap costs nothing beyond the listing they already do.
func reapStaleTempFile(dir string, entry os.DirEntry) {
	if !strings.HasPrefix(entry.Name(), ".tmp-") {
		return
	}
	info, err := entry.Info()
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) < staleAfter {
		return
	}
	_ = os.Remove(filepath.Join(dir, entry.Name()))
}

// registerAt is Register generalized to any namespace/directory; RegisterHeavy
// (heavy.go) is its only other caller.
func registerAt(projectsRoot string, namespace ticketNamespace, dir string, self Participant) *Ticket {
	now := time.Now().UTC()
	ticket := &Ticket{projectsRoot: projectsRoot, namespace: namespace, createdAt: now, self: self}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ticket
	}
	seq := atomic.AddInt64(&ticketSeq, 1)
	name := fmt.Sprintf("%020d-%d-%d.json", now.UnixNano(), self.PID, seq)
	path := filepath.Join(dir, name)
	payload, err := json.Marshal(ticketRecord{Participant: self, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return ticket
	}
	if err := atomicWriteFile(path, payload, 0o600); err != nil {
		return ticket
	}
	ticket.path = path
	return ticket
}

// Forget removes the waiter's ticket. Safe to call more than once and on a
// Ticket whose registration never succeeded.
func (ticket *Ticket) Forget() {
	if ticket == nil {
		return
	}
	ticket.mu.Lock()
	defer ticket.mu.Unlock()
	if ticket.path == "" {
		return
	}
	_ = os.Remove(ticket.path)
	ticket.path = ""
}

// Heartbeat refreshes the ticket's UpdatedAt so readTickets keeps treating it
// as live while its caller is still waiting. Callers already poll queue
// state on an interval (e.g. `wb run`'s queued-command heartbeat every ~10s);
// call Heartbeat from that same loop rather than adding a new one. Safe to
// call on a nil Ticket or one whose registration never succeeded, and
// best-effort like Register: a failed refresh just means the ticket may age
// past staleAfter and be reaped as if its process had died, which only
// affects visibility, never admission.
func (ticket *Ticket) Heartbeat() {
	if ticket == nil {
		return
	}
	ticket.mu.Lock()
	defer ticket.mu.Unlock()
	if ticket.path == "" {
		return
	}
	payload, err := json.Marshal(ticketRecord{Participant: ticket.self, CreatedAt: ticket.createdAt, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return
	}
	_ = atomicWriteFile(ticket.path, payload, 0o600)
}

// Snapshot reports this ticket's current position and the queue depth, plus
// the Participants currently holding CPU lease slots in this ticket's own
// namespace. Safe to call repeatedly (e.g. from a heartbeat); it reflects
// other WB processes' registrations and slot holdings on disk at the moment
// of the call.
func (ticket *Ticket) Snapshot(budget int) State {
	if ticket == nil {
		return State{}
	}
	return snapshot(ticket.projectsRoot, ticket.namespace, ticket.pathLocked(), budget)
}

// Peek reports legacy-pool queue state without registering a waiter — used
// by `wb run --queue` and by the initial "admitted (queue empty)" check.
func Peek(projectsRoot string, budget int) State {
	return snapshot(projectsRoot, namespaceLegacy, "", budget)
}

// readTicketsIn lists every live ticket record in dir, oldest first,
// reaping a dead PID's record along the way. It is the shared engine behind
// readTickets (the legacy pool) and the heavy pool's waiting-ticket reads
// (heavy.go).
func readTicketsIn(dir string) []ticketRecord {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	tickets := make([]ticketRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			reapStaleTempFile(dir, entry)
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record ticketRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		if !isLive(record.PID, record.UpdatedAt) {
			// Best-effort reap: a killed waiter's ticket file otherwise sits
			// forever, since nothing else claims its uniquely named path.
			// Losing a race with another reader/reaper here is fine — the
			// file is either already gone or about to be skipped again next
			// read; never fail a run over it.
			_ = os.Remove(path)
			continue
		}
		record.path = path
		tickets = append(tickets, record)
	}
	sort.Slice(tickets, func(i, j int) bool {
		if tickets[i].CreatedAt.Equal(tickets[j].CreatedAt) {
			return tickets[i].path < tickets[j].path
		}
		return tickets[i].CreatedAt.Before(tickets[j].CreatedAt)
	})
	return tickets
}

func readTickets(projectsRoot string) []ticketRecord {
	return readTicketsIn(ticketDir(projectsRoot))
}

func readHolders(projectsRoot string, budget int) []Holder {
	if budget < 1 {
		budget = 1
	}
	directory := queueRoot(projectsRoot)
	holders := make([]Holder, 0, budget)
	for slot := 0; slot < budget; slot++ {
		lockPath := filepath.Join(directory, fmt.Sprintf("slot-%02d.lock", slot))
		raw, err := os.ReadFile(holderPathFor(lockPath))
		if err != nil {
			continue
		}
		var holder Holder
		if err := json.Unmarshal(raw, &holder); err != nil {
			continue
		}
		if !isLive(holder.PID, holder.UpdatedAt) {
			// A killed holder's slot file otherwise self-heals only when
			// that slot is reused; reap it here instead so it does not sit
			// reporting a phantom holder in the meantime. Best-effort, same
			// as readTickets: never fail a run over a stale file.
			_ = os.Remove(holderPathFor(lockPath))
			continue
		}
		holders = append(holders, holder)
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].StartedAt.Before(holders[j].StartedAt) })
	return holders
}

// readHolderRecords lists every live holder record directly inside dir,
// oldest first — the heavy pool's holders live one JSON file per holder
// (heavy.go's readHeavyHolders), unlike the legacy pool's readHolders,
// which derives each holder from a numbered slot lock file instead.
func readHolderRecords(dir string) []Holder {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	holders := make([]Holder, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			reapStaleTempFile(dir, entry)
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var holder Holder
		if err := json.Unmarshal(raw, &holder); err != nil {
			continue
		}
		if !isLive(holder.PID, holder.UpdatedAt) {
			_ = os.Remove(path)
			continue
		}
		holders = append(holders, holder)
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].StartedAt.Before(holders[j].StartedAt) })
	return holders
}

func snapshot(projectsRoot string, namespace ticketNamespace, selfPath string, budget int) State {
	var tickets []ticketRecord
	var holders []Holder
	if namespace == namespaceHeavy {
		tickets = readTicketsIn(heavyWaitingDir(projectsRoot))
		holders = readHeavyHolders(projectsRoot)
	} else {
		tickets = readTickets(projectsRoot)
		holders = readHolders(projectsRoot, budget)
	}
	state := State{Total: len(tickets), Holders: holders}
	if selfPath != "" {
		for index, ticket := range tickets {
			if ticket.path == selfPath {
				state.Position = index + 1
				break
			}
		}
	}
	return state
}

// Announcement is the live handle returned by Lease.Announce. Heartbeat keeps
// the holder records fresh while the lease is held — call it from the same
// ~10s loop `wb run` already runs while a command executes, mirroring how a
// waiting Ticket is refreshed — so readHolders does not age the slots out as
// stale while the holder is legitimately still running. Cleanup removes the
// holder records once the lease is released; callers should defer it
// alongside Lease.Release. Both methods are safe to call on a nil
// Announcement (e.g. the zero-unit case where Announce never wrote anything).
type Announcement struct {
	self    Participant
	started time.Time
	paths   []string
}

// Announce records this Lease's slots as held by self, for State/Snapshot and
// `wb run --queue` visibility. Best-effort: a failure to write a holder file
// just means that slot stays anonymous in State (readHolders skips slots
// with no holder file), never a hard error.
func (lease *Lease) Announce(self Participant) *Announcement {
	if lease == nil || len(lease.files) == 0 {
		return &Announcement{}
	}
	started := time.Now().UTC()
	announcement := &Announcement{self: self, started: started}
	for _, file := range lease.files {
		holderPath := holderPathFor(file.Name())
		payload, err := json.Marshal(Holder{Participant: self, StartedAt: started, UpdatedAt: started})
		if err != nil {
			continue
		}
		if err := atomicWriteFile(holderPath, payload, 0o600); err != nil {
			continue
		}
		announcement.paths = append(announcement.paths, holderPath)
	}
	return announcement
}

// Heartbeat refreshes every announced holder record's UpdatedAt so
// readHolders keeps treating this lease as live. Best-effort, like Announce:
// a failed refresh just risks the holder aging past staleAfter and being
// reaped as if the process had died, which only affects visibility.
func (announcement *Announcement) Heartbeat() {
	if announcement == nil || len(announcement.paths) == 0 {
		return
	}
	payload, err := json.Marshal(Holder{Participant: announcement.self, StartedAt: announcement.started, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return
	}
	for _, path := range announcement.paths {
		_ = atomicWriteFile(path, payload, 0o600)
	}
}

// Cleanup removes this announcement's holder records. Safe to call more than
// once and on a nil Announcement.
func (announcement *Announcement) Cleanup() {
	if announcement == nil {
		return
	}
	for _, path := range announcement.paths {
		_ = os.Remove(path)
	}
	announcement.paths = nil
}

// QueueEntry is one running or waiting governed command, for `wb run
// --queue`. Units is the number of CPU-budget slots a running holder
// currently occupies; it is omitted (zero) for a waiting entry, which has
// not been granted any slots yet.
type QueueEntry struct {
	PID      int           `json:"pid"`
	Summary  string        `json:"summary"`
	Worktree string        `json:"worktree,omitempty"`
	Units    int           `json:"units,omitempty"`
	Age      time.Duration `json:"age_ns"`
}

// QueueListing is the inspectable state of the CPU lease queue for `wb run
// --queue`: who currently holds a slot, and who is waiting, oldest first,
// across both the legacy budget-sum pool and the large-machine heavy pool.
// HeavyK is the current k (sneat-dev/wb#621): every heavy job alive, running
// or waiting, right now — the same live count a heavy job arriving this
// instant would be sized against.
type QueueListing struct {
	Budget  int          `json:"budget"`
	Running []QueueEntry `json:"running"`
	Waiting []QueueEntry `json:"waiting"`
	HeavyK  int          `json:"heavy_k,omitempty"`
}

// ListQueue reports every currently announced holder and registered waiter,
// legacy and heavy alike. It is read-only and safe to call from a separate
// `wb run --queue` invocation while other WB processes hold or wait for
// slots.
//
// readHolders returns one record per held slot (Announce writes one holder
// file per unit in the lease), so a multi-unit legacy holder would
// otherwise print once per unit it holds. groupRunningHolders collapses
// those into one QueueEntry per distinct holder (PID + StartedAt identifies
// one Announce call, i.e. one lease) and sums the collapsed records' Units.
// A heavy holder needs no such collapsing — it is already exactly one
// record — but shares the same helper for a uniform QueueEntry shape.
func ListQueue(projectsRoot string, budget int) QueueListing {
	now := time.Now().UTC()
	listing := QueueListing{Budget: budget, Running: []QueueEntry{}, Waiting: []QueueEntry{}}
	listing.Running = append(listing.Running, groupRunningHolders(readHolders(projectsRoot, budget), now)...)
	listing.Running = append(listing.Running, groupRunningHolders(readHeavyHolders(projectsRoot), now)...)
	for _, ticket := range readTickets(projectsRoot) {
		listing.Waiting = append(listing.Waiting, QueueEntry{
			PID: ticket.PID, Summary: ticket.Summary, Worktree: ticket.Worktree,
			Age: now.Sub(ticket.CreatedAt),
		})
	}
	heavyWaiting := readTicketsIn(heavyWaitingDir(projectsRoot))
	for _, ticket := range heavyWaiting {
		listing.Waiting = append(listing.Waiting, QueueEntry{
			PID: ticket.PID, Summary: ticket.Summary, Worktree: ticket.Worktree,
			Age: now.Sub(ticket.CreatedAt),
		})
	}
	listing.HeavyK = len(readHeavyHolders(projectsRoot)) + len(heavyWaiting)
	return listing
}

// groupRunningHolders collapses readHolders'/readHeavyHolders' one-row-per-
// record output into one QueueEntry per distinct holder (PID + StartedAt
// identifies one Announce call, i.e. one lease), preserving oldest-first
// order (via first sight of each holder's key) and summing each holder's
// own Units — a legacy holder record leaves Units unset (0), so each of its
// slot files contributes 1; a heavy holder stores its real share directly,
// and holds exactly one record, so it is unaffected by the fallback.
func groupRunningHolders(holders []Holder, now time.Time) []QueueEntry {
	entries := make([]QueueEntry, 0, len(holders))
	index := make(map[string]int, len(holders))
	for _, holder := range holders {
		perRecordUnits := holder.Units
		if perRecordUnits <= 0 {
			perRecordUnits = 1
		}
		key := fmt.Sprintf("%d@%d", holder.PID, holder.StartedAt.UnixNano())
		if position, ok := index[key]; ok {
			entries[position].Units += perRecordUnits
			continue
		}
		index[key] = len(entries)
		entries = append(entries, QueueEntry{
			PID: holder.PID, Summary: holder.Summary, Worktree: holder.Worktree,
			Units: perRecordUnits, Age: now.Sub(holder.StartedAt),
		})
	}
	return entries
}
