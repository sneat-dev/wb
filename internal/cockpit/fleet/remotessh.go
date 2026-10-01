package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
//     X11 and no port, and gives up connecting after sshConnectSeconds. Host key
//     checking is the user's ssh configuration's and is never switched off.
//   - One call lasts at most sshTotalTimeout: the runner kills the process (its
//     whole group, with remotessh.GroupRunner) when the context ends, and Export
//     returns only when the runner has, so a process is always waited for.
//   - stdout is read through DecodeEnvelope, as every transport's bytes are, and
//     never more than MaxEnvelopeBytes of it is held.
//   - A failure is a code. stderr is held to maxSSHDiagnosticBytes, rendered by
//     remotessh.SanitizeDiagnostic and written to the daemon's own log only: it
//     never reaches the document, an error value or any reader.
//
// A daemon run by launchd or systemd may have no SSH agent socket. Nothing here
// looks for one: the login then fails and is shown as auth_failed.

const (
	// sshConnectSeconds bounds the connection, and sshTotalTimeout the whole call.
	sshConnectSeconds = 5
	sshTotalTimeout   = 15 * time.Second
	// maxSSHDiagnosticBytes bounds the stderr that is held of one call.
	maxSSHDiagnosticBytes = 4 * remotessh.MaxDiagnosticBytes
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

// sshExportArguments is the whole argument vector of one export: constants and
// the route, and nothing else.
func sshExportArguments(route SSHRoute, metricsOnly bool) []string {
	remote := []string{route.command(), "cockpit", "export", "--format", "json"}
	if metricsOnly {
		remote = append(remote, "--metrics-only")
	}
	return remotessh.BuildWith(remotessh.Options{ConnectTimeoutSeconds: sshConnectSeconds, NoForwarding: true}, route.Host, route.User, remote)
}

// SSHExporter is the SSH RemoteExporter.
type SSHExporter struct {
	lookPath func(string) (string, error)
	runner   remotessh.Runner
	timeout  time.Duration
	now      func() time.Time
	logf     func(string, ...any)

	mu sync.Mutex
	// executable is the local ssh, resolved once: a lookup that fails is made
	// again at the next export, and one that succeeded is kept.
	executable string
	// logged is, by machine, the last line written of its failure, so a machine
	// that keeps failing the same way is logged once.
	logged map[string]string
}

// NewSSHExporter is the SSH exporter over lookPath (which finds the local ssh)
// and runner, on the clock now (nil means time.Now), logging to logf (nil means
// nowhere).
func NewSSHExporter(lookPath func(string) (string, error), runner remotessh.Runner, now func() time.Time, logf func(string, ...any)) *SSHExporter {
	return newSSHExporter(lookPath, runner, sshTotalTimeout, now, logf)
}

// newSSHExporter is the exporter with the given total timeout.
func newSSHExporter(lookPath func(string) (string, error), runner remotessh.Runner, timeout time.Duration, now func() time.Time, logf func(string, ...any)) *SSHExporter {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &SSHExporter{lookPath: lookPath, runner: runner, timeout: timeout, now: now, logf: logf, logged: map[string]string{}}
}

// resolve is the local ssh executable, looked up until it is found and then
// kept.
func (e *SSHExporter) resolve() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.executable != "" {
		return e.executable, nil
	}
	executable, err := remotessh.Resolve(e.lookPath)
	if err != nil {
		return "", err
	}
	e.executable = executable
	return executable, nil
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
		return Envelope{}, e.failed(target.Machine, RemoteErrorSSHUnavailable, nil)
	}
	call, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	stdout := remotessh.NewLimitedBuffer(MaxEnvelopeBytes + 1)
	stderr := remotessh.NewLimitedBuffer(maxSSHDiagnosticBytes)
	// Run returns when the process has ended and been waited for; the call is
	// never left running behind a timeout.
	if runErr := e.runner.Run(call, executable, sshExportArguments(*route, metricsOnly), nil, stdout, stderr); runErr != nil {
		if call.Err() != nil {
			return Envelope{}, e.failed(target.Machine, RemoteErrorTimeout, stderr)
		}
		code := sshFailure(runErr, stdout.Bytes(), stderr.Bytes())
		if code == "" {
			return Envelope{}, ErrRemoteWarmingUp
		}
		return Envelope{}, e.failed(target.Machine, code, stderr)
	}
	body := stdout.Bytes()
	envelope, err := DecodeEnvelope(bytes.NewReader(body), metricsOnly, e.now())
	if err != nil {
		if otherSchema(body) {
			return Envelope{}, e.failed(target.Machine, RemoteErrorWBTooOld, nil)
		}
		// The refusal names a rule and never a value; the scheduler shows it as
		// bad_payload.
		return Envelope{}, err
	}
	e.succeeded(target.Machine)
	return envelope, nil
}

// sshFailure is the code of a call that ended with runErr, or "" for a remote
// that says it is warming up (which is not a failure). The whole rule:
//
//   - not an exit status at all (ssh could not be started, or was killed by a
//     signal): ssh_unavailable;
//   - 255, ssh's own failure: auth_failed when stderr holds one of
//     sshLoginRefusals, else ssh_unavailable (an unknown or unreachable host, a
//     refused or timed-out connection);
//   - 127 or 126, the remote shell found no wb it can run: wb_missing;
//   - 2, wb refused the invocation, which is a wb with no `cockpit export` or
//     without one of its flags: wb_too_old;
//   - 1 with a typed reason on stdout: daemon_not_running and export_refused as
//     they are, export_failed as bad_payload, warming_up as "";
//   - anything else (another status, a reason that is not one of the four): the
//     remote answered, and not as an exporter does: bad_payload.
func sshFailure(runErr error, stdout, stderr []byte) string {
	var exited interface{ ExitCode() int }
	if !errors.As(runErr, &exited) || exited.ExitCode() < 0 {
		return RemoteErrorSSHUnavailable
	}
	switch exited.ExitCode() {
	case exitSSHFailed:
		said := strings.ToLower(string(stderr))
		for _, phrase := range sshLoginRefusals {
			if strings.Contains(said, phrase) {
				return RemoteErrorAuthFailed
			}
		}
		return RemoteErrorSSHUnavailable
	case exitNotFound, exitNotExecutable:
		return RemoteErrorWBMissing
	case exitUsage:
		return RemoteErrorWBTooOld
	case exitFindings:
		var typed ExportError
		if len(stdout) <= maxFailureBytes {
			_ = json.Unmarshal(stdout, &typed)
		}
		switch typed.Error {
		case ErrorDaemonNotRunning:
			return RemoteErrorDaemonNotRunning
		case ErrorExportRefused:
			return RemoteErrorExportRefused
		case ErrorWarmingUp:
			return ""
		}
	}
	return RemoteErrorBadPayload
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
// writes the daemon's log line for it: the code, and what stderr held, as
// remotessh.SanitizeDiagnostic renders it (one line, no control character, at
// most remotessh.MaxDiagnosticBytes). The line is written when it differs from
// the last one written for the machine.
func (e *SSHExporter) failed(machine, code string, stderr *remotessh.LimitedBuffer) error {
	line := code
	if stderr != nil {
		if diagnostic := remotessh.SanitizeDiagnostic(stderr.Bytes(), stderr.Exceeded()); diagnostic != "" {
			line += ": " + diagnostic
		}
	}
	e.mu.Lock()
	changed := e.logged[machine] != line
	e.logged[machine] = line
	e.mu.Unlock()
	if changed {
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
