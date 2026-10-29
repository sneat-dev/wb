package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRecordedLegacySessionProtectsOccupiedPIDAcrossReadersAndPrune(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record := Record{PID: os.Getpid(), WBSessionID: "wbs-legacy", StartedAt: time.Unix(1, 0).UTC()}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, strconv.Itoa(record.PID)+".json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if !ProcessAlive(record.PID) {
		t.Fatal("fixture must occupy a live OS PID")
	}
	exact, live, err := LookupExact(dir, record.PID)
	if err == nil || live || exact.WBSessionID != record.WBSessionID {
		t.Fatalf("legacy process was classified as inactive: %+v,%t,%v", exact, live, err)
	}
	if _, live := Lookup(dir, record.PID); live {
		t.Fatal("convenience lookup granted unproved legacy identity")
	}
	if _, live := LookupByWBSessionID(dir, record.WBSessionID); live {
		t.Fatal("WB-ID lookup granted unproved legacy identity")
	}
	views, err := List(dir)
	if err != nil || len(views) != 1 || views[0].State != StateUnknown {
		t.Fatalf("legacy views=%+v,%v", views, err)
	}
	if n, err := Prune(dir); err != nil || n != 0 {
		t.Fatalf("legacy record pruned: %d,%v", n, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy record lost: %v", err)
	}
}

func TestRecordedNativeGenerationRejectsReusedLivePIDAcrossReadersAndPrune(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-retired-instance"})
	if err != nil {
		t.Fatal(err)
	}
	if record.ProcessStartedAt.IsZero() {
		t.Fatal("native process start unavailable")
	}
	record.ProcessStartedAt = record.ProcessStartedAt.Add(-time.Hour)
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, strconv.Itoa(record.PID)+".json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if !ProcessAlive(record.PID) {
		t.Fatal("fixture must hold a live operating-system PID")
	}
	exact, live, err := LookupExact(dir, record.PID)
	if err != nil || live || exact.WBSessionID != record.WBSessionID {
		t.Fatalf("retired native generation=%+v,%t,%v", exact, live, err)
	}
	if _, live := Lookup(dir, record.PID); live {
		t.Fatal("convenience lookup accepted retired generation")
	}
	if _, live := LookupByWBSessionID(dir, record.WBSessionID); live {
		t.Fatal("WB-ID lookup accepted retired generation")
	}
	views, err := List(dir)
	if err != nil || len(views) != 1 || views[0].State != StateGone {
		t.Fatalf("retired native views=%+v,%v", views, err)
	}
	if n, err := Prune(dir); err != nil || n != 1 {
		t.Fatalf("native generation prune=%d,%v", n, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retired native record remains: %v", err)
	}
	if !ProcessAlive(record.PID) {
		t.Fatal("pruning affected the live process")
	}
}

