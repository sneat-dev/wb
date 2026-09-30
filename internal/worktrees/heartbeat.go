package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

const heartbeatName = worktreeclaims.HeartbeatName
const DefaultSessionFreshness = worktreeclaims.DefaultSessionFreshness

type heartbeatRecord = worktreeclaims.HeartbeatRecord

func TouchHeartbeat(worktree, command string) { heartbeatPorts().TouchHeartbeat(worktree, command) }
func HeartbeatAt(worktree string) time.Time   { return heartbeatPorts().HeartbeatAt(worktree) }
func LastActivity(ctx context.Context, result ListResult) time.Time {
	return heartbeatPorts().LastActivity(ctx, worktreeclaims.ActivitySnapshot{WorktreeDir: result.WorktreeDir, LastCommit: result.LastCommit, Owners: toClaimOwnerViews(result.Owners)})
}
func NewestChangedFileTime(ctx context.Context, worktree string) time.Time {
	return heartbeatPorts().NewestChangedFileTime(ctx, worktree)
}

func gitRawOutput(ctx context.Context, worktree string, args ...string) (string, error) {
	return heartbeatPorts().GitRawOutput(ctx, worktree, args...)
}
func TouchHeartbeatForCurrentDirectory(command string) {
	heartbeatPorts().TouchHeartbeatForCurrentDirectory(command)
}

func heartbeatPorts() worktreeclaims.HeartbeatPorts {
	return worktreeclaims.HeartbeatPorts{
		OpenJournal: openJournalDirectory, ReadBytesAt: readBytesAt, WriteAtomicAt: writeBytesAtomicAt, Now: time.Now, PID: os.Getpid,
		GitRaw: func(ctx context.Context, worktree string, args ...string) ([]byte, error) {
			command := exec.CommandContext(ctx, "git", append([]string{"-C", worktree}, args...)...)
			command.Env = console.Env()
			return command.Output()
		},
		Lstat: os.Lstat, Getwd: os.Getwd, Abs: filepath.Abs, Stat: os.Stat,
	}
}
