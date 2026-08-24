package gitrepo

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// Claim acquires or refreshes a claim on a task.
// Implemented in the claims task.
func (p *Provider) Claim(ctx context.Context, claim remotestate.Claim, mode remotestate.ClaimMode) (remotestate.ClaimOutcome, error) {
	return remotestate.ClaimOutcome{}, fmt.Errorf("claims: not implemented")
}

// Release removes a claim.
// Implemented in the claims task.
func (p *Provider) Release(ctx context.Context, task, login, machine string, force bool) (remotestate.ReleaseOutcome, error) {
	return remotestate.ReleaseOutcome{}, fmt.Errorf("claims: not implemented")
}

// Claims returns every claim currently in the store, sorted by task name.
// Implemented in the claims task.
func (p *Provider) Claims(ctx context.Context) ([]remotestate.ClaimEntry, error) {
	return nil, fmt.Errorf("claims: not implemented")
}
