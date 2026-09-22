package waitregistry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedAlive(t *testing.T, live bool) {
	t.Helper()
	previous := Alive
	Alive = func(int) bool { return live }
	t.Cleanup(func() { Alive = previous })
}

func TestRegisterMakesTheWaitVisibleAndReleaseRemovesIt(t *testing.T) {
	home := t.TempDir()
	fixedAlive(t, true)
	release, err := Register(home, Record{ID: "a", PID: 42, Kind: "pr", Targets: []string{"acme/app#1"}, Until: "checks-settled", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	records, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Targets[0] != "acme/app#1" {
		t.Fatalf("wait not visible while it is running: %+v", records)
	}
	if records[0].Stale {
		t.Error("a live waiter was reported stale")
	}
	release()
	records, err = List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("a finished wait left a record behind: %+v", records)
	}
}

// TestRegisterStampsProvenanceFromEnv is wb#631's wait-record half of the
// acceptance test: a registered wait carries every declared provenance
// field plus wb_version.
func TestRegisterStampsProvenanceFromEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-wait-1")
	t.Setenv("AI_AGENT", "claude-code")
	t.Setenv("CLAUDE_EFFORT", "high")
	t.Setenv("WB_SUBAGENT_ID", "agent-3")
	t.Setenv("WB_SUBAGENT_TOOL_USE_ID", "toolu_4")

	home := t.TempDir()
	fixedAlive(t, true)
	release, err := Register(home, Record{ID: "b", PID: 42, Kind: "pr", Targets: []string{"acme/app#2"}, Until: "checks-settled", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	records, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.HarnessSessionID != "sess-wait-1" || record.Harness != "claude-code" || record.EffortLevel != "high" ||
		record.AgentID != "agent-3" || record.ToolUseID != "toolu_4" || record.WBVersion == "" {
		t.Fatalf("record did not carry every provenance field: %+v", record)
	}
}

func TestListReportsADeadWaiterRatherThanHidingIt(t *testing.T) {
	home := t.TempDir()
	fixedAlive(t, true)
	if _, err := Register(home, Record{ID: "b", PID: 7, Kind: "pr", Targets: []string{"acme/app#2"}, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// The waiter is killed without releasing: this is the case worth seeing,
	// because the session is now quiet with nothing watching for it.
	fixedAlive(t, false)
	records, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].Stale {
		t.Fatalf("a dead waiter was not reported as stale: %+v", records)
	}
}

func TestListNeverDeletesAndPruneRemovesOnlyStale(t *testing.T) {
	home := t.TempDir()
	fixedAlive(t, false)
	if _, err := Register(home, Record{ID: "dead", PID: 7, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := List(home); err != nil {
		t.Fatal(err)
	}
	// Listing must not have removed it: seeing a dead waiter cannot be a
	// side effect that destroys the evidence.
	if _, err := os.Stat(filepath.Join(home, directory, "dead.json")); err != nil {
		t.Fatalf("List deleted a stale record: %v", err)
	}
	fixedAlive(t, true)
	if _, err := Register(home, Record{ID: "live", PID: 8, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Only the dead one goes.
	Alive = func(pid int) bool { return pid == 8 }
	removed, err := Prune(home)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("pruned %d, want 1", removed)
	}
	records, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "live" {
		t.Errorf("prune removed the wrong record: %+v", records)
	}
}

func TestRegisterRefusesARecordWithNoIdentity(t *testing.T) {
	if _, err := Register(t.TempDir(), Record{PID: 1}); err == nil {
		t.Error("registered a wait with no id")
	}
}

func TestListIsEmptyRatherThanFailingBeforeAnyWait(t *testing.T) {
	records, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("listing before any wait failed: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("records = %+v, want none", records)
	}
}

func TestListIgnoresJunkInTheRegistry(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, directory), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"broken.json": "{not json",
		"future.json": `{"schema":999,"id":"future","pid":1}`,
		"notes.txt":   "ignored",
	} {
		if err := os.WriteFile(filepath.Join(home, directory, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	records, err := List(home)
	if err != nil {
		t.Fatalf("junk in the registry broke listing: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("records = %+v, want none", records)
	}
}
