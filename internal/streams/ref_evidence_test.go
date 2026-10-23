package streams

import (
	"context"
	"testing"
)

func TestRemoteHeadTreatsEmptySuccessfulOutputAsAbsent(t *testing.T) {
	t.Parallel()
	calls := 0
	git := gitCommands{command: func(_ context.Context, dir, input string, args ...string) (string, error) {
		calls++
		if dir != "checkout" || input != "" || len(args) != 4 || args[3] != "refs/remotes/origin/stream/example" {
			t.Fatalf("dir=%q input=%q args=%v", dir, input, args)
		}
		return " \n", nil
	}}
	sha, present, err := git.RemoteHead(context.Background(), "checkout", "stream/example")
	if sha != "" || present || err != nil || calls != 1 {
		t.Fatalf("sha=%q present=%v error=%v calls=%d", sha, present, err, calls)
	}
}
