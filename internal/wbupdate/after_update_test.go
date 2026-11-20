package wbupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/stalewb"
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

func TestRestartDaemonAfterSelfUpdatePrintsSummaryInsteadOfTheLifecycleJSON(t *testing.T) {
	t.Parallel()
	binary := fakeSelfUpdateBinary(t, `cat <<'EOF'
wb: daemon restart: draining daemon pid 1
wb: daemon restart: starting replacement daemon
{
  "action": "restart",
  "managed": true,
  "process_manager_running": true,
  "reachable": true,
  "ready_verified": true,
  "reported_state": "ready",
  "state": {"status": "ready", "pid": 5632, "provenance": {"sha256": "deadbeef"}}
}
EOF`)
	var stdout, stderr bytes.Buffer
	testService().RestartDaemon(Output{Out: &stdout, Err: &stderr}, context.Background(), successfulSelfUpdate(binary))

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "Daemon restarted and ready.\n" {
		t.Fatalf("stderr = %q, want a one-line summary", stderr.String())
	}
}

func TestRestartDaemonAfterSelfUpdateSaysWhenNothingWasRunning(t *testing.T) {
	t.Parallel()
	binary := fakeSelfUpdateBinary(t, `printf '%s\n' '{"action":"restart","managed":false}'`)
	var stderr bytes.Buffer
	testService().RestartDaemon(Output{Out: io.Discard, Err: &stderr}, context.Background(), successfulSelfUpdate(binary))

	if stderr.String() != "No running daemon to restart.\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRestartDaemonAfterSelfUpdateKeepsTheReceiptWhenTheHandoffFails(t *testing.T) {
	t.Parallel()
	binary := fakeSelfUpdateBinary(t, `printf '%s\n' '{"action":"restart","state":{"pid":1}}'; exit 1`)
	var stderr bytes.Buffer
	testService().RestartDaemon(Output{Out: io.Discard, Err: &stderr}, context.Background(), successfulSelfUpdate(binary))

	for _, want := range []string{"daemon handoff reported a failure", `"action":"restart"`} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}

func TestDaemonRestartSummaryCoversPartialAndUnreadableReceipts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{name: "unreadable", output: "not json", want: "Daemon restart finished."},
		{name: "broken object", output: "prefix {", want: "Daemon restart finished."},
		{name: "missing action", output: `{"managed":true}`, want: "Daemon restart finished."},
		{
			name:   "starting",
			output: `{"action":"restart","process_manager_running":true,"reported_state":"starting","state":{"status":"starting","pid":5}}`,
			want:   "Daemon restarted (starting).",
		},
		{
			name:   "status only",
			output: `{"action":"restart","process_manager_running":true,"state":{"status":"draining","pid":5}}`,
			want:   "Daemon restarted (draining).",
		},
		{
			name:   "warning",
			output: `{"action":"restart","ready_verified":true,"reported_state":"ready","warning":"supervisor mismatch","state":{"status":"ready","pid":5}}`,
			want:   "Daemon restarted and ready.\nwarning: supervisor mismatch",
		},
		{
			name:   "no state",
			output: `{"action":"restart","process_manager_running":true,"reachable":true}`,
			want:   "Daemon restarted.",
		},
		{
			name:   "status ready without the verified flag",
			output: `{"action":"restart","process_manager_running":true,"state":{"status":"ready","pid":5}}`,
			want:   "Daemon restarted and ready.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := daemonRestartSummary([]byte(tc.output)); got != tc.want {
				t.Fatalf("daemonRestartSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAfterUpdateWarnsAboutStaleWbCopiesAndNeverTouchesThem(t *testing.T) {
	t.Parallel()
	stale := stubWb(t)
	scanner := func() stalewb.Scanner {
		return stalewb.Scanner{Executable: stubExecutable, PathEnv: filepath.Dir(stale), Probe: func(context.Context, string) (string, error) { return "0.198.2", nil }}
	}
	for name, test := range map[string]struct {
		update selfupdate.AfterUpdate
		want   bool
	}{
		"installed target":    {selfupdate.AfterUpdate{Outcome: selfupdate.Outcome{Action: selfupdate.ActionManagerExecuted, Target: "0.208.3"}}, true},
		"latest as target":    {selfupdate.AfterUpdate{Outcome: selfupdate.Outcome{Action: selfupdate.ActionUpdated, Result: selfupdate.CheckResult{Latest: "0.208.3"}}}, true},
		"already current":     {selfupdate.AfterUpdate{Outcome: selfupdate.Outcome{Action: selfupdate.ActionAlreadyCurrent, Result: selfupdate.CheckResult{Current: "0.208.3"}}}, true},
		"a dry run":           {selfupdate.AfterUpdate{Outcome: selfupdate.Outcome{Action: selfupdate.ActionPlanned, Target: "0.208.3"}}, false},
		"no version to judge": {selfupdate.AfterUpdate{Outcome: selfupdate.Outcome{Action: selfupdate.ActionUpdated}}, false},
	} {
		var stderr bytes.Buffer
		service := testService()
		service.StaleScanner = scanner
		service.WarnStaleCopies(Output{Out: io.Discard, Err: &stderr}, nil, test.update) //nolint:staticcheck // a nil context is tolerated
		if got := strings.Contains(stderr.String(), stale+" is 0.198.2") && strings.Contains(stderr.String(), "fix: rm "+stale); got != test.want {
			t.Errorf("%s: warned = %t, want %t: %q", name, got, test.want, stderr.String())
		}
	}
	var stderr bytes.Buffer
	service := testService()
	service.WarnStaleCopies(Output{Out: io.Discard, Err: &stderr}, context.Background(), successfulSelfUpdate(stale))
	if stderr.Len() != 0 {
		t.Fatalf("without a scanner: %q", stderr.String())
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("the stale copy was touched: %v", err)
	}
}

func TestAfterUpdateRunsTheStaleCopyWarningAfterSyncingSkills(t *testing.T) {
	t.Parallel()
	binary := stubWb(t)
	var stderr bytes.Buffer
	service := testService()
	service.RunChild = func(context.Context, ChildRequest) (ChildResult, error) {
		return ChildResult{Stdout: []byte("ok")}, nil
	}
	service.StaleScanner = func() stalewb.Scanner {
		return stalewb.Scanner{Executable: stubExecutable, PathEnv: filepath.Dir(binary), Probe: func(context.Context, string) (string, error) { return "0.1.0", nil }}
	}
	update := successfulSelfUpdate(binary)
	update.Outcome.Target = "0.96.3"
	if err := service.AfterUpdate(context.Background(), update, Output{Out: io.Discard, Err: &stderr}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "older wb copies on PATH than 0.96.3") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// stubWb writes a non-empty file named wb under a temp dir. It is only found on
// a fake PATH through stubExecutable, never run: the probe is a seam.
func stubWb(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb")
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// stubExecutable counts a non-empty stub as executable; tests never set an
// execute bit.
func stubExecutable(info os.FileInfo) bool { return info.Size() > 0 }
