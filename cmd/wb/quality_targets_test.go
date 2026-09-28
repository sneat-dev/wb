package main

import "testing"

// TestQualityTargetsRejectsInvalidOptions drives the three option-validation
// refusal branches of qualityTargets before it ever touches the filesystem
// or a regexp.
func TestQualityTargetsRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options qualityOptions
		want    string
	}{
		{"parallel-below-one", qualityOptions{parallel: 0}, "parallelism must be at least 1"},
		{"negative-retry", qualityOptions{parallel: 1, retry: -1}, "retry count must not be negative"},
		{"negative-timeout", qualityOptions{parallel: 1, timeout: -1}, "timeout must not be negative"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := qualityTargets("", t.TempDir(), "", testCase.options)
			if err == nil {
				t.Fatalf("qualityTargets(%+v) returned nil error, want a refusal", testCase.options)
			}
			if err.Error() != testCase.want {
				t.Fatalf("qualityTargets(%+v) error = %q, want %q", testCase.options, err.Error(), testCase.want)
			}
		})
	}
}
