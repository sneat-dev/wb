package orchestrate

import "testing"

func TestLandedIncompleteMessagesNameWhereTheMergeLanded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		result PullRequestLandResult
		want   string
	}{
		{"base and merge commit", PullRequestLandResult{BaseRef: "main", MergeSHA: "0123456789abcdef"}, "main (0123456789ab)"},
		{"base only", PullRequestLandResult{BaseRef: "main"}, "main"},
		{"neither", PullRequestLandResult{}, "the base branch"},
	} {
		if got := test.result.baseDescription(); got != test.want {
			t.Errorf("%s: baseDescription = %q, want %q", test.name, got, test.want)
		}
	}
}
