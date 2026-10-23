package cmdpr

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"

	"github.com/sneat-dev/wb/internal/cli/shared"
)

// An operator holds a pull request in whichever form their source gave them:
// what they typed, what a report printed, or what they copied from a browser.
// Every one of them addresses the same pull request, and making the caller
// normalize it is how a URL ends up inside an API path.
func TestPRLandDefaultsToAUsableBoundedWait(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	command := NewLand(testRuntime(), deps)
	if got := command.Flags().Lookup("timeout").DefValue; got != shared.DefaultCIWaitSlice.String() {
		t.Fatalf("--timeout default = %s, want %s", got, shared.DefaultCIWaitSlice)
	}
	if got := command.Flags().Lookup("poll-interval").DefValue; got != githubchecks.DefaultCheckPollInterval.String() {
		t.Fatalf("--poll-interval default = %s, want %s", got, githubchecks.DefaultCheckPollInterval)
	}
	if shared.DefaultCIWaitSlice <= githubchecks.DefaultCheckPollInterval {
		t.Fatalf("default timeout %s must outlive poll interval %s", shared.DefaultCIWaitSlice, githubchecks.DefaultCheckPollInterval)
	}
}

func TestPRLandHelpStatesItsDefaultsAndItsRefusals(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	command := NewLand(testRuntime(), deps)
	if got := command.Flags().Lookup("merge-method").DefValue; got != "merge" {
		t.Fatalf("--merge-method default = %q, want merge", got)
	}
	for _, wanted := range []string{
		"CLEANUP IS THE DEFAULT",
		"MERGE COMMIT IS THE DEFAULT",
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

func TestPRLandKeepCommitsRequiresExplicitSquashBeforePreflight(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	for _, method := range []string{"", "merge", "rebase"} {
		name := method
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			command := NewLand(testRuntime(), deps)
			if err := command.Flags().Set("keep-commits", "abc123"); err != nil {
				t.Fatal(err)
			}
			if method != "" {
				if err := command.Flags().Set("merge-method", method); err != nil {
					t.Fatal(err)
				}
			}
			err := command.RunE(command, []string{"acme/app#7"})
			if err == nil || !strings.Contains(err.Error(), "explicit --merge-method squash") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSplitCommaSeparatedAcceptsRepeatedAndJoinedValues(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	got := splitCommaSeparated([]string{"a,b", " c ", "", "d,,e"})
	want := []string{"a", "b", "c", "d", "e"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("splitCommaSeparated = %v, want %v", got, want)
	}
}

func TestPRLandRejectsWaiveCheckWithoutReason(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	command := NewLand(testRuntime(), deps)
	if err := command.Flags().Set("waive-check", "ci"); err != nil {
		t.Fatal(err)
	}
	err := command.RunE(command, []string{"acme/app#7"})
	if err == nil || !strings.Contains(err.Error(), "--waive-check requires a non-empty --waive-reason") {
		t.Fatalf("error = %v, want missing waive-reason usage error", err)
	}
}

func TestPRLandRejectsWaiveReasonWithoutCheck(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	command := NewLand(testRuntime(), deps)
	if err := command.Flags().Set("waive-reason", "some reason"); err != nil {
		t.Fatal(err)
	}
	err := command.RunE(command, []string{"acme/app#7"})
	if err == nil || !strings.Contains(err.Error(), "--waive-reason was given without any --waive-check") {
		t.Fatalf("error = %v, want waive-reason without waive-check usage error", err)
	}
}
