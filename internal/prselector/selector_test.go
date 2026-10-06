package prselector

import (
	"testing"
)

// An operator holds a pull request in whichever form their source gave them:
// what they typed, what a report printed, or what they copied from a browser.
// Every one of them addresses the same pull request, and making the caller
// normalize it is how a URL ends up inside an API path.
func TestPRLandSelectorAcceptsEveryFormAnOperatorHolds(t *testing.T) {
	t.Parallel()
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
		repository, number, err := Parse(testCase.selector)
		if err != nil || repository != testCase.repository || number != testCase.number {
			t.Errorf("Parse(%q) = %q, %q, %v", testCase.selector, repository, number, err)
		}
	}
	for _, invalid := range []string{"", "acme/app", "acme#41", "acme/app/extra#41", "acme/app#", "acme/app#abc"} {
		if _, _, err := Parse(invalid); err == nil {
			t.Errorf("Parse(%q) accepted an ambiguous selector", invalid)
		}
	}
}

func TestSelectorRejectsMalformedPullURL(t *testing.T) {
	t.Parallel()
	if _, _, err := Parse("/pull/7"); err == nil {
		t.Fatal("accepted malformed URL")
	}
}
