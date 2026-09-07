// Package landinglane records exclusive ownership of one (repository,
// target) landing lane so two live sessions never drive `wb pr land` or
// `wb worktree merge` toward the same target branch at once.
//
// On 2026-09-07 two live sessions landed on sneat-dev/wb main concurrently:
// main advanced under a published candidate four times in one session,
// costing a re-prepare or stranding a receipt each time. The working
// agreement is one landing owner per (repository, target branch); this
// package is the mechanical enforcement of that agreement.
//
// A lane record is a durable claim, not a running lock: it survives the
// command that acquired it so a later `wb pr land` / `wb worktree merge`
// invocation from the same session (resume, retry, a second batch) is
// recognised as the same owner, while a different live session is refused
// and named. Mutual exclusion during the read-modify-write itself is a
// short-lived flock scoped to one Acquire/Release/Heartbeat call, not the
// whole command.
package landinglane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

// DirName is the directory under WB's home that holds lane ownership records.
const DirName = "lanes"

// DefaultStaleAfter is how long a lane owner's heartbeat may go unrefreshed
// before it is treated as abandoned and taken over automatically. It is
// deliberately generous: `wb pr land` and `wb worktree merge` can spend real
// minutes waiting on CI, and a lane must not flap ownership under a slow but
// live run.
const DefaultStaleAfter = 30 * time.Minute

// SchemaVersion guards forward-incompatible record shape changes.
const SchemaVersion = 1

