package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DefaultTimeout bounds every [Client] call that does not override it with
// [WithTimeout]. herdr's own socket calls are local and fast; ten seconds
// is generous headroom for load, not an expected steady-state latency.
const DefaultTimeout = 10 * time.Second

// Client is a bounded, injectable-exec-seam adapter over the herdr CLI.
// Every method shells out to the resolved herdr binary as argv, never
// through a shell, under a per-call timeout derived from the context
// passed in and the Client's configured timeout, whichever is shorter.
type Client struct {
	binary      string
	runner      Runner
	timeout     time.Duration
	socketPath  string
	sessionName string
}

// ClientOption configures a [Client] constructed by [NewClient].
type ClientOption func(*Client)

// WithRunner overrides the [Runner] a [Client] uses to execute herdr.
// Production code never needs this; tests use it to inject a fake so no
// test call reaches a real herdr socket.
func WithRunner(runner Runner) ClientOption {
	return func(c *Client) {
		if runner != nil {
			c.runner = runner
		}
	}
}

// WithTimeout overrides [DefaultTimeout] for every call the resulting
// Client makes.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithSocketPath makes the herdr server this Client talks to explicit: it
// runs every call with HERDR_SOCKET_PATH set to path, overriding whatever
// the ambient environment has (see [buildEnv]). herdr's own agent and pane
// IDs are scoped to one server (herdr --skill: "IDs and live agent names
// are scoped to one server"), so a caller that must reach a specific
// session's herdr — such as a daemon coordinating several — should always
// configure this explicitly from that session's own [Identity.SocketPath]
// rather than relying on the ambient HERDR_SOCKET_PATH the daemon process
// itself happens to have (which may belong to a different session, or none).
func WithSocketPath(path string) ClientOption {
	return func(c *Client) {
		c.socketPath = path
	}
}

// WithSessionName targets a specific named persistent herdr session by
// prepending herdr's own global `--session <name>` flag to every call
// (see `herdr --help`: "herdr --session <name> [options]" and
// "--session <name>  Use or create a named persistent session"). Combine
// with [WithSocketPath] only when the named session's server is not the
// one the ambient HERDR_SOCKET_PATH already resolves to.
func WithSessionName(name string) ClientOption {
	return func(c *Client) {
		c.sessionName = name
	}
}

// NewClient resolves the herdr binary via [ResolveBinary] and returns a
// [Client] for it. lookup must not be nil; pass [OSLookupEnv] in
// production and a map-backed fake in tests.
func NewClient(lookup EnvLookup, opts ...ClientOption) (*Client, error) {
	binary, err := ResolveBinary(lookup)
	if err != nil {
		return nil, err
	}
	client := &Client{binary: binary, runner: execRunner{}, timeout: DefaultTimeout}
	for _, opt := range opts {
		opt(client)
	}
	return client, nil
}

// Binary reports the resolved herdr executable path this Client calls.
func (c *Client) Binary() string {
	if c == nil {
		return ""
	}
	return c.binary
}

// SocketPath reports the socket path configured via [WithSocketPath], or ""
// when this Client relies on the ambient HERDR_SOCKET_PATH.
func (c *Client) SocketPath() string {
	if c == nil {
		return ""
	}
	return c.socketPath
}

// SessionName reports the named session configured via [WithSessionName],
// or "" when this Client does not target one.
func (c *Client) SessionName() string {
	if c == nil {
		return ""
	}
	return c.sessionName
}

// targeted reports whether this Client was configured with [WithSocketPath]
// or [WithSessionName]. A targeted Client's subprocess environment is
// scrubbed of ambient identity (see [buildEnv]), and [Client.PaneCurrent]
// refuses on one, because "current" is ambiguous once a caller has said
// which server or session it means instead of relying on its own ambient
// one.
func (c *Client) targeted() bool {
	return c != nil && (c.socketPath != "" || c.sessionName != "")
}

// herdrEnvelope is the JSON shape every herdr socket-API command returns on
// stdout, success or failure: {"id":"cli:...","result":{...}} on exit 0,
// or {"id":"cli:...","error":{"code":"...","message":"..."}} — observed on
// both exit 0 (not seen live) and exit 1 (the documented case). --version
// is the one exception, handled separately in version.go via rawCall.
type herdrEnvelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *herdrErrorBody `json:"error"`
}

// herdrErrorBody is herdr's own structured error, as CLI server errors
// report it on stderr with exit status 1 (see herdr --skill: "CLI server
// errors are JSON on stderr with exit status 1").
type herdrErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// knownTargetErrorCodes are the herdr error codes observed, live and
// read-only, for a target that does not exist: "agent_not_found" and
// "pane_not_found" (herdr agent get / pane get against a made-up target,
// 2026-09-19). "workspace_not_found" and "tab_not_found" are included by
// the same naming convention though not independently observed.
var knownTargetErrorCodes = map[string]bool{
	"agent_not_found":     true,
	"pane_not_found":      true,
	"workspace_not_found": true,
	"tab_not_found":       true,
}

