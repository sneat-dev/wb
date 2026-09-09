// Package repositoryevents receives provider-owned repository events and
// turns them into durable local sync jobs.
package repositoryevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

type Source interface {
	PollRepositoryEvents(context.Context, string, int, time.Duration) (repositoryevent.PollResponse, error)
	AckRepositoryEvents(context.Context, repositoryevent.AckRequest) (repositoryevent.AckResponse, error)
}

type Enqueuer interface {
	Enqueue(context.Context, repositoryevent.Event) (string, error)
}

type Receiver struct {
	Source        Source
	Queue         Enqueuer
	Cursor        CursorStore
	PollWait      time.Duration
	RetryDelay    time.Duration
	Progress      func(string)
	ProgressEvery time.Duration
}

func (receiver Receiver) Run(ctx context.Context) {
	wait := receiver.PollWait
	if wait <= 0 || wait > repositoryevent.MaxWaitSeconds*time.Second {
		wait = repositoryevent.DefaultWaitSeconds * time.Second
	}
	retry := receiver.RetryDelay
	if retry <= 0 {
		retry = time.Second
	}
	progressEvery := receiver.ProgressEvery
	if progressEvery <= 0 || progressEvery > 10*time.Second {
		progressEvery = 10 * time.Second
	}
	ticker := time.NewTicker(progressEvery)
	defer ticker.Stop()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				receiver.progress("repository event receiver: waiting for provider events")
			}
		}
	}()
	defer close(done)
	for ctx.Err() == nil {
		if err := receiver.ReceiveOnce(ctx, wait); err != nil {
			receiver.progress("repository event receiver: " + err.Error())
			if sleepContext(ctx, retry) != nil {
				return
			}
			continue
		}
	}
}

func (receiver Receiver) ReceiveOnce(ctx context.Context, wait time.Duration) error {
	if receiver.Source == nil || receiver.Queue == nil {
		return errors.New("repository event receiver is not configured")
	}
	state, err := receiver.Cursor.loadState()
	if err != nil {
		return fmt.Errorf("load repository event cursor: %w", err)
	}
	if state.PendingAck != nil {
		if _, err := receiver.Source.AckRepositoryEvents(ctx, *state.PendingAck); err != nil {
			return fmt.Errorf("resume repository event acknowledgement: %w", err)
		}
		state.Cursor = state.PendingAck.Cursor
		state.PendingAck = nil
		if err := receiver.Cursor.saveState(state); err != nil {
			return fmt.Errorf("persist resumed repository event acknowledgement: %w", err)
		}
	}
	cursor := state.Cursor
	response, err := receiver.Source.PollRepositoryEvents(ctx, cursor, repositoryevent.DefaultLimit, wait)
	if err != nil {
		return err
	}
	if len(response.Events) == 0 {
		if response.NextCursor != cursor {
			state.Cursor = response.NextCursor
			if err := receiver.Cursor.saveState(state); err != nil {
				return fmt.Errorf("persist empty repository event cursor: %w", err)
			}
		}
		return nil
	}
	ids := make([]string, 0, len(response.Events))
	for _, event := range response.Events {
		if _, err := receiver.Queue.Enqueue(ctx, event); err != nil {
			// The provider sees no ACK for this batch. Events already enqueued
			// before this failure are harmless on replay because Queue dedupes IDs.
			return fmt.Errorf("durably enqueue repository event %s: %w", event.ID, err)
		}
		ids = append(ids, event.ID)
	}
	ack := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: response.NextCursor, EventIDs: ids}
	state.PendingAck = &ack
	if err := receiver.Cursor.saveState(state); err != nil {
		return fmt.Errorf("persist pending repository event acknowledgement: %w", err)
	}
	if _, err := receiver.Source.AckRepositoryEvents(ctx, ack); err != nil {
		return err
	}
	state.Cursor = response.NextCursor
	state.PendingAck = nil
	if err := receiver.Cursor.saveState(state); err != nil {
		return fmt.Errorf("persist acknowledged repository event cursor: %w", err)
	}
	receiver.progress(fmt.Sprintf("repository event receiver: acknowledged %d event(s)", len(ids)))
	return nil
}

func (receiver Receiver) progress(message string) {
	if receiver.Progress != nil {
		receiver.Progress(message)
	}
}

type CursorStore struct{ Path string }

type cursorState struct {
	Version    int                         `json:"version"`
	Cursor     string                      `json:"cursor"`
	PendingAck *repositoryevent.AckRequest `json:"pending_ack,omitempty"`
}

func (store CursorStore) Load() (string, error) {
	state, err := store.loadState()
	return state.Cursor, err
}

func (store CursorStore) loadState() (cursorState, error) {
	raw, err := os.ReadFile(store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return cursorState{Version: repositoryevent.ContractVersion}, nil
	}
	if err != nil {
		return cursorState{}, err
	}
	var state cursorState
	if err := json.Unmarshal(raw, &state); err != nil {
		return cursorState{}, err
	}
	if state.Version != repositoryevent.ContractVersion {
		return cursorState{}, fmt.Errorf("unsupported cursor state version %d", state.Version)
	}
	if err := repositoryevent.ValidateCursor(state.Cursor, state.Cursor != ""); err != nil {
		return cursorState{}, err
	}
	if state.PendingAck != nil {
		if err := state.PendingAck.Validate(); err != nil {
			return cursorState{}, fmt.Errorf("invalid pending repository event acknowledgement: %w", err)
		}
	}
	return state, nil
}

func (store CursorStore) Save(cursor string) error {
	return store.saveState(cursorState{Version: repositoryevent.ContractVersion, Cursor: cursor})
}

func (store CursorStore) saveState(state cursorState) error {
	state.Version = repositoryevent.ContractVersion
	cursor := state.Cursor
	if err := repositoryevent.ValidateCursor(cursor, true); err != nil {
		if cursor != "" || state.PendingAck == nil {
			return err
		}
	}
	if state.PendingAck != nil {
		if err := state.PendingAck.Validate(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.Path), ".cursor-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, store.Path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(store.Path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
