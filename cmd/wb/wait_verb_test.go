package main

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// localFlagNames returns a command's own flags, ignoring inherited ones. Two
// spellings of one implementation must expose exactly the same set.
func localFlagNames(t *testing.T, command *cobra.Command) []string {
	t.Helper()
	names := []string{}
	command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
		names = append(names, flag.Name)
	})
	sort.Strings(names)
	return names
}

// find resolves a command path like "wait checks" against a fresh root.
func find(t *testing.T, path string) *cobra.Command {
	t.Helper()
	command, _, err := newRootCmd().Find(strings.Fields(path))
	if err != nil {
		t.Fatalf("resolve %q: %v", path, err)
	}
	if command.CommandPath() != "wb "+path {
		t.Fatalf("resolved %q to %q", path, command.CommandPath())
	}
	return command
}

// TestWaitVerbSpellingsReachTheSameImplementation is the contract that makes
// the verb-first spellings safe: they are built from the same constructors, so
// a receipt produced through either is the same receipt. If they ever diverge
// in flags or usage, one of them has grown a private implementation.
func TestWaitVerbSpellingsReachTheSameImplementation(t *testing.T) {
	for _, pair := range []struct{ older, verbFirst string }{
		{"ci wait", "wait checks"},
		{"agent await", "wait agent"},
		{"daemon operation wait", "wait operation"},
	} {
		t.Run(pair.verbFirst, func(t *testing.T) {
			older, verbFirst := find(t, pair.older), find(t, pair.verbFirst)

			gotOlder, gotVerbFirst := localFlagNames(t, older), localFlagNames(t, verbFirst)
			if strings.Join(gotOlder, ",") != strings.Join(gotVerbFirst, ",") {
				t.Errorf("flags differ:\n  %s: %v\n  %s: %v", pair.older, gotOlder, pair.verbFirst, gotVerbFirst)
			}
			if len(gotOlder) == 0 {
				t.Fatalf("%s reported no local flags; the comparison would pass vacuously", pair.older)
			}
			if older.Long != verbFirst.Long {
				t.Errorf("%s and %s do not share Long help, so they document different behaviour", pair.older, pair.verbFirst)
			}
			// The older spelling must keep working: absorbing is not removing.
			if !older.Runnable() {
				t.Errorf("%s stopped being runnable", pair.older)
			}
		})
	}
}

// TestWaitVerbListsEveryKindItCanWaitFor pins the discovery property the verb
// exists for: one `wb wait --help` enumerates everything, which no arrangement
// of `ci wait`, `agent await` and `daemon operation wait` ever did.
func TestWaitVerbListsEveryKindItCanWaitFor(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"wait", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	help := stdout.String()
	for _, kind := range []string{"pr", "checks", "agent", "operation", "list"} {
		if !strings.Contains(help, kind) {
			t.Errorf("wb wait --help does not mention %q: %s", kind, help)
		}
	}
}

// TestWaitVerbKeepsTheOlderSpellingsDiscoverable proves absorbing did not
// orphan the commands agents already know.
func TestWaitVerbKeepsTheOlderSpellingsDiscoverable(t *testing.T) {
	for _, path := range []string{"ci wait", "agent await", "daemon operation wait"} {
		args := append(strings.Fields(path), "--help")
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitOK {
			t.Errorf("wb %s --help exit = %d, stderr = %s", path, code, stderr.String())
		}
	}
}
