package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/streams"
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

func TestPRLandDefaultsToAUsableBoundedWait(t *testing.T) {
	command := newPRLandCmd()
	if got := command.Flags().Lookup("timeout").DefValue; got != defaultCIWaitSlice.String() {
		t.Fatalf("--timeout default = %s, want %s", got, defaultCIWaitSlice)
	}
	if got := command.Flags().Lookup("poll-interval").DefValue; got != orchestrate.DefaultCheckPollInterval.String() {
		t.Fatalf("--poll-interval default = %s, want %s", got, orchestrate.DefaultCheckPollInterval)
	}
	if defaultCIWaitSlice <= orchestrate.DefaultCheckPollInterval {
		t.Fatalf("default timeout %s must outlive poll interval %s", defaultCIWaitSlice, orchestrate.DefaultCheckPollInterval)
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

// A landing outside a stream appends to .fleet. The next PR landing must
// inventory that event log without calling it corrupt stream state before the
// local-link guard can make its real decision.
func TestPRLandFleetEventLogDoesNotMakeTheNextLandingGuardFailClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	log, streamName := landingEventLog("acme/app")
	if streamName != "" {
		t.Fatalf("stream name = %q, want an outside-stream landing", streamName)
	}
	if err := log.Append(streams.Event{Verb: "wb pr land", Outcome: "refused"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "streams", fleetEventLogName, "events.jsonl")); err != nil {
		t.Fatalf("fleet event log was not appended: %v", err)
	}

	if err := refuseLinkedRepositoryWorktrees("acme/app"); err != nil {
		t.Fatalf("next landing guard rejected only the fleet event log: %v", err)
	}
}
