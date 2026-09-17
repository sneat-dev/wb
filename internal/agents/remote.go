package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/remotessh"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// RemoteArgument selects the private remote entry point. It is handled before
// normal command dispatch on the target machine, so the request can only be a
// validated protocol value read from stdin — never text the remote shell
// parsed.
const RemoteArgument = "--wb-internal-agent-remote"

const remoteSchemaVersion = 1

// Remote operations, one per local command that can address another machine.
const (
	RemoteDispatch = "dispatch"
	RemoteStatus   = "status"
	RemoteAwait    = "await"
	RemoteList     = "list"
	RemoteLogs     = "logs"
	RemoteStop     = "stop"
)

// RemoteTarget is one configured machine WB can dispatch to. It is derived from
// WB's existing session_move target map, so there is one host list rather than
// a second one that can drift from it.
type RemoteTarget struct {
	Machine string
	Host    string
	User    string
	WBPath  string
}

// RemoteRequest is the exact request a local WB sends to a remote WB. The
// remote validates it with the same code that validates local flags, so a
// request can never reach a state a local invocation could not.
type RemoteRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Operation     string `json:"operation"`

	// dispatch
	Mode       string `json:"mode,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Profile    string `json:"profile,omitempty"`
	Task       string `json:"task,omitempty"`
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Base       string `json:"base,omitempty"`
	TimeoutMS  int64  `json:"timeout_ms,omitempty"`

	// status, await, logs, stop
	AgentID       string `json:"agent_id,omitempty"`
	WaitTimeoutMS int64  `json:"wait_timeout_ms,omitempty"`
	Raw           bool   `json:"raw,omitempty"`
	Tail          int    `json:"tail,omitempty"`
}

// RemoteResponse is the exact reply a remote WB sends back. A refusal on the
// remote side travels as Failure rather than as a transport error, so the
// caller can tell "the remote refused" apart from "the remote was unreachable".
type RemoteResponse struct {
	SchemaVersion int     `json:"schema_version"`
	Operation     string  `json:"operation"`
	Result        *Result `json:"result,omitempty"`
	// Results deliberately has no omitempty: an empty inventory must travel as
	// an empty list, not disappear into a null the caller has to special-case.
	Results []Result `json:"results"`
	Logs    string   `json:"logs,omitempty"`
	Failure string   `json:"failure,omitempty"`
}

// Validate refuses a request that is not a well-formed WB agent protocol value.
// It is deliberately independent of the local flag parser: a hand-written
// request must satisfy exactly the same constraints.
func (request RemoteRequest) Validate() error {
	if request.SchemaVersion != remoteSchemaVersion {
		return fmt.Errorf("remote request schema_version %d unsupported; want %d", request.SchemaVersion, remoteSchemaVersion)
	}
	switch request.Operation {
	case RemoteDispatch:
		if request.Mode != ModeNew && request.Mode != ModeExisting {
			return fmt.Errorf("remote dispatch mode %q must be %q or %q", request.Mode, ModeNew, ModeExisting)
		}
		if strings.TrimSpace(request.Worktree) == "" {
			return fmt.Errorf("remote dispatch requires a worktree name")
		}
		if strings.TrimSpace(request.Task) == "" {
			return fmt.Errorf("remote dispatch requires a task")
		}
		if strings.TrimSpace(request.Profile) == "" {
			return fmt.Errorf("remote dispatch requires a profile")
		}
		if request.TimeoutMS <= 0 {
			return fmt.Errorf("remote dispatch requires a positive timeout")
		}
	case RemoteStatus, RemoteAwait, RemoteLogs, RemoteStop:
		if err := validateAgentID(request.AgentID); err != nil {
			return err
		}
		if request.Operation == RemoteAwait && request.WaitTimeoutMS < 0 {
			return fmt.Errorf("remote await requires a non-negative wait timeout")
		}
		if request.Operation == RemoteLogs && request.Tail < 0 {
			return fmt.Errorf("remote logs requires a non-negative tail")
		}
	case RemoteList:
	default:
		return fmt.Errorf("remote operation %q is unsupported", request.Operation)
	}
	return nil
}

// CallRemote performs one remote WB agent operation over SSH and returns the
// decoded reply. A non-empty reply Failure is returned as an error: the remote
// ran and refused, which is a finding rather than a transport failure.
func CallRemote(ctx context.Context, target RemoteTarget, request RemoteRequest, deps RemoteDeps) (RemoteResponse, error) {
	if err := target.Validate(); err != nil {
		return RemoteResponse{}, err
	}
	if err := request.Validate(); err != nil {
		return RemoteResponse{}, err
	}
	if deps.Runner == nil {
		deps.Runner = remotessh.ExecRunner{}
	}
	if deps.LookPath == nil {
		return RemoteResponse{}, fmt.Errorf("resolve ssh executable: executable lookup is unavailable")
	}
	executable, err := remotessh.Resolve(deps.LookPath)
	if err != nil {
		return RemoteResponse{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return RemoteResponse{}, fmt.Errorf("encode remote request: %w", err)
	}
	remoteWB := target.WBPath
	if remoteWB == "" {
		remoteWB = remotessh.DefaultWBCommand
	}
	// Every element here is a constant: the request travels on stdin. The
	// private argument is deliberately first, because WB's private entry points
	// are selected by argv position before any flag parsing, and a leading
	// --non-interactive would make cobra claim the invocation instead.
	args := remotessh.Build(target.Host, target.User, []string{remoteWB, RemoteArgument})

	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = DefaultRemoteTimeout
	}
	if request.Operation == RemoteAwait {
		// The remote blocks for the caller's bound; the transport must outlast
		// it, or a slow-but-healthy worker would look like a dropped connection.
		wait := time.Duration(request.WaitTimeoutMS) * time.Millisecond
		if wait > 0 {
			timeout = wait + remoteAwaitSlack
		}
	}
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := remotessh.NewLimitedBuffer(maxRemoteStdoutBytes)
	stderr := remotessh.NewLimitedBuffer(maxRemoteStderrBytes)
	if err := deps.Runner.Run(callContext, executable, args, payload, stdout, stderr); err != nil {
		if contextErr := callContext.Err(); contextErr != nil && ctx.Err() == nil {
			return RemoteResponse{}, fmt.Errorf("%s %s on %s timed out after %s: %w", "wb agent", request.Operation, target.Machine, timeout, contextErr)
		}
		diagnostic := remotessh.SanitizeDiagnostic(stderr.Bytes(), stderr.Exceeded())
		if diagnostic == "" {
			return RemoteResponse{}, fmt.Errorf("wb agent %s on %s: %w", request.Operation, target.Machine, err)
		}
		return RemoteResponse{}, fmt.Errorf("wb agent %s on %s: %w: %s", request.Operation, target.Machine, err, diagnostic)
	}
	if stdout.Exceeded() {
		return RemoteResponse{}, fmt.Errorf("response from %s exceeds %d bytes", target.Machine, maxRemoteStdoutBytes)
	}
	response, err := decodeRemoteResponse(stdout.Bytes())
	if err != nil {
		return RemoteResponse{}, fmt.Errorf("decode response from %s: %w", target.Machine, err)
	}
	if response.Operation != request.Operation {
		return RemoteResponse{}, fmt.Errorf("response from %s answered %q, want %q", target.Machine, response.Operation, request.Operation)
	}
	if response.Failure != "" {
		return response, fmt.Errorf("%s: %s", target.Machine, response.Failure)
	}
	return response, nil
}

func decodeRemoteResponse(raw []byte) (RemoteResponse, error) {
	var response RemoteResponse
	if len(raw) == 0 {
		return response, fmt.Errorf("remote WB wrote nothing; is the wb on that machine new enough to support wb agent?")
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return response, fmt.Errorf("remote WB did not return an agent protocol document: %w", err)
	}
	if response.SchemaVersion != remoteSchemaVersion {
		return response, fmt.Errorf("remote response schema_version %d unsupported; want %d (upgrade wb on the target)", response.SchemaVersion, remoteSchemaVersion)
	}
	return response, nil
}

// Validate refuses a target whose configured address could be reinterpreted by
// OpenSSH. The values come from WB configuration, but the remote command line
// is the one place a configuration typo could become a remote shell command.
func (target RemoteTarget) Validate() error {
	if strings.TrimSpace(target.Machine) == "" {
		return fmt.Errorf("remote target requires a machine name")
	}
	if strings.TrimSpace(target.Host) == "" {
		return fmt.Errorf("remote target %s requires an ssh host", target.Machine)
	}
	if target.WBPath != "" {
		if !path.IsAbs(target.WBPath) || path.Clean(target.WBPath) != target.WBPath {
			return fmt.Errorf("remote target %s wb_path %q must be a clean absolute path", target.Machine, target.WBPath)
		}
	}
	// Reuse WB's existing SSH address validation rather than restating it: the
	// rule that keeps a host or user from becoming shell text must be one rule.
	return sessionmove.SSHConfig{Host: target.Host, User: target.User, WBPath: target.WBPath}.Validate()
}

// LoadRemoteTargets reads the configured machines WB may dispatch to.
//
// It reuses WB's session_move target map on purpose. A machine and its courier
// address are one fact about the fleet; a second list would be a second place
// to be wrong, and the operator would have to keep them in step by hand.
func LoadRemoteTargets(configPath string) (map[string]RemoteTarget, error) {
	config, err := sessionmove.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]RemoteTarget, len(config.Targets))
	for machine, target := range config.Targets {
		if target.SSH == nil {
			continue
		}
		targets[machine] = RemoteTarget{
			Machine: machine,
			Host:    target.SSH.Host,
			User:    target.SSH.User,
			WBPath:  target.SSH.WBPath,
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no ssh-addressable machine is configured in %s; add session_move.targets.<machine>.ssh with host and user", configPath)
	}
	return targets, nil
}

// ResolveRemoteTarget looks up one configured machine, naming the alternatives
// when it is unknown so a typo is one command away from being fixed.
func ResolveRemoteTarget(configPath, machine string) (RemoteTarget, error) {
	wanted := strings.TrimSpace(machine)
	if wanted == "" {
		return RemoteTarget{}, requestErrorf("--to requires a configured machine name")
	}
	targets, err := LoadRemoteTargets(configPath)
	if err != nil {
		return RemoteTarget{}, err
	}
	target, ok := targets[wanted]
	if !ok {
		names := make([]string, 0, len(targets))
		for name := range targets {
			names = append(names, name)
		}
		sort.Strings(names)
		return RemoteTarget{}, requestErrorf("unknown machine %q; configured machines: %s", wanted, strings.Join(names, ", "))
	}
	if err := target.Validate(); err != nil {
		return RemoteTarget{}, err
	}
	return target, nil
}

// SplitAgentRef accepts either a bare agent run ID or the machine-qualified
// form a remote dispatch prints, so a supervisor can pass back exactly what it
// was given. An agent ID contains only the prefix and hexadecimal, so the colon
// is unambiguous.
func SplitAgentRef(reference string) (machine, agentID string, err error) {
	trimmed := strings.TrimSpace(reference)
	if machine, agentID, found := strings.Cut(trimmed, ":"); found {
		if strings.TrimSpace(machine) == "" || strings.TrimSpace(agentID) == "" {
			return "", "", requestErrorf("agent reference %q must be <machine>:<agent-id>", reference)
		}
		return strings.TrimSpace(machine), strings.TrimSpace(agentID), nil
	}
	return "", trimmed, nil
}

// StripAgentID returns the bare run ID from a possibly machine-qualified
// reference.
func StripAgentID(reference string) string {
	_, agentID, err := SplitAgentRef(reference)
	if err != nil {
		return strings.TrimSpace(reference)
	}
	return agentID
}

// RemoteDeps are the seams the local side needs to reach another machine.
type RemoteDeps struct {
	// LookPath resolves the local ssh executable.
	LookPath func(string) (string, error)
	// Runner executes it. Replacement is for tests only.
	Runner remotessh.Runner
	// Timeout bounds one remote call when the operation has no bound of its own.
	Timeout time.Duration
}

// DefaultRemoteDeps returns the production seams.
func DefaultRemoteDeps() RemoteDeps {
	return RemoteDeps{LookPath: execLookPath, Runner: remotessh.ExecRunner{}, Timeout: DefaultRemoteTimeout}
}

// Remote bounds and limits. A remote reply is a small protocol document, so the
// caps are generous for it and deliberately not generous for a transcript.
const (
	DefaultRemoteTimeout = 10 * time.Minute
	remoteAwaitSlack     = 30 * time.Second
	maxRemoteStdoutBytes = 4 << 20
	maxRemoteStderrBytes = 64 << 10
	// MaxRemoteLogBytes bounds a remote transcript fetch. `wb agent logs --raw`
	// over a link is the one operation that could pull an entire run into a
	// caller's context, so it is refused past this rather than truncated.
	MaxRemoteLogBytes = 2 << 20
)
