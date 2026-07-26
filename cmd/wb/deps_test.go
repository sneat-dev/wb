package main

import (
	"io"
	"strings"
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
	for _, name := range []string{"commit", "push", "pr", "merge", "parallel", "resume", "retry", "timeout", "propagate", "max-waves", "release-poll", "refresh-after", "dependency-order", "layer"} {
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
	for _, name := range []string{"changed", "fleet", "parallel", "max-waves", "release-poll", "refresh-after", "resume", "commit", "push", "pr", "merge"} {
		if bump.Flags().Lookup(name) == nil {
			t.Errorf("deps bump is missing --%s", name)
		}
	}
}

func TestDepsCommandIncludesGraphViewsAndBrowserReportFlags(t *testing.T) {
	t.Parallel()
	command := newDepsCmd()
	graph, _, err := command.Find([]string{"graph"})
	if err != nil || graph == command {
		t.Fatalf("find graph: command=%q, error=%v", graph.Name(), err)
	}
	for _, name := range []string{"fleet", "match", "regex", "ref", "parallel", "dependency", "view", "format", "report-dir", "open"} {
		if graph.Flags().Lookup(name) == nil {
			t.Errorf("deps graph is missing --%s", name)
		}
	}
}

func TestDepsSetRejectsUnusableDependencyOrderCombinations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args    []string
		message string
	}{
		{args: []string{"go", "example.com/a@v1.0.0", "--layer", "0"}, message: "--layer requires --dependency-order"},
		{args: []string{"github-actions", "acme/cicd@v1.0.0", "--dependency-order"}, message: "only for the go ecosystem"},
		{args: []string{"go", "example.com/a@v1.0.0", "--dependency-order", "--propagate", "--fleet"}, message: "cannot be used together"},
		{args: []string{"go", "example.com/a@v1.0.0", "--dependency-order", "--layer", "two"}, message: "invalid layer selection"},
	}
	for _, test := range tests {
		command := newDepsSetCmd()
		command.SetArgs(test.args)
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SilenceUsage = true
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("deps set %v error = %v, want %q", test.args, err, test.message)
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
