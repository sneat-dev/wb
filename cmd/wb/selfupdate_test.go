package main

import (
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/internal/wbupdate"
)

// TestRootCmdRegistersSelfUpdate pins that the command is actually wired
// into the root command tree, and that both its canonical name and its
// alias resolve there — the alias resolution a caller of `wb update` relies
// on happens through cobra.Command.Find, not through a second registration.
func TestRootCmdRegistersSelfUpdate(t *testing.T) {
	root := newRootCmd()

	found, _, err := root.Find([]string{"self-update"})
	if err != nil {
		t.Fatalf("wb self-update: %v", err)
	}
	if found.Name() != "self-update" {
		t.Errorf("wb self-update resolved to %q", found.Name())
	}

	foundAlias, _, err := root.Find([]string{"update"})
	if err != nil {
		t.Fatalf("wb update: %v", err)
	}
	if foundAlias.Name() != "self-update" {
		t.Errorf("wb update resolved to %q, want the self-update command", foundAlias.Name())
	}
}

// TestSelfUpdateVersionFlagNotSwallowedByRoot proves the interaction the
// brief calls out: hasVersionFlag stops at the first non-flag token (the
// subcommand name), so self-update's own --version — which pins a release
// tag, not a request for wb's build identity — is never mistaken for the
// root-level `wb --version` handled before cobra even runs
// (REQ: version-flag). main_test.go's TestHasVersionFlagRecognisesOnlyRootLevelRequests
// covers the same claim from hasVersionFlag's own table; this test proves it
// from the self-update command's actual registered --version flag instead of
// a hard-coded string, so the two can't silently drift apart.
func TestSelfUpdateVersionFlagNotSwallowedByRoot(t *testing.T) {
	cmd := newSelfUpdateCmd()
	if cmd.Flags().Lookup("version") == nil {
		t.Fatal("self-update does not register its own --version flag")
	}

	for _, name := range append([]string{cmd.Name()}, cmd.Aliases...) {
		args := []string{name, "--version", "v0.24.0"}
		if hasVersionFlag(args) {
			t.Errorf("hasVersionFlag(%v) = true; self-update's own --version was taken as the root's", args)
		}
	}
}

// selfUpdateDaemonHandoffTimeout must always track the daemon package's
// CURRENT bounds and leave headroom over both of them — the previous fixed
// 15s constant could cut a supervised wait off partway through, killing the
// child before it could ever report its own timeout and leaving a
// misleading "could not run" warning instead (sneat-dev/wb#622 review item
// 11; the bug that motivated this function existing at all).
func TestSelfUpdateDaemonHandoffTimeoutTracksCurrentBoundsWithHeadroom(t *testing.T) {
	bounds := daemonruntime.DefaultLifecycleBounds()
	timeout := daemonHandoffTimeout(func() daemonruntime.LifecycleBounds { return bounds })

	bounds.SupervisorRestart = 45 * time.Second
	want := bounds.Stop + bounds.SupervisorRestart + wbupdate.HandoffMargin
	if got := timeout(); got != want {
		t.Fatalf("timeout() = %s, want %s", got, want)
	}
	if got := timeout(); got <= bounds.Stop+bounds.SupervisorRestart {
		t.Fatalf("timeout %s does not leave headroom over drain (%s) + supervisor wait (%s)", got, bounds.Stop, bounds.SupervisorRestart)
	}

	bounds.SupervisorRestart = 5 * time.Second
	shrunk := timeout()
	if shrunk >= want {
		t.Fatalf("timeout did not track a shrunk supervisor bound: %s, want less than %s", shrunk, want)
	}
	if shrunk <= bounds.Stop+bounds.SupervisorRestart {
		t.Fatalf("shrunk timeout %s does not leave headroom over drain (%s) + supervisor wait (%s)", shrunk, bounds.Stop, bounds.SupervisorRestart)
	}
}
