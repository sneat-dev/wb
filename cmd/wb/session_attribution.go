package main

import (
	"sort"
	"strconv"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sessionRow is a registered session enriched with what it worked on,
// derived from the worktree owners registry. The lists are sorted, distinct,
// and always non-nil so JSON renders [] rather than null.
type sessionRow struct {
	session.View
	Efforts   []string `json:"efforts"`
	Worktrees []string `json:"worktrees"`
	Branches  []string `json:"branches"`
}

// attributeSessions joins owner entries to sessions by declared PID, with a
// started-at guard so an entry written by a previous holder of a recycled
// PID is never attributed to the new session.
func attributeSessions(views []session.View, results []worktrees.ListResult) []sessionRow {
	rows := make([]sessionRow, 0, len(views))
	for _, view := range views {
		efforts, worktreeDirs, branches := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, result := range results {
			matched := false
			for _, owner := range result.Owners {
				if owner.PID != view.PID || owner.At.Before(view.StartedAt) {
					continue
				}
				matched = true
				if owner.Effort != "" {
					efforts[owner.Effort] = true
				}
			}
			if matched {
				if result.WorktreeDir != "" {
					worktreeDirs[result.WorktreeDir] = true
				}
				if result.Branch != "" {
					branches[result.Branch] = true
				}
			}
		}
		rows = append(rows, sessionRow{
			View:      view,
			Efforts:   sortedKeys(efforts),
			Worktrees: sortedKeys(worktreeDirs),
			Branches:  sortedKeys(branches),
		})
	}
	return rows
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// condense renders a derived list for the text table: nothing → "-", one
// value → the value (truncated to max runes), several → their count.
func condense(values []string, max int) string {
	switch len(values) {
	case 0:
		return "-"
	case 1:
		runes := []rune(values[0])
		if len(runes) > max {
			return string(runes[:max]) + "…"
		}
		return values[0]
	default:
		return strconv.Itoa(len(values))
	}
}
