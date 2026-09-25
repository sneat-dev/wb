package agents

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/remotessh"
)

// TestNormalizeState_NilAndNonNil drives both branches of NormalizeState: a
// nil result must return without touching anything, and a non-nil result
// must have Terminal derived from its State.
func TestNormalizeState_NilAndNonNil(t *testing.T) {
	t.Parallel()

	// Must not panic.
	NormalizeState(nil)

	running := &Result{State: StateRunning}
	NormalizeState(running)
	if running.Terminal {
		t.Errorf("Terminal = true for StateRunning, want false")
	}

	completed := &Result{State: StateCompleted}
	NormalizeState(completed)
	if !completed.Terminal {
		t.Errorf("Terminal = false for StateCompleted, want true")
	}
}

// TestValidateProvider_RejectsBothCredentialSources drives the branch that
// rejects a provider naming both credential_env and credential_file: exactly
// one credential source is required, not both.
func TestValidateProvider_RejectsBothCredentialSources(t *testing.T) {
	t.Parallel()

	err := validateProvider("acme", Provider{
		BaseURL:        "https://example.test",
		CredentialEnv:  "ACME_TOKEN",
		CredentialFile: "/etc/acme/token",
	})
	if err == nil {
		t.Fatal("validateProvider() = nil, want an error")
	}
	const want = "must name exactly one credential source"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("validateProvider() error = %q, want it to contain %q", err.Error(), want)
	}
}

// TestParseShortstat_SkipsUnparsableInsertionCount drives the strconv.Atoi
// error branch: a clause whose count is not a plain integer is skipped
// instead of poisoning the result.
func TestParseShortstat_SkipsUnparsableInsertionCount(t *testing.T) {
	t.Parallel()

	insertions, deletions := parseShortstat("3 files changed, many insertions(+), 2 deletions(-)")
	if insertions != 0 {
		t.Errorf("insertions = %d, want 0 (unparsable clause skipped)", insertions)
	}
	if deletions != 2 {
		t.Errorf("deletions = %d, want 2", deletions)
	}
}

// TestRunOwner_DefaultsDepsBeforeLoadFailure drives RunOwner's nil-dependency
// defaulting (LookPath and Now) along the path that returns before starting
// any process: a store with no persisted record for the agent ID fails at
// store.Load, before the harness would ever be launched.
func TestRunOwner_DefaultsDepsBeforeLoadFailure(t *testing.T) {
	t.Parallel()

	store := Store{Root: t.TempDir()}
	agentID := IDPrefix + strings.Repeat("a", 32)

	err := RunOwner(context.Background(), store, agentID, OwnerDeps{})
	if err == nil {
		t.Fatal("RunOwner() = nil, want an error for an unknown agent run")
	}
	var unknown *UnknownAgentError
	if !errors.As(err, &unknown) {
		t.Errorf("RunOwner() error = %v, want *UnknownAgentError", err)
	}
}

// TestDispatch_DefaultsNowBeforeValidationFailure drives Dispatch's
// deps.Now defaulting along the path that fails request validation before
// touching configuration or worktrees.
func TestDispatch_DefaultsNowBeforeValidationFailure(t *testing.T) {
	t.Parallel()

	_, err := Dispatch(context.Background(), DispatchRequest{}, DispatchDeps{})
	if err == nil {
		t.Fatal("Dispatch() = nil, want a validation error for an empty request")
	}
	const want = "worktree"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Dispatch() error = %q, want it to mention %q", err.Error(), want)
	}
}

// TestCallRemote_DefaultsRunnerAndRequiresLookPath drives CallRemote's
// deps.Runner default (to remotessh.ExecRunner{}) along the path that then
// fails immediately because deps.LookPath is nil -- no ssh process starts.
func TestCallRemote_DefaultsRunnerAndRequiresLookPath(t *testing.T) {
	t.Parallel()

	target := RemoteTarget{Machine: "vm", Host: "vm.example.test"}
	request := RemoteRequest{SchemaVersion: remoteSchemaVersion, Operation: RemoteList}

	_, err := CallRemote(context.Background(), target, request, RemoteDeps{})
	if err == nil {
		t.Fatal("CallRemote() = nil, want an error when LookPath is unavailable")
	}
	const want = "executable lookup is unavailable"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("CallRemote() error = %q, want it to contain %q", err.Error(), want)
	}
}

type fakeRunner struct{ err error }

func (f fakeRunner) Run(_ context.Context, _ string, _ []string, _ []byte, _, _ io.Writer) error {
	return f.err
}

// TestCallRemote_DefaultsTimeoutWhenNonPositive drives the timeout <= 0
// default (to DefaultRemoteTimeout) with a fake Runner so no ssh process
// starts; the fake runner fails immediately, which is enough to prove the
// call reached the transport step with the defaulted timeout in place.
func TestCallRemote_DefaultsTimeoutWhenNonPositive(t *testing.T) {
	t.Parallel()

	target := RemoteTarget{Machine: "vm", Host: "vm.example.test"}
	request := RemoteRequest{SchemaVersion: remoteSchemaVersion, Operation: RemoteList}
	deps := RemoteDeps{
		LookPath: func(string) (string, error) { return "/usr/bin/ssh", nil },
		Runner:   fakeRunner{err: errors.New("boom")},
		Timeout:  0,
	}

	_, err := CallRemote(context.Background(), target, request, deps)
	if err == nil {
		t.Fatal("CallRemote() = nil, want the fake runner's error to surface")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("CallRemote() error = %q, want it to wrap the runner's failure", err.Error())
	}
}

// TestDefaultRemoteDeps_ReturnsProductionSeams drives DefaultRemoteDeps's
// literal construction of the production seams.
func TestDefaultRemoteDeps_ReturnsProductionSeams(t *testing.T) {
	t.Parallel()

	deps := DefaultRemoteDeps()
	if deps.LookPath == nil {
		t.Error("DefaultRemoteDeps().LookPath = nil, want execLookPath")
	}
	if deps.Runner != (remotessh.ExecRunner{}) {
		t.Errorf("DefaultRemoteDeps().Runner = %#v, want remotessh.ExecRunner{}", deps.Runner)
	}
	if deps.Timeout != DefaultRemoteTimeout {
		t.Errorf("DefaultRemoteDeps().Timeout = %v, want %v", deps.Timeout, DefaultRemoteTimeout)
	}
}
