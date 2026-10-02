package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/remotessh"
)

// No test here starts ssh or any other process: the runner is a fake, and the
// "ssh executable" the lookup returns is the test binary, which is only ever
// compared.

// exitStatus is a command that ended with a status, as *exec.ExitError says it.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

// sshAnswer is what a fake ssh call writes and how it ends.
type sshAnswer struct {
	stdout, stderr []byte
	err            error
}

// fakeSSH is a remotessh.Runner that starts nothing. It records every call and
// answers from answer; with hang set it does what a hung ssh does, and returns
// only when its context ends.
type fakeSSH struct {
	mu          sync.Mutex
	clock       *manualClock
	calls       [][]string
	executables []string
	stdins      [][]byte
	times       []time.Duration
	answer      func(args []string) sshAnswer
	hang        bool
	entered     chan struct{}
	running     atomic.Int64
	most        atomic.Int64
	returned    atomic.Int64
}

func (f *fakeSSH) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	f.mu.Lock()
	f.calls, f.executables, f.stdins = append(f.calls, slices.Clone(args)), append(f.executables, executable), append(f.stdins, stdin)
	if f.clock != nil {
		f.times = append(f.times, f.clock.Now().Sub(newClock().Now()))
	}
	answer, hang, entered := f.answer, f.hang, f.entered
	f.mu.Unlock()
	if now := f.running.Add(1); now > f.most.Load() {
		f.most.Store(now)
	}
	defer f.running.Add(-1)
	defer f.returned.Add(1)
	if hang {
		if entered != nil {
			entered <- struct{}{}
		}
		<-ctx.Done()
		return errors.New("signal: killed")
	}
	said := answer(args)
	_, _ = stdout.Write(said.stdout)
	_, _ = stderr.Write(said.stderr)
	return said.err
}

func (f *fakeSSH) set(answer func([]string) sshAnswer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
}

func (f *fakeSSH) all() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeSSH) count() int { return len(f.all()) }

// seconds is when each call was made, in seconds since the test clock's start.
func (f *fakeSSH) seconds() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var at []int
	for _, moment := range f.times {
		at = append(at, int(moment/time.Second))
	}
	return at
}

// exporting answers as a machine whose export verb works: the full envelope, or
// the metrics-only one when asked with --metrics-only.
func exporting(t *testing.T, full, only Envelope) func([]string) sshAnswer {
	t.Helper()
	fullBody, onlyBody := append(marshalled(t, full), '\n'), append(marshalled(t, only), '\n')
	return func(args []string) sshAnswer {
		if slices.Contains(args, "--metrics-only") {
			return sshAnswer{stdout: onlyBody}
		}
		return sshAnswer{stdout: fullBody}
	}
}

func failingSSH(status int, stdout, stderr string) func([]string) sshAnswer {
	return func([]string) sshAnswer {
		return sshAnswer{stdout: []byte(stdout), stderr: []byte(stderr), err: exitStatus(status)}
	}
}

// testSSHPath is a clean absolute path of a regular executable file, which is
// what remotessh.Resolve demands. It is never run.
func testSSHPath(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

// foundSSH finds the local ssh, and counts how often it is asked.
func foundSSH(t *testing.T, lookups *atomic.Int64) func() (string, error) {
	t.Helper()
	executable := testSSHPath(t)
	return func() (string, error) {
		if lookups != nil {
			lookups.Add(1)
		}
		return executable, nil
	}
}

var vmRoute = SSHRoute{Host: "vm.example", User: "alex", WBPath: "/usr/local/bin/wb"}

func sshTarget() RemoteTarget { return RemoteTarget{Machine: vmKey, SSH: &vmRoute} }

// newSSHLive is a snapshotter of the local machine that reads vmKey over SSH
// alone through runner, and what it logs.
func newSSHLive(t *testing.T, sources *fakeSources, runner *fakeSSH, timeout time.Duration, change func(*Options)) (*Snapshotter, *manualClock, *logRecorder) {
	t.Helper()
	logs := &logRecorder{}
	var clock *manualClock
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Remotes = []RemoteTarget{sshTarget()}
		options.Transports = []RemoteTransport{{Name: TransportSSH, Exporter: newSSHExporter(foundSSH(t, nil), runner, timeout, func() time.Time { return clock.Now() }, logs.logf)}}
		options.Logf = logs.logf
		if change != nil {
			change(options)
		}
	})
	runner.clock = clock
	return snapshotter, clock, logs
}

// TestSSHArgumentVectorContainsOnlyConfiguredValues proves
// cockpit-views#ac:ssh-argument-vector-contains-only-configured-values through
// the whole daemon path: with a published snapshot naming a machine
// `vm; touch x` and metrics requests carrying hostile machine ids, the only
// processes the daemon would start are the resolved ssh with exactly
// remotessh.BuildWith of the transport's constants and the configured host,
// user and wb path, followed by the constant export words (and --metrics-only
// for the metrics call). Nothing of the snapshot or of a request is in it, and
// nothing is written to its stdin.
func TestSSHArgumentVectorContainsOnlyConfiguredValues(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	runner := &fakeSSH{answer: exporting(t, full, only)}
	sources := oneRepoSources("/repos/widgets")
	hostile := cachedVM("alex")
	hostile.Snapshot.Machine = "vm; touch x"
	sources.remote = append(sources.remote, cachedVM("alex"), hostile)
	snapshotter, clock, _ := newSSHLive(t, sources, runner, sshTotalTimeout, nil)
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	vm, found := machineNamed(snapshotter.Document(), vmKey)
	if !found || vm.Route != RouteLiveRemote || vm.Transport != TransportSSH || vm.RemoteError != "" || vm.WorktreeCount != 3 {
		t.Fatalf("the machine read over ssh = %+v", vm)
	}
	before := runner.count()
	for _, id := range []string{vm.ID, "vm; touch x", "$(reboot)", "-oProxyCommand=evil", "vm.example\nevil"} {
		server.get(metricsURL+url.QueryEscape(id), nil)
	}
	if runner.count() != before {
		t.Fatal("a request started a process")
	}
	clock.advance(metricsOnlyInterval)
	pollAndSettle(t, snapshotter)

	options := remotessh.Options{ConnectTimeoutSeconds: 5, NoForwarding: true, Unattended: true}
	wantFull := remotessh.BuildWith(options, "vm.example", "alex", []string{"/usr/local/bin/wb", "cockpit", "export", "--format", "json"})
	wantOnly := remotessh.BuildWith(options, "vm.example", "alex", []string{"/usr/local/bin/wb", "cockpit", "export", "--format", "json", "--metrics-only"})
	if got := runner.all(); len(got) != 2 || !slices.Equal(got[0], wantFull) || !slices.Equal(got[1], wantOnly) {
		t.Fatalf("the argument vectors = %q, want %q and %q", got, wantFull, wantOnly)
	}
	// The vector, spelled out: this is everything ssh is given.
	literal := []string{
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=no", "-o", "RemoteCommand=none", "-o", "PermitLocalCommand=no", "-o", "LogLevel=ERROR",
		"-l", "alex", "--", "vm.example", "/usr/local/bin/wb", "cockpit", "export", "--format", "json",
	}
	if !slices.Equal(wantFull, literal) {
		t.Fatalf("the full vector = %q, want %q", wantFull, literal)
	}
	for index, executable := range runner.executables {
		if executable != testSSHPath(t) || len(runner.stdins[index]) != 0 {
			t.Errorf("call %d ran %q with %d bytes of stdin", index, executable, len(runner.stdins[index]))
		}
	}
	joined := fmt.Sprint(runner.all())
	for _, absent := range []string{"touch", "reboot", "ProxyCommand", "evil", vmOwnName, "StrictHostKeyChecking", "UserKnownHostsFile", "ControlPath"} {
		if strings.Contains(joined, absent) {
			t.Errorf("the argument vector holds %q: %s", absent, joined)
		}
	}
	// A route with no user and no wb path is the host and the command name.
	if got, want := sshExportArguments(SSHRoute{Host: "vm"}, false), remotessh.BuildWith(options, "vm", "", []string{"wb", "cockpit", "export", "--format", "json"}); !slices.Equal(got, want) || slices.Contains(got, "-l") {
		t.Errorf("the vector of a bare host = %q, want %q", got, want)
	}
}