// Owner identifies the WB session and command driving a landing lane.
type Owner struct {
	WBSessionID string `json:"wb_session_id"`
	PID         int    `json:"pid"`
	Runtime     string `json:"runtime,omitempty"`
	Model       string `json:"model,omitempty"`
	// Command names the verb holding the lane, e.g. "wb pr land" or
	// "wb worktree merge land", so a refusal can say what the owner is doing.
	Command string `json:"command,omitempty"`
	// ReceiptPath is the local receipt this owner is driving, when one
	// exists yet (a `wb pr land` run before its first receipt write has
	// none).
	ReceiptPath string    `json:"receipt_path,omitempty"`
	AcquiredAt  time.Time `json:"acquired_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

// Record is the durable state of one (repository, target) landing lane.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	Lane          string `json:"lane"`
	Repository    string `json:"repository"`
	Target        string `json:"target"`
	Owner         Owner  `json:"owner"`

	// PriorOwner and the takeover fields are set only on the acquisition
	// that took the lane from a different owner, so a reader can see why
	// ownership moved without replaying history.
	PriorOwner     *Owner `json:"prior_owner,omitempty"`
	TakenOver      bool   `json:"taken_over,omitempty"`
	TakeoverReason string `json:"takeover_reason,omitempty"`
	// TakeoverNote explains an automatic (stale-owner) takeover; it is
	// distinct from TakeoverReason, which only an explicit --take-over-lane
	// --reason override sets.
	TakeoverNote string `json:"takeover_note,omitempty"`
}

// ConflictError reports that a different live session holds the lane. Error
// names the owner, its command and receipt, and the only sanctioned
// overrides, so a refusal is actionable without a second lookup.
type ConflictError struct {
	Record Record
}

func (e *ConflictError) Error() string {
	owner := e.Record.Owner
	since := "unknown time"
	if !owner.AcquiredAt.IsZero() {
		since = owner.AcquiredAt.UTC().Format(time.RFC3339)
	}
	runtime := owner.Runtime
	if owner.Model != "" {
		runtime = strings.TrimSpace(runtime + "/" + owner.Model)
	}
	if runtime == "" {
		runtime = "unknown runtime"
	}
	command := owner.Command
	if command == "" {
		command = "a landing command"
	}
	receipt := owner.ReceiptPath
	if receipt == "" {
		receipt = "no receipt written yet"
	}
	return fmt.Sprintf(
		"landing lane %s (%s -> %s) is held by session %s (pid %d, %s) since %s, running %s; receipt: %s; "+
			"ask it to hand off with `wb session request-handoff %s`, or override with --take-over-lane --reason <text>",
		e.Record.Lane, e.Record.Repository, e.Record.Target,
		owner.WBSessionID, owner.PID, runtime, since, command, receipt, owner.WBSessionID,
	)
}

// TakeoverReasonRequiredError reports that --take-over-lane was requested
// without the --reason it must carry into the record and the receipt.
var ErrTakeoverReasonRequired = errors.New("--take-over-lane requires --reason <text>")

// AcquireRequest describes one attempt to acquire or refresh a landing lane.
type AcquireRequest struct {
	Repository string
	Target     string
	Self       Owner

	// SessionDir is the WB session registry directory used to evaluate the
	// current owner's liveness (see internal/session). Required unless
	// IsOwnerLive is supplied directly, e.g. by a test.
	SessionDir string
	// IsOwnerLive overrides the default session-registry liveness check.
	// Tests inject this to avoid depending on real processes; production
	// callers should leave it nil.
	IsOwnerLive func(Owner) bool

	StaleAfter time.Duration
	Now        func() time.Time

	TakeOver       bool
	TakeoverReason string
}

// Acquire admits Self as the owner of the (Repository, Target) lane, refusing
// a different live owner, taking over a stale or dead one, and refreshing
// the record when Self already owns it.
func Acquire(home string, request AcquireRequest) (Record, error) {
	repository := strings.TrimSpace(request.Repository)
	target := strings.TrimSpace(request.Target)
	if repository == "" || target == "" {
		return Record{}, fmt.Errorf("landing lane requires both a repository and a target branch")
	}
	if strings.TrimSpace(request.Self.WBSessionID) == "" {
		return Record{}, fmt.Errorf("landing lane requires the acquiring session's WB session ID")
	}
	if request.TakeOver && strings.TrimSpace(request.TakeoverReason) == "" {
		return Record{}, ErrTakeoverReasonRequired
	}
	now := time.Now
	if request.Now != nil {
		now = request.Now
	}
	staleAfter := request.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	isLive := request.IsOwnerLive
	if isLive == nil {
		sessionDir := request.SessionDir
		isLive = func(owner Owner) bool {
			return defaultOwnerLive(sessionDir, owner)
		}
	}

	lane := LaneID(repository, target)
	lockFile, err := openLockFile(home, lane)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = lockFile.Close() }()
	if err := lockExclusive(lockFile); err != nil {
		return Record{}, fmt.Errorf("lock landing lane %s: %w", lane, err)
	}
	defer func() { _ = unlock(lockFile) }()

	existing, found, err := readRecord(recordPath(home, lane))
	if err != nil {
		return Record{}, err
	}

	moment := now().UTC()
	self := request.Self
	self.AcquiredAt = moment
	self.HeartbeatAt = moment

	if !found {
		record := Record{SchemaVersion: SchemaVersion, Lane: lane, Repository: repository, Target: target, Owner: self}
		return record, writeRecord(recordPath(home, lane), record)
	}

	if existing.Owner.WBSessionID == request.Self.WBSessionID {
		// Same session: proceed and refresh. AcquiredAt is preserved across a
		// refresh so a receipt started earlier by this same session still
		// reports how long it has actually held the lane; only the heartbeat
		// (and any updated PID/command/receipt for a resumed invocation)
		// moves forward.
		refreshed := existing
		refreshed.Owner.PID = self.PID
		refreshed.Owner.Runtime = self.Runtime
		refreshed.Owner.Model = self.Model
		refreshed.Owner.Command = self.Command
		refreshed.Owner.ReceiptPath = self.ReceiptPath
		refreshed.Owner.HeartbeatAt = moment
		return refreshed, writeRecord(recordPath(home, lane), refreshed)
	}

	stale := moment.Sub(existing.Owner.HeartbeatAt) > staleAfter
	live := isLive(existing.Owner)

	if !request.TakeOver && live && !stale {
		return existing, &ConflictError{Record: existing}
	}

	prior := existing.Owner
	record := Record{
		SchemaVersion: SchemaVersion,
		Lane:          lane,
		Repository:    repository,
		Target:        target,
		Owner:         self,
		PriorOwner:    &prior,
		TakenOver:     true,
	}
	if request.TakeOver {
		record.TakeoverReason = strings.TrimSpace(request.TakeoverReason)
	} else {
		note := "prior owner's heartbeat is older than the stale threshold"
		if !live {
			note = "prior owner's WB session is no longer live"
		}
		record.TakeoverNote = note
	}
	return record, writeRecord(recordPath(home, lane), record)
}

// Heartbeat refreshes the current owner's heartbeat timestamp while a
// landing command keeps running. It is a no-op, not an error, when the
// caller no longer owns the lane (another session already took it over, or
// it was already released) so a background ticker never needs to
// distinguish "lost the lane" from "nothing to do".
func Heartbeat(home, repository, target, wbSessionID string, now func() time.Time) error {
	lane := LaneID(strings.TrimSpace(repository), strings.TrimSpace(target))
	lockFile, err := openLockFile(home, lane)
	if err != nil {
		return err
	}
	defer func() { _ = lockFile.Close() }()
	if err := lockExclusive(lockFile); err != nil {
		return fmt.Errorf("lock landing lane %s: %w", lane, err)
	}
	defer func() { _ = unlock(lockFile) }()

	record, found, err := readRecord(recordPath(home, lane))
	if err != nil || !found || record.Owner.WBSessionID != wbSessionID {
		return err
	}
	moment := time.Now
	if now != nil {
		moment = now
	}
	record.Owner.HeartbeatAt = moment().UTC()
	return writeRecord(recordPath(home, lane), record)
}

// Release retires this session's ownership of the lane. It is a no-op when
// the lane is not currently held by wbSessionID, matching the working
// agreement that a command exiting without an active receipt (or reaching a
// terminal receipt status) frees the lane rather than leaving a stale claim
// behind for the stale-timeout to eventually clear.
func Release(home, repository, target, wbSessionID string) error {
	lane := LaneID(strings.TrimSpace(repository), strings.TrimSpace(target))
	lockFile, err := openLockFile(home, lane)
	if err != nil {
		return err
	}
	defer func() { _ = lockFile.Close() }()
	if err := lockExclusive(lockFile); err != nil {
		return fmt.Errorf("lock landing lane %s: %w", lane, err)
	}
	defer func() { _ = unlock(lockFile) }()

	path := recordPath(home, lane)
	record, found, err := readRecord(path)
	if err != nil || !found || record.Owner.WBSessionID != wbSessionID {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release landing lane %s: %w", lane, err)
	}
	return nil
}

// Read returns the current lane record, if any. It takes a shared read of
// the lock so a reader never observes a torn write.
func Read(home, repository, target string) (Record, bool, error) {
	lane := LaneID(strings.TrimSpace(repository), strings.TrimSpace(target))
	lockFile, err := openLockFile(home, lane)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = lockFile.Close() }()
	if err := lockShared(lockFile); err != nil {
		return Record{}, false, fmt.Errorf("lock landing lane %s: %w", lane, err)
	}
	defer func() { _ = unlock(lockFile) }()
	return readRecord(recordPath(home, lane))
}

// LaneID deterministically names the (repository, target) landing lane. It
// mirrors the readable-plus-hash shape used elsewhere in WB (see
// worktreeMergeLaneID in internal/orchestrate) so a lane id is stable,
// filesystem-safe, and recognisable at a glance without importing the
// orchestrate package, which itself depends on this one.
func LaneID(repository, target string) string {
	hash := sha256.Sum256([]byte(repository + "\x00" + target))
	readable := strings.NewReplacer("/", "-", ".", "-", "_", "-", " ", "-").Replace(repository + "-" + target)
	readable = strings.Trim(readable, "-")
	if len(readable) > 48 {
		readable = readable[:48]
	}
	return "lane-" + readable + "-" + hex.EncodeToString(hash[:6])
}

func recordPath(home, lane string) string {
	return filepath.Join(home, DirName, lane+".json")
}

func lockPath(home, lane string) string {
	return filepath.Join(home, DirName, lane+".lock")
}

func openLockFile(home, lane string) (*os.File, error) {
	dir := filepath.Join(home, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create landing lane directory: %w", err)
	}
	file, err := os.OpenFile(lockPath(home, lane), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open landing lane lock %s: %w", lane, err)
	}
	return file, nil
}

func lockExclusive(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func lockShared(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_SH)
}

func unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func readRecord(path string) (Record, bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("read landing lane record: %w", err)
	}
	var record Record
	if err := json.Unmarshal(contents, &record); err != nil {
		// A corrupt record must not wedge the lane forever: treat it as
		// absent so the next Acquire replaces it, the same posture WB takes
		// for other best-effort local state files.
		return Record{}, false, nil
	}
	return record, true, nil
}

func writeRecord(path string, record Record) error {
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode landing lane record: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return fmt.Errorf("write landing lane record: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit landing lane record: %w", err)
	}
	return nil
}

// defaultOwnerLive evaluates liveness the way the rest of WB does: through
// the session registry keyed by PID, never by inferring anything from a bare
// PID (a PID can be reused by an unrelated process the moment its original
// owner exits).
func defaultOwnerLive(sessionDir string, owner Owner) bool {
	if sessionDir == "" || owner.PID <= 0 {
		return false
	}
	record, ok := session.Lookup(sessionDir, owner.PID)
	if !ok {
		return false
	}
	if owner.WBSessionID == "" {
		return true
	}
	return record.WBSessionID == owner.WBSessionID
}
