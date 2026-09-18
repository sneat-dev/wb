package gitops

import (
	"errors"
	"reflect"
	"testing"
)

// lastLine is the primitive openPR uses to pull the PR URL out of gh's output,
// which can carry warnings on earlier lines. It must keep everything before the
// final line intact and trim surrounding whitespace.
func TestLgCovLastLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "single line", in: "only", want: "only"},
		{name: "trailing newline", in: "a\nb\n", want: "b"},
		{name: "warnings before url", in: "warning: creating\nhttps://example.test/pr/1\n", want: "https://example.test/pr/1"},
		{name: "surrounding whitespace", in: "  spaced  ", want: "spaced"},
		{name: "blank lines", in: "\n\nlast\n\n", want: "last"},
		{name: "inner spaces preserved", in: "a\nb  c\n", want: "b  c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastLine(tc.in); got != tc.want {
				t.Fatalf("lastLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// isTransientPullFailure(nil) must not panic and must not claim a nil error is
// worth retrying; Pull relies on that to return success immediately.
func TestLgCovIsTransientPullFailureNil(t *testing.T) {
	if isTransientPullFailure(nil) {
		t.Fatal("isTransientPullFailure(nil) = true, want false")
	}
}

// exitCode reports -1 for an error that did not come from a finished process,
// which is how SkipSync and HasCommits tell "git refused" apart from "could not
// run git at all".
func TestLgCovExitCodeNonProcessError(t *testing.T) {
	if got := exitCode(errors.New("no process here")); got != -1 {
		t.Fatalf("exitCode(plain error) = %d, want -1", got)
	}
}

// A detached HEAD has no branch, and Summary must say so instead of rendering
// an empty name.
func TestLgCovTrackingStateSummaryDetached(t *testing.T) {
	if got := (TrackingState{}).Summary(); got != "detached HEAD" {
		t.Fatalf("zero TrackingState Summary() = %q, want %q", got, "detached HEAD")
	}
}

// RepoStatus.Summary must count every category it renders, including conflicts
// and stash entries, and pluralize each one on its own.
func TestLgCovRepoStatusSummaryConflictsAndStashes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status RepoStatus
		want   string
	}{
		{
			name:   "singular conflict and stash",
			status: RepoStatus{Conflicted: []string{"a.txt"}, Stashed: []string{"s1"}},
			want:   "1 conflict, 1 stash entry",
		},
		{
			name:   "plural conflict",
			status: RepoStatus{Conflicted: []string{"a.txt", "b.txt"}, Stashed: []string{"s1"}},
			want:   "2 conflicts, 1 stash entry",
		},
		{
			name:   "everything together",
			status: RepoStatus{Modified: []string{"m"}, Untracked: []string{"u"}, Conflicted: []string{"c"}, Unpushed: []string{"p"}, Stashed: []string{"s"}},
			want:   "1 modified file, 1 untracked file, 1 conflict, 1 unpushed commit, 1 stash entry",
		},
		{
			name:   "empty",
			status: RepoStatus{},
			want:   "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Summary(); got != tc.want {
				t.Fatalf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// parseUnpushedCommit rejects records that do not carry the sha/subject/parents
// tab structure the git log format promises.
func TestLgCovParseUnpushedCommitRejectsMalformedRecords(t *testing.T) {
	for _, line := range []string{
		"no-tabs-at-all",
		"\tmissing-sha",
		"sha-with-no-subject-tab\t",
		"",
	} {
		if _, err := parseUnpushedCommit(line); err == nil {
			t.Fatalf("parseUnpushedCommit(%q) = nil error, want a malformed-record error", line)
		}
	}
}

// reachableUnpushedCommits walks parents and must not restart work for a commit
// already reached through another path, and must ignore shas its graph does not
// know (a parent outside the unpushed set).
func TestLgCovReachableUnpushedCommitsHandlesSharedAndUnknownParents(t *testing.T) {
	graph := map[string]unpushedCommit{
		"a":  {sha: "a", parents: []string{"b", "c"}},
		"b":  {sha: "b", parents: []string{"c", "outside"}},
		"c":  {sha: "c"},
		"zz": {sha: "zz"}, // not reachable from a
	}
	got := reachableUnpushedCommits("a", graph)
	want := map[string]struct{}{"a": {}, "b": {}, "c": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reachableUnpushedCommits = %v, want %v", got, want)
	}
	if got := reachableUnpushedCommits("missing", graph); len(got) != 0 {
		t.Fatalf("reachableUnpushedCommits(unknown tip) = %v, want empty", got)
	}
}
