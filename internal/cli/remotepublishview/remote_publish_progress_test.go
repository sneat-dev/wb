package remotepublishview

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
	progress := NewProgress(&out, true)
	progress.Start(2)
	progress.RepositoryComplete("acme/one", nil)
	progress.RepositoryComplete("acme/two", errors.New("broken"))
	progress.Phase("inspecting worktrees")
	progress.Worktree(worktrees.ListProgress{Path: "/tmp/acme/one", Done: false})
	progress.Worktree(worktrees.ListProgress{Repository: "acme/one", Done: true})
	progress.Phase("publishing snapshot")
	progress.Finish("published 2 repositories and 1 worktrees")

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
