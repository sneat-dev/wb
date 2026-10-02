package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/remotessh"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// This file is the SSH transport of a remote machine's export
// (cockpit-views#req:remote-ssh-fetch). The daemon starts `ssh` in the
// background, so what it guarantees is stated here:
//
//   - The argument vector is remotessh.BuildWith of constants and of the
//     SSHRoute it is handed, which the daemon builds from local configuration
//     only (session_move.targets.<machine>.ssh) and which is held to
//     sessionmove.SSHConfig.Validate again here, before anything is started.
//     Nothing of a request, a snapshot, an envelope, the remote's output or the
//     environment is ever part of it. Every remote word is a constant or the
//     validated wb_path, which that rule keeps to characters no shell
//     interprets, so the remote shell has nothing to expand.
//   - ssh never prompts (BatchMode=yes), has no terminal, forwards no agent, no
//     X11 and no port, never becomes a connection-sharing master, runs no
//     RemoteCommand or LocalCommand of the user's ssh configuration, and gives
//     up connecting after sshConnectSeconds. Host key checking is the user's ssh
//     configuration's and is never switched off.
//   - The daemon's runner (remotessh.GroupRunner) gives ssh an allow-listed
//     environment, not the daemon's.
//   - One call lasts at most sshTotalTimeout: the runner kills the process (its
//     whole group) when the context ends, and Export returns only when the
//     runner has, so a process is always waited for.
//   - stdout is read through DecodeEnvelope, as every transport's bytes are, and
//     never more than MaxEnvelopeBytes of it is held.
//   - A failure is a code. The end of stderr (maxSSHDiagnosticBytes of it) is
//     held only to tell a refused login from an unreachable host, and is then
//     dropped: it never reaches the document, an error value, the daemon's log
//     (which the dashboard's log route serves) or any reader.
//
// The code is chosen by the exit status (sshFailure). ssh passes the remote
// command's status on, so a remote command that itself exits 255, 127, 126 or 2
// picks the code: the remote login already has that machine's full authority,
// and the choice is between codes of one closed set.
//
// A remote login shell that prints to stdout (a profile that echoes, a banner
// script) puts text before the envelope: the export is then bad_payload, and
// the log line says "output before the envelope".
//
// A daemon run by launchd or systemd may have no SSH agent socket. Nothing here
// looks for one: the login then fails and is shown as auth_failed.
//
// How often ssh runs is the scheduler's rule, the same for every transport
// (remoteSchedule in remote.go).

const (
	// sshConnectSeconds bounds the connection, and sshTotalTimeout the whole call.
	sshConnectSeconds = 5
	sshTotalTimeout   = 15 * time.Second
	// maxSSHDiagnosticBytes bounds the stderr that is held of one call: its end.
	maxSSHDiagnosticBytes = remotessh.MaxDiagnosticBytes
	// outputBeforeEnvelope is the log's cause for a stdout that does not start
	// with the envelope.
	outputBeforeEnvelope = "output before the envelope"
)

// The exit statuses an export over SSH is understood by. They are statuses, not
// text: ssh itself exits 255 for every failure of its own, a POSIX shell exits
// 127 for a command it cannot find and 126 for one it cannot execute, and wb
// exits 2 for an invocation it refuses (an unknown command or flag) and 1 with a
// typed reason on stdout for an export it could not make.
const (
	exitSSHFailed     = 255
	exitNotFound      = 127
	exitNotExecutable = 126
	exitUsage         = 2
	exitFindings      = 1
)

// sshLoginRefusals are the fixed phrases OpenSSH's client prints when the
// server or the host key refuses the login. An exit status of 255 says only
// that ssh failed, so the login is told from an unreachable host by these,
// compared without regard to case; anything else is ssh_unavailable.
var sshLoginRefusals = []string{
	"permission denied (",
	"host key verification failed",
	"too many authentication failures",
	"no more authentication methods",
}

// SSHRoute is one machine's SSH route, from local configuration
// (session_move.targets.<machine>.ssh): the host, the optional login and the
// optional path of the remote wb.
type SSHRoute struct {
	Host   string
	User   string
	WBPath string
}

// command is the remote wb: the configured path, or the command name.
func (r SSHRoute) command() string {
	if r.WBPath == "" {
		return remotessh.DefaultWBCommand
	}
	return r.WBPath
}

// valid reports whether the route passes the rule every SSH address of WB is
// held to: a host that starts with a letter or digit and has no space, control
// or shell character, a login of the same kind, and a clean absolute wb path of
// shell-inert segments. A value that starts with "-" never passes.
func (r SSHRoute) valid() bool {
	return sessionmove.SSHConfig{Host: r.Host, User: r.User, WBPath: r.WBPath}.Validate() == nil
}

