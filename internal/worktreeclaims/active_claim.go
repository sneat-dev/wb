package worktreeclaims

import (
	"fmt"
	"os"
	"path/filepath"
)

type ActiveClaimPorts struct {
	ReadProjectionForClaim func(home, worktree string) (Projection, error)
	ReadProjectionReadOnly func(worktree string) (Projection, error)
	Corroborate            func(home, worktree string, projection Projection) error
	OpenRun                func(home, effort, run string, create bool) (*os.File, string, error)
	ReadClaimAt            func(*os.File, string) (Claim, error)
}

func (p ActiveClaimPorts) ActiveWorkLogClaim(home, worktree string) (Claim, Projection, string, error) {
	return p.ActiveWorkLogClaimWithMode(home, worktree, false)
}
func (p ActiveClaimPorts) ActiveWorkLogClaimReadOnly(home, worktree string) (Claim, Projection, string, error) {
	return p.ActiveWorkLogClaimWithMode(home, worktree, true)
}
func (p ActiveClaimPorts) ActiveWorkLogClaimWithMode(home, worktree string, readOnly bool) (Claim, Projection, string, error) {
	var projection Projection
	var err error
	if readOnly {
		projection, err = p.ReadProjectionReadOnly(worktree)
	} else {
		projection, err = p.ReadProjectionForClaim(home, worktree)
	}
	if err != nil {
		return Claim{}, Projection{}, "", err
	}
	if projection.Lifecycle != "active" {
		return Claim{}, projection, "", fmt.Errorf("work-log projection is %s, not active", projection.Lifecycle)
	}
	if err := p.Corroborate(home, worktree, projection); err != nil {
		return Claim{}, projection, "", fmt.Errorf("corroborate active work-log claim: %w", err)
	}
	runDir, runPath, err := p.OpenRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return Claim{}, projection, "", err
	}
	defer func() { _ = runDir.Close() }()
	claim, err := p.ReadClaimAt(runDir, projection.ClaimID)
	if err != nil {
		return Claim{}, projection, "", err
	}
	return claim, projection, filepath.Join(runPath, "claims", projection.ClaimID+".json"), nil
}
