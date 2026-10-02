package main

import (
	"errors"
	"strings"
	"testing"
)

func TestAffectedCoverageFlags(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		options qualityOptions
		want    string
	}{
		{name: "requires ratchet", options: qualityOptions{affectedPackages: true}, want: "requires --changed"},
		{name: "reject explicit packages", options: qualityOptions{affectedPackages: true, changed: true, explicitGoTestPackages: true}, want: "cannot be combined with --package"},
		{name: "reject aggregate floor", options: qualityOptions{affectedPackages: true, changed: true, minimumCoverage: 94}, want: "repository-wide --minimum"},
		{name: "accepted", options: qualityOptions{affectedPackages: true, changed: true, target: "main", testShards: 1, minimumCoverage: -1, format: "json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCoverageExecutionOptions(test.options)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%s", err, test.want)
			}
			var usage *exitError
			if !errors.As(err, &usage) || usage.code != exitUsage {
				t.Fatalf("misused affected flag error=%v, want usage exit code %d", err, exitUsage)
			}
		})
	}
}
