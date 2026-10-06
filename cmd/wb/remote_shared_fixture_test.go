package main

import (
	"context"
	"errors"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// Genuine retirement tests share this private recorded provider with the
// separately owned remote native fixtures. No command or domain policy lives here.
type slowStatusProvider struct {
	delay                               time.Duration
	statusCalls, listCalls, claimsCalls int
}

func (provider *slowStatusProvider) Publish(context.Context, remotestate.Snapshot) (remotestate.PublishResult, error) {
	return remotestate.PublishResult{}, errors.New("unexpected Publish call")
}

func (provider *slowStatusProvider) List(context.Context) ([]remotestate.Entry, error) {
	provider.listCalls++
	return nil, errors.New("unexpected List call")
}

func (provider *slowStatusProvider) Claim(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
	return remotestate.ClaimOutcome{}, errors.New("unexpected Claim call")
}

func (provider *slowStatusProvider) Release(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
	return remotestate.ReleaseOutcome{}, errors.New("unexpected Release call")
}

func (provider *slowStatusProvider) Claims(context.Context) ([]remotestate.ClaimEntry, error) {
	provider.claimsCalls++
	return nil, errors.New("unexpected Claims call")
}

func (provider *slowStatusProvider) Status(context.Context) (remotestate.StatusSnapshot, error) {
	provider.statusCalls++
	time.Sleep(provider.delay)
	return remotestate.StatusSnapshot{}, nil
}
