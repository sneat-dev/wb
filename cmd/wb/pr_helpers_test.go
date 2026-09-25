package main

import "testing"

// TestParseIssueNumbersDedupesInFirstSeenOrder drives the seen[number]
// skip branch and the happy-path append branch together.
func TestParseIssueNumbersDedupesInFirstSeenOrder(t *testing.T) {
	t.Parallel()
	issues, err := parseIssueNumbers([]string{"5", " 6 ", "5"})
	if err != nil {
		t.Fatalf("parseIssueNumbers returned %v, want nil", err)
	}
	want := []int{5, 6}
	if len(issues) != len(want) || issues[0] != want[0] || issues[1] != want[1] {
		t.Fatalf("parseIssueNumbers = %v, want %v", issues, want)
	}
}

func TestParseIssueNumbersRejectsNonPositiveValue(t *testing.T) {
	t.Parallel()
	_, err := parseIssueNumbers([]string{"0"})
	if err == nil {
		t.Fatal("parseIssueNumbers(0) returned nil error, want a refusal")
	}
	const want = `--closes "0" is not a positive issue number`
	if err.Error() != want {
		t.Fatalf("parseIssueNumbers(0) error = %q, want %q", err.Error(), want)
	}
}

func TestParseIssueNumbersRejectsNonNumericValue(t *testing.T) {
	t.Parallel()
	_, err := parseIssueNumbers([]string{"abc"})
	if err == nil {
		t.Fatal("parseIssueNumbers(abc) returned nil error, want a refusal")
	}
	const want = `--closes "abc" is not a positive issue number`
	if err.Error() != want {
		t.Fatalf("parseIssueNumbers(abc) error = %q, want %q", err.Error(), want)
	}
}

func TestFormatSuggestedIssuesJoinsHashPrefixedNumbers(t *testing.T) {
	t.Parallel()
	if got := formatSuggestedIssues([]int{5, 6}); got != "#5, #6" {
		t.Fatalf("formatSuggestedIssues = %q, want %q", got, "#5, #6")
	}
}

func TestFormatSuggestedIssuesEmptyInputYieldsEmptyString(t *testing.T) {
	t.Parallel()
	if got := formatSuggestedIssues(nil); got != "" {
		t.Fatalf("formatSuggestedIssues(nil) = %q, want empty string", got)
	}
}
