package desk

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// reducedEnvelope is a repositories-level envelope as the daemon sends it:
// repository identities, keyed tasks, and a worktree on the default branch.
const reducedEnvelope = `{"schema_version":1,"machine":"macbook","fleet":{
	"repositories":[{"id":"r1","name":"acme/widgets","default_branch":"main"}],
	"worktrees":[{"id":"wt-1","repository":"r1","branch":"main","task_key":"k1"},
		{"id":"wt-2","repository":"r1","name":"","task":null,"stream":""}],
	"pull_requests":[{"id":"p1","repository":"r1","number":7,"worktree":"","branch":""}],
	"agents":[{"id":"a1","kind":"claude","state":"running","worktrees":[],"task_key":"k1"}],
	"throughput":{"window_days":7,"slowest":[{"task_key":"k1","duration_seconds":90}]}}}`

func TestCheckLevelAcceptsTheReducedEnvelopeAtRepositories(t *testing.T) {
	t.Parallel()
	if err := CheckLevel(json.RawMessage(reducedEnvelope), LevelRepositories); err != nil {
		t.Fatalf("CheckLevel(reduced) = %v", err)
	}
	if err := CheckLevel(json.RawMessage(`{"machine":"macbook"}`), LevelRepositories); err != nil {
		t.Fatalf("CheckLevel(no fleet) = %v", err)
	}
}

func TestCheckLevelRefusesEachWithheldFieldAtRepositoriesAndNamesOnlyItsPath(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"fleet.worktrees[].name":             `{"fleet":{"worktrees":[{"name":"secret-value"}]}}`,
		"fleet.worktrees[].task":             `{"fleet":{"worktrees":[{},{"task":"secret-value"}]}}`,
		"fleet.worktrees[].stream":           `{"fleet":{"worktrees":[{"stream":"secret-value"}]}}`,
		"fleet.worktrees[].branch":           `{"fleet":{"repositories":[{"id":"r1","default_branch":"main"}],"worktrees":[{"repository":"r1","branch":"secret-value"}]}}`,
		"fleet.pull_requests[].worktree":     `{"fleet":{"pull_requests":[{"worktree":"secret-value"}]}}`,
		"fleet.pull_requests[].branch":       `{"fleet":{"pull_requests":[{"branch":"secret-value"}]}}`,
		"fleet.pull_requests[].failed_check": `{"fleet":{"pull_requests":[{"failed_check":"secret-value"}]}}`,
		"fleet.agents[].task":                `{"fleet":{"agents":[{"task":"secret-value"}]}}`,
		"fleet.agents[].worktrees":           `{"fleet":{"agents":[{"worktrees":["secret-value"]}]}}`,
		"fleet.throughput.slowest[].task":    `{"fleet":{"throughput":{"slowest":[{"task":"secret-value"}]}}}`,
	}
	for _, path := range WithheldAtRepositories() {
		envelope, ok := cases[path]
		if !ok {
			t.Errorf("no case for withheld path %s", path)
			continue
		}
		err := CheckLevel(json.RawMessage(envelope), LevelRepositories)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), path) {
			t.Errorf("CheckLevel(%s) = %v, want ErrInvalid naming the path", path, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-value") {
			t.Errorf("CheckLevel(%s) leaks the value: %v", path, err)
		}
		if err := CheckLevel(json.RawMessage(envelope), LevelFull); err != nil {
			t.Errorf("CheckLevel(%s, full) = %v", path, err)
		}
	}
}

func TestCheckLevelAllowsABranchOnlyWhenItIsTheWorktreeRepositorysDefault(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		// The default branch of a different repository is still a chosen name.
		`{"fleet":{"repositories":[{"id":"r1","default_branch":"main"},{"id":"r2","default_branch":"trunk"}],"worktrees":[{"repository":"r2","branch":"main"}]}}`: false,
		`{"fleet":{"repositories":[{"id":"r1","default_branch":"main"}],"worktrees":[{"repository":"r1","branch":"main"}]}}`:                                      true,
		`{"fleet":{"repositories":[{"id":"r1"}],"worktrees":[{"repository":"r1","branch":"main"}]}}`:                                                              false,
		`{"fleet":{"worktrees":[{"branch":"main"}]}}`:                                                                   false,
		`{"fleet":{"repositories":[{"id":"r1","default_branch":"main"}],"worktrees":[{"repository":"r1","branch":7}]}}`: false,
	}
	for envelope, allowed := range cases {
		err := CheckLevel(json.RawMessage(envelope), LevelRepositories)
		if (err == nil) != allowed {
			t.Errorf("CheckLevel(%s) = %v, want allowed=%v", envelope, err, allowed)
		}
	}
}

func TestCheckLevelRefusesAMalformedShapeOnAWithheldPath(t *testing.T) {
	t.Parallel()
	for _, envelope := range []string{
		`{"fleet":"secret-value"}`,
		`{"fleet":{"worktrees":{"task":"secret-value"}}}`,
		`{"fleet":{"worktrees":["secret-value"]}}`,
		`{"fleet":{"agents":[{"task":7}]}}`,
		`{"fleet":{"agents":[{"task":{"name":"secret-value"}}]}}`,
		`{"fleet":{"throughput":[{"slowest":[]}]}}`,
	} {
		if err := CheckLevel(json.RawMessage(envelope), LevelRepositories); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckLevel(%s) = %v, want ErrInvalid", envelope, err)
		}
	}
	for _, envelope := range []string{
		`{"fleet":null}`,
		`{"fleet":{"worktrees":null,"throughput":null}}`,
		`{"fleet":{"agents":[null,{"task":{}}]}}`,
	} {
		if err := CheckLevel(json.RawMessage(envelope), LevelRepositories); err != nil {
			t.Errorf("CheckLevel(%s) = %v, want nil: nothing is carried", envelope, err)
		}
	}
}

func TestCheckLevelRefusesANonObjectEnvelopeAndAnUnknownLevel(t *testing.T) {
	t.Parallel()
	for _, envelope := range []string{``, `null`, `[]`, `"x"`, `{`} {
		if err := CheckLevel(json.RawMessage(envelope), LevelFull); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckLevel(%q) = %v, want ErrInvalid", envelope, err)
		}
	}
	err := CheckLevel(json.RawMessage(`{}`), Level("everything"))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "detail") {
		t.Errorf("CheckLevel(unknown level) = %v, want ErrInvalid naming detail", err)
	}
}

func TestSnapshotRequestValidateRefusesAnEnvelopeAboveItsDetail(t *testing.T) {
	t.Parallel()
	request := SnapshotRequest{SchemaVersion: SchemaVersion, Detail: LevelRepositories, TaskKeyID: "0123456789abcdef",
		ObservedAt: testTime, Hash: testHash, Envelope: json.RawMessage(`{"fleet":{"agents":[{"task":"secret-value"}]}}`)}
	if err := request.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate(task name at repositories) = %v, want ErrInvalid", err)
	}
	request.Detail, request.TaskKeyID = LevelFull, ""
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate(task name at full) = %v", err)
	}
}

func TestWithheldAtRepositoriesReturnsACopy(t *testing.T) {
	t.Parallel()
	paths := WithheldAtRepositories()
	paths[0] = "changed"
	if again := WithheldAtRepositories(); again[0] == "changed" || !slices.Equal(again, withheldAtRepositories) {
		t.Fatalf("WithheldAtRepositories shares its backing array: %v", again)
	}
}
