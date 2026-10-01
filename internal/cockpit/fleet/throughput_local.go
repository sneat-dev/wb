package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// maxTerminalBytes bounds one terminal record read from disk.
const maxTerminalBytes = 1 << 20

// LocalTerminals lists and reads this machine's sealed terminal records, the
// files <home>/worklogs/<task>/runs/<run>/terminals/<claim id>.json that
// `worktreeclaims.TerminalPorts.SealTerminal` writes, in every WB home the
// projects root resolves to (the current home and a legacy one). It only lists
// directories and reads files: it never writes, creates or locks anything, and a
// symbolic link is skipped rather than followed. The record type is
// worktreeclaims.TerminalRecord; no path or content leaves the daemon.
//
// A terminal record is immutable and sealing one changes the modification time
// of its `terminals` directory (and a new run that of the task's `runs`
// directory), so List remembers each of those directories' listing with the
// modification time it saw and lists a directory again only when its time moved
// or is too recent to trust (within racyWindow of the listing, like Git's racy
// timestamps). An unchanged task costs two stats. Build one with
// NewLocalTerminals; it is used by one goroutine at a time.
type LocalTerminals struct {
	// ProjectsRoot resolves the WB homes; Home is one more home to read, which
	// is usually the first of them.
	ProjectsRoot string
	Home         string
	// Homes, when set, are the only homes read: nothing is resolved from the
	// projects root or the environment. A test names its own.
	Homes []string
	// Now is the clock of the racy-time rule; nil means time.Now.
	Now func() time.Time

	runs  map[string]cachedRuns
	files map[string]cachedFiles
}

// racyWindow is how close to the listing a directory's modification time may be
// before the listing is not trusted to be complete.
const racyWindow = 2 * time.Second

type cachedRuns struct {
	modified time.Time
	runs     []string
}

type cachedFiles struct {
	modified time.Time
	files    []TerminalFile
}

// NewLocalTerminals is the source for the homes the projects root and home name.
func NewLocalTerminals(projectsRoot, home string) *LocalTerminals {
	return &LocalTerminals{ProjectsRoot: projectsRoot, Home: home}
}

// homes is the distinct WB homes to read.
func (l *LocalTerminals) homes() []string {
	if l.Homes != nil {
		return l.Homes
	}
	var homes []string
	add := func(home string) {
		home = filepath.Clean(home)
		for _, known := range homes {
			if known == home {
				return
			}
		}
		homes = append(homes, home)
	}
	if l.Home != "" {
		add(l.Home)
	}
	if resolution, err := wbhome.Resolve(l.ProjectsRoot); err == nil {
		for _, layout := range resolution.Read {
			add(layout.Home)
		}
	}
	return homes
}

// List walks the Work Log of every home and returns up to limit terminal
// files, saying whether more existed. A missing directory is an empty Work
// Log; any other unreadable directory is skipped.
func (l *LocalTerminals) List(ctx context.Context, limit int) ([]TerminalFile, bool, error) {
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	seenRuns, seenFiles := map[string]cachedRuns{}, map[string]cachedFiles{}
	var files []TerminalFile
	truncated := false
	defer func() { l.runs, l.files = seenRuns, seenFiles }()
	for _, home := range l.homes() {
		for _, effort := range subdirectories(filepath.Join(home, "worklogs")) {
			for _, run := range l.runsOf(filepath.Join(effort, "runs"), now(), seenRuns) {
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
				for _, file := range l.filesOf(filepath.Join(run, "terminals"), now(), seenFiles) {
					if len(files) == limit {
						truncated = true
						continue
					}
					files = append(files, file)
				}
			}
		}
	}
	return files, truncated, nil
}

// trusted reports whether a directory with this modification time, listed at
// now, can be reused: its time is unchanged and old enough.
func trusted(cachedAt, modified, now time.Time) bool {
	return !cachedAt.IsZero() && cachedAt.Equal(modified) && now.Sub(modified) > racyWindow
}

// runsOf lists the run directories under a task's runs directory, from the cache
// while its modification time holds.
func (l *LocalTerminals) runsOf(dir string, now time.Time, seen map[string]cachedRuns) []string {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	if cached := l.runs[dir]; trusted(cached.modified, info.ModTime(), now) {
		seen[dir] = cached
		return cached.runs
	}
	runs := subdirectories(dir)
	seen[dir] = cachedRuns{modified: info.ModTime(), runs: runs}
	return runs
}

// filesOf lists the terminal records in a run's terminals directory, from the
// cache while its modification time holds. Only regular files named by a claim
// id count.
func (l *LocalTerminals) filesOf(dir string, now time.Time, seen map[string]cachedFiles) []TerminalFile {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	if cached := l.files[dir]; trusted(cached.modified, info.ModTime(), now) {
		seen[dir] = cached
		return cached.files
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []TerminalFile
	for _, entry := range entries {
		claim, isJSON := strings.CutSuffix(entry.Name(), ".json")
		if !isJSON || !worktreeclaims.ValidClaimID(claim) || !entry.Type().IsRegular() {
			continue
		}
		// A record that vanished since the listing is skipped.
		if info, err := entry.Info(); err == nil {
			files = append(files, TerminalFile{Key: filepath.Join(dir, entry.Name()), Size: info.Size(), ModTime: info.ModTime()})
		}
	}
	seen[dir] = cachedFiles{modified: info.ModTime(), files: files}
	return files
}

// subdirectories are the real directories directly under dir, as paths: a
// symbolic link to a directory is not one.
func subdirectories(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.Type().IsDir() {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths
}

var errTerminalTooLarge = errors.New("terminal record too large")

// Read decodes the record at key, a path List returned.
func (*LocalTerminals) Read(key string) (worktreeclaims.TerminalRecord, error) {
	var record worktreeclaims.TerminalRecord
	file, err := os.Open(key)
	if err != nil {
		return record, err
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, maxTerminalBytes+1))
	if err != nil {
		return record, err
	}
	if len(body) > maxTerminalBytes {
		return record, errTerminalTooLarge
	}
	return record, json.Unmarshal(body, &record)
}
