package runqueue

import "testing"

func TestGovernGOMAXPROCSPreservesAnExistingNonEmptyValue(t *testing.T) {
	t.Parallel()
	if got := GovernGOMAXPROCS("  4  ", 2); got != "  4  " {
		t.Fatalf("GovernGOMAXPROCS(existing set) = %q, want the existing value preserved verbatim", got)
	}
}

func TestGovernGOMAXPROCSFillsInUnitsWhenUnset(t *testing.T) {
	t.Parallel()
	if got := GovernGOMAXPROCS("   ", 3); got != "3" {
		t.Fatalf("GovernGOMAXPROCS(blank existing) = %q, want %q", got, "3")
	}
}

func TestLookupEnvFindsExactKeyAmongOtherEntries(t *testing.T) {
	t.Parallel()
	environment := []string{"PATH=/usr/bin", "GOFLAGS=-race", "HOME=/home/ai"}
	if got := LookupEnv(environment, "GOFLAGS"); got != "-race" {
		t.Fatalf("LookupEnv(GOFLAGS) = %q, want %q", got, "-race")
	}
}

func TestLookupEnvReturnsEmptyForMissingKey(t *testing.T) {
	t.Parallel()
	environment := []string{"PATH=/usr/bin"}
	if got := LookupEnv(environment, "GOFLAGS"); got != "" {
		t.Fatalf("LookupEnv(missing) = %q, want empty", got)
	}
}

func TestGovernGoFlagsAppendsExplicitParallelismForGoInvocations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		argv     []string
		existing string
		units    int
		want     string
	}{
		{"non-positive units leaves existing untouched", []string{"go", "test"}, "-x", 0, "-x"},
		{"non-go argv0 leaves existing untouched", []string{"node", "test"}, "-x", 4, "-x"},
		{"empty argv leaves existing untouched", nil, "-x", 4, "-x"},
		{"already has -p flag, only trims", []string{"go", "test"}, "  -p=8  ", 4, "-p=8"},
		{"already has bare -p flag, only trims", []string{"go", "test"}, "-p 8", 4, "-p 8"},
		{"blank existing gets exactly the new flag", []string{"go", "test"}, "  ", 4, "-p=4"},
		{"non-blank existing gets the flag appended", []string{"go", "test"}, "-race", 4, "-race -p=4"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := GovernGoFlags(testCase.argv, testCase.existing, testCase.units); got != testCase.want {
				t.Fatalf("GovernGoFlags(%v, %q, %d) = %q, want %q", testCase.argv, testCase.existing, testCase.units, got, testCase.want)
			}
		})
	}
}
