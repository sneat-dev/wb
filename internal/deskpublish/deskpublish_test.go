package deskpublish

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
)

func fixedKey(t *testing.T, fill string) TaskKey {
	t.Helper()
	key, err := NewTaskKey(strings.NewReader(strings.Repeat(fill, TaskKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestTaskKeysAreStableKeyedAndFingerprinted(t *testing.T) {
	t.Parallel()
	key, other := fixedKey(t, "a"), fixedKey(t, "b")
	first, again := key.Of("task-a"), key.Of("task-a")
	if first != again || first == other.Of("task-a") || first == key.Of("task-b") {
		t.Fatal("a task key must be deterministic per key and name, and differ across keys and names")
	}
	if len(key.Of("task-a")) != 22 || key.Of("") != "" {
		t.Fatalf("Of() = %q; an empty task has no key", key.Of("task-a"))
	}
	if len(key.ID()) != 16 || key.ID() == other.ID() {
		t.Fatalf("ID() = %q", key.ID())
	}
	parsed, err := ParseTaskKey(key.Text())
	if err != nil || parsed.ID() != key.ID() {
		t.Fatalf("ParseTaskKey(Text()) = %v, %v", parsed, err)
	}
	for _, text := range []string{"", "not base64!", "c2hvcnQ"} {
		if _, err := ParseTaskKey(text); !errors.Is(err, ErrInvalidTaskKey) {
			t.Fatalf("ParseTaskKey(%q) = %v", text, err)
		}
	}
	if _, err := NewTaskKey(brokenReader{}); err == nil {
		t.Fatal("a failed random source must fail key generation")
	}
	if got := key.entry("wt", "wt-123"); !strings.HasPrefix(got, "wt-k") || got == other.entry("wt", "wt-123") {
		t.Fatalf("entry() = %q", got)
	}
}

func TestTaskKeyFileIsCreatedOnceAndKeptPrivate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials", TaskKeyFile)
	created, isNew, err := LoadOrCreateTaskKey(path, strings.NewReader(strings.Repeat("z", TaskKeyBytes)))
	if err != nil || !isNew {
		t.Fatalf("first load = %v, %v", isNew, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v", info.Mode().Perm())
	}
	loaded, isNew, err := LoadOrCreateTaskKey(path, brokenReader{})
	if err != nil || isNew || loaded.ID() != created.ID() {
		t.Fatalf("second load = %v, %v, %v", loaded.ID(), isNew, err)
	}
	if err := InstallTaskKey(path, created); err != nil {
		t.Fatalf("installing the same key again is a no-op: %v", err)
	}
	if err := InstallTaskKey(path, fixedKey(t, "q")); !errors.Is(err, ErrTaskKeyMismatch) {
		t.Fatalf("a different key must never replace the existing one: %v", err)
	}
	garbled := filepath.Join(dir, "garbled-key")
	if err := os.WriteFile(garbled, []byte("a b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallTaskKey(garbled, created); !errors.Is(err, ErrTaskKeyMismatch) {
		t.Fatalf("an unreadable existing key is not silently replaced: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateTaskKey(path, nil); err == nil || !strings.Contains(err.Error(), "other users") {
		t.Fatalf("a readable key file must be refused, got %v", err)
	}
}

func TestTaskKeyFileFailuresAreReported(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("two tokens\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateTaskKey(garbage, nil); !errors.Is(err, ErrInvalidTaskKey) {
		t.Fatalf("garbage key file = %v", err)
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("c2hvcnQ\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateTaskKey(short, nil); !errors.Is(err, ErrInvalidTaskKey) {
		t.Fatalf("short key file = %v", err)
	}
	asDirectory := filepath.Join(dir, "directory")
	if err := os.Mkdir(asDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateTaskKey(asDirectory, nil); err == nil || !strings.Contains(err.Error(), "read desk task key") {
		t.Fatalf("a directory in place of the key = %v", err)
	}
	underFile := filepath.Join(garbage, "key")
	if _, _, err := LoadOrCreateTaskKey(underFile, nil); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("a path under a file = %v", err)
	}
	if _, _, err := LoadOrCreateTaskKey(filepath.Join(dir, "new"), brokenReader{}); err == nil {
		t.Fatal("no entropy must fail creation")
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallTaskKey(filepath.Join(blocked, "sub", "key"), fixedKey(t, "a")); err == nil {
		t.Fatal("an uncreatable directory must fail installation")
	}
	if _, _, err := LoadOrCreateTaskKey(filepath.Join(dir, "missing", "key"), strings.NewReader(strings.Repeat("y", TaskKeyBytes))); err != nil {
		t.Fatalf("creation makes the credentials directory: %v", err)
	}
	readOnly := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if _, _, err := LoadOrCreateTaskKey(filepath.Join(readOnly, "key"), strings.NewReader(strings.Repeat("x", TaskKeyBytes))); err == nil {
			t.Fatal("an unwritable directory must fail creation")
		}
	}
}

func publishableEnvelope() fleet.Envelope {
	repository := fleet.Repository{Entry: fleet.Entry{ID: "rp-1"}, Name: "acme/widgets", DefaultBranch: "main"}
	return fleet.Envelope{SchemaVersion: fleet.ExportSchemaVersion, Machine: "macbook", Fleet: &fleet.Document{
		RepositoriesTotal: 1, Repositories: []fleet.Repository{repository},
		Machines: []fleet.Machine{{Entry: fleet.Entry{ID: "mc-1", ObservedAt: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)}, WBVersion: "0.211.0"}},
		Worktrees: []fleet.Worktree{
			{Entry: fleet.Entry{ID: "wt-main"}, Repository: "rp-1", Name: "widgets", Task: "", Branch: "main"},
			{Entry: fleet.Entry{ID: "wt-a"}, Repository: "rp-1", Name: "task-a", Task: "task-a", Branch: "feature/a"},
		},
		PullRequests: []fleet.PullRequest{{Entry: fleet.Entry{ID: "pr-7"}, Repository: "rp-1", Worktree: "wt-a", Branch: "feature/a", Number: 7, FailedCheck: "lint"}},
		Agents:       []fleet.Agent{{Entry: fleet.Entry{ID: "ag-1"}, Kind: fleet.AgentRun, State: "running", Task: "task-a", Worktrees: []string{"wt-a"}}},
	}}
}

func TestReduceKeepsTheDefaultBranchAndHashesCanonically(t *testing.T) {
	t.Parallel()
	key := fixedKey(t, "a")
	first, err := Reduce(publishableEnvelope(), nil, desk.LevelRepositories, key)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Reduce(publishableEnvelope(), nil, desk.LevelRepositories, key)
	if first.Hash != again.Hash || !strings.HasPrefix(first.Hash, "sha256:") || first.TaskKeyID != key.ID() {
		t.Fatalf("hash %s vs %s, key id %s", first.Hash, again.Hash, first.TaskKeyID)
	}
	var decoded struct {
		Fleet struct {
			Worktrees []map[string]any `json:"worktrees"`
			Pulls     []map[string]any `json:"pull_requests"`
			Agents    []map[string]any `json:"agents"`
		} `json:"fleet"`
	}
	if err := json.Unmarshal(first.Envelope, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Fleet.Worktrees[0]["branch"] != "main" || decoded.Fleet.Worktrees[0]["task_key"] != nil {
		t.Fatalf("a default-branch worktree keeps its branch and has no task: %v", decoded.Fleet.Worktrees[0])
	}
	named := decoded.Fleet.Worktrees[1]
	if named["branch"] != nil || named["task"] != nil || named["name"] != nil || named["task_key"] != key.Of("task-a") || named["id"] == "wt-a" {
		t.Fatalf("a task worktree is withheld and re-keyed: %v", named)
	}
	pull := decoded.Fleet.Pulls[0]
	if pull["worktree"] != nil || pull["branch"] != nil || pull["failed_check"] != nil || pull["number"] != float64(7) {
		t.Fatalf("pull request = %v", pull)
	}
	agent := decoded.Fleet.Agents[0]
	if agent["task"] != nil || agent["worktrees"] != nil || agent["task_key"] != key.Of("task-a") {
		t.Fatalf("agent = %v", agent)
	}
	full, err := Reduce(publishableEnvelope(), nil, desk.LevelFull, TaskKey{})
	if err != nil || full.TaskKeyID != "" || !strings.Contains(string(full.Envelope), `"task":"task-a"`) {
		t.Fatalf("full = %s, %v", full.Envelope, err)
	}
	request := first.Request(time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC))
	if request.Validate() != nil || request.Hash != first.Hash || string(request.Envelope) != string(first.Envelope) {
		t.Fatalf("request = %+v", request)
	}
}

func TestReduceRefusesWhatMustNotBePublished(t *testing.T) {
	t.Parallel()
	key := fixedKey(t, "a")
	warming := publishableEnvelope()
	warming.Fleet.WarmingUp = true
	unlistable := publishableEnvelope()
	unlistable.Fleet.Error, unlistable.Fleet.RepositoriesTotal = fleet.ErrorRepositoriesUnreadable, 0
	for name, envelope := range map[string]fleet.Envelope{"metrics only": {}, "warming up": warming, "unlistable": unlistable} {
		if _, err := Reduce(envelope, nil, desk.LevelFull, key); !errors.Is(err, ErrNothingToPublish) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Reduce(publishableEnvelope(), nil, desk.LevelRepositories, TaskKey{}); !errors.Is(err, ErrInvalidTaskKey) {
		t.Errorf("repositories detail without a key = %v", err)
	}
	if _, err := Reduce(publishableEnvelope(), nil, "summary", key); err == nil {
		t.Error("summary is a share level, never a machine detail level")
	}
	unencodable := &fleet.Throughput{Slowest: []fleet.ThroughputTask{{Task: "t", LandedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}}
	if _, err := Reduce(publishableEnvelope(), unencodable, desk.LevelRepositories, key); err == nil {
		t.Error("an unencodable snapshot must fail, not publish")
	}
}
