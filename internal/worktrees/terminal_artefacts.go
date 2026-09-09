package worktrees

import (
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

// PurgedArtefact records one terminal WB-owned artefact that a read path
// retired. It exists so a receipt can state what was swept without the sweep
// itself becoming a per-invocation log line.
type PurgedArtefact struct {
	Task          string `json:"task"`
	WorktreesRoot string `json:"worktrees_root"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
}

const (
	purgedRetiredStage = "retired_stage"
	purgedRetiredLock  = "retired_lock"
)

func isRetiredWorktreeOperationLock(name string) bool {
	return strings.HasPrefix(name, ".wb-retired-lock-") && len(name) > len(".wb-retired-lock-")
}

// purgeTerminalArtefacts removes the two terminal artefacts a finished task
// accumulates: an empty quarantined `.wb-retired-stage-*` directory, and an
// inert `.wb-retired-lock-*` file.
//
// Both are terminal by construction. A retired stage is quarantine evidence
// whose whole content has already been reclaimed; a retired lock exists only so
// a *later* operation on the same task can reclaim its inode, and a task nobody
// touches again keeps it forever. Until now their removal was coupled to an
// unrelated success path — cleaning the task — so a task that is never cleanable
// kept both artefacts permanently and `wb worktree list` printed one `info:`
// line per stage before its table. Measured on one workstation: 55 empty stage
// directories and 63 lock files, 220 KB in total, 55 log lines on every
// invocation.
//
// It is unconditional, silent, and best effort. It never reports an error: a
// task it cannot fully sweep is simply swept next time. The three things it will
// not do are the whole of its safety:
//
//   - a task holding any `.lock` entry is left completely alone, because a live
//     or interrupted operation owns these names and its own release path will
//     reclaim them;
//   - a stage is removed with AT_REMOVEDIR, so a non-empty one fails the syscall
//     and stays as the audited recovery backlog `--recover-stages` exists for;
//   - a lock is removed only when it is a plain single-link regular file, the
//     exact shape this package itself writes (see quarantineLockEntry).
func purgeTerminalArtefacts(worktreesRoot, task string) []PurgedArtefact {
	taskPath := filepath.Join(worktreesRoot, task)
	directory, err := openAbsoluteDirectoryNoFollow(taskPath, false)
	if err != nil {
		return nil
	}
	defer func() { _ = directory.Close() }()
	if _, err := directory.Seek(0, 0); err != nil {
		return nil
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil
	}
	candidates := make([]PurgedArtefact, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" {
			// An operation is running or was interrupted. Its release path owns
			// every retired name under this task; never race it.
			return nil
		}
		switch {
		case isRetiredWorktreeStagingDirectory(name):
			candidates = append(candidates, PurgedArtefact{
				Task: task, WorktreesRoot: worktreesRoot,
				Path: filepath.Join(taskPath, name), Kind: purgedRetiredStage,
			})
		case isRetiredWorktreeOperationLock(name):
			candidates = append(candidates, PurgedArtefact{
				Task: task, WorktreesRoot: worktreesRoot,
				Path: filepath.Join(taskPath, name), Kind: purgedRetiredLock,
			})
		}
	}
	purged := make([]PurgedArtefact, 0, len(candidates))
	for _, candidate := range candidates {
		name := filepath.Base(candidate.Path)
		var stat unix.Stat_t
		if statErr := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
			continue
		}
		switch candidate.Kind {
		case purgedRetiredStage:
			if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
				continue // a symlink or file wearing a reserved name is not WB's.
			}
			if unix.Unlinkat(int(directory.Fd()), name, unix.AT_REMOVEDIR) != nil {
				continue // non-empty, or it vanished; either way leave it.
			}
		case purgedRetiredLock:
			if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
				continue
			}
			if unix.Unlinkat(int(directory.Fd()), name, 0) != nil {
				continue
			}
		}
		purged = append(purged, candidate)
	}
	return purged
}
