package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A /proc stat line whose comm field contains both spaces and parentheses must
// not shift the fields after it: field 22 is the process start time either way.
func TestParseProcStatStartTicksCountsFromTheLastClosingParen(t *testing.T) {
	fields := []string{"S", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "4242"}
	stat := "1234 (my (odd) name) " + strings.Join(fields, " ")
	ticks, ok := ParseProcStatStartTicks(stat)
	if !ok || ticks != 4242 {
		t.Fatalf("start ticks = %d, %t; want 4242, true", ticks, ok)
	}
}

func TestParseProcStatStartTicksRejectsUnreadableRecords(t *testing.T) {
	for name, stat := range map[string]string{
		"no comm":        "1234 S 1 2",
		"empty":          "",
		"comm unclosed":  "1234 (wb S 1 2",
		"too few fields": "1234 (wb) S 1 2",
		"not a number":   "1234 (wb) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 notanumber",
	} {
		if ticks, ok := ParseProcStatStartTicks(stat); ok {
			t.Fatalf("%s: parsed %d ticks, want failure", name, ticks)
		}
	}
}

func TestProcessStartFromProcStatUsesBootTimeAndUserHz(t *testing.T) {
	boot := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	// 250 ticks at USER_HZ=100 is 2.5 seconds after boot.
	started := ProcessStartFromProcStat(250, boot)
	if want := boot.Add(2500 * time.Millisecond); !started.Equal(want) {
		t.Fatalf("process start = %s, want %s", started, want)
	}
	if !ProcessStartFromProcStat(250, time.Time{}).IsZero() {
		t.Fatal("a missing boot time must not produce a start time")
	}
}

func TestParseProcStatBootTime(t *testing.T) {
	boot, ok := ParseProcStatBootTime("cpu  1 2 3\nbtime 1700000000\nprocesses 7\n")
	if !ok || !boot.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Fatalf("boot time = %s, %t", boot, ok)
	}
	for name, contents := range map[string]string{
		"missing":     "cpu 1 2 3\n",
		"not numeric": "btime notanumber\n",
		"extra field": "btime 1 2\n",
	} {
		if boot, ok := ParseProcStatBootTime(contents); ok || !boot.IsZero() {
			t.Fatalf("%s: boot time = %s, %t", name, boot, ok)
		}
	}
}

func TestProcessGenerationMatches(t *testing.T) {
	recorded := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name           string
		state          State
		observed       time.Time
		observedKnown  bool
		wantMatch      bool
		wantComparison bool
	}{
		{name: "no recorded pid", state: State{}, observed: recorded, observedKnown: true, wantMatch: false, wantComparison: true},
		{name: "no recorded start time", state: State{PID: 12}, observed: recorded, observedKnown: true},
		{name: "platform cannot observe", state: State{PID: 12, ProcessStartedAt: recorded}, observedKnown: false},
		{name: "same process", state: State{PID: 12, ProcessStartedAt: recorded}, observed: recorded.Add(time.Second), observedKnown: true, wantMatch: true, wantComparison: true},
		{name: "recycled pid", state: State{PID: 12, ProcessStartedAt: recorded}, observed: recorded.Add(time.Hour), observedKnown: true, wantComparison: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			match, known := testCase.state.ProcessGenerationMatches(testCase.observed, testCase.observedKnown)
			if match != testCase.wantMatch || known != testCase.wantComparison {
				t.Fatalf("match = %t, known = %t; want %t, %t", match, known, testCase.wantMatch, testCase.wantComparison)
			}
		})
	}
}

func TestStateIsForeignHome(t *testing.T) {
	cases := []struct {
		name        string
		recorded    string
		current     string
		wantForeign bool
		wantKnown   bool
	}{
		{name: "same home", recorded: "/home/a/.wb", current: "/home/a/.wb", wantKnown: true},
		{name: "different home", recorded: "/home/a/.wb", current: "/home/b/.wb", wantForeign: true, wantKnown: true},
		{name: "trailing separator is the same home", recorded: "/home/a/.wb/", current: "/home/a/.wb", wantKnown: true},
		{name: "unrecorded", recorded: "", current: "/home/a/.wb"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			foreign, known := (State{WBHome: testCase.recorded}).IsForeignHome(testCase.current)
			if foreign != testCase.wantForeign || known != testCase.wantKnown {
				t.Fatalf("foreign = %t, known = %t; want %t, %t", foreign, known, testCase.wantForeign, testCase.wantKnown)
			}
		})
	}
}

func TestRuntimePathsResolveThroughTheHome(t *testing.T) {
	// The resolver resolves symlinks eagerly, so the fixture's own path has to
	// be resolved the same way before it can be compared.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", home)
	root := t.TempDir()

	runtimeDir, err := RuntimeDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(runtimeDir) != RuntimeDirName || filepath.Dir(runtimeDir) != home {
		t.Fatalf("runtime dir = %s, want a %s child of %s", runtimeDir, RuntimeDirName, home)
	}
	for name, resolve := range map[string]func(string) (string, error){"state": StatePath, "socket": SocketPath} {
		path, err := resolve(root)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != runtimeDir {
			t.Fatalf("%s path = %s, want a child of %s", name, path, runtimeDir)
		}
	}
	// The legacy shape is a fixed historical path, not a resolved home: it is
	// reported, never written to.
	if legacy := LegacyRuntimeDir(root); legacy != filepath.Join(root, ".wb", "runtime") {
		t.Fatalf("legacy runtime dir = %s", legacy)
	}
	if legacy := LegacyStatePath(root); legacy != filepath.Join(root, ".wb", "runtime", StateFileName) {
		t.Fatalf("legacy state path = %s", legacy)
	}
	if LegacyRuntimeDir("") != "" || LegacyStatePath("") != "" {
		t.Fatal("an empty projects root must not name a legacy endpoint")
	}
}

func TestStateRecordsWhereItLivesAndWhyItStopped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", StateFileName)
	now := time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)
	state := NewStartingAt(nil, "127.0.0.1:8766", Provenance{Executable: "wb", Version: "v"}, "token", "/home/a/.wb", path, now)
	if state.WBHome != "/home/a/.wb" || state.StatePath != path || state.SchemaVersion != StateSchemaVersion {
		t.Fatalf("starting state = %#v", state)
	}
	state.MarkReadyWithProcess(4321, now.Add(-time.Minute), now)
	state.MarkStoppedWithReason("runtime directory is gone", now)
	if state.StoppedReason != "runtime directory is gone" || state.Status != StatusStopped || state.PID != 0 {
		t.Fatalf("stopped state = %#v", state)
	}
	store := Store{Path: path}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.Load()
	if err != nil || !found {
		t.Fatalf("load = %t, %v", found, err)
	}
	if stored.WBHome != state.WBHome || stored.StatePath != state.StatePath || stored.StoppedReason != state.StoppedReason {
		t.Fatalf("stored identity = %#v", stored)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %v, %v", info.Mode().Perm(), err)
	}
}
