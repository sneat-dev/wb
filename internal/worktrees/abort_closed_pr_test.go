package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbortClosedPullRequestOptionValidation(t *testing.T) {
	t.Parallel()
	base := AbortOptions{ProjectsRoot: t.TempDir(), Task: "x", Disposition: AbortDiscarded, ClosedPullRequest: "6", Reason: "duplicate"}
	for _, test := range []struct {
		name   string
		mutate func(*AbortOptions)
		want   string
	}{
		{"needs a reason", func(o *AbortOptions) { o.Reason = " " }, "--reason is required with --closed-pr"},
		{"reason is one line", func(o *AbortOptions) { o.Reason = "a\nb" }, "--reason is required with --closed-pr"},
		{"excludes absorbed-by", func(o *AbortOptions) { o.AbsorbedBy = "5" }, "mutually exclusive"},
		{"only discarded", func(o *AbortOptions) { o.Disposition, o.Successor = AbortHandoff, "next" }, "valid only with discarded"},
		{"reason alone is for orphaned", func(o *AbortOptions) { o.ClosedPullRequest = "" }, "valid only with the orphaned disposition or --closed-pr"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := base
			test.mutate(&options)
			if _, err := Abort(context.Background(), options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRecordClosedPullRequestDiscardWithoutEvidenceWritesNothing(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := recordClosedPullRequestDiscard(home, "task", &AbortResult{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "closed-pr-discards")); !os.IsNotExist(err) {
		t.Fatalf("an audit directory appeared without evidence: %v", err)
	}
	blocked := filepath.Join(t.TempDir(), "closed-pr-discards")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := &AbortResult{ListResult: ListResult{Repository: "acme/app", HeadSHA: "abc"}, ClosedPullRequest: &ClosedPullRequestEvidence{}}
	if err := recordClosedPullRequestDiscard(filepath.Dir(blocked), "task", result); err == nil {
		t.Fatal("an unwritable audit directory must refuse the discard")
	}
}
