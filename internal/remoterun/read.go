package remoterun

import (
	"context"
	"time"
)

func runRemoteMachines(deps Dependencies, projectsRoot string, stale time.Duration) ([]MachineRow, error) {
	_, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return nil, err
	}
	entries, err := provider.List(context.Background())
	if err != nil {
		return nil, deps.ExitError(1, "read remote store: "+err.Error())
	}
	rows := MachineRows(entries, deps.Now(), stale)
	return rows, nil
}
func runRemoteClaims(deps Dependencies, projectsRoot string, stale time.Duration) ([]ClaimRow, error) {
	_, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	claims, err := provider.Claims(ctx)
	if err != nil {
		return nil, deps.ExitError(1, "read remote store: "+err.Error())
	}
	machines, err := provider.List(ctx)
	if err != nil {
		return nil, deps.ExitError(1, "read remote store: "+err.Error())
	}
	rows := ClaimRows(claims, machines, deps.Now(), stale)
	return rows, nil
}
func (s *Service) Machines(root string, stale time.Duration) ([]MachineRow, error) {
	return runRemoteMachines(s.deps, root, stale)
}
func (s *Service) Claims(root string, stale time.Duration) ([]ClaimRow, error) {
	return runRemoteClaims(s.deps, root, stale)
}
