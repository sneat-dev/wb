package hostload

// This file adds behavior-asserting coverage for the resolution, admission,
// consumer-listing, and config-path paths that internal/hostload exposes.
// Every helper here is prefixed tailCov to keep it disjoint from the helpers
// in hostload_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestTailCovDisabledNamesWhyAdmissionIsOff pins Disabled's contract: it
// reports both whether admission is off and the reason Resolve found, so a
// caller can record the reason without resolving a second time.
func TestTailCovDisabledNamesWhyAdmissionIsOff(t *testing.T) {
	clearAdmissionEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	off, reason := Disabled("")
	if off {
		t.Fatal("Disabled() with nothing overriding the floor = true, want admission active")
	}
	if reason != "" {
		t.Fatalf("Disabled() reason = %q while admission is active, want empty", reason)
	}

	t.Setenv(EnvLoadFloor, "0")
	off, reason = Disabled("")
	if !off {
		t.Fatal("Disabled() with WB_ADMISSION_LOAD_FLOOR=0 = false, want true")
	}
	if reason != "env" {
		t.Fatalf("Disabled() reason = %q, want %q", reason, "env")
	}
}

// TestTailCovResolveReadsTheDefaultConfigPath pins that an empty configPath
// resolves wbconfig.DefaultPath(), that a missing file there keeps the default
// floor, and that a file planted there is honoured — the path a plain
// `wb run` takes on a machine with no explicit --config.
func TestTailCovResolveReadsTheDefaultConfigPath(t *testing.T) {
	clearAdmissionEnv(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	floor, reason := Resolve("")
	if reason != "" {
		t.Fatalf("Resolve(\"\") reason = %q, want admission active by default", reason)
	}
	if want := defaultFloor(); floor != want {
		t.Fatalf("Resolve(\"\") with no config file = %v, want the %v default", floor, want)
	}

	path := filepath.Join(configHome, "wb", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("admission:\n  load_floor: 3.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if floor, reason = Resolve(""); floor != 3.5 || reason != "" {
		t.Fatalf("Resolve(\"\") with a default-path config = (%v, %q), want (3.5, active)", floor, reason)
	}
}

// TestTailCovResolveKeepsTheDefaultOnAnUnparsableConfig pins the documented
// fail-safe: a broken wb.yaml must not disable admission, it must leave the
// default floor in place and still report admission active.
func TestTailCovResolveKeepsTheDefaultOnAnUnparsableConfig(t *testing.T) {
	clearAdmissionEnv(t)
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("admission: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	floor, reason := Resolve(path)
	if want := defaultFloor(); floor != want {
		t.Fatalf("Resolve(unparsable) = %v, want the %v default", floor, want)
	}
	if reason != "" {
		t.Fatalf("Resolve(unparsable) reason = %q, want admission active", reason)
	}
}

// TestTailCovCheckUsesTheSystemReaderWhenNoneIsInjected pins that Check(nil,…)
// goes through the package-level System reader rather than admitting blindly,
// and that the resulting refusal names the load it read.
func TestTailCovCheckUsesTheSystemReaderWhenNoneIsInjected(t *testing.T) {
	previous := System
	t.Cleanup(func() { System = previous })

	calls := 0
	System = func() (float64, error) { calls++; return 42.0, nil }

	err := Check(nil, 1.0, false)
	if calls != 1 {
		t.Fatalf("Check(nil, floor 1.0) called System %d times, want 1", calls)
	}
	if err == nil {
		t.Fatal("Check(nil, floor 1.0) admitted a load of 42.0 read through System")
	}
	if !strings.Contains(err.Error(), "42.00") || !strings.Contains(err.Error(), "1.00") {
		t.Fatalf("refusal %q does not name the load System returned and the floor", err)
	}
}

// TestTailCovCheckAdmitsWithoutAnyReader pins the last fail-open rung: even
// when the System reader itself is absent, Check admits instead of panicking.
func TestTailCovCheckAdmitsWithoutAnyReader(t *testing.T) {
	previous := System
	t.Cleanup(func() { System = previous })
	System = nil

	if err := Check(nil, 1.0, false); err != nil {
		t.Fatalf("Check(nil, floor 1.0) with no System reader = %v, want nil (fail open)", err)
	}
}

// fakePS returns a runnertest.Fake scripted to answer the exact `ps` argv
// Consumers issues with stdout, so a test can drive consumers' parsing and
// ordering logic without a real process.
func fakePS(t *testing.T, stdout string) *runnertest.Fake {
	t.Helper()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"ps", "-Ao", "pid,pcpu,etime,comm"}, runner.Result{Stdout: stdout}, nil)
	return fake
}

// TestConsumersIgnoresOutputWithoutDataLines pins that empty and header-only
// `ps` output yield no consumers at all, rather than a one-line note or a
// panic. The empty output is the zero value; the header-only output is an
// empty (never nil) list.
func TestConsumersIgnoresOutputWithoutDataLines(t *testing.T) {
	t.Parallel()
	if got := consumers(5, fakePS(t, "")); got != nil {
		t.Fatalf("consumers(empty output) = %v, want nil", got)
	}
	if got := consumers(5, fakePS(t, "PID %CPU ELAPSED COMM\n")); len(got) != 0 {
		t.Fatalf("consumers(header-only output) = %v, want no consumers", got)
	}
}

// TestConsumersSkipsRowsItCannotTrust pins the two parse guards: a row with
// fewer than the four expected fields and a row whose %CPU is not a number
// are both dropped, while a well-formed row beside them survives.
func TestConsumersSkipsRowsItCannotTrust(t *testing.T) {
	t.Parallel()
	fake := fakePS(t, "PID %CPU ELAPSED COMM\n"+
		"1 2\n"+
		"7 not-a-number 00:01 greedy\n"+
		"9 12.5 00:02 keeper\n")
	got := consumers(5, fake)
	if len(got) != 1 {
		t.Fatalf("consumers(malformed rows) = %v, want exactly the one well-formed row", got)
	}
	if !strings.Contains(got[0], "pid=9") || !strings.Contains(got[0], "comm=keeper") {
		t.Fatalf("consumers kept the wrong row: %q", got[0])
	}
}

// TestConsumersOrdersAndBoundsTheList pins the two behaviours the refusal
// message depends on: busiest process first, and never more than the
// requested number of lines.
func TestConsumersOrdersAndBoundsTheList(t *testing.T) {
	t.Parallel()
	fake := fakePS(t, "PID %CPU ELAPSED COMM\n"+
		"1 5.0 00:01 quiet\n"+
		"2 80.0 00:02 loudest\n"+
		"3 40.0 00:03 middle\n")
	got := consumers(2, fake)
	if len(got) != 2 {
		t.Fatalf("consumers(2) = %v, want exactly 2 lines", got)
	}
	if !strings.Contains(got[0], "comm=loudest") || !strings.Contains(got[1], "comm=middle") {
		t.Fatalf("consumers(2) = %v, want loudest then middle", got)
	}
}

// TestConsumersReducesLongCommandNames pins the bound on the comm field: a
// name longer than maxConsumerNameLength is cut, and a path is reduced to
// its executable basename, so one runaway executable cannot bloat the
// refusal.
func TestConsumersReducesLongCommandNames(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("z", maxConsumerNameLength+10)
	fake := fakePS(t, "PID %CPU ELAPSED COMM\n"+
		"1 90.0 00:01 "+long+"\n"+
		"2 10.0 00:02 /opt/homebrew/bin/python3\n")
	got := consumers(5, fake)
	if len(got) != 2 {
		t.Fatalf("consumers(long comm) = %v, want 2 lines", got)
	}
	want := "comm=" + strings.Repeat("z", maxConsumerNameLength) + "…"
	if !strings.Contains(got[0], want) {
		t.Fatalf("consumers(long comm) = %q, want it to contain %q", got[0], want)
	}
	if strings.Contains(got[0], long) {
		t.Fatalf("a comm longer than %d was not truncated: %q", maxConsumerNameLength, got[0])
	}
	if !strings.Contains(got[1], "comm=python3") {
		t.Fatalf("consumers(path comm) = %q, want the basename comm=python3", got[1])
	}
}

// TestConsumersOmitsListOnFailingRunner covers the non-timeout failure
// branch: the runner errors, but the context has not expired, so Consumers
// omits the list instead of reporting a timeout.
func TestConsumersOmitsListOnFailingRunner(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"ps", "-Ao", "pid,pcpu,etime,comm"}, runner.Result{}, errors.New("ps: no such process source"))
	if got := consumers(5, fake); got != nil {
		t.Fatalf("consumers(failing runner) = %v, want nil (omitted, not an error)", got)
	}
}

