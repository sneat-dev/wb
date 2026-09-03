package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// A worktree is in use when someone is using it, and nothing in WB could tell.
//
// The first version of this asked whether the owning session's process id was
// alive. Process ids are recycled, so ten finished review checkouts pinned
// themselves open for seventeen hours. The second version added "and the owner
// registration is recent" — and that was worse, because the owner registration
// is written once at creation: `RecordCustody` deduplicates on identity, so a
// lane can run four verbs and advance nothing. A lane that had been working for
// three hours was reported as a recycled process id, and escaped deletion only
// because its tree happened to be dirty.
//
// So freshness is measured from what a lane actually does. Any of these counts,
// and the newest of them wins:
//
//   - a heartbeat that every wb invocation refreshes on the worktree it is run
//     inside — this is what keeps a lane that only reads alive;
//   - the newest modification time among the paths Git reports as changed,
//     which keeps a lane that only edits alive;
//   - the newest Work Log event;
//   - the newest commit on the branch.
//
// A live process id on its own still counts for nothing. It is evidence about a
// process, and the question is about a worktree.

const heartbeatName = "heartbeat.json"

// DefaultSessionFreshness is how long since the last sign of activity a
// checkout stays "in use". Six hours is deliberately generous: the cost of
// waiting is a checkout that lingers, and the cost of being wrong is someone's
// working tree deleted underneath them.
const DefaultSessionFreshness = 6 * time.Hour

type heartbeatRecord struct {
	At      time.Time `json:"at"`
	Command string    `json:"command,omitempty"`
	PID     int       `json:"pid,omitempty"`
}

// TouchHeartbeat records that something is using this worktree right now.
//
// It is deliberately a single overwritten file rather than an appended event:
// the custody chain is a record of who took charge, and turning it into a
// command trace would destroy the thing it is for. It is best effort — a
// heartbeat that cannot be written must never fail the command that was trying
// to do real work — and it never creates the journal directory, so touching a
// path that is not a WB worktree does nothing at all.
func TouchHeartbeat(worktree, command string) {
	directory, err := openJournalDirectory(worktree, false)
	if err != nil {
		return
	}
	defer func() { _ = directory.Close() }()
	record := heartbeatRecord{At: time.Now().UTC(), Command: strings.TrimSpace(command), PID: os.Getpid()}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	_ = writeBytesAtomicAt(directory, heartbeatName, encoded, 0o600)
}

// HeartbeatAt reports when this worktree was last used, or the zero time.
func HeartbeatAt(worktree string) time.Time {
	directory, err := openJournalDirectory(worktree, false)
	if err != nil {
		return time.Time{}
	}
	defer func() { _ = directory.Close() }()
	raw, err := readBytesAt(directory, heartbeatName)
	if err != nil {
		return time.Time{}
	}
	var record heartbeatRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return time.Time{}
	}
	return record.At.UTC()
}

// LastActivity is the newest sign that anyone is using this checkout.
//
// It reads four independent signals because a lane may be doing only one kind
// of work, and any of them alone is enough to mean "in use".
func LastActivity(ctx context.Context, result ListResult) time.Time {
	newest := time.Time{}
	// A timestamp in the future cannot be a record of something that happened.
	// Taken at face value it pins the checkout open forever while reporting
	// "used 0m ago", which is both wrong and unfalsifiable, so it is ignored.
	ceiling := time.Now().UTC().Add(time.Minute)
	advance := func(candidate time.Time) {
		candidate = candidate.UTC()
		if candidate.After(ceiling) {
			return
		}
		if candidate.After(newest) {
			newest = candidate
		}
	}
	advance(HeartbeatAt(result.WorktreeDir))
	advance(result.LastCommit)
	advance(NewestChangedFileTime(ctx, result.WorktreeDir))
	advance(newestWorkLogEventTime(result.WorktreeDir))
	for _, owner := range result.Owners {
		advance(owner.At)
	}
	return newest
}

// NewestChangedFileTime is the newest modification time among the paths Git
// reports as changed. It reads Git's own answer rather than walking the tree:
// a frontend worktree holds a hundred thousand node_modules files, none of
// which anyone edited, and walking them to learn nothing would make every
// inventory read pay for a signal that is already available cheaply.
func NewestChangedFileTime(ctx context.Context, worktree string) time.Time {
	output, err := gitRawOutput(ctx, worktree, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return time.Time{}
	}
	newest := time.Time{}
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if path == "" || strings.HasPrefix(path, ".git/") {
			continue
		}
		// A rename reports "old -> new"; the new path is the one that was
		// written.
		if _, renamed, found := strings.Cut(path, " -> "); found {
			path = renamed
		}
		info, statErr := os.Lstat(filepath.Join(worktree, strings.Trim(path, `"`)))
		if statErr != nil {
			continue
		}
		if modified := info.ModTime(); modified.After(newest) {
			newest = modified
		}
	}
	return newest.UTC()
}

// newestWorkLogEventTime is when the local Work Log last recorded anything.
func newestWorkLogEventTime(worktree string) time.Time {
	directory, err := openJournalDirectory(worktree, false)
	if err != nil {
		return time.Time{}
	}
	defer func() { _ = directory.Close() }()
	// Deliberately NOT the directory's own modification time: writing the
	// heartbeat renames a file into this directory, which bumps it, and the
	// whole point of excluding the heartbeat here is that a read must not look
	// like a Work Log write.
	newest := time.Time{}
	if _, err := directory.Seek(0, 0); err != nil {
		return newest
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return newest
	}
	for _, entry := range entries {
		if entry.Name() == heartbeatName {
			// The heartbeat is reported on its own; counting it here as well
			// would make every read look like Work Log activity.
			continue
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if modified := entryInfo.ModTime(); modified.After(newest) {
			newest = modified
		}
	}
	return newest.UTC()
}

// gitRawOutput runs git without trimming its output.
//
// The trimming matters: porcelain v1 encodes the index state in column one, so
// a leading space is data, and trimming it shifts every path by one character
// — which turns README.md into EADME.md and makes the file that was edited
// unstattable. This is the same defect canonicalrescue records having hit.
func gitRawOutput(ctx context.Context, worktree string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", worktree}, args...)...)
	command.Env = console.Env()
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), worktree, err)
	}
	return string(output), nil
}

// TouchHeartbeatForCurrentDirectory records activity on the worktree the
// command is being run inside, if it is being run inside one.
//
// The current directory is the whole point. A lane runs its commands from its
// own checkout, so this records exactly the lane that is working; a fleet-wide
// sweep run from somewhere else records nothing, which is what keeps it from
// making every checkout look busy.
func TouchHeartbeatForCurrentDirectory(command string) {
	directory, err := os.Getwd()
	if err != nil {
		return
	}
	root, err := worktreeRootOf(directory)
	if err != nil || root == "" {
		return
	}
	TouchHeartbeat(root, command)
}

// worktreeRootOf walks up from a directory to the checkout that contains it,
// stopping at the first directory holding a WB journal. It never leaves the
// filesystem it started on and never follows a link.
func worktreeRootOf(directory string) (string, error) {
	current, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	for {
		if info, statErr := os.Stat(filepath.Join(current, journalRootDirectory, journalLocalDirectory, manifestName)); statErr == nil && !info.IsDir() {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil
		}
		current = parent
	}
}
