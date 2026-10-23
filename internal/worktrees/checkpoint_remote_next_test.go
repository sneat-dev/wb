package worktrees

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRemoteCheckpointNextRejectsTaskBeforeGit(t *testing.T) {
	t.Parallel()
	for _, task := range []string{"", "../escape", "task with spaces"} {
		t.Run(task, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			_, cause := CheckpointRemoteRef(task)
			if cause == nil {
				t.Fatal("invalid task control was accepted")
			}
			pushed, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{Root: root, Task: task, HeadSHA: strings.Repeat("a", 40)})
			if err == nil || err.Error() != cause.Error() || pushed != (RemoteCheckpointResult{}) {
				t.Fatalf("push task admission = %+v, %v; want %v", pushed, err, cause)
			}
			fetched, err := FetchRemoteCheckpoint(context.Background(), FetchRemoteCheckpointOptions{Root: root, Task: task})
			if err == nil || err.Error() != cause.Error() || fetched != (RemoteCheckpointFetchResult{}) {
				t.Fatalf("fetch task admission = %+v, %v; want %v", fetched, err, cause)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("task refusal changed repository contents: %v, %v", entries, err)
			}
		})
	}
}
