package cmdsession

import "testing"

func TestCondense(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   []string
		max  int
		want string
	}{
		{nil, 24, "-"},
		{[]string{"only"}, 24, "only"},
		{[]string{"a", "b", "c"}, 24, "3"},
		{[]string{"a-very-long-effort-identifier-here"}, 10, "a-very-lon…"},
	}
	for _, c := range cases {
		if got := condense(c.in, c.max); got != c.want {
			t.Errorf("condense(%v, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
func TestCondenseTruncatesRunesNotBytes(t *testing.T) {
	t.Parallel()
	if got := condense([]string{"héllo wörld effort"}, 11); got != "héllo wörld…" {
		t.Fatalf("condense = %q, want rune-aware truncation", got)
	}
}