// TestSSHFailureIsACodeChosenByExitStatus is the whole mapping of a failed ssh
// call to a remote_error code: by exit status, with the login told from an
// unreachable host by fixed OpenSSH phrases only, and the typed reason of the
// export verb read from stdout.
func TestSSHFailureIsACodeChosenByExitStatus(t *testing.T) {
	t.Parallel()
	typed := func(code string) string { return string(marshalled(t, NewExportError(code))) + "\n" }
	for name, test := range map[string]struct {
		err            error
		stdout, stderr string
		want           string
	}{
		"ssh could not be started":            {err: errors.New("fork/exec /usr/bin/ssh: permission denied"), want: RemoteErrorSSHUnavailable},
		"ssh was killed by a signal":          {err: exitStatus(-1), want: RemoteErrorSSHUnavailable},
		"an unresolvable host":                {err: exitStatus(255), stderr: "ssh: Could not resolve hostname vm.example: nodename nor servname provided, or not known", want: RemoteErrorSSHUnavailable},
		"a refused connection":                {err: exitStatus(255), stderr: "ssh: connect to host vm.example port 22: Connection refused", want: RemoteErrorSSHUnavailable},
		"a connection that timed out":         {err: exitStatus(255), stderr: "ssh: connect to host vm.example port 22: Operation timed out", want: RemoteErrorSSHUnavailable},
		"255 and nothing said":                {err: exitStatus(255), want: RemoteErrorSSHUnavailable},
		"a refused key":                       {err: exitStatus(255), stderr: "alex@vm.example: Permission denied (publickey).", want: RemoteErrorAuthFailed},
		"a refused key, in capitals":          {err: exitStatus(255), stderr: "PERMISSION DENIED (publickey,password).", want: RemoteErrorAuthFailed},
		"an unknown host key":                 {err: exitStatus(255), stderr: "Host key verification failed.", want: RemoteErrorAuthFailed},
		"too many keys offered":               {err: exitStatus(255), stderr: "Received disconnect from 192.0.2.1 port 22:2: Too many authentication failures", want: RemoteErrorAuthFailed},
		"no method left":                      {err: exitStatus(255), stderr: "Disconnected: No more authentication methods available", want: RemoteErrorAuthFailed},
		"permission denied by a shell":        {err: exitStatus(126), stderr: "sh: /usr/local/bin/wb: Permission denied", want: RemoteErrorWBMissing},
		"no wb":                               {err: exitStatus(127), stderr: "sh: wb: command not found", want: RemoteErrorWBMissing},
		"a wb with no such command":           {err: exitStatus(2), stderr: `unknown command "export" for "wb cockpit"`, want: RemoteErrorWBTooOld},
		"a wb with no such flag":              {err: exitStatus(2), stderr: "unknown flag: --metrics-only", want: RemoteErrorWBTooOld},
		"no daemon":                           {err: exitStatus(1), stdout: typed(ErrorDaemonNotRunning), want: RemoteErrorDaemonNotRunning},
		"a daemon that refuses":               {err: exitStatus(1), stdout: typed(ErrorExportRefused), want: RemoteErrorExportRefused},
		"a failed export":                     {err: exitStatus(1), stdout: typed(ErrorExportFailed), want: RemoteErrorBadPayload},
		"a daemon that is warming up":         {err: exitStatus(1), stdout: typed(ErrorWarmingUp), want: ""},
		"a reason that is not one of the set": {err: exitStatus(1), stdout: `{"schema_version":1,"error":"` + sentinel + `"}`, want: RemoteErrorBadPayload},
		"status 1 and no reason":              {err: exitStatus(1), stderr: "Permission denied (publickey).", want: RemoteErrorBadPayload},
		"a reason hidden in a long output":    {err: exitStatus(1), stdout: strings.Repeat(" ", maxFailureBytes) + typed(ErrorDaemonNotRunning), want: RemoteErrorBadPayload},
		"another status":                      {err: exitStatus(3), stdout: typed(ErrorDaemonNotRunning), want: RemoteErrorBadPayload},
		"a reason with another field":         {err: exitStatus(1), stdout: `{"schema_version":1,"error":"daemon_not_running","more":1}`, want: RemoteErrorBadPayload},
		"a reason and then more":              {err: exitStatus(1), stdout: typed(ErrorDaemonNotRunning) + `{"error":"export_refused"}`, want: RemoteErrorBadPayload},
		"a reason of another generation":      {err: exitStatus(1), stdout: `{"schema_version":2,"error":"daemon_not_running"}`, want: RemoteErrorBadPayload},
		"a reason after a shell's greeting":   {err: exitStatus(1), stdout: "Welcome\n" + typed(ErrorDaemonNotRunning), want: RemoteErrorBadPayload},
		"a reason that is not an object":      {err: exitStatus(1), stdout: `"daemon_not_running"`, want: RemoteErrorBadPayload},
		"a remote command that exits 255":     {err: exitStatus(255), stdout: typed(ErrorDaemonNotRunning), stderr: "the remote said: Permission denied (", want: RemoteErrorAuthFailed},
		"a wrapped status":                    {err: fmt.Errorf("run: %w", exitStatus(127)), want: RemoteErrorWBMissing},
	} {
		got, started := sshFailure(test.err, []byte(test.stdout), []byte(test.stderr))
		if got != test.want {
			t.Errorf("%s: code %q, want %q", name, got, test.want)
		}
		// Only a call that never ran, or was killed, is not a status of ssh's.
		if notRun := name == "ssh could not be started" || name == "ssh was killed by a signal"; started == notRun {
			t.Errorf("%s: started = %v", name, started)
		}
	}
	// Every code is one of the closed vocabulary.
	for _, code := range sshTransportCodes {
		if !slices.Contains(remoteErrorCodes, code) {
			t.Errorf("%s is not a remote_error code", code)
		}
	}
}

