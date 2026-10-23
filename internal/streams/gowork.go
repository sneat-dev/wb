package streams

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/mod/modfile"
)

// GoWorkFile and GoWorkSum are the two untracked files a Go local link
// creates. They are named here because more than one verb has to recognise
// them: the link creates them, and both `wb worktree merge` and
// `wb worktree end` refuse a worktree that still carries one.
const (
	GoWorkFile = "go.work"
	GoWorkSum  = "go.work.sum"
)

// GoWorkUseEntries reads the `use` entries of a worktree's go.work, if any.
//
// This is the file-based half of `merge-refuses-a-linked-worktree`, and it is
// deliberately independent of stream state: state alone would miss a
// hand-written workspace, so the refusal reads the file too.
func GoWorkUseEntries(worktree string) ([]string, error) {
	path := filepath.Join(worktree, GoWorkFile)
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if _, pathErr := os.Lstat(path); os.IsNotExist(pathErr) {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("read %s in %s: %w", GoWorkFile, worktree, err)
	}
	return parseGoWorkUseEntries(path, contents)
}

// parseGoWorkUseEntries owns Go workspace syntax and sorted dependency paths.
func parseGoWorkUseEntries(path string, contents []byte) ([]string, error) {
	workspace, err := modfile.ParseWork(path, contents, nil)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	entries := make([]string, 0, len(workspace.Use))
	for _, use := range workspace.Use {
		entries = append(entries, use.Path)
	}
	sort.Strings(entries)
	return entries, nil
}
