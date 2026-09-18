//go:build darwin

package hostload

// This file covers loadavg_darwin.go, which exists only in a darwin build.
// On any other GOOS it is absent, so Linux CI neither compiles nor counts it.
// The tests never read the host's real load: they prepend a fake `sysctl`
// script to PATH, so the parsing and failure branches are asserted against
// controlled output rather than whatever the machine happens to be doing.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tailCovWithFakeSysctl installs a fake `sysctl` first on PATH that prints
// output and exits with exitCode. Everything else on PATH still resolves.
func tailCovWithFakeSysctl(t *testing.T, output string, exitCode int) {
	t.Helper()
	directory := t.TempDir()
	payload := filepath.Join(directory, "payload.txt")
	if err := os.WriteFile(payload, []byte(output), 0o644); err != nil {
		t.Fatalf("write the fake sysctl payload: %v", err)
	}
	script := "#!/bin/sh\n" +
		"cat '" + payload + "'\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(directory, "sysctl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the fake sysctl: %v", err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestTailCovReadLoadAvg1ParsesTheSysctlDocument covers the darwin Reader's
// happy path: the "{"/"}" decoration is skipped and a field that is not a
// number is passed over rather than ending the parse.
func TestTailCovReadLoadAvg1ParsesTheSysctlDocument(t *testing.T) {
	tailCovWithFakeSysctl(t, "{ nope 2.25 1.50 }\n", 0)

	load, err := readLoadAvg1()
	if err != nil {
		t.Fatalf("readLoadAvg1: %v", err)
	}
	if load != 2.25 {
		t.Fatalf("readLoadAvg1 = %v, want the first numeric field 2.25", load)
	}
}

// TestTailCovReadLoadAvg1ErrorsWhenSysctlFails pins the wrapped error a caller
// sees when the tool itself fails, and that no load is reported alongside it.
func TestTailCovReadLoadAvg1ErrorsWhenSysctlFails(t *testing.T) {
	tailCovWithFakeSysctl(t, "", 3)

	load, err := readLoadAvg1()
	if err == nil {
		t.Fatal("readLoadAvg1 succeeded with a failing sysctl")
	}
	if load != 0 {
		t.Fatalf("readLoadAvg1 = %v alongside an error, want 0", load)
	}
	if !strings.Contains(err.Error(), "read vm.loadavg") {
		t.Fatalf("error %q does not name the vm.loadavg read", err)
	}
}

// TestTailCovReadLoadAvg1ErrorsOnUnparseableOutput pins the second failure
// mode: a sysctl that exits 0 but prints nothing numeric must be an error,
// not a silent 0.0 load that would disable admission.
func TestTailCovReadLoadAvg1ErrorsOnUnparseableOutput(t *testing.T) {
	tailCovWithFakeSysctl(t, "no numbers here\n", 0)

	load, err := readLoadAvg1()
	if err == nil {
		t.Fatal("readLoadAvg1 accepted output holding no number")
	}
	if load != 0 {
		t.Fatalf("readLoadAvg1 = %v alongside an error, want 0", load)
	}
	if !strings.Contains(err.Error(), "parse vm.loadavg output") {
		t.Fatalf("error %q does not say which output could not be parsed", err)
	}
}