// TestFailedSSHExportShowsATypedErrorAndNeverTheRemotesText proves the SSH half
// of cockpit-views#ac:failed-export-shows-a-typed-error on a machine with one
// transport and a published-store entry: an unresolvable host, `Permission
// denied (publickey)`, a deadline overrun, `wb: command not found`, `unknown
// command "cockpit export"`, an export of daemon_not_running, of export_refused
// and an invalid payload are shown as ssh_unavailable, auth_failed, timeout,
// wb_missing, wb_too_old, daemon_not_running, export_refused and bad_payload on
// the published entries; neither stderr nor stdout text reaches the document,
// the session, the metrics route or an error value; and the next success clears
// the code. A missing local ssh, a wb of another envelope generation and a
// remote that keeps warming up are shown too.
func TestFailedSSHExportShowsATypedErrorAndNeverTheRemotesText(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	typed := func(code string) string { return string(marshalled(t, NewExportError(code))) + "\n" }
	said := sentinel + "remote-text"
	otherGeneration := bytes.Replace(marshalled(t, full), []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)
	unknownField := bytes.Replace(marshalled(t, full), []byte(`{"schema_version":1`), []byte(`{"commands":["`+said+`"],"schema_version":1`), 1)
	for name, test := range map[string]struct {
		answer func([]string) sshAnswer
		hang   bool
		noSSH  bool
		want   string
		quiet  bool
		logged string
	}{
		"an unresolvable host":  {answer: failingSSH(255, "", "ssh: Could not resolve hostname "+said), want: RemoteErrorSSHUnavailable},
		"a refused login":       {answer: failingSSH(255, "", said+": Permission denied (publickey)."), want: RemoteErrorAuthFailed},
		"a deadline overrun":    {hang: true, want: RemoteErrorTimeout},
		"no wb":                 {answer: failingSSH(127, "", "sh: wb: command not found "+said), want: RemoteErrorWBMissing},
		"an old wb":             {answer: failingSSH(2, "", `unknown command "cockpit export" `+said), want: RemoteErrorWBTooOld},
		"no daemon":             {answer: failingSSH(1, typed(ErrorDaemonNotRunning), "wb cockpit export: daemon_not_running "+said), want: RemoteErrorDaemonNotRunning},
		"a daemon that refuses": {answer: failingSSH(1, typed(ErrorExportRefused), said), want: RemoteErrorExportRefused},
		"a failed export":       {answer: failingSSH(1, typed(ErrorExportFailed), said), want: RemoteErrorBadPayload},
		"an invalid payload":    {answer: func([]string) sshAnswer { return sshAnswer{stdout: unknownField} }, want: RemoteErrorBadPayload, quiet: true},
		"not an envelope at all": {answer: func([]string) sshAnswer {
			return sshAnswer{stdout: append([]byte("Welcome to "+said+"\n"), marshalled(t, full)...)}
		}, want: RemoteErrorBadPayload, logged: "bad_payload: output before the envelope)"},
		"a wb of another generation":  {answer: func([]string) sshAnswer { return sshAnswer{stdout: otherGeneration} }, want: RemoteErrorWBTooOld},
		"no local ssh":                {noSSH: true, want: RemoteErrorSSHUnavailable},
		"a daemon that is warming up": {answer: failingSSH(1, typed(ErrorWarmingUp), said), want: RemoteErrorWarmingUp, quiet: true},
	} {
		runner := &fakeSSH{answer: test.answer, hang: test.hang}
		sources := &fakeSources{}
		sources.remote = append(sources.remote, cachedVM("alex"))
		timeout := sshTotalTimeout
		if test.hang {
			timeout = 20 * time.Millisecond
		}
		snapshotter, clock, logs := newSSHLive(t, sources, runner, timeout, func(options *Options) {
			if test.noSSH {
				missing := func() (string, error) { return "", errors.New("executable file not found in $PATH " + said) }
				options.Transports = []RemoteTransport{{Name: TransportSSH, Exporter: NewSSHExporter(missing, runner, nil, options.Logf)}}
			}
		})
		refreshAndSettle(t, snapshotter)
		pollAndSettle(t, snapshotter)
		document := snapshotter.Document()
		vm, found := machineNamed(document, vmKey)
		if !found || vm.Route != RouteCached || vm.RemoteError != test.want || vm.Transport != "" || vm.ObservedAt.IsZero() {
			t.Errorf("%s: the machine = %+v, want its published entry with %s", name, vm, test.want)
			continue
		}
		if !slices.ContainsFunc(document.Worktrees, func(worktree Worktree) bool { return worktree.Task == "stale-task" && worktree.Route == RouteCached }) {
			t.Errorf("%s: the published entries are not shown", name)
		}
		server := newCockpitServer(t, snapshotter)
		read := string(marshalled(t, document)) + server.get(cockpit.APIPrefix+FleetRoute, nil).Body.String() +
			server.get(cockpit.APIPrefix+"session", nil).Body.String() + server.get(metricsURL+vm.ID, nil).Body.String()
		for _, absent := range []string{said, "Permission denied", "command not found", "unknown command", "resolve hostname", "vm.example", "/usr/local/bin"} {
			if strings.Contains(read, absent) {
				t.Errorf("%s: a reader is sent %q", name, absent)
			}
		}
		if test.noSSH != (runner.count() == 0) {
			t.Errorf("%s: %d processes", name, runner.count())
		}
		// The daemon's own log names the code, and what stderr held as one
		// sanitised line; it is the only place that text goes.
		// A refused envelope is logged by the scheduler, with its code alone.
		if got := logs.count("the ssh export of vm failed (" + test.want); (got == 1) == test.quiet || logs.count(test.logged) == 0 {
			t.Errorf("%s: %d log lines of the ssh failure: %q", name, got, logs.all())
		}
		// Nothing ssh or the remote wrote is in the log either: the dashboard's log
		// route serves it.
		for _, absent := range []string{said, "Permission denied", "command not found", "unknown command", "resolve hostname", "vm.example"} {
			if logs.count(absent) > 0 {
				t.Errorf("%s: the log holds %q: %q", name, absent, logs.all())
			}
		}
		// The next success clears the code, after the failure's backoff.
		runner.mu.Lock()
		runner.hang, runner.answer = false, exporting(t, full, only)
		runner.mu.Unlock()
		if test.noSSH {
			continue
		}
		clock.advance(2 * DefaultInterval)
		pollAndSettle(t, snapshotter)
		if vm, found := machineNamed(snapshotter.Document(), vmKey); !found || vm.Route != RouteLiveRemote || vm.Transport != TransportSSH || vm.RemoteError != "" {
			t.Errorf("%s: after a success the machine = %+v", name, vm)
		}
	}
}

