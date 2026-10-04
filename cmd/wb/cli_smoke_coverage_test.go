package main

import (
	"slices"
	"testing"
)

func TestSmokeCoverageBuildPreservesDefaultExplicitExcludedAndPlainScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode, directory, packages string
		want                            []string
	}{
		{name: "plain", want: []string{"build", "-o", "private-wb", "."}},
		{name: "no-runner-input", mode: "atomic", want: []string{"build", "-o", "private-wb", "."}},
		{name: "no-instrumentation", directory: "owned", want: []string{"build", "-o", "private-wb", "."}},
		{name: "default", mode: "atomic", directory: "owned", want: []string{"build", "-o", "private-wb", "-covermode=atomic", "-coverpkg=github.com/sneat-dev/wb/cmd/wb", "."}},
		{name: "explicit", mode: "set", directory: "owned", packages: "github.com/sneat-dev/wb/cmd/wb,github.com/sneat-dev/wb/internal/quality", want: []string{"build", "-o", "private-wb", "-covermode=set", "-coverpkg=github.com/sneat-dev/wb/cmd/wb,github.com/sneat-dev/wb/internal/quality", "."}},
		{name: "excluded", mode: "atomic", directory: "owned", packages: "example.test/excluded", want: []string{"build", "-o", "private-wb", "-covermode=atomic", "-coverpkg=example.test/excluded", "."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := smokeCoverageBuildArguments("private-wb", tc.mode, tc.directory, tc.packages); !slices.Equal(got, tc.want) {
				t.Fatalf("actual native build args %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSmokeCoverageChildEnvironmentPreservesExplicitInputs(t *testing.T) {
	t.Parallel()
	for _, covered := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "covered"}[covered], func(t *testing.T) {
			t.Parallel()
			original := []string{"WB_AGENT_ID=private-fixture", "GOCOVERDIR=ambient", "TERM=dumb"}
			mode := ""
			if covered {
				mode = "atomic"
			}
			child := smokeCoverageChildEnvironment(original, mode, "owned-native-directory")
			if !slices.Equal(child[:len(original)], original) {
				t.Fatalf("explicit child input changed %v", child)
			}
			if covered {
				if len(child) != len(original)+1 || child[len(child)-1] != "GOCOVERDIR=owned-native-directory" {
					t.Fatalf("owned native output not last %v", child)
				}
			} else if len(child) != len(original) {
				t.Fatalf("plain child polluted %v", child)
			}
			child[0] = "changed"
			if original[0] != "WB_AGENT_ID=private-fixture" {
				t.Fatal("caller environment aliased")
			}
		})
	}
}
