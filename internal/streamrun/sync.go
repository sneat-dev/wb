package streamrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
)

// SyncRefusal qualifies an existing engine refusal with its affected member.
type SyncRefusal struct {
	Repository string
	Refusal    *streamsync.Refusal
}

func (refusal *SyncRefusal) Error() string {
	return refusal.Repository + ": " + refusal.Refusal.Error()
}
func (refusal *SyncRefusal) Unwrap() error { return refusal.Refusal }
func (service *Service) Sync(ctx context.Context, request SyncRequest) ([]streamsync.Result, error) {
	store, err := service.open(request.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	stream, err := store.Load(request.Name)
	if err != nil {
		return nil, err
	}
	trigger := streamsync.PushTrigger("")
	if request.PushTrigger != "" {
		trigger = request.PushTrigger
	}

	sync := service.sync
	if sync == nil {
		engine := &streamsync.Engine{
			Git:      streamsync.ExecGit{Timeout: request.Timeout},
			Bumper:   streamsync.ExecBumper{Timeout: request.Timeout},
			Verifier: batchVerifier{timeout: request.Timeout},
			CI:       workflowMechanisms{},
			Events:   streamEventSink{log: store.EventLog(request.Name)},
		}
		sync = engine.Sync
	}

	results := make([]streamsync.Result, 0, len(stream.Members))
	// Every MEMBER, not only the consumers: the library has its own
	// stream/<name> branch and its base moves too. The bumps are
	// consumer-only, but the rebase is not — an empty Libraries set
	// makes the library's bump phase a no-op by itself.
	for _, member := range stream.Members {
		if member.Worktree == "" {
			continue
		}
		memberBase := member.Base
		if request.Base != "" {
			memberBase = request.Base
		}
		memberLibraries := request.Libraries
		if member.Role == streams.RoleLibrary {
			// A library does not bump itself to its own version.
			memberLibraries = nil
		}
		result, syncErr := sync(ctx, streamsync.Options{
			Stream: stream.Name, Worktree: member.Worktree, Repository: member.Repository,
			Branch: member.Branch, Base: memberBase, Libraries: memberLibraries,
			RecordedRemoteHead: member.Lease.RecordedHead,
			Verify:             request.Verify, AllowMidReview: request.AllowMidReview,
			PushTrigger: trigger, PushReason: request.PushReason, Timeout: request.Timeout,
		})
		if syncErr != nil {
			var refusal *streamsync.Refusal
			if errors.As(syncErr, &refusal) {
				return nil, &SyncRefusal{Repository: member.Repository, Refusal: refusal}
			}
			return nil, syncErr
		}
		if result.RecordedRemoteHead != "" {
			if err := store.RecordRemoteHead(stream.Name, member.Repository, result.RecordedRemoteHead); err != nil {
				return nil, fmt.Errorf("record fetched %s head for %s: %w", result.StreamRebase.Branch, member.Repository, err)
			}
		}
		results = append(results, result)
	}
	return results, nil
}
