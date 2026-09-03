package main

import (
	"strings"
	"testing"
)

// An operator holds a pull request in whichever form their source gave them:
// what they typed, what a report printed, or what they copied from a browser.
// Every one of them addresses the same pull request, and making the caller
// normalize it is how a URL ends up inside an API path.
func TestPRLandSelectorAcceptsEveryFormAnOperatorHolds(t *testing.T) {
	for _, testCase := range []struct {
		selector   string
		repository string
		number     string
	}{
		{"acme/app#41", "acme/app", "41"},
		{" acme/app#41 ", "acme/app", "41"},
		{"https://github.com/acme/app/pull/41", "acme/app", "41"},
		{"https://github.com/acme/app/pull/41/files", "acme/app", "41"},
	} {
		repository, number, err := splitPullRequestSelector(testCase.selector)
		if err != nil || repository != testCase.repository || number != testCase.number {
			t.Errorf("splitPullRequestSelector(%q) = %q, %q, %v", testCase.selector, repository, number, err)
		}
	}
	for _, invalid := range []string{"", "acme/app", "acme#41", "acme/app/extra#41", "acme/app#", "acme/app#abc"} {
		if _, _, err := splitPullRequestSelector(invalid); err == nil {
			t.Errorf("splitPullRequestSelector(%q) accepted an ambiguous selector", invalid)
		}
	}
}

func TestPRLandHelpStatesItsDefaultsAndItsRefusals(t *testing.T) {
	command := newPRLandCmd()
	for _, wanted := range []string{
		"CLEANUP IS THE DEFAULT",
		"SQUASH IS THE DEFAULT",
		"--keep-commits",
		"--reason",
		"made from the diff",
		"gh api",
		"Exit codes",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("wb pr land help does not mention %q", wanted)
		}
	}
	for _, flag := range []string{"keep", "approved-by", "keep-commits", "reason", "format", "allow-unfenced"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("wb pr land is missing --%s", flag)
		}
	}
	// Cleanup must be the default, so the flag that exists is the one that
	// opts out of it. A --cleanup flag reintroduces the measured failure.
	if command.Flags().Lookup("cleanup") != nil {
		t.Fatal("cleanup is the default; an opt-in --cleanup is the failure this verb exists to fix")
	}
}

func TestSplitCommaSeparatedAcceptsRepeatedAndJoinedValues(t *testing.T) {
	got := splitCommaSeparated([]string{"a,b", " c ", "", "d,,e"})
	want := []string{"a", "b", "c", "d", "e"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("splitCommaSeparated = %v, want %v", got, want)
	}
}
