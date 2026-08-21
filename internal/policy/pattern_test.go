package policy

import "testing"

func TestPatternMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		self    string
		path    string
		want    bool
	}{
		{"exact module path", "github.com/acme/thing", "", "github.com/acme/thing", true},
		{"exact rejects subpackage", "github.com/acme/thing", "", "github.com/acme/thing/sub", false},
		{"ellipsis matches the prefix itself", "github.com/acme/thing/...", "", "github.com/acme/thing", true},
		{"ellipsis matches one segment", "github.com/acme/thing/...", "", "github.com/acme/thing/sub", true},
		{"ellipsis matches many segments", "github.com/acme/thing/...", "", "github.com/acme/thing/a/b/c", true},
		{"ellipsis does not match a sibling prefix", "github.com/acme/thing/...", "", "github.com/acme/thingamajig", false},

		{"star matches within a segment", "github.com/acme/ext-*/...", "", "github.com/acme/ext-cal/backend", true},
		{"star does not cross a separator", "github.com/acme/ext-*/...", "", "github.com/acme/ext/cal/backend", false},
		{"star requires the literal prefix", "github.com/acme/ext-*/...", "", "github.com/acme/cal/backend", false},
		{"bare star matches any one segment", "github.com/acme/*/...", "", "github.com/acme/cal/backend", true},

		{"brace alternation first", "github.com/acme/{host,bots}/...", "", "github.com/acme/host/pkg", true},
		{"brace alternation second", "github.com/acme/{host,bots}/...", "", "github.com/acme/bots/pkg", true},
		{"brace alternation rejects others", "github.com/acme/{host,bots}/...", "", "github.com/acme/other/pkg", false},
		{"brace combines with star", "github.com/dal-go/dalgo{2,4}*/...", "", "github.com/dal-go/dalgo2firestore", true},
		{"brace with star rejects the core module", "github.com/dal-go/dalgo{2,4}*/...", "", "github.com/dal-go/dalgo", false},

		{"self expands to the scanned module", "<self>/...", "github.com/acme/thing/backend", "github.com/acme/thing/backend/dal4thing", true},
		{"self rejects a different module", "<self>/...", "github.com/acme/thing/backend", "github.com/acme/other/backend/dal", false},
		{"self without ellipsis is exact", "<self>", "github.com/acme/thing/backend", "github.com/acme/thing/backend", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pattern, err := CompilePattern(tc.pattern)
			if err != nil {
				t.Fatalf("CompilePattern(%q): %v", tc.pattern, err)
			}
			if got := pattern.Match(tc.path, tc.self); got != tc.want {
				t.Fatalf("Match(%q, self=%q) = %v, want %v", tc.path, tc.self, got, tc.want)
			}
		})
	}
}

func TestCompilePatternRejectsMalformed(t *testing.T) {
	for _, pattern := range []string{"", "github.com/acme/{unclosed/...", "github.com/acme/.../trailing", "github.com/acme/{}/x"} {
		if _, err := CompilePattern(pattern); err == nil {
			t.Fatalf("CompilePattern(%q) succeeded, want error", pattern)
		}
	}
}

func TestPatternCoversReportsShadowing(t *testing.T) {
	broad, err := CompilePattern("github.com/acme/*/...")
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := CompilePattern("github.com/acme/ext-*/...")
	if err != nil {
		t.Fatal(err)
	}
	if !broad.Covers(narrow) {
		t.Fatal("github.com/acme/*/... should cover github.com/acme/ext-*/...")
	}
	if narrow.Covers(broad) {
		t.Fatal("github.com/acme/ext-*/... must not cover github.com/acme/*/...")
	}
}
