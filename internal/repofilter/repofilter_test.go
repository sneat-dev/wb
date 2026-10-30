package repofilter

import "testing"

func TestMatchSelectsBySubstringOrGlob(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		filter     string
		candidates []string
		want       bool
	}{
		{"empty filter selects everything", "", []string{"acme/app"}, true},
		{"substring is case-insensitive", "DataTug/", []string{"datatug/websites"}, true},
		{"substring misses other owners", "datatug/", []string{"sneat-co/backstage"}, false},
		{"glob selects the owner's repositories", "datatug/*", []string{"datatug/websites"}, true},
		{"glob selects a host-qualified slug", "datatug/*", []string{"github.com/datatug/websites"}, true},
		{"glob selects a checkout path", "datatug/*", []string{"/Users/a/projects/.worktrees/t/github.com/datatug/backend"}, true},
		{"glob does not cross into another owner", "datatug/*", []string{"sneat-co/datatug"}, false},
		{"glob star does not span a nested path", "datatug/*", []string{"datatug/websites/sub"}, false},
		{"question mark glob", "acme/ap?", []string{"acme/app"}, true},
		{"empty candidates are skipped", "acme", []string{"", "acme/app"}, true},
		{"malformed glob matches nothing", "acme/[", []string{"acme/["}, false},
		{"no candidates match nothing", "acme", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Match(tc.filter, tc.candidates...); got != tc.want {
				t.Fatalf("Match(%q, %q) = %v, want %v", tc.filter, tc.candidates, got, tc.want)
			}
		})
	}
}

func TestValidRejectsOnlyMalformedGlobs(t *testing.T) {
	t.Parallel()
	for filter, want := range map[string]bool{"": true, "acme/": true, "acme/*": true, "acme/[": false} {
		if got := Valid(filter); got != want {
			t.Fatalf("Valid(%q) = %v, want %v", filter, got, want)
		}
	}
}
