package cmdfleet

import "testing"

func TestPluralSuffix(t *testing.T) {
	t.Parallel()
	for count, want := range map[int]string{0: "s", 1: "", 2: "s", 9: "s"} {
		if got := pluralSuffix(count); got != want {
			t.Errorf("pluralSuffix(%d) = %q, want %q", count, got, want)
		}
	}
}
