package diskprune

// Report is wb disk prune's machine-readable result
// (disk-prune#req:machine-readable-output).
type Report struct {
	SchemaVersion int                `json:"schema_version"`
	Mode          string             `json:"mode"`
	Outcome       string             `json:"outcome"`
	Reasons       []string           `json:"reasons"`
	Lock          LockReport         `json:"lock"`
	Admission     AdmissionReport    `json:"admission"`
	Filesystems   []FilesystemReport `json:"filesystems"`
	Categories    []CategoryReport   `json:"categories"`
	State         []StateReport      `json:"state"`
}

// LockReport says whether the per-user run lock was held.
type LockReport struct {
	Held      bool   `json:"held"`
	HolderPID int    `json:"holder_pid,omitempty"`
	Error     string `json:"error,omitempty"`
}

// AdmissionReport says how the run got past WB's admission queue.
type AdmissionReport struct {
	Status string `json:"status"`
	WaitMS int64  `json:"wait_ms"`
}

// FilesystemReport is one filesystem prune worked on, with the three figures
// kept apart: what the plan estimated, what was unlinked, and what free space
// actually did.
type FilesystemReport struct {
	Device            string       `json:"device"`
	Path              string       `json:"path"`
	Target            TargetReport `json:"target"`
	Before            SpaceReport  `json:"before"`
	After             SpaceReport  `json:"after"`
	TargetMet         bool         `json:"target_met"`
	EstimatedBytes    int64        `json:"estimated_bytes"`
	UnlinkedBytes     int64        `json:"unlinked_bytes"`
	ObservedGainBytes int64        `json:"observed_gain_bytes"`
	Note              string       `json:"note,omitempty"`
}

// TargetReport is a Target as JSON.
type TargetReport struct {
	Kind  string  `json:"kind"`
	Value float64 `json:"value"`
}

// SpaceReport is a filesystem's capacity at one moment.
type SpaceReport struct {
	TotalBytes     int64 `json:"total_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

// CategoryReport is one category's plan or result.
type CategoryReport struct {
	Name                 string        `json:"name"`
	Order                int           `json:"order"`
	Status               string        `json:"status"`
	Reason               string        `json:"reason,omitempty"`
	Partial              bool          `json:"partial"`
	EffectiveFloor       string        `json:"effective_floor,omitempty"`
	FloorSetByPID        int           `json:"floor_set_by_pid,omitempty"`
	EstimateBytes        *int64        `json:"estimate_bytes"`
	EstimateIsLowerBound bool          `json:"estimate_is_lower_bound"`
	EstimateIsUpperBound bool          `json:"estimate_is_upper_bound"`
	UnlinkedBytes        int64         `json:"unlinked_bytes"`
	PIDs                 []PIDReport   `json:"pids,omitempty"`
	Shards               []ShardReport `json:"shards,omitempty"`
	Items                []ItemReport  `json:"items,omitempty"`
}

// ShardReport aggregates one build-cache shard, so a six-gigabyte cache is a
// few hundred lines rather than hundreds of thousands.
type ShardReport struct {
	Path    string `json:"path"`
	Entries int64  `json:"entries"`
	Bytes   int64  `json:"bytes"`
	Status  string `json:"status"`
}

// ItemReport is one candidate or kept entry.
type ItemReport struct {
	Path                    string      `json:"path"`
	Filesystem              string      `json:"filesystem,omitempty"`
	Bytes                   int64       `json:"bytes"`
	ProjectedAvailableBytes *int64      `json:"projected_available_bytes"`
	Status                  string      `json:"status"`
	Reason                  string      `json:"reason,omitempty"`
	PIDs                    []PIDReport `json:"pids,omitempty"`
	Error                   string      `json:"error,omitempty"`
}

// PIDReport names a process that caused a skip.
type PIDReport struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// StateReport classifies one state directory's entries; nothing in it is
// ever pruned (disk-prune#req:state-is-report-only).
type StateReport struct {
	Root    string             `json:"root"`
	Entries []StateEntryReport `json:"entries"`
}

// StateEntryReport is one classified state entry.
type StateEntryReport struct {
	Path  string `json:"path"`
	Class string `json:"class"`
	Bytes int64  `json:"bytes"`
}

// Item and category statuses.
const (
	StatusDelete           = "delete"
	StatusDeleted          = "deleted"
	StatusKeep             = "keep"
	StatusBeyondTarget     = "beyond-target"
	StatusSkipped          = "skipped"
	StatusFailed           = "failed"
	StatusReplaced         = "replaced"
	StatusPlanned          = "planned"
	StatusApplied          = "applied"
	StatusPartial          = "partial"
	StatusRequiresCategory = "requires-category"
	StatusReportOnly       = "report-only"
)

// Outcomes, in precedence order (disk-prune#req:exit-codes).
const (
	OutcomeLocked           = "locked"
	OutcomeCancelled        = "cancelled"
	OutcomeFailed           = "failed"
	OutcomeFailClosed       = "fail-closed"
	OutcomeSpaceNotReturned = "space-not-returned"
	OutcomeTargetUnmet      = "target-unmet"
	OutcomePlanned          = "planned"
	OutcomeDone             = "done"
)

var outcomePrecedence = []string{
	OutcomeLocked, OutcomeCancelled, OutcomeFailed, OutcomeFailClosed,
	OutcomeSpaceNotReturned, OutcomeTargetUnmet, OutcomePlanned,
}

// failClosedReasons are skips that count as findings, because prune could
// not establish that something was safe.
var failClosedReasons = map[string]bool{
	"unknown": true, "busy-unverified": true, "unsupported-platform": true,
	"incomplete-reference-set": true, "partial-scan": true,
	"requested-step-skipped": true, "target-unreachable": true,
	"lock-unavailable": true, "probe-failed": true,
}

// IsFinding reports whether the report should exit 1.
func (r Report) IsFinding() bool { return r.Outcome != OutcomeDone }
