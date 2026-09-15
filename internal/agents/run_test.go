package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	return store
}

func sampleRecord(t *testing.T) Record {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	return Record{
		AgentID:          id,
		State:            StateRunning,
		RequestedProfile: "cheap",
		Resolved: Resolved{
			Profile: "cheap", Harness: HarnessCodex, Provider: "deepseek",
			Model: "deepseek-flash", Reasoning: "high",
			Routing: Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "DEEPSEEK_API_KEY", WireAPI: WireAPIResponses},
		},
		Task:        "do the bounded thing",
		TaskSummary: "do the bounded thing",
		Repository:  "acme/app",
		Worktree:    "task-one",
		StartedAt:   time.Now().UTC(),
	}
}

func TestNewIDIsPrefixedRandomAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for index := 0; index < 64; index++ {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, IDPrefix) {
			t.Fatalf("id %q lacks the %q prefix", id, IDPrefix)
		}
		if len(id) != len(IDPrefix)+32 {
			t.Fatalf("id %q is not a 16-byte hex suffix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestStoreCreateSaveLoadRoundTrip(t *testing.T) {
	store := newTestStore(t)
	record := sampleRecord(t)
	record.OwnerPID = 4321
	code := 7
	record.ExitCode = &code
	record.Usage = &Usage{InputTokens: 10, OutputTokens: 2}
	record.Changes = &ChangeSummary{FilesChanged: 1, Files: []string{"a.txt"}}

	if err := store.Create(record); err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := os.Stat(store.RecordPath(record.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("run record mode = %o, want 600: the task is private data", mode)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.AgentID != record.AgentID || loaded.Task != record.Task || loaded.SchemaVersion != schemaVersion {
		t.Fatalf("round trip lost data: %#v", loaded)
	}
	if loaded.ExitCode == nil || *loaded.ExitCode != 7 {
		t.Fatalf("exit code did not survive: %#v", loaded.ExitCode)
	}
	if loaded.Usage == nil || loaded.Usage.OutputTokens != 2 {
		t.Fatalf("usage did not survive: %#v", loaded.Usage)
	}
	if loaded.Changes == nil || len(loaded.Changes.Files) != 1 {
		t.Fatalf("changes did not survive: %#v", loaded.Changes)
	}
	// Saving twice must replace atomically and leave no staging residue.
	record.Failure = "second write"
	if err := store.Save(record); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err = store.Load(record.AgentID)
	if err != nil || loaded.Failure != "second write" {
		t.Fatalf("Save did not replace the record: %#v %v", loaded, err)
	}
	entries, err := os.ReadDir(store.Dir(record.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".run-") {
			t.Fatalf("staged record residue left behind: %s", entry.Name())
		}
	}
}

func TestStorePathsLiveUnderOnePrivateDirectory(t *testing.T) {
	store := NewStore("/home/example/.wb")
	dir := store.Dir("agt-00")
	if dir != filepath.Join("/home/example/.wb", DirName, "agt-00") {
		t.Fatalf("Dir = %q", dir)
	}
	for name, path := range map[string]string{
		"record":      store.RecordPath("agt-00"),
		"log":         store.LogPath("agt-00"),
		"lastMessage": store.LastMessagePath("agt-00"),
		"harnessHome": store.HarnessHomePath("agt-00"),
	} {
		if filepath.Dir(path) != dir {
			t.Fatalf("%s (%s) is not inside the run directory %s", name, path, dir)
		}
	}
}

func TestStoreRejectsMalformedAgentIDs(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{
		"", "agt-", "wbs-00", "agt-zz", "agt-0", "../escape", "agt-0011",
		"agt-0000000000000000000000000000000", "agt-000000000000000000000000000000000",
		"agt-0000000000000000000000000000000g",
	} {
		if _, err := store.Load(id); err == nil {
			t.Errorf("Load(%q) accepted a malformed ID", id)
		}
		if err := store.Save(Record{AgentID: id}); err == nil {
			t.Errorf("Save(%q) accepted a malformed ID", id)
		}
		if err := store.Create(Record{AgentID: id}); err == nil {
			t.Errorf("Create(%q) accepted a malformed ID", id)
		}
	}
}

func TestStoreLoadMissingRunIsAnUnknownAgent(t *testing.T) {
	store := newTestStore(t)
	_, err := store.Load("agt-00000000000000000000000000000000")
	if err == nil {
		t.Fatal("Load must fail for a missing run")
	}
	if !IsUnknownAgent(err) {
		t.Fatalf("missing run must be an UnknownAgentError, got %T", err)
	}
	var unknown *UnknownAgentError
	if !errors.As(err, &unknown) || unknown.Root != store.Root {
		t.Fatalf("unknown-agent error must name the searched root: %#v", unknown)
	}
}

func TestStoreLoadReportsCorruptAndFutureRecords(t *testing.T) {
	store := newTestStore(t)
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(Record{AgentID: id, State: StateRunning}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(store.RecordPath(id), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil || !strings.Contains(err.Error(), "parse agent run") {
		t.Fatalf("corrupt record error = %v", err)
	}

	raw, err := json.Marshal(Record{AgentID: id, SchemaVersion: schemaVersion + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.RecordPath(id), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("future schema error = %v", err)
	}
}

func TestStoreListIsNewestFirstAndTolerantOfAnAbsentRoot(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "never-created"))
	records, err := store.List()
	if err != nil || len(records) != 0 {
		t.Fatalf("an absent root must list as empty: %v %v", records, err)
	}

	store = newTestStore(t)
	base := time.Now().UTC()
	for index, offset := range []time.Duration{3 * time.Hour, time.Hour, 2 * time.Hour} {
		record := sampleRecord(t)
		record.StartedAt = base.Add(-offset)
		record.TaskSummary = string(rune('a' + index))
		if err := store.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file and an unrelated directory must be ignored, not fail the
	// listing: the store is a directory an operator may well drop things into.
	if err := os.WriteFile(filepath.Join(store.Root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.Root, "not-a-run"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Two runs recorded at the same instant must still order deterministically.
	same := sampleRecord(t)
	same.StartedAt = base.Add(-time.Hour)
	other := sampleRecord(t)
	other.StartedAt = base.Add(-time.Hour)
	for _, record := range []Record{same, other} {
		if err := store.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	records, err = store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for index := 1; index < len(records); index++ {
		if records[index-1].StartedAt.Equal(records[index].StartedAt) && records[index-1].AgentID > records[index].AgentID {
			t.Fatalf("equal timestamps are not ordered by ID: %v", records)
		}
	}
	if len(records) != 5 {
		t.Fatalf("List returned %d records, want 5", len(records))
	}
	for index := 1; index < len(records); index++ {
		if records[index-1].StartedAt.Before(records[index].StartedAt) {
			t.Fatalf("records are not newest-first: %v", records)
		}
	}
}

func TestStoreListReportsACorruptRecordRatherThanHidingIt(t *testing.T) {
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.RecordPath(record.AgentID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("a corrupt record must surface through List, not be skipped")
	}
}

func TestRenderResolvesDeadRunsToAbandoned(t *testing.T) {
	store := newTestStore(t)

	running := sampleRecord(t)
	running.OwnerPID = os.Getpid()
	result := store.Render(running)
	if result.State != StateRunning || !result.OwnerAlive {
		t.Fatalf("a live owner must render as running: %#v", result)
	}
	if result.Terminal {
		t.Fatal("a running run must not be terminal")
	}

	// A run persisted moments ago, with no process recorded yet, is still being
	// admitted: the dispatcher has written the record and the owner has not yet
	// recorded itself.
	fresh := sampleRecord(t)
	fresh.StartedAt = time.Now().UTC()
	if result := store.Render(fresh); result.State != StateRunning || result.Terminal {
		t.Fatalf("a freshly admitted run must not be called abandoned: %#v", result)
	}

	// Once that window has passed with no process ever recorded, the dispatcher
	// died before it could start an owner and no outcome will ever be written.
	orphan := sampleRecord(t)
	orphan.StartedAt = time.Now().UTC().Add(-2 * admissionGrace)
	if result := store.Render(orphan); result.State != StateAbandoned || !result.Terminal {
		t.Fatalf("an ownerless running record must render as abandoned: %#v", result)
	}

	// A worker that outlived its owner cannot produce an outcome either, so the
	// run is abandoned — but the stray process is reported so it can be stopped.
	survivor := sampleRecord(t)
	survivor.WorkerPID = os.Getpid()
	if result := store.Render(survivor); result.State != StateAbandoned || result.OwnerAlive || !result.WorkerAlive {
		t.Fatalf("a worker that outlived its owner must be abandoned with the worker surfaced: %#v", result)
	}

	terminal := sampleRecord(t)
	terminal.State = StateCompleted
	terminal.FinishedAt = time.Now().UTC()
	if result := store.Render(terminal); result.State != StateCompleted || result.OwnerAlive {
		t.Fatalf("a terminal run must not be re-interpreted: %#v", result)
	}
}

func TestRenderNeverLeaksThePrivateTask(t *testing.T) {
	store := newTestStore(t)
	record := sampleRecord(t)
	record.Task = "SUPER SECRET PROMPT"
	// The summary is derived from the task, so it is private for the same
	// reason the task is. Neither may reach a caller through status or await.
	record.TaskSummary = "SUPER SECRET PROMPT"
	record.LogPath = "/private/run/events.jsonl"
	encoded, err := json.Marshal(store.Render(record))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SUPER SECRET PROMPT") {
		t.Fatalf("rendered result leaked the private task: %s", encoded)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"task", "task_summary"} {
		if _, present := document[key]; present {
			t.Fatalf("rendered result must not carry a %q field at all", key)
		}
	}
	for _, key := range []string{"agent_id", "state", "terminal", "resolved", "worktree", "log_path"} {
		if _, present := document[key]; !present {
			t.Fatalf("rendered result is missing %q", key)
		}
	}
}

func TestRenderOmitsFinishedAtUntilTheRunEnds(t *testing.T) {
	store := newTestStore(t)
	record := sampleRecord(t)
	if result := store.Render(record); result.FinishedAt != nil {
		t.Fatalf("an unfinished run must not carry a finish time: %v", result.FinishedAt)
	}
	finished := time.Now().UTC()
	record.FinishedAt = finished
	result := store.Render(record)
	if result.FinishedAt == nil || !result.FinishedAt.Equal(finished) {
		t.Fatalf("finish time not rendered: %#v", result.FinishedAt)
	}
}

func TestStateTerminalVocabularyIsClosed(t *testing.T) {
	terminal := []State{StateCompleted, StateFailed, StateTimeout, StateAbandoned}
	for _, state := range terminal {
		if !state.Terminal() {
			t.Errorf("%s must be terminal", state)
		}
	}
	if StateRunning.Terminal() {
		t.Error("running must not be terminal")
	}
	if State("").Terminal() || State("done").Terminal() {
		t.Error("an unknown state must never read as terminal")
	}
}

func TestSummaryLineAndBoundResultKeepOutputCompact(t *testing.T) {
	if got := SummaryLine("\n\n  first   line  \nsecond line\n"); got != "first line" {
		t.Fatalf("SummaryLine = %q", got)
	}
	if got := SummaryLine("   "); got != "" {
		t.Fatalf("SummaryLine of whitespace = %q", got)
	}
	long := strings.Repeat("x", 400)
	if got := SummaryLine(long); len(got) > 210 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long summary was not bounded: %d chars", len(got))
	}
	if got := BoundResult("  hello  "); got != "hello" {
		t.Fatalf("BoundResult = %q", got)
	}
	big := strings.Repeat("y", maxResultBytes*2)
	bounded := BoundResult(big)
	if !strings.Contains(bounded, "truncated") || len(bounded) > maxResultBytes+80 {
		t.Fatalf("an oversized message was not bounded: %d chars", len(bounded))
	}
	if got := BoundResult(strings.Repeat("z", maxResultBytes)); len(got) != maxResultBytes {
		t.Fatalf("an exactly-bounded message must pass through: %d", len(got))
	}
}

func TestStoreSurfacesFilesystemFailuresRatherThanLosingWork(t *testing.T) {
	// A root that is a regular file cannot host runs, and Create must say so.
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := Store{Root: filepath.Join(root, "agents")}
	record := sampleRecord(t)
	if err := store.Create(record); err == nil || !strings.Contains(err.Error(), "create agent run directory") {
		t.Fatalf("Create error = %v", err)
	}

	// List must report an unreadable root rather than pretending it is empty.
	if _, err := store.List(); err == nil {
		t.Fatal("List must report a root it cannot read")
	}

	// Load must distinguish a corrupt/unreadable record from a missing one.
	good := newTestStore(t)
	record = sampleRecord(t)
	if err := good.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(good.RecordPath(record.AgentID)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(good.RecordPath(record.AgentID), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := good.Load(record.AgentID); err == nil || !strings.Contains(err.Error(), "read agent run") {
		t.Fatalf("Load error = %v", err)
	}

	// Save must fail when the run directory is gone rather than silently
	// dropping the terminal state.
	gone := Store{Root: filepath.Join(t.TempDir(), "absent")}
	if err := gone.Save(record); err == nil || !strings.Contains(err.Error(), "stage agent run record") {
		t.Fatalf("Save error = %v", err)
	}
}
