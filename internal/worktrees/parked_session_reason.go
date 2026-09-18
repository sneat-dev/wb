package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// ParkedSessionReason reports, across every wbhome-resolved home for root,
// whether any un-picked-up (parked, not yet resumed) session bundle names a
// checkout under one of paths — a clone, a relocation candidate, or one of
// their linked worktrees. A parked session's bundle binds a worktree to an
// exact active Work Log claim and custody record (see session_park_local.go);
// moving it out from under that binding before the session resumes would
// leave the bundle pointing at a path that no longer holds what it recorded.
func ParkedSessionReason(root string, paths []string) string {
	resolution, err := wbhome.Resolve(root)
	if err != nil {
		return ""
	}
	seen := make(map[string]bool, len(resolution.Read))
	for _, layout := range resolution.Read {
		home := filepath.Clean(layout.Home)
		if home == "" || seen[home] {
			continue
		}
		seen[home] = true
		storeRoot := filepath.Join(home, sessionpark.SourceDirName)
		entries, readErr := os.ReadDir(storeRoot)
		if readErr != nil {
			continue
		}
		store := sessionpark.NewStore(storeRoot)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			state, loadErr := store.Load(entry.Name())
			if loadErr != nil || state.Status != sessionpark.StatusParked {
				continue
			}
			for _, member := range state.Bundle.Worktrees {
				for _, path := range paths {
					if underPath(member.CanonicalDir, path) || underPath(member.WorktreeDir, path) {
						return fmt.Sprintf("an un-picked-up parked session (%s) references %s", state.Bundle.ParkedSessionID, path)
					}
				}
			}
		}
	}
	return ""
}

// underPath reports whether candidate is path itself or nested under it.
func underPath(candidate, path string) bool {
	if candidate == "" || path == "" {
		return false
	}
	candidate, path = filepath.Clean(candidate), filepath.Clean(path)
	if candidate == path {
		return true
	}
	return strings.HasPrefix(candidate, path+string(filepath.Separator))
}
