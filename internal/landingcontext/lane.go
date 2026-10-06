package landingcontext

import (
	"path/filepath"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func ResolveOwner(projectsRoot, command string, pid int) landinglane.Owner {
	return resolveOwner(projectsRoot, command, pid, sessionDirectory, session.ResolveForProcess)
}
func resolveOwner(projectsRoot, command string, pid int, directoryFor func(string) (string, error), resolve func(string, int) (session.Record, bool)) landinglane.Owner {
	directory, err := directoryFor(projectsRoot)
	if err != nil {
		return landinglane.Owner{}
	}
	record, ok := resolve(directory, pid)
	if !ok {
		return landinglane.Owner{}
	}
	return landinglane.Owner{
		WBSessionID: record.WBSessionID,
		PID:         record.PID,
		Runtime:     record.Runtime,
		Model:       record.Model,
		Command:     command,
	}
}

func LaneRequest(projectsRoot, command, reason string, takeOver bool, pid int) orchestrate.LaneGuardRequest {
	return orchestrate.LaneGuardRequest{
		Owner:          ResolveOwner(projectsRoot, command, pid),
		TakeOver:       takeOver,
		TakeoverReason: reason,
	}
}

func ReleaseWorktreeLane(projectsRoot string, receipt orchestrate.WorktreeMergeReceipt, pid int) {
	releaseWorktreeLane(projectsRoot, receipt, pid, ResolveOwner, wbhome.Root, landinglane.Release)
}
func releaseWorktreeLane(projectsRoot string, receipt orchestrate.WorktreeMergeReceipt, pid int, ownerFor func(string, string, int) landinglane.Owner, rootFor func(string) (string, error), release func(string, string, string, string) error) {
	if receipt.Repository == "" || receipt.Target == "" || !orchestrate.WorktreeMergeLaneReleasable(receipt.Status) {
		return
	}
	owner := ownerFor(projectsRoot, "", pid)
	if owner.WBSessionID == "" {
		return
	}
	home, err := rootFor(projectsRoot)
	if err != nil {
		return
	}
	_ = release(home, receipt.Repository, receipt.Target, owner.WBSessionID)
}
func sessionDirectory(projectsRoot string) (string, error) {
	return sessionDirectoryWithRoot(projectsRoot, wbhome.Root)
}
func sessionDirectoryWithRoot(projectsRoot string, rootFor func(string) (string, error)) (string, error) {
	home, err := rootFor(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, session.DirName), nil
}
