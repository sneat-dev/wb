package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

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
type LocalTerminals struct {
	// ProjectsRoot resolves the WB homes; Home is one more home to read, which
	// is usually the first of them.
	ProjectsRoot string
	Home         string
	// Homes, when set, are the only homes read: nothing is resolved from the
	// projects root or the environment. A test names its own.
	Homes []string
}

// homes is the distinct WB homes to read.
func (l LocalTerminals) homes() []string {
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
func (l LocalTerminals) List(ctx context.Context, limit int) ([]TerminalFile, bool, error) {
	var files []TerminalFile
	for _, home := range l.homes() {
		for _, effort := range subdirectories(filepath.Join(home, "worklogs")) {
			for _, run := range subdirectories(filepath.Join(effort, "runs")) {
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
				entries, err := os.ReadDir(filepath.Join(run, "terminals"))
				if err != nil {
					continue
				}
				for _, entry := range entries {
					claim, isJSON := strings.CutSuffix(entry.Name(), ".json")
					if !isJSON || !worktreeclaims.ValidClaimID(claim) || !entry.Type().IsRegular() {
						continue
					}
					if len(files) == limit {
						return files, true, nil
					}
					// A record that vanished since the listing is skipped.
					if info, err := entry.Info(); err == nil {
						files = append(files, TerminalFile{Key: filepath.Join(run, "terminals", entry.Name()), Size: info.Size(), ModTime: info.ModTime()})
					}
				}
			}
		}
	}
	return files, false, nil
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
func (LocalTerminals) Read(key string) (worktreeclaims.TerminalRecord, error) {
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