// TestAFailingSSHMachineIsRetriedWithADelayThatDoublesToFiveMinutes: the cadence
// of the SSH transport is the scheduler's, as the HTTP transport's is: a machine
// that fails is asked again after 2, 4, 5 and 5 minutes, and never for its
// metrics alone.
func TestAFailingSSHMachineIsRetriedWithADelayThatDoublesToFiveMinutes(t *testing.T) {
	t.Parallel()
	runner := &fakeSSH{answer: failingSSH(255, "", "ssh: connect to host vm.example port 22: Connection refused")}
	snapshotter, clock, logs := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, sshTotalTimeout, nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	for range 17 * 60 / 5 {
		pollAndSettle(t, snapshotter)
		if vm, found := machineNamed(snapshotter.Document(), vmKey); found {
			server.get(metricsURL+vm.ID, nil)
		}
		clock.advance(remoteStep)
	}
	if got, want := runner.seconds(), []int{0, 120, 360, 660, 960}; !slices.Equal(got, want) {
		t.Errorf("attempts at %v seconds, want %v", got, want)
	}
	if slices.ContainsFunc(runner.all(), func(args []string) bool { return slices.Contains(args, "--metrics-only") }) {
		t.Error("a failing machine was asked for its metrics alone")
	}
	if vm, found := machineNamed(snapshotter.Document(), vmKey); !found || vm.RemoteError != RemoteErrorSSHUnavailable || vm.Route != RouteLiveRemote {
		t.Errorf("the failing machine = %+v", vm)
	}
	// The same failure, five times, is logged once.
	if got := logs.count("the ssh export of vm failed (ssh_unavailable)"); got != 1 {
		t.Errorf("%d log lines of one repeated failure: %q", got, logs.all())
	}
}

// TestAHungSSHNeverDelaysTheLocalSnapshotIsReadOnceAtATimeAndIsWaitedFor: while
// an ssh hangs, the local snapshot is taken and published, a second look of the
// loop starts no second process for that machine, and the call ends at its
// timeout as `timeout`, with the runner returned before the export is: the
// process is always waited for, never abandoned.
func TestAHungSSHNeverDelaysTheLocalSnapshotIsReadOnceAtATimeAndIsWaitedFor(t *testing.T) {
	t.Parallel()
	runner := &fakeSSH{hang: true, entered: make(chan struct{}, 4)}
	snapshotter, _, _ := newSSHLive(t, oneRepoSources("/repos/widgets"), runner, time.Hour, nil)
	ctx, stop := context.WithCancel(t.Context())
	snapshotter.pollRemotes(ctx)
	<-runner.entered
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if document := snapshotter.Document(); document.WarmingUp || document.RepositoriesScanned != 1 {
		t.Fatalf("the local snapshot waited for ssh: %+v", document)
	}
	snapshotter.pollRemotes(ctx)
	snapshotter.pollRemotes(ctx)
	if runner.count() != 1 || runner.most.Load() != 1 {
		t.Fatalf("%d processes for one machine, %d at once", runner.count(), runner.most.Load())
	}
	stop()
	snapshotter.side.Wait()
	if runner.returned.Load() != 1 || runner.running.Load() != 0 {
		t.Fatalf("the export ended with %d processes running", runner.running.Load())
	}

	// The exporter alone: the call is cut at its total timeout, and Export
	// returns only after the runner did.
	hung := &fakeSSH{hang: true}
	exporter := newSSHExporter(foundSSH(t, nil), hung, 20*time.Millisecond, nil, nil)
	started := time.Now()
	_, err := exporter.Export(t.Context(), sshTarget(), false)
	var failure *RemoteError
	if !errors.As(err, &failure) || failure.Code != RemoteErrorTimeout || !failure.Fallback {
		t.Fatalf("a hung ssh = %v, want timeout", err)
	}
	if hung.returned.Load() != 1 || hung.running.Load() != 0 || time.Since(started) > 10*time.Second {
		t.Fatalf("Export returned with the process still running (after %s)", time.Since(started))
	}
	if sshTotalTimeout != 15*time.Second || sshConnectSeconds != 5 || sshTotalTimeout+remoteTotalTimeout > remoteExportTimeout {
		t.Fatalf("the transport's bounds are %s and %d s", sshTotalTimeout, sshConnectSeconds)
	}
}

// TestNoSSHProcessIsStartedWithoutAValidConfiguredRoute: a target with no ssh
// route, or with a host, user or wb path that could be read as an option or
// that holds a space, a control character or a shell character, has no SSH
// route: nothing is looked up and nothing is run.
func TestNoSSHProcessIsStartedWithoutAValidConfiguredRoute(t *testing.T) {
	t.Parallel()
	var lookups atomic.Int64
	runner := &fakeSSH{answer: failingSSH(255, "", "")}
	exporter := NewSSHExporter(foundSSH(t, &lookups), runner, nil, nil)
	for name, route := range map[string]*SSHRoute{
		"no ssh section":             nil,
		"no host":                    {},
		"a host that is an option":   {Host: "-oProxyCommand=evil"},
		"a host with a space":        {Host: "vm example"},
		"a host with a tab":          {Host: "vm\texample"},
		"a host with a line break":   {Host: "vm\nexample"},
		"a host with a NUL":          {Host: "vm\x00example"},
		"a host with shell text":     {Host: "vm;touch"},
		"a host with a user in it":   {Host: "alex@vm"},
		"a user that is an option":   {Host: "vm", User: "-oProxyCommand=evil"},
		"a user with a space":        {Host: "vm", User: "al ex"},
		"a user with a control":      {Host: "vm", User: "al\x1bex"},
		"a wb path that is a flag":   {Host: "vm", WBPath: "-x"},
		"a relative wb path":         {Host: "vm", WBPath: "bin/wb"},
		"a wb path with a space":     {Host: "vm", WBPath: "/opt/my wb/wb"},
		"a wb path with a control":   {Host: "vm", WBPath: "/opt/wb\x07"},
		"a wb path with shell text":  {Host: "vm", WBPath: "/opt/$(reboot)/wb"},
		"a wb path that is unclean":  {Host: "vm", WBPath: "/opt/../wb"},
		"a wb path with a semicolon": {Host: "vm", WBPath: "/opt/wb;reboot"},
	} {
		if _, err := exporter.Export(t.Context(), RemoteTarget{Machine: vmKey, SSH: route, HTTP: &HTTPRoute{URL: "https://vm.example"}}, false); !errors.Is(err, ErrNoRoute) {
			t.Errorf("%s: Export = %v, want ErrNoRoute", name, err)
		}
	}
	if runner.count() != 0 || lookups.Load() != 0 {
		t.Fatalf("%d processes and %d lookups for targets with no ssh route", runner.count(), lookups.Load())
	}
	// A snapshotter given such a route as a copy route does not offer it either.
	snapshotter, _ := newSnapshotter((&fakeSources{}).collectors(), func(options *Options) {
		options.SSHRoutes = map[string]SSHRoute{"bad": {Host: "-oProxyCommand=evil"}, "": {Host: "vm"}, testMachine: {Host: "self"}}
	})
	if routes := snapshotter.MachineRoutes(); len(routes) != 0 {
		t.Fatalf("the machine routes = %+v", routes)
	}
}