func TestRecordProcessStateRequiresPositiveProcessInstanceEvidence(t *testing.T) {
	t.Parallel()
	kernel := time.Unix(200, 0).UTC()
	for _, tc := range []struct {
		name               string
		alive              bool
		recorded, observed time.Time
		known              bool
		want               string
	}{
		{name: "dead", want: StateGone},
		{name: "legacy live PID", alive: true, want: StateUnknown},
		{name: "unavailable observation", alive: true, recorded: kernel, want: StateUnknown},
		{name: "zero observation", alive: true, recorded: kernel, known: true, want: StateUnknown},
		{name: "current generation", alive: true, recorded: kernel, observed: kernel, known: true, want: StateLive},
		{name: "earlier generation", alive: true, recorded: kernel, observed: kernel.Add(-time.Millisecond), known: true, want: StateGone},
		{name: "later generation", alive: true, recorded: kernel, observed: kernel.Add(time.Millisecond), known: true, want: StateGone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := recordProcessState(Record{PID: 42, StartedAt: time.Unix(1, 0), ProcessStartedAt: tc.recorded}, func(pid int) bool {
				if pid != 42 {
					t.Fatalf("pid=%d", pid)
				}
				return tc.alive
			}, func(int) (time.Time, bool) {
				if !tc.alive || tc.recorded.IsZero() {
					t.Fatal("unnecessary kernel observation")
				}
				return tc.observed, tc.known
			})
			if got != tc.want {
				t.Fatalf("state=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestProcessStartTimeRejectsUncertainAndInvalidNativeResults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		pid          int
		milliseconds int64
		err          error
		known        bool
	}{
		{name: "invalid PID", pid: 0},
		{name: "negative PID", pid: -1},
		{name: "truncated PID", pid: 1 << 33},
		{name: "permission denied", pid: 42, err: os.ErrPermission},
		{name: "unknown kernel error", pid: 42, err: os.ErrInvalid},
		{name: "missing time", pid: 42},
		{name: "invalid time", pid: 42, milliseconds: -1},
		{name: "native time", pid: 42, milliseconds: 200123, known: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reads := 0
			observed, known := processStartTimeWith(tc.pid, func(pid int32) (int64, error) {
				reads++
				if pid != 42 {
					t.Fatalf("unexpected native PID %d", pid)
				}
				return tc.milliseconds, tc.err
			})
			if known != tc.known || (known && !observed.Equal(time.UnixMilli(tc.milliseconds))) || (!known && !observed.IsZero()) {
				t.Fatalf("observed=%v, known=%t", observed, known)
			}
			if tc.pid != 42 && reads != 0 {
				t.Fatal("invalid PID reached native reader")
			}
		})
	}
}

func TestExactSessionLookupDoesNotTurnUnknownIdentityIntoInactive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record, err := Register(dir, Record{PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	access := nativeExactRecordIO()
	access.state = func(got Record) string {
		if got.WBSessionID != record.WBSessionID {
			t.Fatal("wrong recorded session")
		}
		return StateUnknown
	}
	exact, live, err := lookupExactWithIO(dir, record.PID, access)
	if err == nil || live || exact.WBSessionID != record.WBSessionID {
		t.Fatalf("unknown identity became absence: %+v, %t, %v", exact, live, err)
	}
}

func TestCurrentKernelProcessAndLegacyUnknownTimestampRemainProtected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record, err := Register(dir, Record{PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	observed, known := processStartTime(record.PID)
	if !known || observed.IsZero() || observed.After(time.Now()) {
		t.Fatalf("native process start unavailable or future: %v %t", observed, known)
	}
	if got, live, err := LookupExact(dir, record.PID); err != nil || !live || got.WBSessionID != record.WBSessionID {
		t.Fatalf("current instance=%+v,%t,%v", got, live, err)
	}
	record.StartedAt = time.Time{}
	record.ProcessStartedAt = time.Time{}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, strconv.Itoa(record.PID)+".json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, live, err := LookupExact(dir, record.PID); err == nil || live {
		t.Fatalf("unknown legacy time=%t,%v", live, err)
	}
	views, err := List(dir)
	if err != nil || len(views) != 1 || views[0].State != StateUnknown {
		t.Fatalf("unknown views=%+v,%v", views, err)
	}
	if n, err := Prune(dir); err != nil || n != 0 {
		t.Fatalf("uncertain legacy record pruned: %d,%v", n, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("uncertain legacy record lost: %v", err)
	}
}

func TestNativeProcessGenerationOverridesCallerClockAndRejectsMismatches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record, err := Register(dir, Record{PID: os.Getpid(), StartedAt: time.Unix(1, 0).UTC(), ProcessStartedAt: time.Unix(2, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	observed, known := processStartTime(record.PID)
	if !known || record.ProcessStartedAt.IsZero() || !record.ProcessStartedAt.Equal(observed) {
		t.Fatalf("registration accepted caller's kernel observation: %+v native=%v", record, observed)
	}
	if _, live, err := LookupExact(dir, record.PID); err != nil || !live {
		t.Fatalf("native generation lost behind caller clock: %t,%v", live, err)
	}
	updated, err := Register(dir, Record{PID: record.PID, Model: "corrected"})
	if err != nil || updated.WBSessionID != record.WBSessionID || !updated.StartedAt.Equal(record.StartedAt) || !updated.ProcessStartedAt.Equal(observed) || updated.Model != "corrected" {
		t.Fatalf("corroborated current generation lost semantic session identity: %+v,%v", updated, err)
	}

	for _, delta := range []time.Duration{-time.Millisecond, 0, time.Millisecond} {
		got := recordProcessState(record, func(int) bool { return true }, func(int) (time.Time, bool) { return observed.Add(delta), true })
		want := StateGone
		if delta == 0 {
			want = StateLive
		}
		if got != want {
			t.Fatalf("delta=%v state=%q want=%q", delta, got, want)
		}
	}
}

func TestRegisterDoesNotInheritIdentityFromProvenReusedPID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	native, known := processStartTime(os.Getpid())
	if !known {
		t.Fatal("native process observation unavailable")
	}
	old := Record{PID: os.Getpid(), WBSessionID: "wbs-retired", NativeHarnessID: "retired-native", StartedAt: time.Unix(1, 0).UTC(), ProcessStartedAt: native.Add(-time.Hour)}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(old.PID)+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	record, err := Register(dir, Record{PID: old.PID})
	if err != nil {
		t.Fatal(err)
	}
	if record.WBSessionID == old.WBSessionID || record.NativeHarnessID != "" || record.StartedAt.Equal(old.StartedAt) {
		t.Fatalf("reused PID inherited retired identity: %+v", record)
	}
	if _, live, err := LookupExact(dir, record.PID); err != nil || !live {
		t.Fatalf("fresh identity not usable: %t,%v", live, err)
	}
}

func TestRegisterDoesNotPromoteUncertainPriorIdentityOrMetadata(t *testing.T) {
	t.Parallel()
	for _, parked := range []bool{false, true} {
		t.Run(strconv.FormatBool(parked), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			old := Record{PID: os.Getpid(), WBSessionID: "wbs-unproved", Machine: "unproved-machine", Runtime: "codex", Model: "old-model", NativeHarnessID: "unproved-harness", TmuxName: "unproved-tmux", PredecessorWBSessionID: "unproved-predecessor", HandoffID: "unproved-handoff", StartedAt: time.Unix(1, 0).UTC(), RegisteredAtPark: true}
			if parked {
				old.Lifecycle = "parked"
				old.ParkedSessionID = "unproved-park"
			}
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(old.PID)+".json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			if state(old) != StateUnknown {
				t.Fatal("fixture must have an occupied PID without a native generation")
			}
			registered, err := registerWithMachineAndMode(dir, Record{PID: old.PID, Runtime: "codex", Model: "current-model"}, func() (string, error) { return "current-machine", nil }, os.Chmod)
			if err != nil {
				t.Fatal(err)
			}
			if registered.WBSessionID == "" || registered.WBSessionID == old.WBSessionID || registered.Machine != "current-machine" || registered.NativeHarnessID != "" || registered.AgentID != "" || registered.TmuxName != "" || registered.PredecessorWBSessionID != "" || registered.HandoffID != "" || registered.RegisteredAtPark || registered.Lifecycle != "" || registered.ParkedSessionID != "" || registered.StartedAt.IsZero() || registered.StartedAt.Equal(old.StartedAt) || registered.Model != "current-model" {
				t.Fatalf("uncertain prior identity or metadata promoted into registration: %+v", registered)
			}
			if exact, live, err := LookupExact(dir, registered.PID); err != nil || !live || exact.WBSessionID != registered.WBSessionID {
				t.Fatalf("fresh own identity not usable: %+v,%t,%v", exact, live, err)
			}
		})
	}
}

func TestRegisterBindsInheritedIdentityToFinalNativeGeneration(t *testing.T) {
	t.Parallel()
	generation := time.Unix(200, 0).UTC()
	for _, tc := range []struct {
		name    string
		final   time.Time
		known   bool
		inherit bool
	}{
		{name: "unchanged", final: generation, known: true, inherit: true},
		{name: "recycled", final: generation.Add(time.Second), known: true},
		{name: "unavailable"},
		{name: "zero known", known: true},
		{name: "unproved nonzero", final: generation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			old := Record{PID: 42, WBSessionID: "wbs-generation-a", Machine: "old-machine", NativeHarnessID: "old-harness", TmuxName: "old-tmux", PredecessorWBSessionID: "old-predecessor", HandoffID: "old-handoff", Lifecycle: "parked", ParkedSessionID: "old-park", RegisteredAtPark: true, StartedAt: time.Unix(1, 0).UTC(), ProcessStartedAt: generation}
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(recordPath(dir, old.PID), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			reads := 0
			registered, err := registerWithProcessObservation(dir, Record{PID: old.PID, Runtime: "codex", Model: "current"}, func() (string, error) { return "current-machine", nil }, os.Chmod, func(int) bool { return true }, func(pid int) (time.Time, bool) {
				if pid != old.PID {
					t.Fatalf("PID=%d", pid)
				}
				reads++
				if reads == 1 {
					return generation, true
				}
				return tc.final, tc.known
			})
			if err != nil {
				t.Fatal(err)
			}
			if reads != 2 {
				t.Fatalf("native observations=%d want=2", reads)
			}
			if tc.inherit {
				if registered.WBSessionID != old.WBSessionID || registered.Machine != old.Machine || registered.NativeHarnessID != old.NativeHarnessID || registered.TmuxName != old.TmuxName || registered.PredecessorWBSessionID != old.PredecessorWBSessionID || registered.HandoffID != old.HandoffID || registered.Lifecycle != old.Lifecycle || registered.ParkedSessionID != old.ParkedSessionID || !registered.RegisteredAtPark || !registered.StartedAt.Equal(old.StartedAt) {
					t.Fatalf("corroborated identity lost: %+v", registered)
				}
			} else if registered.WBSessionID == "" || registered.WBSessionID == old.WBSessionID || registered.Machine != "current-machine" || registered.NativeHarnessID != "" || registered.TmuxName != "" || registered.PredecessorWBSessionID != "" || registered.HandoffID != "" || registered.Lifecycle != "" || registered.ParkedSessionID != "" || registered.RegisteredAtPark || registered.StartedAt.Equal(old.StartedAt) {
				t.Fatalf("uncorroborated final generation inherited prior identity: %+v", registered)
			}
			want := tc.final
			if !tc.known {
				want = time.Time{}
			}
			if !registered.ProcessStartedAt.Equal(want) {
				t.Fatalf("persisted generation=%v want=%v", registered.ProcessStartedAt, want)
			}
			persisted, ok := readRecord(recordPath(dir, old.PID))
			if !ok || persisted.WBSessionID != registered.WBSessionID || !persisted.ProcessStartedAt.Equal(want) {
				t.Fatalf("persisted registration=%+v", persisted)
			}
		})
	}
}
