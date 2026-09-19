package herdr

import (
	"errors"
	"testing"
)

func TestParseVersionFromCLIOutput(t *testing.T) {
	got, err := ParseVersion("herdr 0.9.1\n")
	if err != nil {
		t.Fatalf("ParseVersion() error = %v", err)
	}
	want := Version{Major: 0, Minor: 9, Patch: 1, Raw: "herdr 0.9.1"}
	if got != want {
		t.Fatalf("ParseVersion() = %#v, want %#v", got, want)
	}
}

func TestParseVersionBareNumber(t *testing.T) {
	got, err := ParseVersion(MinimumVersion)
	if err != nil {
		t.Fatalf("ParseVersion(%q) error = %v", MinimumVersion, err)
	}
	if got.String() != MinimumVersion {
		t.Fatalf("ParseVersion(%q).String() = %q", MinimumVersion, got.String())
	}
}

func TestParseVersionUnparseable(t *testing.T) {
	_, err := ParseVersion("not a version at all")
	if !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("ParseVersion(garbage) error = %v, want ErrUnparseableOutput", err)
	}
}

func TestParseVersionOutOfIntRange(t *testing.T) {
	// A component with far more digits than fits in an int forces
	// strconv.Atoi to fail even though the regex matched.
	_, err := ParseVersion("99999999999999999999999999999999.0.0")
	if !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("ParseVersion(overflow) error = %v, want ErrUnparseableOutput", err)
	}
}

func TestMustParseVersionPanicsOnGarbage(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("mustParseVersion(garbage) did not panic")
		}
	}()
	mustParseVersion("not a version")
}

func TestMinimumVersionParsesCleanly(t *testing.T) {
	if minimumVersion.String() == "" {
		t.Fatal("package-level minimumVersion did not parse at init")
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b Version
		want bool
	}{
		{Version{Major: 0, Minor: 9, Patch: 0}, Version{Major: 0, Minor: 9, Patch: 1}, true},
		{Version{Major: 0, Minor: 9, Patch: 1}, Version{Major: 0, Minor: 9, Patch: 1}, false},
		{Version{Major: 0, Minor: 9, Patch: 2}, Version{Major: 0, Minor: 9, Patch: 1}, false},
		{Version{Major: 0, Minor: 8, Patch: 9}, Version{Major: 0, Minor: 9, Patch: 0}, true},
		{Version{Major: 1, Minor: 0, Patch: 0}, Version{Major: 0, Minor: 99, Patch: 99}, false},
	}
	for _, tc := range cases {
		if got := tc.a.Less(tc.b); got != tc.want {
			t.Fatalf("%s.Less(%s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestVersionStringFallsBackWithoutRaw(t *testing.T) {
	v := Version{Major: 1, Minor: 2, Patch: 3}
	if got, want := v.String(), "1.2.3"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate(short) = %q", got)
	}
	if got := truncate("0123456789abcdef", 4); got != "0123…" {
		t.Fatalf("truncate(long) = %q, want %q", got, "0123…")
	}
}