// TestTheLocalSSHIsSearchedForUntilFoundKeptAndSearchedForAgainWhenItCannotBeStarted:
// while there is no ssh each export is ssh_unavailable and searches again; once
// found it is kept; and when a call cannot be started with it (it was removed
// or replaced), the next export searches again.
func TestTheLocalSSHIsSearchedForUntilFoundKeptAndSearchedForAgainWhenItCannotBeStarted(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 0, false)
	runner := &fakeSSH{answer: exporting(t, full, full)}
	var lookups atomic.Int64
	installed := false
	exporter := NewSSHExporter(func() (string, error) {
		lookups.Add(1)
		if !installed {
			return "", errors.New("not found")
		}
		return testSSHPath(t), nil
	}, runner, func() time.Time { return newClock().Now() }, nil)
	export := func() error {
		_, err := exporter.Export(t.Context(), sshTarget(), false)
		return err
	}
	var failure *RemoteError
	for range 2 {
		if err := export(); !errors.As(err, &failure) || failure.Code != RemoteErrorSSHUnavailable || !failure.Fallback {
			t.Fatalf("with no ssh: %v", err)
		}
	}
	installed = true
	for range 3 {
		if err := export(); err != nil {
			t.Fatalf("with ssh: %v", err)
		}
	}
	if lookups.Load() != 3 || runner.count() != 3 {
		t.Fatalf("%d searches and %d processes, want 3 and 3", lookups.Load(), runner.count())
	}
	// A status of ssh's own, whatever it is, keeps the executable.
	runner.set(failingSSH(255, "", "ssh: connect to host vm.example port 22: Connection refused"))
	_ = export()
	_ = export()
	if lookups.Load() != 3 {
		t.Fatalf("%d searches after ssh ran and failed, want 3", lookups.Load())
	}
	runner.set(func([]string) sshAnswer { return sshAnswer{err: errors.New("fork/exec: no such file or directory")} })
	if err := export(); !errors.As(err, &failure) || failure.Code != RemoteErrorSSHUnavailable {
		t.Fatalf("an ssh that cannot be started: %v", err)
	}
	_ = export()
	if lookups.Load() != 4 {
		t.Fatalf("%d searches after a call that could not be started, want 4", lookups.Load())
	}
}

// TestSSHStderrDecidesTheCodeFromItsEndAndGoesNowhere: the end of what ssh or
// the remote writes to stderr is held (a long banner cannot push the refusal out
// of it) to tell a refused login from an unreachable host, and is then dropped:
// the error an export returns is its code, and the log line is the code, written
// once per code.
func TestSSHStderrDecidesTheCodeFromItsEndAndGoesNowhere(t *testing.T) {
	t.Parallel()
	attempt := 0
	runner := &fakeSSH{answer: func([]string) sshAnswer {
		attempt++
		banner := strings.Repeat(sentinel+"banner \x1b[31m\u202e\u2028", 400)
		return sshAnswer{stdout: []byte(sentinel + "stdout"), err: exitStatus(255),
			stderr: []byte(banner + fmt.Sprintf("\r\nattempt %d\x00\x07 alex@vm.example: Permission denied (publickey).\n", attempt))}
	}}
	logs := &logRecorder{}
	exporter := NewSSHExporter(foundSSH(t, nil), runner, nil, logs.logf)
	export := func() error {
		_, err := exporter.Export(t.Context(), sshTarget(), false)
		return err
	}
	for range 3 {
		err := export()
		var failure *RemoteError
		// The refusal is at the end of a stderr many times the buffer's size.
		if !errors.As(err, &failure) || failure.Code != RemoteErrorAuthFailed || err.Error() != "remote export failed: auth_failed" {
			t.Fatalf("the error = %v", err)
		}
	}
	if lines := logs.all(); len(lines) != 1 || lines[0] != "cockpit fleet: the ssh export of vm failed (auth_failed)" {
		t.Fatalf("the log of one failure worded three ways = %q", lines)
	}
	// Another code is another line, and after a success the same code is said
	// again.
	runner.set(failingSSH(127, "", "sh: wb: command not found"))
	_ = export()
	full := exportOf(t, vmOwnName, vmSources(), 0, false)
	runner.set(exporting(t, full, full))
	exporter.now = func() time.Time { return newClock().Now() }
	if err := export(); err != nil {
		t.Fatal(err)
	}
	runner.set(failingSSH(127, "", "sh: wb: command not found"))
	_ = export()
	if got := logs.count("the ssh export of vm failed (wb_missing)"); got != 2 || len(logs.all()) != 3 {
		t.Fatalf("the log = %q", logs.all())
	}
	if maxSSHDiagnosticBytes != remotessh.MaxDiagnosticBytes {
		t.Fatalf("stderr is held to %d bytes", maxSSHDiagnosticBytes)
	}
}

// TestADaemonThatStopsIsNotATimedOutMachine: when the daemon's own context is
// cancelled while ssh runs, the export ends with that cancellation: nothing is
// logged and no code is made of it. The overall bound of an export running out
// is still a timeout.
func TestADaemonThatStopsIsNotATimedOutMachine(t *testing.T) {
	t.Parallel()
	hung := &fakeSSH{hang: true, entered: make(chan struct{}, 1)}
	logs := &logRecorder{}
	exporter := newSSHExporter(foundSSH(t, nil), hung, time.Hour, nil, logs.logf)
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := exporter.Export(ctx, sshTarget(), false)
		done <- err
	}()
	<-hung.entered
	stop()
	var failure *RemoteError
	if err := <-done; !errors.Is(err, context.Canceled) || errors.As(err, &failure) {
		t.Fatalf("a stopped daemon's export = %v, want the cancellation", err)
	}
	if lines := logs.all(); len(lines) != 0 || hung.running.Load() != 0 {
		t.Fatalf("the stop was logged: %q", lines)
	}
	bounded, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	t.Cleanup(cancel)
	hung.entered = nil
	if _, err := exporter.Export(bounded, sshTarget(), false); !errors.As(err, &failure) || failure.Code != RemoteErrorTimeout {
		t.Fatalf("an export that ran out of its overall time = %v, want timeout", err)
	}
}

