package worktrees

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestInspectRepositoryBranchesReportsHeartbeatWhileRepositoryBlocks(t *testing.T) {
	var progress bytes.Buffer
	repository := discover.Repo{Org: "sneat-dev", Name: "wb"}
	sweep := branchSweepOptions{Progress: &progress}
	inspect := func(context.Context, discover.Repo, branchSweepOptions, map[string]string) ([]BranchEntry, string) {
		time.Sleep(35 * time.Millisecond)
		return []BranchEntry{{Repository: repository.Slug()}}, ""
	}

	entries, diagnostic := inspectRepositoryBranchesWithHeartbeat(
		context.Background(), repository, sweep, nil, 2, 7, 10*time.Millisecond, inspect,
	)
	if diagnostic != "" {
		t.Fatalf("diagnostic = %q, want empty", diagnostic)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	line := "[2/7] still scanning sneat-dev/wb"
	if count := strings.Count(progress.String(), line); count < 3 {
		t.Fatalf("heartbeat count = %d, want at least 3; progress:\n%s", count, progress.String())
	}
}

func TestInspectRepositoryBranchesDoesNotReportHeartbeatForFastRepository(t *testing.T) {
	var progress bytes.Buffer
	repository := discover.Repo{Org: "sneat-dev", Name: "wb"}
	sweep := branchSweepOptions{Progress: &progress}
	inspect := func(context.Context, discover.Repo, branchSweepOptions, map[string]string) ([]BranchEntry, string) {
		return nil, ""
	}

	_, _ = inspectRepositoryBranchesWithHeartbeat(
		context.Background(), repository, sweep, nil, 1, 1, time.Hour, inspect,
	)
	if progress.Len() != 0 {
		t.Fatalf("unexpected heartbeat for fast repository: %q", progress.String())
	}
}
