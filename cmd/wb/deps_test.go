package main

import (
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
)

func TestGitHubSlugSupportsSSHAndHTTPS(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"git@github.com:strongo/cicd.git":       "strongo/cicd",
		"https://github.com/sneat-dev/wb.git":   "sneat-dev/wb",
		"ssh://git@github.com/dal-go/dalgo.git": "dal-go/dalgo",
	}
	for remote, want := range tests {
		if got := githubSlug(remote); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestDepsCommandExposesCumulativeLifecycleFlags(t *testing.T) {
	t.Parallel()
	command := newDepsSetCmd()
	for _, name := range []string{"commit", "push", "pr", "merge", "parallel", "resume", "retry", "timeout", "propagate", "max-waves", "release-poll"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("deps set is missing --%s", name)
		}
	}
}

func TestDepsCommandIncludesBumpWithWaveLifecycleFlags(t *testing.T) {
	t.Parallel()
	command := newDepsCmd()
	bump, _, err := command.Find([]string{"bump"})
	if err != nil || bump == command {
		t.Fatalf("find bump: command=%q, error=%v", bump.Name(), err)
	}
	for _, name := range []string{"changed", "fleet", "parallel", "max-waves", "release-poll", "resume", "commit", "push", "pr", "merge"} {
		if bump.Flags().Lookup(name) == nil {
			t.Errorf("deps bump is missing --%s", name)
		}
	}
}

func TestParseReleaseEventsPreservesMultipleExactSeeds(t *testing.T) {
	t.Parallel()
	events, err := parseReleaseEvents([]string{"example.com/a@v0.2.0", "example.com/b@v1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != (deps.ReleaseEvent{Dependency: "example.com/a", Version: "v0.2.0", Source: "explicit"}) {
		t.Fatalf("events = %+v", events)
	}
}
