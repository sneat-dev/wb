package worktreeclaims

import (
	"fmt"
	"os"
)

type TerminalReadPorts struct {
	ReadProjectionForClaim func(home, worktree string) (Projection, error)
	ReadProjectionReadOnly func(worktree string) (Projection, error)
	ProjectionMissing      func(error) bool
	Corroborate            func(home, worktree string, projection Projection) error
	OpenRun                func(home, effort, run string, create bool) (*os.File, string, error)
	ReadTerminalAt         func(*os.File, string) (TerminalRecord, error)
}

func (ports TerminalReadPorts) ReadTerminal(home, worktree string, readOnly bool) (*TerminalRecord, error) {
	var projection Projection
	var err error
	if readOnly {
		projection, err = ports.ReadProjectionReadOnly(worktree)
	} else {
		projection, err = ports.ReadProjectionForClaim(home, worktree)
	}
	if ports.ProjectionMissing(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if projection.Lifecycle != "terminal" {
		return nil, nil
	}
	if err := ports.Corroborate(home, worktree, projection); err != nil {
		return nil, fmt.Errorf("corroborate terminal work-log claim: %w", err)
	}
	runDir, _, err := ports.OpenRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = runDir.Close() }()
	terminal, err := ports.ReadTerminalAt(runDir, projection.ClaimID)
	if err != nil {
		return nil, err
	}
	return &terminal, nil
}
