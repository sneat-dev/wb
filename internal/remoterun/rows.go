package remoterun

import (
	"time"

	"github.com/sneat-dev/wb/internal/age"
	"github.com/sneat-dev/wb/internal/remotestate"
)

type MachineRow struct {
	Key         string    `json:"key"`
	PublishedAt time.Time `json:"published_at"`
	Age         string    `json:"age"`
	SeenAt      time.Time `json:"seen_at"`
	Seen        string    `json:"seen"`
	Stale       bool      `json:"stale"`
	WBVersion   string    `json:"wb_version,omitempty"`
	Attention   int       `json:"attention"`
	Worktrees   int       `json:"worktrees"`
	Error       string    `json:"error,omitempty"`
}

type ClaimRow struct {
	Task         string    `json:"task"`
	Holder       string    `json:"holder"`
	ClaimedAt    time.Time `json:"claimed_at"`
	HeartbeatAge string    `json:"heartbeat_age"`
	Stale        bool      `json:"stale"`
	Note         string    `json:"note,omitempty"`
	Error        string    `json:"error,omitempty"`
}

func MachineRows(entries []remotestate.Entry, now time.Time, stale time.Duration) []MachineRow {
	rows := make([]MachineRow, 0, len(entries))
	for _, entry := range entries {
		row := MachineRow{Key: entry.Snapshot.Key(), Error: entry.Error}
		if entry.Error == "" {
			snap := entry.Snapshot
			if snap.PublishedAt.IsZero() && snap.LastSeenAt.IsZero() {
				row.Error = "snapshot has no published_at (truncated or empty file)"
			} else {
				heartbeat := snap.Heartbeat()
				row.PublishedAt = snap.PublishedAt
				if !snap.PublishedAt.IsZero() {
					row.Age = age.HumanAge(now.Sub(snap.PublishedAt))
				}
				row.SeenAt = heartbeat
				row.Seen = age.HumanAge(now.Sub(heartbeat))
				row.Stale = stale > 0 && now.Sub(heartbeat) > stale
				row.WBVersion = snap.WBVersion
				row.Attention = len(snap.Repositories)
				row.Worktrees = len(snap.Worktrees)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func findSnapshot(machines []remotestate.Entry, login, machine string) (remotestate.Snapshot, bool) {
	for _, m := range machines {
		if m.Error != "" {
			continue
		}
		if m.Snapshot.Login == login && m.Snapshot.Machine == machine {
			return m.Snapshot, true
		}
	}
	return remotestate.Snapshot{}, false
}

func HolderStale(machines []remotestate.Entry, login, machine string, now time.Time, stale time.Duration) bool {
	snap, ok := findSnapshot(machines, login, machine)
	if !ok {
		return true
	}
	return stale > 0 && now.Sub(snap.Heartbeat()) > stale
}

func HeartbeatPhrase(machines []remotestate.Entry, login, machine string, now time.Time, none string) string {
	snap, ok := findSnapshot(machines, login, machine)
	if !ok {
		return none
	}
	return age.PublishedAgo(age.HumanAge(now.Sub(snap.Heartbeat())))
}

func HolderDesc(mine, theirs remotestate.Claim) string {
	if theirs.Login == mine.Login && theirs.Machine != mine.Machine {
		return "you on " + theirs.Machine
	}
	return theirs.Holder()
}

func ClaimRows(claims []remotestate.ClaimEntry, machines []remotestate.Entry, now time.Time, stale time.Duration) []ClaimRow {
	rows := make([]ClaimRow, 0, len(claims))
	for _, entry := range claims {
		if entry.Error != "" {
			rows = append(rows, ClaimRow{Task: entry.Claim.Task, Error: entry.Error})
			continue
		}
		c := entry.Claim
		rows = append(rows, ClaimRow{
			Task:         c.Task,
			Holder:       c.Holder(),
			ClaimedAt:    c.ClaimedAt,
			HeartbeatAge: HeartbeatPhrase(machines, c.Login, c.Machine, now, "never published"),
			Stale:        HolderStale(machines, c.Login, c.Machine, now, stale),
			Note:         c.Note,
		})
	}
	return rows
}
