package hooks

import (
	"strings"
	"testing"
)

func TestOnlyRemoteRefDeletionsAcceptsAPushThatSendsNothingFromTheCheckout(t *testing.T) {
	t.Parallel()
	zero := strings.Repeat("0", 40)
	sha := strings.Repeat("a", 40)
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{"one deletion", "(delete) " + zero + " refs/heads/feature " + sha + "\n", true},
		{"two deletions", "(delete) " + zero + " refs/heads/a " + sha + "\n(delete) " + zero + " refs/heads/b " + sha + "\n", true},
		{"sha256 deletion", "(delete) " + strings.Repeat("0", 64) + " refs/heads/a " + strings.Repeat("b", 64) + "\n", true},
		{"a deletion and an update", "(delete) " + zero + " refs/heads/a " + sha + "\nrefs/heads/b " + sha + " refs/heads/b " + zero + "\n", false},
		{"an update", "refs/heads/b " + sha + " refs/heads/b " + zero + "\n", false},
		{"no refs at all", "", false},
		{"blank lines only", "\n\n", false},
	} {
		got, err := OnlyRemoteRefDeletions(strings.NewReader(test.input))
		if err != nil || got != test.want {
			t.Errorf("%s: OnlyRemoteRefDeletions = (%t, %v), want %t", test.name, got, err, test.want)
		}
	}
}

func TestOnlyRemoteRefDeletionsRejectsAMalformedRefList(t *testing.T) {
	t.Parallel()
	got, err := OnlyRemoteRefDeletions(strings.NewReader("not four fields\n"))
	if err == nil || got {
		t.Fatalf("OnlyRemoteRefDeletions(malformed) = (%t, %v), want an error and false", got, err)
	}
}
