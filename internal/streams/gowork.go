package streams

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	contents, err := os.ReadFile(filepath.Join(worktree, GoWorkFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s in %s: %w", GoWorkFile, worktree, err)
	}
	return ParseGoWorkUseEntries(string(contents)), nil
}

var goWorkSingleUse = regexp.MustCompile(`(?m)^\s*use\s+([^\s()]+)\s*$`)

// ParseGoWorkUseEntries reads both spellings Go accepts — a bare `use <dir>`
// and a `use (...)` block — and skips comments, so a commented-out entry is
// never read as a live link.
func ParseGoWorkUseEntries(contents string) []string {
	var entries []string
	for _, match := range goWorkSingleUse.FindAllStringSubmatch(contents, -1) {
		entries = append(entries, strings.Trim(match[1], `"`))
	}
	inBlock := false
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if !inBlock {
			if strings.HasPrefix(trimmed, "use") && strings.Contains(trimmed, "(") {
				inBlock = true
			}
			continue
		}
		if trimmed == ")" {
			inBlock = false
			continue
		}
		if trimmed == "" {
			continue
		}
		entries = append(entries, strings.Trim(trimmed, `"`))
	}
	sort.Strings(entries)
	return entries
}
