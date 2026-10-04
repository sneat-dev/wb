package worktreerun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktrees"
	"path/filepath"
	"strings"
	"testing"
)

// These are the original native load/lane/custody clauses of
// TestCollaborationInfoBoundaries, split from its Cobra rendering clauses.
func TestOriginalCollaborationInfoServiceBoundaries(t *testing.T) {
	t.Parallel()
	authority := infoAuthority(t)
	ports := infoPorts{load: func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		return worktrees.WorkLogView{}, nil
	}, lane: func(string, worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) { return nil, nil }, collaboration: func(string) (worktreecollab.Service, error) { return authority, nil }}
	request := InfoRequest{ProjectsRoot: t.TempDir(), Worktree: "worktree"}
	ports.load = func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		return worktrees.WorkLogView{}, errors.New("view unavailable")
	}
	if _, err := (InfoService{ports: ports}).Inspect(context.Background(), request); err == nil || !strings.Contains(err.Error(), "view unavailable") {
		t.Fatalf("view failure = %v", err)
	}
	ports.load = func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		return worktrees.WorkLogView{}, nil
	}
	ports.lane = func(string, worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) {
		return nil, errors.New("lane unavailable")
	}
	if _, err := (InfoService{ports: ports}).Inspect(context.Background(), request); err == nil || !strings.Contains(err.Error(), "lane unavailable") {
		t.Fatalf("lane failure = %v", err)
	}
	ports.lane = func(string, worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) { return nil, nil }
	ports.collaboration = func(string) (worktreecollab.Service, error) {
		return worktreecollab.Service{}, errors.New("service unavailable")
	}
	if _, err := (InfoService{ports: ports}).Inspect(context.Background(), request); err == nil || !strings.Contains(err.Error(), "service unavailable") {
		t.Fatalf("service failure = %v", err)
	}
	ports.collaboration = func(string) (worktreecollab.Service, error) { return authority, nil }
	request.Worktree = filepath.Join(t.TempDir(), "missing")
	if _, err := (InfoService{ports: ports}).Inspect(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	authority.Ports.Resolve = func(context.Context, string) (worktreecollab.Checkout, error) {
		return worktreecollab.Checkout{}, errors.New("identity unavailable")
	}
	ports.collaboration = func(string) (worktreecollab.Service, error) { return authority, nil }
	request.Worktree = "worktree"
	if _, err := (InfoService{ports: ports}).Inspect(context.Background(), request); err == nil || !strings.Contains(err.Error(), "identity unavailable") {
		t.Fatalf("collaboration refusal = %v", err)
	}
}
