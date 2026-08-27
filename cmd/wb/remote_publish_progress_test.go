package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestRemotePublishProgressShowsRepositoryAndWorktreePhases(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newRemotePublishProgress(&out, true)
	progress.start(2)
	progress.repositoryComplete("acme/one", nil)
	progress.repositoryComplete("acme/two", errors.New("broken"))
	progress.phase("inspecting worktrees")
	progress.worktree(worktrees.ListProgress{Path: "/tmp/acme/one", Done: false})
	progress.worktree(worktrees.ListProgress{Repository: "acme/one", Done: true})
	progress.phase("publishing snapshot")
	progress.finish("published 2 repositories and 1 worktrees")

	rendered := out.String()
	for _, want := range []string{
		"remote publish: scanning 0/2 repositories",
		"scanned 1/2 repositories; acme/one: ok",
		"scanned 2/2 repositories; acme/two: error",
		"remote publish: inspecting worktrees",
		"worktrees inspecting acme/one; 0 completed",
		"worktrees inspected acme/one; 1 completed",
		"remote publish: publishing snapshot",
		"published 2 repositories and 1 worktrees",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
}
