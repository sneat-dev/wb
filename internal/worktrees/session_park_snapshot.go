package worktrees

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

// CaptureParkedSessionWorktree binds one parked member to retained Git
// directories and the source session's exact active Work Log claim. It makes
// no Git mutation and never pushes; the remote query only observes whether the
// captured commit is already the exact branch tip.
func CaptureParkedSessionWorktree(ctx context.Context, projectsRoot string, listed ListResult, source session.Record) (sessionpark.Worktree, error) {
	var snapshot sessionpark.Worktree
	if err := validateSourceSession(source); err != nil {
		return snapshot, err
	}
	members := []parkedSessionCaptureMember{{listed: listed}}
	defer closeParkedSessionMembers(members)
	member := &members[0]
	if err := member.acquireParkedCaptureGit(ctx, projectsRoot); err != nil {
		return snapshot, err
	}
	readWorkLog := func() (string, string, error) {
		return ParkedSessionWorkLogSnapshot(projectsRoot, member.guard.Path, source)
	}
	if err := member.captureParkedObservation(ctx, readWorkLog); err != nil {
		return snapshot, err
	}
	if err := member.revalidateParkedObservation(ctx, readWorkLog,
		"parked source Git or Work Log authority changed during capture"); err != nil {
		return snapshot, err
	}
	return member.snapshot, nil
}

func parkedRemoteBranchTip(ctx context.Context, canonical *canonicalRepository, remote, branch string) (string, error) {
	raw, err := gitCanonicalBytes(ctx, canonical, "ls-remote", "--heads", "--", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	return parseParkedRemoteBranchTip(raw, branch)
}

func parseParkedRemoteBranchTip(raw []byte, branch string) (string, error) {
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || !isGitObjectID(fields[0]) || fields[1] != "refs/heads/"+branch {
		return "", fmt.Errorf("remote branch response was not one exact branch tip")
	}
	return fields[0], nil
}