// sshOptions are the fixed ssh options of every export.
var sshOptions = remotessh.Options{ConnectTimeoutSeconds: sshConnectSeconds, NoForwarding: true, Unattended: true}

// sshExportArguments is the whole argument vector of one export: constants and
// the route, and nothing else.
func sshExportArguments(route SSHRoute, metricsOnly bool) []string {
	remote := []string{route.command(), "cockpit", "export", "--format", "json"}
	if metricsOnly {
		remote = append(remote, "--metrics-only")
	}
	return remotessh.BuildWith(sshOptions, route.Host, route.User, remote)
}

// SSHExporter is the SSH RemoteExporter.
type SSHExporter struct {
	find    func() (string, error)
	runner  remotessh.Runner
	timeout time.Duration
	now     func() time.Time
	logf    func(string, ...any)

	mu sync.Mutex
	// executable is the local ssh: found once and kept, until a call cannot be
	// started with it; a search that fails is made again at the next export.
	executable string
	// logged is, by machine, the code of its last logged failure, so a machine
	// that keeps failing the same way is logged once.
	logged map[string]string
}

// NewSSHExporter is the SSH exporter over find (which finds the local ssh
// executable; the daemon's is remotessh.ResolveTrusted) and runner, on the
// clock now (nil means time.Now), logging to logf (nil means nowhere).
func NewSSHExporter(find func() (string, error), runner remotessh.Runner, now func() time.Time, logf func(string, ...any)) *SSHExporter {
	return newSSHExporter(find, runner, sshTotalTimeout, now, logf)
}

// newSSHExporter is the exporter with the given total timeout.
func newSSHExporter(find func() (string, error), runner remotessh.Runner, timeout time.Duration, now func() time.Time, logf func(string, ...any)) *SSHExporter {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &SSHExporter{find: find, runner: runner, timeout: timeout, now: now, logf: logf, logged: map[string]string{}}
}

// resolve is the local ssh executable, searched for until it is found and then
// kept.
func (e *SSHExporter) resolve() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.executable != "" {
		return e.executable, nil
	}
	executable, err := e.find()
	if err != nil {
		return "", err
	}
	e.executable = executable
	return executable, nil
}

// forget drops the kept executable, so the next export searches again.
func (e *SSHExporter) forget() {
	e.mu.Lock()
	e.executable = ""
	e.mu.Unlock()
}

// Export reads target's envelope by running the export verb on it over SSH. A
// target with no route, or whose route the address rule refuses, has no SSH
// route and no process is started.
func (e *SSHExporter) Export(ctx context.Context, target RemoteTarget, metricsOnly bool) (Envelope, error) {
	route := target.SSH
	if route == nil || !route.valid() {
		return Envelope{}, ErrNoRoute
	}
	executable, err := e.resolve()
	if err != nil {
		return Envelope{}, e.failed(target.Machine, RemoteErrorSSHUnavailable, "")
	}
	call, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	stdout := remotessh.NewLimitedBuffer(MaxEnvelopeBytes + 1)
	stderr := remotessh.NewTailBuffer(maxSSHDiagnosticBytes)
	// Run returns when the process has ended and been waited for; the call is
	// never left running behind a timeout.
	if runErr := e.runner.Run(call, executable, sshExportArguments(*route, metricsOnly), nil, stdout, stderr); runErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			// The daemon is stopping: that is not a failure of the machine, and
			// nothing is logged or recorded of it.
			return Envelope{}, ctx.Err()
		}
		if call.Err() != nil {
			return Envelope{}, e.failed(target.Machine, RemoteErrorTimeout, "")
		}
		code, started := sshFailure(runErr, stdout.Bytes(), stderr.Bytes())
		if !started {
			e.forget()
		}
		if code == "" {
			return Envelope{}, ErrRemoteWarmingUp
		}
		return Envelope{}, e.failed(target.Machine, code, "")
	}
	body := stdout.Bytes()
	envelope, err := DecodeEnvelope(bytes.NewReader(body), metricsOnly, e.now())
	switch {
	case err == nil:
		e.succeeded(target.Machine)
		return envelope, nil
	case !bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("{")):
		return Envelope{}, e.failed(target.Machine, RemoteErrorBadPayload, outputBeforeEnvelope)
	case otherSchema(body):
		return Envelope{}, e.failed(target.Machine, RemoteErrorWBTooOld, "")
	}
	// The refusal names a rule and never a value; the scheduler shows it as
	// bad_payload.
	return Envelope{}, err
}

