package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestPersistentCommandSelectedFleetRepoStatusNeverSelectsFleet drives the
// "repo status" case: a repository path is always a single-repository
// invocation, never the --projects-root fleet, regardless of the --fleet
// flag or positional args.
func TestPersistentCommandSelectedFleetRepoStatusNeverSelectsFleet(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "status"}
	if got := persistentCommandSelectedFleet(command, "repo status", nil); got {
		t.Fatal("persistentCommandSelectedFleet(\"repo status\") = true, want false")
	}
	if got := persistentCommandSelectedFleet(command, "repo status", []string{"."}); got {
		t.Fatal("persistentCommandSelectedFleet(\"repo status\", [.]) = true, want false")
	}
}
