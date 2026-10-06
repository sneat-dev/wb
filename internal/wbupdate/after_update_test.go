package wbupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv" // fakeSelfUpdateBinary writes an executable POSIX shell script standing in
	// for the exact installed executable identity supplied by the shared provider.
	"github.com/strongo/cli-helpers/selfupdate"
)

func fakeSelfUpdateBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-wb")
	script := "#!/bin/sh\n" + body + "\n"
	if err := testenv.WriteExecutableFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
func successfulSelfUpdate(binary string) selfupdate.AfterUpdate {
	return selfupdate.AfterUpdate{Outcome: selfupdate.Outcome{Action: selfupdate.ActionManagerExecuted, Result: selfupdate.CheckResult{Current: "0.96.2", Latest: "0.96.3"}}, Executable: selfupdate.ExecutableIdentity{Path: binary, ResolvedPath: binary}}
}

func TestSyncSkillsAfterSelfUpdateUsesProviderExecutableAndReportsVerifiedTarget(t *testing.T) {
	t.Parallel()
	binary := fakeSelfUpdateBinary(t, `echo "synced: $1 $2"`)
	cmd := &Output{Out: io.Discard, Err: io.Discard}
	var stdout, stderr bytes.Buffer
	cmd.Out = &stdout
	cmd.Err = &stderr

	if err := testService().SyncSkills(*cmd, context.Background(), successfulSelfUpdate(binary)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Verified installed wb version: 0.96.3 (was 0.96.2).", "synced: skills sync"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestSyncSkillsAfterSelfUpdateSkipsAlreadyCurrentAndUnverifiedManagerOutcome(t *testing.T) {
	t.Parallel()
	binary := fakeSelfUpdateBinary(t, `echo "unexpected invocation"`)
	cmd := &Output{Out: io.Discard, Err: io.Discard}

	current := successfulSelfUpdate(binary)
	current.Outcome.Action = selfupdate.ActionAlreadyCurrent
	if err := testService().SyncSkills(*cmd, context.Background(), current); err != nil {
		t.Fatalf("already-current callback = %v, want nil", err)
	}

	stale := successfulSelfUpdate(binary)
	stale.Outcome.PostSwapWarning = errors.New("installed 0.96.2, want 0.96.3")
	err := testService().SyncSkills(*cmd, context.Background(), stale)
	if err == nil || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("stale callback error = %v, want verification refusal", err)
	}
}

func TestSyncSkillsAfterSelfUpdateReturnsActionableFailure(t *testing.T) {
	t.Parallel()
	binary := fakeSelfUpdateBinary(t, `echo "boom" 1>&2; exit 1`)
	cmd := &Output{Out: io.Discard, Err: io.Discard}
	err := testService().SyncSkills(*cmd, context.Background(), successfulSelfUpdate(binary))
	if err == nil {
		t.Fatal("skills sync error = nil, want warning source")
	}
	for _, want := range []string{"skills sync failed", "wb skills sync", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
}

func TestSyncSkillsAfterSelfUpdateKeepsJSONStdoutSingleDocument(t *testing.T) {
	t.Parallel()
	binary := fakeSelfUpdateBinary(t, `echo "skills-sync-output"`)
	cmd := &Output{Out: io.Discard, Err: io.Discard}
	cmd.JSON = true
	var stdout, stderr bytes.Buffer
	cmd.Out = &stdout
	cmd.Err = &stderr

	if err := testService().SyncSkills(*cmd, context.Background(), successfulSelfUpdate(binary)); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no nested output in JSON mode", stdout.String())
	}
	for _, want := range []string{"Verified installed wb version: 0.96.3", "skills-sync-output"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}

// A verified update that made no change (ActionAlreadyCurrent), or one whose
// post-swap version probe already flagged a problem (PostSwapWarning), must
// never attempt a daemon restart at all — there is nothing to hand a new
// binary to in the first case, and the second is already reporting its own
// distinct warning.
func TestRestartDaemonAfterSelfUpdateSkipsWhenAlreadyCurrentOrPostSwapWarned(t *testing.T) {
	t.Parallel()
	command := &Output{Out: io.Discard, Err: io.Discard}
	var stderr bytes.Buffer
	command.Err = &stderr

	testService().RestartDaemon(*command, context.Background(), selfupdate.AfterUpdate{
		Outcome: selfupdate.Outcome{Action: selfupdate.ActionAlreadyCurrent},
	})
	if stderr.Len() != 0 {
		t.Fatalf("already-current must not attempt a restart: %q", stderr.String())
	}

	testService().RestartDaemon(*command, context.Background(), selfupdate.AfterUpdate{
		Outcome: selfupdate.Outcome{Action: selfupdate.ActionUpdated, PostSwapWarning: errors.New("post-swap probe did not confirm the expected version")},
	})
	if stderr.Len() != 0 {
		t.Fatalf("a post-swap warning must not also attempt a restart: %q", stderr.String())
	}
}
