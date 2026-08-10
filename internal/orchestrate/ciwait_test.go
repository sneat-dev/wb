package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWaitForCommitChecksRejectsSliceAboveForegroundCeiling(t *testing.T) {
	_, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: "0123456789012345678901234567890123456789",
		Slice: MaxForegroundCheckWaitSlice + time.Second, CheckPollInterval: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("overlong wait error = %v", err)
	}
}
