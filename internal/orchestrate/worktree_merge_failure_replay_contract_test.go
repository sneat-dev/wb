package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLandedFailureSourceHeadIncompleteIdentityHasNoCustodyEffects(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"task", "worktree", "branch", "SHA"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := WorktreeMergeSource{Task: "source", Worktree: filepath.Join(root, "absent"), Branch: "feature/source", SHA: "recorded"}
			switch field {
			case "task":
				source.Task = ""
			case "worktree":
				source.Worktree = ""
			case "branch":
				source.Branch = ""
			case "SHA":
				source.SHA = ""
			}
			head := "unmodified"
			err := validateLandedFailureAcknowledgementSourceHead(t.Context(), root, WorktreeMergeReceipt{Repository: "acme/app", Target: "main"}, source, "", false, &head)
			if err == nil || err.Error() != "receipt contains an incomplete source identity" || head != "unmodified" {
				t.Fatalf("incomplete %s result head=%q error=%v", field, head, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("incomplete identity created custody state: %v %v", entries, err)
			}
			if _, err := os.Stat(source.Worktree); source.Worktree != "" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete identity created source: %v", err)
			}
		})
	}
}
