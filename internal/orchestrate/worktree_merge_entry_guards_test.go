package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareWorktreeMergeRejectsMissingInputsBeforeCreatingCandidate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name    string
		options WorktreeMergePrepareOptions
		want    string
	}{
		{"missing projects root", WorktreeMergePrepareOptions{Sources: []string{"source"}, Target: "main", PrepareTimeout: time.Second}, "projects root is required"},
		{"missing source", WorktreeMergePrepareOptions{ProjectsRoot: root, Target: "main"}, "at least one source worktree is required"},
		{"unresolvable default target", WorktreeMergePrepareOptions{ProjectsRoot: root, Sources: []string{filepath.Join(root, "missing")}}, "resolve source canonical clone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := PrepareWorktreeMerge(context.Background(), tc.options)
			if err == nil || !strings.Contains(err.Error(), tc.want) || got.ReceiptPath != "" {
				t.Fatalf("receipt=%+v error=%v, want %q without a receipt", got, err, tc.want)
			}
		})
	}
}

func TestLandWorktreeMergeRefusesUnsafeEarlyRequestsWithoutMutation(t *testing.T) {
	fixture, original, _ := orchCovMergeReceiptFixture(t)
	for _, tc := range []struct {
		name    string
		options WorktreeMergeLandOptions
		want    string
	}{
		{"direct stop before merge", WorktreeMergeLandOptions{StopBeforeMerge: true, Route: WorktreeMergeRouteDirect}, "requires the pull-request route"},
		{"cleanup before landing", WorktreeMergeLandOptions{StopBeforeMerge: true, Route: WorktreeMergeRoutePullRequest, Cleanup: true}, "cannot clean managed assets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := tc.options
			options.ProjectsRoot, options.Receipt = fixture.githubDir, original.ReceiptPath
			got, err := LandWorktreeMerge(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), tc.want) || got.Candidate.SHA != original.Candidate.SHA || got.Status != original.Status {
				t.Fatalf("receipt=%+v error=%v, want %q and unchanged candidate", got, err, tc.want)
			}
		})
	}
}
