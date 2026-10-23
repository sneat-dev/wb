package worktrees

import "testing"

func TestWorktreeFilterPreservesRepositoryCaseVariants(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		filter     string
		candidates []string
		want       bool
	}{
		{"Acme/App", []string{"acme/app"}, true},
		{"acme/app", []string{"Acme/App"}, true},
		{"ACME/APP", []string{"", "/worktrees/task/github.com/Acme/App"}, true},
		{"acme/app", []string{"acme/app-extra"}, true},
		{"acme/app", []string{"acme/another-app", "other/app"}, false},
		{"acme/app", []string{""}, false},
		{"", nil, true},
	} {
		if got := filterMatches(test.filter, test.candidates...); got != test.want {
			t.Fatalf("filterMatches(%q, %q)=%t want %t", test.filter, test.candidates, got, test.want)
		}
	}
}
