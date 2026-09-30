package worktreeclaims

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

const HeartbeatName = "heartbeat.json"
const DefaultSessionFreshness = 6 * time.Hour

type HeartbeatRecord struct {
	At      time.Time `json:"at"`
	Command string    `json:"command,omitempty"`
	PID     int       `json:"pid,omitempty"`
}
type ActivitySnapshot struct {
	WorktreeDir string
	LastCommit  time.Time
	Owners      []OwnerView
}
type HeartbeatPorts struct {
	OpenJournal   func(string, bool) (*os.File, error)
	ReadBytesAt   func(*os.File, string) ([]byte, error)
	WriteAtomicAt func(*os.File, string, []byte, os.FileMode) error
	Now           func() time.Time
	PID           func() int
	GitRaw        func(context.Context, string, ...string) ([]byte, error)
	Lstat         func(string) (os.FileInfo, error)
	Getwd         func() (string, error)
	Abs           func(string) (string, error)
	Stat          func(string) (os.FileInfo, error)
	Rewind        func(*os.File) error
	ReadDir       func(*os.File) ([]os.DirEntry, error)
	EntryInfo     func(os.DirEntry) (os.FileInfo, error)
}

func (p HeartbeatPorts) TouchHeartbeat(worktree, command string) {
	directory, err := p.OpenJournal(worktree, false)
	if err != nil {
		return
	}
	defer directory.Close()
	record := HeartbeatRecord{At: p.Now().UTC(), Command: strings.TrimSpace(command), PID: p.PID()}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	_ = p.WriteAtomicAt(directory, HeartbeatName, encoded, 0600)
}
func (p HeartbeatPorts) HeartbeatAt(worktree string) time.Time {
	directory, err := p.OpenJournal(worktree, false)
	if err != nil {
		return time.Time{}
	}
	defer directory.Close()
	raw, err := p.ReadBytesAt(directory, HeartbeatName)
	if err != nil {
		return time.Time{}
	}
	var record HeartbeatRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return time.Time{}
	}
	return record.At.UTC()
}
func (p HeartbeatPorts) LastActivity(ctx context.Context, result ActivitySnapshot) time.Time {
	newest := time.Time{}
	ceiling := p.Now().UTC().Add(time.Minute)
	advance := func(candidate time.Time) {
		candidate = candidate.UTC()
		if candidate.After(ceiling) {
			return
		}
		if candidate.After(newest) {
			newest = candidate
		}
	}
	advance(p.HeartbeatAt(result.WorktreeDir))
	advance(result.LastCommit)
	advance(p.NewestChangedFileTime(ctx, result.WorktreeDir))
	advance(p.NewestWorkLogEventTime(result.WorktreeDir))
	for _, owner := range result.Owners {
		advance(owner.At)
	}
	return newest
}
func (p HeartbeatPorts) NewestChangedFileTime(ctx context.Context, worktree string) time.Time {
	output, err := p.GitRawOutput(ctx, worktree, "status", "--porcelain=v1", "--untracked-files=all")
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
		if _, renamed, found := strings.Cut(path, " -> "); found {
			path = renamed
		}
		info, statErr := p.Lstat(filepath.Join(worktree, strings.Trim(path, `"`)))
		if statErr != nil {
			continue
		}
		if modified := info.ModTime(); modified.After(newest) {
			newest = modified
		}
	}
	return newest.UTC()
}
func (p HeartbeatPorts) NewestWorkLogEventTime(worktree string) time.Time {
	directory, err := p.OpenJournal(worktree, false)
	if err != nil {
		return time.Time{}
	}
	defer directory.Close()
	newest := time.Time{}
	rewind := p.Rewind
	if rewind == nil {
		rewind = func(directory *os.File) error { _, err := directory.Seek(0, 0); return err }
	}
	if err := rewind(directory); err != nil {
		return newest
	}
	readDir := p.ReadDir
	if readDir == nil {
		readDir = func(directory *os.File) ([]os.DirEntry, error) { return directory.ReadDir(-1) }
	}
	entries, err := readDir(directory)
	if err != nil {
		return newest
	}
	entryInfo := p.EntryInfo
	if entryInfo == nil {
		entryInfo = func(entry os.DirEntry) (os.FileInfo, error) { return entry.Info() }
	}
	for _, entry := range entries {
		if entry.Name() == HeartbeatName {
			continue
		}
		info, infoErr := entryInfo(entry)
		if infoErr != nil {
			continue
		}
		if modified := info.ModTime(); modified.After(newest) {
			newest = modified
		}
	}
	return newest.UTC()
}
func (p HeartbeatPorts) GitRawOutput(ctx context.Context, worktree string, args ...string) (string, error) {
	output, err := p.GitRaw(ctx, worktree, args...)
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), worktree, err)
	}
	return string(output), nil
}
func (p HeartbeatPorts) TouchHeartbeatForCurrentDirectory(command string) {
	directory, err := p.Getwd()
	if err != nil {
		return
	}
	root, err := p.WorktreeRootOf(directory)
	if err != nil || root == "" {
		return
	}
	p.TouchHeartbeat(root, command)
}
func (p HeartbeatPorts) WorktreeRootOf(directory string) (string, error) {
	current, err := p.Abs(directory)
	if err != nil {
		return "", err
	}
	for {
		if info, statErr := p.Stat(filepath.Join(current, worktreejournal.JournalRootDirectory, worktreejournal.JournalLocalDirectory, manifestName)); statErr == nil && !info.IsDir() {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil
		}
		current = parent
	}
}