// TestConsumersReportsATimeoutWhenTheContextHasAlreadyExpired covers the
// bounded-ness branch: consumersTimeout is shrunk to an already-expired
// duration, so the context is Done before consumers ever calls the runner,
// and any runner failure is reported as a timeout rather than an omission.
// It cannot coexist with t.Parallel (it mutates the package-level
// consumersTimeout), matching every other consumersTimeout-mutating test in
// this package.
func TestConsumersReportsATimeoutWhenTheContextHasAlreadyExpired(t *testing.T) {
	previous := consumersTimeout
	consumersTimeout = -1
	t.Cleanup(func() { consumersTimeout = previous })

	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"ps", "-Ao", "pid,pcpu,etime,comm"}, runner.Result{}, errors.New("irrelevant"))
	got := consumers(5, fake)
	if len(got) != 1 || !strings.Contains(got[0], "timed out") {
		t.Fatalf("consumers(expired context) = %v, want a single one-line timeout note", got)
	}
}

// TestTailCovExpandPathResolvesATildeAgainstHome pins the "~/wb.yaml" form the
// docs use, end to end through Resolve, and that an absolute path is used
// verbatim rather than being rewritten.
func TestTailCovExpandPathResolvesATildeAgainstHome(t *testing.T) {
	clearAdmissionEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "wb.yaml"), []byte("admission:\n  load_floor: 4.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if floor := Floor("~/wb.yaml"); floor != 4.5 {
		t.Fatalf("Floor(~/wb.yaml) = %v, want 4.5 read through the home directory", floor)
	}
	if floor := Floor(filepath.Join(home, "wb.yaml")); floor != 4.5 {
		t.Fatalf("Floor(absolute path) = %v, want 4.5", floor)
	}
}

// TestTailCovExpandPathLeavesTildeAloneWithoutAHome pins the degraded case: a
// process with no HOME keeps the literal path and simply finds no config, so
// the default floor still applies.
func TestTailCovExpandPathLeavesTildeAloneWithoutAHome(t *testing.T) {
	clearAdmissionEnv(t)
	t.Setenv("HOME", "")
	if got := expandPath("~/wb.yaml"); got != "~/wb.yaml" {
		t.Fatalf("expandPath(~/wb.yaml) with no HOME = %q, want the input unchanged", got)
	}
	if floor := Floor("~/wb.yaml"); floor != defaultFloor() {
		t.Fatalf("Floor(~/wb.yaml) with no HOME = %v, want the %v default", floor, defaultFloor())
	}
}
