package runqueue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
type Holder struct {
	Participant
	StartedAt time.Time `json:"started_at"`
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

func queueRoot(projectsRoot string) string {
	return filepath.Join(projectsRoot, ".wb", "runtime", "cpu")
}

func ticketDir(projectsRoot string) string {
	return filepath.Join(queueRoot(projectsRoot), "waiting")
}

func holderPathFor(lockPath string) string {
	return strings.TrimSuffix(lockPath, ".lock") + ".holder.json"
}

// Ticket is one caller's registered wait for CPU units, used only for queue
// visibility; it never gates admission itself.
type Ticket struct {
	projectsRoot string
	path         string
	createdAt    time.Time
	self         Participant
}

type ticketRecord struct {
	Participant
	CreatedAt time.Time `json:"created_at"`
	path      string
}

// Register records a waiter so State/Snapshot can report queue position and
// depth while units > 0. Callers must call Forget once they stop waiting,
// admitted or not — typically via defer immediately after Register.
// Registration is best-effort: a failure to write the ticket file degrades
// to an invisible waiter (Snapshot reports Position 0) rather than blocking
// or failing the caller, since visibility must never become a new way for
// `wb run` to hang or refuse work.
func Register(projectsRoot string, self Participant) *Ticket {
	now := time.Now().UTC()
	ticket := &Ticket{projectsRoot: projectsRoot, createdAt: now, self: self}
	dir := ticketDir(projectsRoot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ticket
	}
	seq := atomic.AddInt64(&ticketSeq, 1)
	name := fmt.Sprintf("%020d-%d-%d.json", now.UnixNano(), self.PID, seq)
	path := filepath.Join(dir, name)
	payload, err := json.Marshal(ticketRecord{Participant: self, CreatedAt: now})
	if err != nil {
		return ticket
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return ticket
	}
	ticket.path = path
	return ticket
}

// Forget removes the waiter's ticket. Safe to call more than once and on a
// Ticket whose registration never succeeded.
func (ticket *Ticket) Forget() {
	if ticket == nil || ticket.path == "" {
		return
	}
	_ = os.Remove(ticket.path)
	ticket.path = ""
}

// Snapshot reports this ticket's current position and the queue depth, plus
// the Participants currently holding CPU lease slots. Safe to call
// repeatedly (e.g. from a heartbeat); it reflects other WB processes'
// registrations and slot holdings on disk at the moment of the call.
func (ticket *Ticket) Snapshot(budget int) State {
	if ticket == nil {
		return State{}
	}
	return snapshot(ticket.projectsRoot, ticket.path, budget)
}

// Peek reports queue state without registering a waiter — used by `wb run
// --queue` and by the initial "admitted (queue empty)" check.
func Peek(projectsRoot string, budget int) State {
	return snapshot(projectsRoot, "", budget)
}

func readTickets(projectsRoot string) []ticketRecord {
	dir := ticketDir(projectsRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	tickets := make([]ticketRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
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
		holders = append(holders, holder)
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].StartedAt.Before(holders[j].StartedAt) })
	return holders
}

func snapshot(projectsRoot, selfPath string, budget int) State {
	tickets := readTickets(projectsRoot)
	state := State{Total: len(tickets), Holders: readHolders(projectsRoot, budget)}
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

// Announce records this Lease's slots as held by self, for State/Snapshot and
// `wb run --queue` visibility. It returns a cleanup that removes the holder
// records; callers should defer it alongside Release. Best-effort: a failure
// to write a holder file just means that slot stays anonymous in State
// (readHolders skips slots with no holder file), never a hard error.
func (lease *Lease) Announce(self Participant) func() {
	if lease == nil || len(lease.files) == 0 {
		return func() {}
	}
	started := time.Now().UTC()
	written := make([]string, 0, len(lease.files))
	for _, file := range lease.files {
		holderPath := holderPathFor(file.Name())
		payload, err := json.Marshal(Holder{Participant: self, StartedAt: started})
		if err != nil {
			continue
		}
		if err := os.WriteFile(holderPath, payload, 0o600); err != nil {
			continue
		}
		written = append(written, holderPath)
	}
	return func() {
		for _, path := range written {
			_ = os.Remove(path)
		}
	}
}

// QueueEntry is one running or waiting governed command, for `wb run
// --queue`.
type QueueEntry struct {
	PID      int           `json:"pid"`
	Summary  string        `json:"summary"`
	Worktree string        `json:"worktree,omitempty"`
	Age      time.Duration `json:"age_ns"`
}

// QueueListing is the inspectable state of the CPU lease queue for `wb run
// --queue`: who currently holds a slot, and who is waiting, oldest first.
type QueueListing struct {
	Budget  int          `json:"budget"`
	Running []QueueEntry `json:"running"`
	Waiting []QueueEntry `json:"waiting"`
}

// ListQueue reports every currently announced holder and registered waiter.
// It is read-only and safe to call from a separate `wb run --queue`
// invocation while other WB processes hold or wait for slots.
func ListQueue(projectsRoot string, budget int) QueueListing {
	now := time.Now().UTC()
	listing := QueueListing{Budget: budget}
	for _, holder := range readHolders(projectsRoot, budget) {
		listing.Running = append(listing.Running, QueueEntry{
			PID: holder.PID, Summary: holder.Summary, Worktree: holder.Worktree,
			Age: now.Sub(holder.StartedAt),
		})
	}
	for _, ticket := range readTickets(projectsRoot) {
		listing.Waiting = append(listing.Waiting, QueueEntry{
			PID: ticket.PID, Summary: ticket.Summary, Worktree: ticket.Worktree,
			Age: now.Sub(ticket.CreatedAt),
		})
	}
	return listing
}