// TestHostilePayloadIsRefusedOverSSH proves the SSH half of
// cockpit-views#ac:hostile-payload-is-refused through the whole daemon path:
// each hostile stdout is bad_payload, nothing of it is rendered or served as
// metrics, at most 8 MiB of it is held, and the unchanged envelope is accepted.
func TestHostilePayloadIsRefusedOverSSH(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 360, false)
	valid := marshalled(t, full)
	later := newClock().Now().Add(time.Minute)
	for name, body := range hostilePayloads(t, valid, later) {
		runner := &fakeSSH{answer: func([]string) sshAnswer { return sshAnswer{stdout: body} }}
		sources := &fakeSources{}
		sources.remote = append(sources.remote, cachedVM("alex"))
		snapshotter, clock, _ := newSSHLive(t, sources, runner, sshTotalTimeout, nil)
		clock.advance(time.Minute)
		refreshAndSettle(t, snapshotter)
		pollAndSettle(t, snapshotter)
		document := snapshotter.Document()
		vm, found := machineNamed(document, vmKey)
		if !found || vm.Route != RouteCached || vm.RemoteError != RemoteErrorBadPayload || vm.Transport != "" {
			t.Errorf("%s: the machine = %+v, want its published entry with bad_payload", name, vm)
		}
		rendered := marshalled(t, document)
		for _, absent := range []string{"vm-task", "agt-vm", "rm -rf", "/home/ai", RouteLiveRemote} {
			if bytes.Contains(rendered, []byte(absent)) {
				t.Errorf("%s: the document renders %q of the refused payload", name, absent)
			}
		}
		if got := routeOf(t, newCockpitServer(t, snapshotter), vm.ID); got.Route != RouteNone || len(got.Samples) != 0 {
			t.Errorf("%s: the metrics of the refused payload are served: %+v", name, got)
		}
	}
	runner := &fakeSSH{answer: func([]string) sshAnswer { return sshAnswer{stdout: valid} }}
	if _, err := NewSSHExporter(foundSSH(t, nil), runner, func() time.Time { return later }, nil).Export(t.Context(), sshTarget(), false); err != nil {
		t.Fatalf("the unchanged body is refused: %v", err)
	}
}

// TestTheCoolDownOnSSHIsAPureRule is the table of fallback.after and of the
// transports an export may use: the cool-down starts when SSH answers after the
// preferred transport failed, lasts five minutes from that export's start,
// goes on while SSH answers within it, and ends with any failed export, with an
// answer of the preferred transport and at its time.
func TestTheCoolDownOnSSHIsAPureRule(t *testing.T) {
	t.Parallel()
	start := newClock().Now()
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	none := fallback{}
	cooling := fallback{until: at(300), failure: RemoteErrorHTTPUnavailable}
	for name, test := range map[string]struct {
		held    fallback
		started time.Time
		result  exportResult
		want    fallback
	}{
		"http answers":                         {none, at(0), exportResult{ok: true, transport: TransportHTTP}, none},
		"http fails and ssh answers":           {none, at(0), exportResult{ok: true, transport: TransportSSH, failure: RemoteErrorHTTPUnavailable}, cooling},
		"http is refused and ssh answers":      {none, at(60), exportResult{ok: true, transport: TransportSSH, failure: RemoteErrorHTTPAuthFailed}, fallback{until: at(360), failure: RemoteErrorHTTPAuthFailed}},
		"ssh answers a machine with no http":   {none, at(0), exportResult{ok: true, transport: TransportSSH}, none},
		"every transport fails":                {none, at(0), exportResult{failure: RemoteErrorSSHUnavailable}, none},
		"ssh answers within the cool-down":     {cooling, at(240), exportResult{ok: true, transport: TransportSSH}, cooling},
		"ssh answers at the cool-down's last":  {cooling, at(299), exportResult{ok: true, transport: TransportSSH}, cooling},
		"ssh fails within the cool-down":       {cooling, at(120), exportResult{failure: RemoteErrorTimeout}, none},
		"the remote warms up within it":        {cooling, at(120), exportResult{warming: true}, cooling},
		"the remote warms up outside it":       {none, at(120), exportResult{warming: true}, none},
		"http answers after the cool-down":     {cooling, at(300), exportResult{ok: true, transport: TransportHTTP}, none},
		"http fails again after the cool-down": {cooling, at(300), exportResult{ok: true, transport: TransportSSH, failure: RemoteErrorHTTPAuthFailed}, fallback{until: at(600), failure: RemoteErrorHTTPAuthFailed}},
		"ssh alone answers after it":           {cooling, at(300), exportResult{ok: true, transport: TransportSSH}, none},
	} {
		if got := test.held.after(test.started, test.result); got != test.want {
			t.Errorf("%s: the cool-down = %+v, want %+v", name, got, test.want)
		}
	}
	for seconds, want := range map[int]bool{0: true, 240: true, 299: true, 300: false, 360: false} {
		if got := cooling.active(at(seconds)); got != want {
			t.Errorf("at %d s the cool-down is active = %v, want %v", seconds, got, want)
		}
	}
	if none.active(at(0)) || (fallback{until: at(300)}).active(at(0)) {
		t.Error("a cool-down with no failure is active")
	}
	if fallbackCoolDown != 5*time.Minute {
		t.Errorf("the cool-down is %s", fallbackCoolDown)
	}
	http, ssh := RemoteTransport{Name: TransportHTTP}, RemoteTransport{Name: TransportSSH}
	names := func(transports []RemoteTransport) string {
		var joined []string
		for _, transport := range transports {
			joined = append(joined, transport.Name)
		}
		return strings.Join(joined, ",")
	}
	for name, test := range map[string]struct {
		transports      []RemoteTransport
		cooling, barred bool
		want            string
	}{
		"both, not cooling":            {[]RemoteTransport{http, ssh}, false, false, "http,ssh"},
		"both, cooling":                {[]RemoteTransport{http, ssh}, true, false, "ssh"},
		"http alone, cooling":          {[]RemoteTransport{http}, true, false, ""},
		"none":                         {nil, true, false, ""},
		"both, ssh barred":             {[]RemoteTransport{http, ssh}, false, true, "http"},
		"the bar comes before cooling": {[]RemoteTransport{http, ssh}, true, true, "http"},
		"ssh alone, barred":            {[]RemoteTransport{ssh}, false, true, ""},
	} {
		if got := names(routed(test.transports, test.cooling, test.barred)); got != test.want {
			t.Errorf("%s: the transports = %q, want %q", name, got, test.want)
		}
	}
}

// bothRoutes is a snapshotter that reads vmKey over the real HTTP exporter
// against hub (or the address url when hub is nil) and then over the real SSH
// exporter on runner, with published-store entries for it.
func bothRoutes(t *testing.T, address string, runner *fakeSSH, httpTimeout time.Duration) (*Snapshotter, *manualClock) {
	t.Helper()
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, cachedVM("alex"))
	var clock *manualClock
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
		now := func() time.Time { return clock.Now() }
		options.Remotes = []RemoteTarget{{Machine: vmKey, HTTP: &HTTPRoute{URL: address, TokenFile: tokenFile(t, vmBearer)}, SSH: &vmRoute}}
		options.Transports = []RemoteTransport{
			{Name: TransportHTTP, Exporter: newHTTPExporter(httpTimeout, httpTimeout, now, nil)},
			{Name: TransportSSH, Exporter: NewSSHExporter(foundSSH(t, nil), runner, now, nil)},
		}
	})
	runner.clock = clock
	return snapshotter, clock
}

