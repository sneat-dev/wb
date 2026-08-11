package orchestrate

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSortRemoteChecksUsesProducerAsFinalDeterministicKey(t *testing.T) {
	checks := []RemoteCheck{
		{Name: "build", Bucket: "pass", Link: "https://example.test/build", AppID: 22},
		{Name: "build", Bucket: "pass", Link: "https://example.test/build", AppID: 11},
	}
	sortRemoteChecks(checks)
	if got := []int64{checks[0].AppID, checks[1].AppID}; !reflect.DeepEqual(got, []int64{11, 22}) {
		t.Fatalf("sorted producer IDs = %v", got)
	}
}

func TestWaitForCommitChecksRejectsSliceAboveForegroundCeiling(t *testing.T) {
	_, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: "0123456789012345678901234567890123456789",
		Slice: MaxForegroundCheckWaitSlice + time.Second, CheckPollInterval: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("overlong wait error = %v", err)
	}
}

func TestGitHubChecksPollIntervalDefaultsToQuotaAwareCadence(t *testing.T) {
	if got := githubChecksPollInterval(Options{}); got != DefaultCheckPollInterval {
		t.Fatalf("default GitHub check poll interval = %s, want %s", got, DefaultCheckPollInterval)
	}
	if DefaultCheckPollInterval != 30*time.Second {
		t.Fatalf("quota-aware default = %s, want 30s", DefaultCheckPollInterval)
	}
}
