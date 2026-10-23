package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func TestPullRequestUpdateRecordsNoOpObservationFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		change     func(*pullRequestUpdateOps)
		wantStatus string
		wantReason string
		wantError  string
	}{
		{"target reread failed", func(ops *pullRequestUpdateOps) {
			calls := 0
			ops.target = func(context.Context, string, string) (string, string) {
				calls++
				if calls == 1 {
					return "target", ""
				}
				return "", "target unavailable"
			}
		}, "unverified", "target unavailable", ""},
		{"local checkout could not sync", func(ops *pullRequestUpdateOps) {
			ops.target = func(context.Context, string, string) (string, string) { return "target", "" }
			ops.syncLocal = func(context.Context, PullRequestUpdateOptions, string, string) string {
				return "local sync skipped: checkout changed"
			}
		}, "unchanged_partial", "", ""},
		{"receipt write failed", func(ops *pullRequestUpdateOps) {
			ops.target = func(context.Context, string, string) (string, string) { return "target", "" }
			ops.persist = func(PullRequestUpdateResult) error { return errors.New("receipt unavailable") }
		}, "unchanged", "", "receipt unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops, _ := prUpdateFake(t)
			ops.contains = func(context.Context, string, string, string) (bool, string) { return true, "" }
			tc.change(&ops)
			got, err := updatePullRequestWith(context.Background(), options, ops)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %q; receipt = %+v", err, tc.wantError, got)
			}
			if got.Status != tc.wantStatus || !strings.Contains(got.Reason, tc.wantReason) {
				t.Fatalf("receipt = %+v, want status %q and reason %q", got, tc.wantStatus, tc.wantReason)
			}
		})
	}
}

func TestPullRequestUpdateReportsEachPostAcceptanceReceiptFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		failWrite  int
		wantError  string
		wantStatus string
	}{
		{"accepted update", 2, "record accepted PR update", "unverified"},
		{"verified update", 3, "record verified PR update", "updated"},
		{"local sync result", 4, "record local sync result", "updated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ops, saved := prUpdateFake(t)
			reads := 0
			ops.read = func(context.Context, string, string) (githubchecks.PullRequestView, error) {
				reads++
				if reads == 1 {
					return testfixture.PullRequestView[githubchecks.PullRequestView](t, "old"), nil
				}
				return testfixture.PullRequestView[githubchecks.PullRequestView](t, "new"), nil
			}
			writes := 0
			ops.persist = func(receipt PullRequestUpdateResult) error {
				writes++
				if writes == tc.failWrite {
					return errors.New("disk full")
				}
				*saved = append(*saved, receipt)
				return nil
			}
			got, err := updatePullRequestWith(context.Background(), options, ops)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || got.Status != tc.wantStatus || got.AfterSHA != "new" {
				t.Fatalf("receipt = %+v, error = %v, saved = %+v", got, err, *saved)
			}
			if tc.wantStatus == "unverified" && (*saved)[len(*saved)-1].Status != "unverified" {
				t.Fatalf("failed update lacked durable unverified receipt: %+v", *saved)
			}
		})
	}
}

func TestPullRequestUpdateProductionOpsRejectInvalidCanonicalRoot(t *testing.T) {
	t.Parallel()
	ops := productionPullRequestUpdateOps()
	if ops.read == nil || ops.target == nil || ops.contains == nil || ops.update == nil || ops.parents == nil || ops.syncLocal == nil || ops.newReceipt == nil || ops.persist == nil {
		t.Fatal("production update operations are incomplete")
	}
	proved, err := ops.proveTree(context.Background(), PullRequestUpdateOptions{Repository: "acme/app"}, "main", "feature", "before", "parent", "after")
	if proved || err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("invalid canonical root proof = %t, %v", proved, err)
	}
	if _, err := UpdatePullRequest(context.Background(), PullRequestUpdateOptions{Repository: "../app", PullRequest: "7"}); err == nil {
		t.Fatal("public update accepted unsafe repository")
	}
	if err := persistPullRequestUpdateReceipt(PullRequestUpdateResult{}); err == nil || !strings.Contains(err.Error(), "receipt path is required") {
		t.Fatalf("missing receipt path error = %v", err)
	}
	if got := syncOwnedPullRequestUpdateWorktree(context.Background(), PullRequestUpdateOptions{Repository: "acme/app"}, "feature", "head"); !strings.Contains(got, "canonical checkout lookup failed") {
		t.Fatalf("invalid canonical root sync = %q", got)
	}
}