// TestHTTPFailureFallsBackToSSH proves cockpit-views#ac:http-failure-falls-back-to-ssh
// with the real HTTP exporter against a server and the real SSH exporter on a
// fake runner: a refused connection, a timeout, 401, 403, 404, 429, 500 and a
// redirect are each followed by one SSH export, whose data the entry shows with
// transport `ssh` and the HTTP failure as remote_error; a well-formed envelope
// that fails validation is bad_payload, and no ssh is started for it.
func TestHTTPFailureFallsBackToSSH(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	status := func(code int) http.HandlerFunc {
		return func(writer http.ResponseWriter, _ *http.Request) {
			if code >= 300 && code < 400 {
				writer.Header().Set("Location", "https://elsewhere.example/")
			}
			writer.WriteHeader(code)
		}
	}
	// A port nothing listens on: it was this test's, and is closed.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + listener.Addr().String()
	_ = listener.Close()
	for name, test := range map[string]struct {
		handler http.HandlerFunc
		address string
		want    string
	}{
		"a refused connection": {address: refused, want: RemoteErrorHTTPUnavailable},
		"a timeout":            {handler: func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }, want: RemoteErrorHTTPUnavailable},
		"401":                  {handler: status(http.StatusUnauthorized), want: RemoteErrorHTTPAuthFailed},
		"403":                  {handler: status(http.StatusForbidden), want: RemoteErrorHTTPAuthFailed},
		"404":                  {handler: status(http.StatusNotFound), want: RemoteErrorHTTPUnavailable},
		"429":                  {handler: status(http.StatusTooManyRequests), want: RemoteErrorHTTPUnavailable},
		"500":                  {handler: status(http.StatusInternalServerError), want: RemoteErrorHTTPUnavailable},
		"a redirect":           {handler: status(http.StatusFound), want: RemoteErrorHTTPUnavailable},
	} {
		address, requests := test.address, func() int { return 1 }
		if test.handler != nil {
			hub := newFakeHub(t, test.handler)
			address, requests = hub.server.URL, func() int { return len(hub.seen()) }
		}
		runner := &fakeSSH{answer: exporting(t, full, only)}
		snapshotter, _ := bothRoutes(t, address, runner, 200*time.Millisecond)
		refreshAndSettle(t, snapshotter)
		pollAndSettle(t, snapshotter)
		document := snapshotter.Document()
		vm, found := machineNamed(document, vmKey)
		if !found || vm.Route != RouteLiveRemote || vm.Transport != TransportSSH || vm.RemoteError != test.want || vm.WorktreeCount != 3 {
			t.Errorf("%s: the machine = %+v, want its ssh data with %s", name, vm, test.want)
		}
		if !slices.ContainsFunc(document.Worktrees, func(worktree Worktree) bool { return worktree.Task == "vm-task-1" && worktree.Route == RouteLiveRemote }) {
			t.Errorf("%s: the ssh export's entries are not shown", name)
		}
		if requests() != 1 || runner.count() != 1 {
			t.Errorf("%s: %d http requests and %d ssh exports, want 1 and 1", name, requests(), runner.count())
		}
	}
	invalid := bytes.Replace(marshalled(t, full), []byte(`{"schema_version":1`), []byte(`{"commands":[],"schema_version":1`), 1)
	hub := newFakeHub(t, serving(invalid, nil))
	runner := &fakeSSH{answer: exporting(t, full, only)}
	snapshotter, _ := bothRoutes(t, hub.server.URL, runner, remoteTotalTimeout)
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	if vm, found := machineNamed(snapshotter.Document(), vmKey); !found || vm.Route != RouteCached || vm.RemoteError != RemoteErrorBadPayload || vm.Transport != "" {
		t.Errorf("an invalid envelope: the machine = %+v", vm)
	}
	if len(hub.seen()) != 1 || runner.count() != 0 {
		t.Errorf("an invalid envelope: %d http requests and %d ssh exports, want 1 and 0", len(hub.seen()), runner.count())
	}
}

// TestFallbackCoolDownIsHonoured proves cockpit-views#ac:fallback-cool-down-is-honoured
// on a fake clock: after one failed HTTP attempt and an SSH answer, no HTTP
// request is made for five minutes (the fleet exports and the metrics-only ones
// a client asks for all go over SSH, and the entry keeps `transport` ssh and the
// HTTP failure); the first export after them asks HTTP once, and on its success
// the entry is back on `transport` http with remote_error cleared.
func TestFallbackCoolDownIsHonoured(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	only := exportOf(t, vmOwnName, vmSources(), 6, true)
	var healthy atomic.Bool
	good := serving(marshalled(t, full), marshalled(t, only))
	hub := newFakeHub(t, func(writer http.ResponseWriter, request *http.Request) {
		if !healthy.Load() {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		good(writer, request)
	})
	runner := &fakeSSH{answer: exporting(t, full, only)}
	snapshotter, clock := bothRoutes(t, hub.server.URL, runner, remoteTotalTimeout)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	onSSH := func(second int) Machine {
		t.Helper()
		vm, found := machineNamed(snapshotter.Document(), vmKey)
		if !found || vm.Route != RouteLiveRemote || vm.Transport != TransportSSH || vm.RemoteError != RemoteErrorHTTPUnavailable {
			t.Fatalf("at %d s the machine = %+v, want ssh with http_unavailable", second, vm)
		}
		return vm
	}
	// Four minutes: HTTP fails once, at the start, and is then left alone. It
	// would answer from the first minute on.
	for second := 0; second < 240; second += 5 {
		pollAndSettle(t, snapshotter)
		vm := onSSH(second)
		healthy.Store(true)
		if second%10 == 0 {
			server.get(metricsURL+vm.ID, nil)
		}
		clock.advance(remoteStep)
	}
	if got := len(hub.seen()); got != 1 {
		t.Fatalf("%d http requests in the first four minutes, want the one that failed", got)
	}
	// Two minutes more, with no client asking: still none until the fifth minute
	// ends, and then exactly one.
	for second := 240; second < 300; second += 5 {
		pollAndSettle(t, snapshotter)
		onSSH(second)
		clock.advance(remoteStep)
	}
	if got := len(hub.seen()); got != 1 {
		t.Fatalf("%d http requests within the cool-down, want 1", got)
	}
	sshBefore := runner.count()
	pollAndSettle(t, snapshotter)
	if got := len(hub.seen()); got != 2 {
		t.Fatalf("%d http requests after the cool-down, want 2", got)
	}
	vm, found := machineNamed(snapshotter.Document(), vmKey)
	if !found || vm.Route != RouteLiveRemote || vm.Transport != TransportHTTP || vm.RemoteError != "" {
		t.Fatalf("after http answered the machine = %+v", vm)
	}
	for second := 305; second < 360; second += 5 {
		clock.advance(remoteStep)
		pollAndSettle(t, snapshotter)
	}
	if runner.count() != sshBefore {
		t.Errorf("ssh was run %d times after http answered", runner.count()-sshBefore)
	}
	// Within the cool-down the fleet was read once a minute and the metrics every
	// 30 seconds while asked for, all over ssh.
	fleetCalls, metricsCalls := 0, 0
	for _, args := range runner.all() {
		if slices.Contains(args, "--metrics-only") {
			metricsCalls++
		} else {
			fleetCalls++
		}
	}
	if fleetCalls != 5 || metricsCalls < 4 {
		t.Errorf("%d fleet and %d metrics-only exports over ssh at %v, want 5 and at least 4", fleetCalls, metricsCalls, runner.seconds())
	}
}

// TestAnSSHFailureWithinTheCoolDownSendsTheNextAttemptBackToHTTP: the cool-down
// is for an SSH that answers. When it stops answering, the entry shows its
// failure and the next attempt asks HTTP first again.
func TestAnSSHFailureWithinTheCoolDownSendsTheNextAttemptBackToHTTP(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	hub := newFakeHub(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusBadGateway) })
	runner := &fakeSSH{answer: exporting(t, full, full)}
	snapshotter, clock := bothRoutes(t, hub.server.URL, runner, remoteTotalTimeout)
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	runner.set(failingSSH(255, "", "ssh: connect to host vm.example port 22: Connection refused"))
	clock.advance(DefaultInterval)
	pollAndSettle(t, snapshotter)
	if vm, _ := machineNamed(snapshotter.Document(), vmKey); vm.RemoteError != RemoteErrorSSHUnavailable || len(hub.seen()) != 1 {
		t.Fatalf("after ssh failed within the cool-down the machine = %+v with %d http requests", vm, len(hub.seen()))
	}
	clock.advance(2 * DefaultInterval)
	pollAndSettle(t, snapshotter)
	if got := len(hub.seen()); got != 2 {
		t.Fatalf("%d http requests, want the next attempt to ask http again", got)
	}
}

