//go:build darwin

package hostload

// This file covers loadavg_darwin.go, which exists only in a darwin build.
// On any other GOOS it is absent, so Linux CI neither compiles nor counts it.
// The tests never read the host's real load: they script runnertest.Fake, so
// the parsing and failure branches are asserted against controlled output
// rather than whatever the machine happens to be doing, and no real process
// starts.

import (
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestReadLoadAvg1ParsesTheSysctlDocument covers the darwin Reader's happy
// path: the "{"/"}" decoration is skipped and a field that is not a number
// is passed over rather than ending the parse.
func TestReadLoadAvg1ParsesTheSysctlDocument(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"sysctl", "-n", "vm.loadavg"}, runner.Result{Stdout: "{ nope 2.25 1.50 }\n"}, nil)

	load, err := readLoadAvg1WithRunner(fake)
	if err != nil {
		t.Fatalf("readLoadAvg1: %v", err)
	}
	if load != 2.25 {
		t.Fatalf("readLoadAvg1 = %v, want the first numeric field 2.25", load)
	}
}

// TestReadLoadAvg1ErrorsWhenSysctlFails pins the wrapped error a caller sees
// when the tool itself fails, and that no load is reported alongside it.
func TestReadLoadAvg1ErrorsWhenSysctlFails(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"sysctl", "-n", "vm.loadavg"}, runner.Result{ExitCode: 3}, errors.New("exit status 3"))

	load, err := readLoadAvg1WithRunner(fake)
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

// TestReadLoadAvg1ErrorsOnUnparseableOutput pins the second failure mode: a
// sysctl that exits 0 but prints nothing numeric must be an error, not a
// silent 0.0 load that would disable admission.
func TestReadLoadAvg1ErrorsOnUnparseableOutput(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"sysctl", "-n", "vm.loadavg"}, runner.Result{Stdout: "no numbers here\n"}, nil)

	load, err := readLoadAvg1WithRunner(fake)
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

// TestReadLoadAvg1DefaultsToProductionRunner proves the exported readLoadAvg1
// resolves a nil runner to the production runner.Runner. Under `go test`,
// runner.Real refuses to start a real process (task-24's guard), so this
// observes the wrapped guard error instead of shelling out to sysctl.
func TestReadLoadAvg1DefaultsToProductionRunner(t *testing.T) {
	t.Parallel()
	if _, err := readLoadAvg1(); err == nil || !strings.Contains(err.Error(), "read vm.loadavg") {
		t.Fatalf("readLoadAvg1 with no injected runner = %v, want it to reach the production runner.Runner", err)
	}
}
