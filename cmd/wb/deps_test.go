package main

import (
	"io"
	"strings"
	"testing"
)

func TestDepsPublishCommandExposesExplicitPublicationAndPropagationFlags(t *testing.T) {
	t.Parallel()
	command := newDepsCmd(&invocation{})
	publish, _, err := command.Find([]string{"publish", "npm"})
	if err != nil || publish == command {
		t.Fatalf("find publish npm: command=%q, error=%v", publish.Name(), err)
	}
	for _, name := range []string{"repo", "workflow", "package", "version", "workflow-input", "registry", "ref", "fleet", "apply", "dry-run", "resume", "workflow-poll", "merge", "report-dir", "format"} {
		if publish.Flags().Lookup(name) == nil {
			t.Errorf("deps publish npm is missing --%s", name)
		}
	}
	if aliases := publish.Parent().Aliases; len(aliases) != 1 || aliases[0] != "release" {
		t.Fatalf("publish parent aliases = %v, want release", aliases)
	}
}
func TestDepsPublishRejectsUnalignedReleaseTuplesBeforeFleetDiscovery(t *testing.T) {
	command := newDepsCmd(&invocation{})
	command.SetArgs([]string{"publish", "npm", "--fleet", "--repo", "acme/provider", "--workflow", "publish.yml", "--package", "@acme/provider", "--version", "1.0.0", "--version", "1.0.1"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "same number of values") {
		t.Fatalf("deps publish npm error = %v, want aligned tuple validation", err)
	}
}