// TestMachineRoutesReachOnlyAnOwnerSessionAndNeverTheDocumentOrAnExport is the
// sentinel of cockpit-views#req:copy-the-command and
// cockpit-views#ac:copy-command-for-an-ssh-machine on the real routes: the
// session response of an owner names, by the ids the document gives the
// machines, the host, user and wb path of every machine with an ssh section
// (read live or not); an anonymous-local reader and the hosted origin, on every
// metadata route, and the export envelope, never see a host, a user or a path.
func TestMachineRoutesReachOnlyAnOwnerSessionAndNeverTheDocumentOrAnExport(t *testing.T) {
	t.Parallel()
	const host, user, path = "SENTINEL-host.example", "SENTINEL_user", "/opt/SENTINEL-path/wb"
	route := SSHRoute{Host: host, User: user, WBPath: path}
	full := exportOf(t, vmOwnName, vmSources(), 6, false)
	runner := &fakeSSH{answer: exporting(t, full, full)}
	sources := oneRepoSources("/repos/widgets")
	old := cachedVM("alex")
	old.Snapshot.Machine = "old"
	bare := cachedVM("alex")
	bare.Snapshot.Machine = "bare"
	sources.remote = append(sources.remote, cachedVM("alex"), old, bare)
	logs := &logRecorder{}
	var clock *manualClock
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Remotes = []RemoteTarget{{Machine: vmKey, SSH: &route}, {Machine: "fresh", SSH: &route}}
		options.Transports = []RemoteTransport{{Name: TransportSSH, Exporter: NewSSHExporter(foundSSH(t, nil), runner, func() time.Time { return clock.Now() }, logs.logf)}}
		// "bare" has an ssh section and is not read (cockpit.remote_ssh: false, say);
		// "old" has none.
		options.SSHRoutes = map[string]SSHRoute{vmKey: route, "fresh": route, "bare": {Host: host}}
		options.Logf = logs.logf
	})
	refreshAndSettle(t, snapshotter)
	pollAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	document := snapshotter.Document()
	ids := map[string]string{}
	for _, name := range []string{vmKey, "old", "bare", "fresh"} {
		machine, found := machineNamed(document, name)
		if !found {
			t.Fatalf("the document has no machine %s", name)
		}
		ids[name] = machine.ID
	}
	var session struct {
		Principal     string                 `json:"principal"`
		MachineRoutes []cockpit.MachineRoute `json:"machine_routes"`
	}
	owned := server.get(cockpit.APIPrefix+"session", server.login())
	if err := json.Unmarshal(owned.Body.Bytes(), &session); err != nil || session.Principal != cockpit.PrincipalOwner {
		t.Fatalf("the owner's session = %s (%v)", owned.Body.String(), err)
	}
	want := []cockpit.MachineRoute{
		{MachineID: ids["bare"], SSH: cockpit.SSHRoute{Host: host, WBPath: "wb"}},
		{MachineID: ids["fresh"], SSH: cockpit.SSHRoute{Host: host, User: user, WBPath: path}},
		{MachineID: ids[vmKey], SSH: cockpit.SSHRoute{Host: host, User: user, WBPath: path}},
	}
	slices.SortFunc(want, func(a, b cockpit.MachineRoute) int { return strings.Compare(a.MachineID, b.MachineID) })
	if !slices.Equal(session.MachineRoutes, want) {
		t.Fatalf("the owner's machine routes = %+v, want %+v", session.MachineRoutes, want)
	}
	if strings.Contains(owned.Body.String(), ids["old"]) {
		t.Error("a machine with no ssh section has a route")
	}
	// Everything an anonymous reader, the hosted origin or another machine is
	// sent, and the daemon's log.
	cookie := server.login()
	read := map[string]string{
		"the export":              string(marshalled(t, snapshotter.Export(false))) + string(marshalled(t, snapshotter.Export(true))),
		"the document":            string(marshalled(t, document)),
		"the log":                 strings.Join(logs.all(), "\n"),
		"the owner's fleet":       server.get(cockpit.APIPrefix+FleetRoute, cookie).Body.String(),
		"the owner's metrics":     server.get(metricsURL+ids[vmKey], cookie).Body.String(),
		"a foreign origin's read": server.get(cockpit.APIPrefix+"session", cookie, "Origin", "https://evil.example").Body.String(),
	}
	for _, target := range []string{cockpit.APIPrefix + "session", cockpit.APIPrefix + FleetRoute, metricsURL + ids[vmKey], metricsURL + ids["bare"], cockpit.APIPrefix + BranchesRoute + "?repository=" + document.Repositories[0].ID} {
		read["anonymous "+target] = server.get(target, nil).Body.String()
		read["hosted "+target] = server.get(target, nil, "Origin", hostedOrigin).Body.String()
		read["hosted with the owner's cookie "+target] = server.get(target, cookie, "Origin", hostedOrigin).Body.String()
		read["proxied "+target] = server.get(target, nil, "Via", "1.1 proxy").Body.String()
	}
	for name, body := range read {
		if body == "" && name != "the log" {
			t.Errorf("%s: nothing was read", name)
		}
		for _, absent := range []string{"SENTINEL", "machine_routes", "/opt/", "wb_path"} {
			if strings.Contains(body, absent) {
				t.Errorf("%s holds %q: %s", name, absent, body)
			}
		}
	}
	// A published entry of the machine under another login is another id of it.
	twice := cachedVM("someone")
	sources.remote = append(sources.remote, twice)
	refreshAndSettle(t, snapshotter)
	if routes := snapshotter.MachineRoutes(); len(routes) != 5 {
		t.Errorf("with two published entries of vm the routes = %+v", routes)
	}
}