// sshFailure is the code of a call that ended with runErr, or "" for a remote
// that says it is warming up (which is not a failure), and whether ssh was
// started and ended with a status. The whole rule:
//
//   - not an exit status at all (ssh could not be started, or was killed by a
//     signal): ssh_unavailable;
//   - 255, ssh's own failure: auth_failed when stderr holds one of
//     sshLoginRefusals, else ssh_unavailable (an unknown or unreachable host, a
//     refused or timed-out connection);
//   - 127 or 126, the remote shell found no wb it can run: wb_missing;
//   - 2, wb refused the invocation, which is a wb with no `cockpit export` or
//     without one of its flags: wb_too_old;
//   - 1 with a typed reason on stdout (typedReason): daemon_not_running and
//     export_refused as they are, export_failed as bad_payload, warming_up as "";
//   - anything else (another status, a reason that is not one of the four or is
//     not exactly the typed form): the remote answered, and not as an exporter
//     does: bad_payload.
func sshFailure(runErr error, stdout, stderr []byte) (code string, started bool) {
	var exited interface{ ExitCode() int }
	if !errors.As(runErr, &exited) || exited.ExitCode() < 0 {
		return RemoteErrorSSHUnavailable, false
	}
	switch exited.ExitCode() {
	case exitSSHFailed:
		told := strings.ToLower(string(stderr))
		for _, phrase := range sshLoginRefusals {
			if strings.Contains(told, phrase) {
				return RemoteErrorAuthFailed, true
			}
		}
		return RemoteErrorSSHUnavailable, true
	case exitNotFound, exitNotExecutable:
		return RemoteErrorWBMissing, true
	case exitUsage:
		return RemoteErrorWBTooOld, true
	case exitFindings:
		switch typedReason(stdout) {
		case ErrorDaemonNotRunning:
			return RemoteErrorDaemonNotRunning, true
		case ErrorExportRefused:
			return RemoteErrorExportRefused, true
		case ErrorWarmingUp:
			return "", true
		}
	}
	return RemoteErrorBadPayload, true
}

// typedReason is the reason of the export verb's typed answer, or "" when
// stdout is not exactly one: at most maxFailureBytes, one JSON object with the
// two fields of ExportError and no other, this binary's schema version, and
// nothing after it.
func typedReason(stdout []byte) string {
	if len(stdout) > maxFailureBytes {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.DisallowUnknownFields()
	var typed ExportError
	if err := decoder.Decode(&typed); err != nil || typed.SchemaVersion != ExportSchemaVersion {
		return ""
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ""
	}
	return typed.Error
}

// otherSchema reports whether body is a JSON object that names an export schema
// version, and not this binary's: the two machines' wb are of different
// generations, which is wb_too_old (the older one is updated) rather than
// bad_payload. Only the number is read.
func otherSchema(body []byte) bool {
	var named struct {
		SchemaVersion *int `json:"schema_version"`
	}
	return json.Unmarshal(body, &named) == nil && named.SchemaVersion != nil && *named.SchemaVersion > 0 && *named.SchemaVersion != ExportSchemaVersion
}

// sshTransportCodes are the failures after which another transport may be
// tried: the ones that say nothing of what the remote daemon would answer.
var sshTransportCodes = []string{RemoteErrorSSHUnavailable, RemoteErrorAuthFailed, RemoteErrorTimeout, RemoteErrorWBMissing, RemoteErrorWBTooOld}

// failed is the error of an export of machine that failed with code, and
// writes the daemon's log line for it: the code and, when there is one, cause,
// which is a fixed text of this file. Nothing ssh or the remote wrote is in the
// line: the daemon's log is served by the dashboard's log route, to whoever can
// reach the listener, so it is held to the same rule as the document. The line
// is written when the code differs from the last one logged for the machine.
func (e *SSHExporter) failed(machine, code, cause string) error {
	e.mu.Lock()
	changed := e.logged[machine] != code
	e.logged[machine] = code
	e.mu.Unlock()
	if changed {
		line := code
		if cause != "" {
			line += ": " + cause
		}
		e.logf("cockpit fleet: the ssh export of %s failed (%s)", machine, line)
	}
	return &RemoteError{Code: code, Fallback: slices.Contains(sshTransportCodes, code)}
}

// succeeded forgets the machine's last logged failure, so the next one is
// logged again.
func (e *SSHExporter) succeeded(machine string) {
	e.mu.Lock()
	delete(e.logged, machine)
	e.mu.Unlock()
}