// knownUnreachableErrorCodes are the herdr error codes observed for a
// socket that cannot be reached: "server_not_running" (HERDR_SOCKET_PATH
// pointed at a nonexistent socket, 2026-09-19).
var knownUnreachableErrorCodes = map[string]bool{
	"server_not_running": true,
}

// call runs a herdr socket-API subcommand and returns its decoded
// `result` field. args must not include the binary name itself.
func (c *Client) call(ctx context.Context, args ...string) (json.RawMessage, error) {
	stdout, err := c.rawCall(ctx, args...)
	if err != nil {
		return nil, err
	}
	var envelope herdrEnvelope
	if unmarshalErr := json.Unmarshal(stdout, &envelope); unmarshalErr != nil {
		return nil, fmt.Errorf("%w: herdr %s did not return its JSON envelope: %w (output: %s)",
			ErrUnparseableOutput, strings.Join(args, " "), unmarshalErr, truncate(string(stdout), 256))
	}
	if envelope.Error != nil {
		return nil, c.mapHerdrError(args, *envelope.Error)
	}
	return envelope.Result, nil
}

// rawCall runs a herdr subcommand and returns its raw stdout, mapping a
// nonzero exit or a transport failure to one of this package's sentinel
// errors. Only [Client.Version] calls it directly, for the one herdr
// command (--version) that does not emit the JSON envelope.
func (c *Client) rawCall(ctx context.Context, args ...string) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil herdr client", ErrBinaryNotFound)
	}
	if c.runner == nil {
		return nil, fmt.Errorf("%w: herdr client has no runner configured", ErrBinaryNotFound)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.effectiveTimeout())
	defer cancel()

	fullArgs := withSessionFlag(c.sessionName, args)
	env := buildEnv(osEnviron(), c.targeted(), c.socketPath)
	stdout, stderr, runErr := c.runner.Run(callCtx, c.binary, fullArgs, env)
	if runErr == nil {
		return stdout, nil
	}
	if isExecNotFound(runErr) {
		return nil, fmt.Errorf("%w: %w", ErrBinaryNotFound, runErr)
	}
	if callCtx.Err() != nil {
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args, " "), callCtx.Err())
	}
	// A nonzero exit that is not a context failure: herdr documents this as
	// either a structured JSON error on stderr (exit 1) or a plain usage
	// message (exit 2). Try the structured shape first.
	var envelope herdrEnvelope
	if unmarshalErr := json.Unmarshal(stderr, &envelope); unmarshalErr == nil && envelope.Error != nil {
		return nil, c.mapHerdrError(args, *envelope.Error)
	}
	return nil, fmt.Errorf("%w: herdr %s: %s: %w",
		ErrCommandFailed, strings.Join(args, " "), truncate(strings.TrimSpace(string(stderr)), 512), runErr)
}

func (c *Client) mapHerdrError(args []string, body herdrErrorBody) error {
	command := strings.Join(args, " ")
	switch {
	case knownTargetErrorCodes[body.Code]:
		return fmt.Errorf("%w: herdr %s: %s (%s)", ErrUnknownTarget, command, body.Message, body.Code)
	case knownUnreachableErrorCodes[body.Code]:
		return fmt.Errorf("%w: herdr %s: %s (%s)", ErrServerUnreachable, command, body.Message, body.Code)
	default:
		return fmt.Errorf("%w: herdr %s: %s (%s)", ErrCommandFailed, command, body.Message, body.Code)
	}
}

func (c *Client) effectiveTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return DefaultTimeout
}

// Version calls `herdr --version` and parses its plain-text reply. Unlike
// every other Client method, this command does not use the JSON envelope.
func (c *Client) Version(ctx context.Context) (Version, error) {
	stdout, err := c.rawCall(ctx, "--version")
	if err != nil {
		return Version{}, err
	}
	version, parseErr := ParseVersion(string(stdout))
	if parseErr != nil {
		return Version{}, parseErr
	}
	return version, nil
}

// EnsureMinimumVersion calls [Client.Version] and returns an error wrapping
// [ErrUnsupportedVersion] when the resolved herdr binary is older than
// [MinimumVersion].
func (c *Client) EnsureMinimumVersion(ctx context.Context) error {
	current, err := c.Version(ctx)
	if err != nil {
		return err
	}
	if current.Less(minimumVersion) {
		return fmt.Errorf("%w: herdr %s is older than the minimum supported %s", ErrUnsupportedVersion, current, minimumVersion)
	}
	return nil
}
