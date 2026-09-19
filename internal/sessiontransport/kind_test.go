package sessiontransport

import "testing"

func TestKindValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind Kind
		want bool
	}{
		{KindHerdr, true},
		{KindTmux, true},
		{KindNone, true},
		{"", false},
		{"docker", false},
		{"HERDR", false},
	}
	for _, tc := range cases {
		if got := tc.kind.Valid(); got != tc.want {
			t.Errorf("Kind(%q).Valid() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestKindString(t *testing.T) {
	t.Parallel()
	if got, want := KindHerdr.String(), "herdr"; got != want {
		t.Errorf("KindHerdr.String() = %q, want %q", got, want)
	}
}

func TestKindsListsEveryShippedTransportExactlyOnce(t *testing.T) {
	t.Parallel()
	seen := map[Kind]bool{}
	for _, kind := range Kinds() {
		if !kind.Valid() {
			t.Errorf("Kinds() contains %q, which Kind.Valid() rejects", kind)
		}
		if seen[kind] {
			t.Errorf("Kinds() lists %q more than once", kind)
		}
		seen[kind] = true
	}
	for _, kind := range []Kind{KindHerdr, KindTmux, KindNone} {
		if !seen[kind] {
			t.Errorf("Kinds() is missing shipped transport %q", kind)
		}
	}
}

func TestKindsReturnsIndependentValues(t *testing.T) {
	t.Parallel()
	// M4's guarantee, applied to Kinds() too: mutating one caller's slice
	// must never affect a later caller's.
	first := Kinds()
	first[0] = "corrupted"
	if second := Kinds(); second[0] == "corrupted" {
		t.Fatal("mutating one Kinds() result affected a later call; want independent slices")
	}
}
