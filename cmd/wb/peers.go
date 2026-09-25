package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// peersDeps are the seams tests replace: the daemon dependencies an admin
// call starts through, the config path, and the clock a relative age is
// rendered against.
type peersDeps struct {
	daemonDeps    daemonDependencies
	configPath    func() string
	adminClient   func(context.Context, daemonDependencies, string) (*peerAdminClient, error)
	listenAddress func(daemonDependencies, string) (string, error)
	httpClient    *http.Client
	now           func() time.Time
}

// isHubConfigured reports whether this machine has a hub: section at all,
// without starting anything: the owner-token RPC needs a running daemon to
// reach, but "is there a hub" is answerable by reading the same wb.yaml the
// daemon itself reads.
func isHubConfigured(deps peersDeps) (bool, error) {
	_, found, err := hubconfig.Load(deps.configPath())
	return found, err
}

// requireHubConfigured refuses a hub-only admin verb (invite, or block/
// unblock/disconnect for a downstream peer, once the upstream special case
// has already been ruled out) as a usage error when this machine has no hub
// section at all, rather than starting a managed daemon only to learn that
// from a 503 over the owner-token RPC.
func requireHubConfigured(deps peersDeps) error {
	found, err := isHubConfigured(deps)
	if err != nil {
		return err
	}
	if !found {
		return &exitError{code: exitUsage, message: "no hub is configured on this machine; `wb peers invite`, `block`, `unblock` and `disconnect` (for a downstream peer) require a self-hosted hub — see `hub:` in wb.yaml, or run `wb peers join` to become a peer of one instead"}
	}
	return nil
}

func defaultPeersDeps() peersDeps {
	return peersDeps{
		daemonDeps:    defaultDaemonDependencies(),
		configPath:    wbconfig.DefaultPath,
		adminClient:   newPeerAdminClient,
		listenAddress: daemonListenAddress,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
		now:           func() time.Time { return time.Now().UTC() },
	}
}

func newPeersCmd(inv *invocation) *cobra.Command {
	command := &cobra.Command{
		Use:   "peers",
		Short: "Admit, list and control the daemons this hub or laptop connects to",
		Long: `wb peers manages peer-connectivity: an always-on hub admits laptops as
peers, and a laptop dials out to its hub.

  wb peers invite <name>              mint a one-time token for a new peer
  wb peers join <hub-url>             join a hub as its peer, from stdin/--token-file
  wb peers list                       every peer (or the upstream), with status
  wb peers get <peer>                 one peer's full record
  wb peers block <peer>               refuse a peer's sessions and calls
  wb peers unblock <peer>             allow a peer back in
  wb peers disconnect <peer>          close a peer's live session only`,
	}
	command.AddCommand(newPeersInviteCmd(inv))
	command.AddCommand(newPeersJoinCmd(inv))
	command.AddCommand(newPeersListCmd(inv))
	command.AddCommand(newPeersGetCmd(inv))
	command.AddCommand(newPeersBlockCmd(inv))
	command.AddCommand(newPeersUnblockCmd(inv))
	command.AddCommand(newPeersDisconnectCmd(inv))
	return command
}

// ---------------------------------------------------------------- invite

func newPeersInviteCmd(inv *invocation) *cobra.Command {
	var rotate, jsonOut bool
	var tokenFile string
	command := &cobra.Command{
		Use:   "invite <name>",
		Short: "Mint a one-time peer credential on this hub",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersInvite(command.Context(), defaultPeersDeps(), inv.projectsRoot, args[0], rotate, tokenFile, jsonOut, command.OutOrStdout())
		},
	}
	command.Flags().BoolVar(&rotate, "rotate", false, "reissue the credential for an existing peer name")
	command.Flags().StringVar(&tokenFile, "token-file", "", "write the one-time token here (mode 0600) instead of printing it")
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers invite laptop vm admit token credential mint one-time hub connect block")
	return command
}

type peersInviteResult struct {
	PeerID    string    `json:"peer_id"`
	Name      string    `json:"name"`
	Token     string    `json:"token,omitempty"`
	TokenFile string    `json:"token_file,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Rotated   bool      `json:"rotated"`
}

func runPeersInvite(ctx context.Context, deps peersDeps, projectsRoot, name string, rotate bool, tokenFile string, jsonOut bool, out io.Writer) error {
	if err := requireHubConfigured(deps); err != nil {
		return err
	}
	var absoluteTokenFile string
	if strings.TrimSpace(tokenFile) != "" {
		var err error
		absoluteTokenFile, err = filepath.Abs(tokenFile)
		if err != nil {
			return fmt.Errorf("resolve token file: %w", err)
		}
		// Refused before minting anything: an operator's typo pointing at an
		// existing file (or, on the second run of a script, the same path
		// twice) must not either overwrite that file or discover the
		// overwrite refusal only after the hub has already minted (and
		// revoked, on a rotate) a fresh credential it can no longer show.
		if err := refuseExistingTokenFile(absoluteTokenFile); err != nil {
			return err
		}
	}
	client, err := deps.adminClient(ctx, deps.daemonDeps, projectsRoot)
	if err != nil {
		return err
	}
	var response peerInviteResponse
	if err := client.call(ctx, peersRPCPrefix+"invite", peerInviteRequest{Name: name, Rotate: rotate}, &response); err != nil {
		return err
	}
	result := peersInviteResult{PeerID: response.PeerID, Name: response.Name, Token: response.Token, CreatedAt: response.CreatedAt, Rotated: response.Rotated}
	if absoluteTokenFile != "" {
		if writeErr := writeOneTimeToken(absoluteTokenFile, response.Token); writeErr != nil {
			// The token is already minted, and the hub never returns it
			// again. Printing it now — with a loud warning — is the only
			// way not to lose the operator's only copy; failing outright
			// here would strand a live, un-recorded credential. The write
			// failure is still real, though: this returns a findings-level
			// (exit 1) error rather than exit 0, and in JSON mode it emits a
			// normal JSON result (with the token still in it, since no file
			// holds it) rather than the loud text warning, so a scripted
			// caller parsing stdout as JSON never has to also understand a
			// plain-text failure mode.
			if jsonOut {
				result.TokenFile = ""
				if err := json.NewEncoder(out).Encode(result); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(out, "wb: could not write token file %s: %v\n", absoluteTokenFile, writeErr); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(out, "Token (copy this now; it cannot be shown again):"); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(out, response.Token); err != nil {
					return err
				}
			}
			return &exitError{code: exitFindings, message: fmt.Sprintf("could not write token file %s: %v", absoluteTokenFile, writeErr)}
		}
		result.Token = ""
		result.TokenFile = absoluteTokenFile
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	verb := "Invited"
	if result.Rotated {
		verb = "Rotated"
	}
	if _, err := fmt.Fprintf(out, "%s %s (%s)\n", verb, result.Name, result.PeerID); err != nil {
		return err
	}
	if result.TokenFile != "" {
		_, err = fmt.Fprintf(out, "Token written to %s (mode 0600). It cannot be shown again.\n", result.TokenFile)
		return err
	}
	_, err = fmt.Fprintf(out, "Token (copy this now; it cannot be shown again):\n%s\n", result.Token)
	return err
}

// refuseExistingTokenFile is invite's pre-mint check: an early, friendly
// refusal before a token is minted. The actual safety guarantee is
// writeOneTimeToken's O_CREATE|O_EXCL open, which refuses the same path
// atomically (including a symlink placed there after this check ran: EEXIST
// applies to a symlink target too, so the open never follows it).
func refuseExistingTokenFile(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("token file %s already exists; refusing to overwrite it", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check token file %s: %w", path, err)
	}
	return nil
}

// writeOneTimeToken creates path exclusively (O_CREATE|O_EXCL, mode 0600):
// the open itself fails with EEXIST if anything — a plain file or a
// symlink — already sits at path, which is the real, race-free guarantee
// behind refuseExistingTokenFile's earlier, friendlier check.
func writeOneTimeToken(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create token file: %w", err)
	}
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write token file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close token file: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- block / unblock / disconnect

func newPeersBlockCmd(inv *invocation) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "block <peer>",
		Short: "Refuse a peer's sessions and HTTP calls, keeping its history",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersTrustChange(command.Context(), defaultPeersDeps(), inv.projectsRoot, peersRPCPrefix+"block", "Blocked", args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers block refuse deny cut off laptop vm hub connect")
	return command
}

func newPeersUnblockCmd(inv *invocation) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "unblock <peer>",
		Short: "Allow a blocked peer's sessions again",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersTrustChange(command.Context(), defaultPeersDeps(), inv.projectsRoot, peersRPCPrefix+"unblock", "Unblocked", args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers unblock allow readmit laptop vm hub connect block")
	return command
}

func runPeersTrustChange(ctx context.Context, deps peersDeps, projectsRoot, path, verb, peer string, jsonOut bool, out io.Writer) error {
	if handled, err := runPeersUpstreamTrustChange(deps, projectsRoot, verb, peer, jsonOut, out); handled {
		return err
	}
	if err := requireHubConfigured(deps); err != nil {
		return err
	}
	client, err := deps.adminClient(ctx, deps.daemonDeps, projectsRoot)
	if err != nil {
		return err
	}
	var response peerTrustResponse
	if err := client.call(ctx, path, peerNameOrIDRequest{Peer: peer}, &response); err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(response)
	}
	_, err = fmt.Fprintf(out, "%s %s (%s): trust=%s reset_pending=%t\n", verb, response.Name, response.PeerID, response.Trust, response.ResetPending)
	return err
}

func newPeersDisconnectCmd(inv *invocation) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "disconnect <peer>",
		Short: "Close a peer's live session only; it stays trusted and reconnects itself",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersDisconnect(command.Context(), defaultPeersDeps(), inv.projectsRoot, args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers disconnect close session live laptop vm hub connect block")
	return command
}

func runPeersDisconnect(ctx context.Context, deps peersDeps, projectsRoot, peer string, jsonOut bool, out io.Writer) error {
	if err := requireHubConfigured(deps); err != nil {
		return err
	}
	client, err := deps.adminClient(ctx, deps.daemonDeps, projectsRoot)
	if err != nil {
		return err
	}
	var response peerDisconnectResponse
	if err := client.call(ctx, peersRPCPrefix+"disconnect", peerNameOrIDRequest{Peer: peer}, &response); err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(response)
	}
	_, err = fmt.Fprintf(out, "%s (%s): %s\n", response.Name, response.PeerID, response.Message)
	return err
}

// ---------------------------------------------------------------- list / get

func newPeersListCmd(inv *invocation) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List every peer, plus the upstream hub when this machine has joined one",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runPeersList(command.Context(), defaultPeersDeps(), inv.projectsRoot, jsonOut, command.OutOrStdout(), command.ErrOrStderr())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers list laptop vm hub connect block status connected offline blocked")
	return command
}

func newPeersGetCmd(inv *invocation) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "get <peer>",
		Short: "Show one peer's full record",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersGet(command.Context(), defaultPeersDeps(), inv.projectsRoot, args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers get show detail laptop vm hub connect block")
	return command
}

func runPeersList(ctx context.Context, deps peersDeps, projectsRoot string, jsonOut bool, out, errOut io.Writer) error {
	list, downstreamErr := readPeersList(ctx, deps, projectsRoot)
	rows := append([]peers.Record(nil), list.Peers...)
	upstream, upstreamFound, err := resolveUpstreamRow(deps, projectsRoot)
	if err != nil {
		return err
	}
	if upstreamFound {
		rows = append(rows, upstream)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	// A downstream read failure never blocks rendering what is known (the
	// upstream row, if any): it is reported, and — only when a hub is
	// actually configured on this machine, so downstream data is expected to
	// exist — escalated to a finding, rather than silently rendering "no
	// peers" as if a self-hosted hub simply had none.
	var finding error
	if downstreamErr != nil {
		_, _ = fmt.Fprintln(errOut, "wb: downstream peers unavailable:", downstreamErr)
		hubConfigured, hubErr := isHubConfigured(deps)
		if hubErr != nil {
			return hubErr
		}
		if hubConfigured {
			finding = &exitError{code: exitFindings, message: "downstream peers are unavailable (see stderr); the local daemon may be stopped or unhealthy"}
		}
	}
	if jsonOut {
		if err := json.NewEncoder(out).Encode(peers.ListResponse{SchemaVersion: peers.SchemaVersion, Peers: rows}); err != nil {
			return err
		}
		return finding
	}
	writePeersTable(out, deps.now(), rows)
	return finding
}

func runPeersGet(ctx context.Context, deps peersDeps, projectsRoot, peer string, jsonOut bool, out io.Writer) error {
	if strings.EqualFold(peer, "upstream") {
		if upstream, found, err := resolveUpstreamRow(deps, projectsRoot); err != nil {
			return err
		} else if found {
			detail := peers.Detail{Record: upstream}
			if jsonOut {
				return json.NewEncoder(out).Encode(detail)
			}
			writePeerDetail(out, deps.now(), detail)
			return nil
		}
		return &exitError{code: exitFindings, message: "no upstream is configured; run `wb peers join <hub-url>`"}
	}
	listen, err := deps.listenAddress(deps.daemonDeps, projectsRoot)
	if err != nil {
		return &exitError{code: exitFindings, message: err.Error()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listen+"/api/v1/peers/"+url.PathEscape(peer), nil)
	if err != nil {
		return err
	}
	response, err := deps.httpClient.Do(request)
	if err != nil {
		return &exitError{code: exitFindings, message: "read local daemon peers API: " + err.Error()}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return &exitError{code: exitFindings, message: fmt.Sprintf("no peer named or with ID %q", peer)}
	}
	if response.StatusCode != http.StatusOK {
		return &exitError{code: exitFindings, message: "read local daemon peers API: " + response.Status}
	}
	var detail peers.Detail
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&detail); err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(detail)
	}
	writePeerDetail(out, deps.now(), detail)
	return nil
}

// errPeersDownstreamUnavailable wraps every reason readPeersList could not
// read the local daemon's peers API: the daemon is not running, the request
// failed, the response was not 200, or the body was not the JSON this API
// always answers with (including the HTML the dashboard's own index used to
// answer with, before /api/v1/peers was mounted unconditionally). The
// caller decides whether that is a finding: see runPeersList and
// isHubConfigured.
var errPeersDownstreamUnavailable = errors.New("downstream peers are unavailable")

func readPeersList(ctx context.Context, deps peersDeps, projectsRoot string) (peers.ListResponse, error) {
	empty := peers.ListResponse{SchemaVersion: peers.SchemaVersion}
	listen, err := deps.listenAddress(deps.daemonDeps, projectsRoot)
	if err != nil {
		return empty, fmt.Errorf("%w: %v", errPeersDownstreamUnavailable, err) //nolint:errorlint // deliberately wraps two errors; %w on the sentinel keeps errors.Is working.
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listen+"/api/v1/peers", nil)
	if err != nil {
		return empty, err
	}
	response, err := deps.httpClient.Do(request)
	if err != nil {
		return empty, fmt.Errorf("%w: %v", errPeersDownstreamUnavailable, err) //nolint:errorlint // see above.
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("%w: local daemon answered %s", errPeersDownstreamUnavailable, response.Status)
	}
	var body peers.ListResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return empty, fmt.Errorf("%w: decode local daemon response: %v", errPeersDownstreamUnavailable, err) //nolint:errorlint // see above.
	}
	return body, nil
}

// ---------------------------------------------------------------- upstream row (laptop side)

// peerUpstreamState is the local, laptop-side flag `wb peers block`/`unblock`
// set on the upstream. Task 2 extends this file with connection status
// (last seen, cursor, lag) and honours Blocked to stop or resume dialing;
// this task only establishes the round trip and shows "-" for what Task 2
// has not filled in yet, per invite-and-join's "Laptop (upstream) side".
type peerUpstreamState struct {
	Blocked bool `json:"blocked"`
}

func peerUpstreamStatePath(projectsRoot string) (string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "state", "peer-upstream.json"), nil
}

func loadPeerUpstreamState(path string) (peerUpstreamState, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // private, WB-owned state path.
	if errors.Is(err, os.ErrNotExist) {
		return peerUpstreamState{}, nil
	}
	if err != nil {
		return peerUpstreamState{}, fmt.Errorf("read upstream peer state: %w", err)
	}
	var state peerUpstreamState
	if err := json.Unmarshal(raw, &state); err != nil {
		return peerUpstreamState{}, fmt.Errorf("parse upstream peer state: %w", err)
	}
	return state, nil
}

// savePeerUpstreamState writes atomically (temp file in the same directory,
// then rename): a `wb peers block upstream` that crashes or is killed
// mid-write must never leave a half-written, unparseable state file behind —
// loadPeerUpstreamState has no repair path, only a parse error.
func savePeerUpstreamState(path string, state peerUpstreamState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create upstream peer state directory: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".peer-upstream-*.json.tmp")
	if err != nil {
		return fmt.Errorf("stage upstream peer state: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect staged upstream peer state: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write upstream peer state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync upstream peer state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close upstream peer state: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace upstream peer state: %w", err)
	}
	return nil
}

// upstreamDisplayName names the upstream row before any session has bound a
// hub-reported name (Task 2): the configured URL's host is what the
// operator themselves typed into `wb peers join`, so it is a stable,
// recognisable label rather than an opaque placeholder.
func upstreamDisplayName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || strings.TrimSpace(parsed.Hostname()) == "" {
		return rawURL
	}
	return parsed.Hostname()
}

// resolveUpstreamRow builds the `role: upstream` row when peers.upstream is
// configured. found is false when this machine has not joined a hub.
func resolveUpstreamRow(deps peersDeps, projectsRoot string) (peers.Record, bool, error) {
	upstream, found, err := wbconfig.LoadPeersUpstream(deps.configPath())
	if err != nil || !found {
		return peers.Record{}, false, err
	}
	statePath, err := peerUpstreamStatePath(projectsRoot)
	if err != nil {
		return peers.Record{}, false, err
	}
	state, err := loadPeerUpstreamState(statePath)
	if err != nil {
		return peers.Record{}, false, err
	}
	status := "offline"
	if state.Blocked {
		status = "blocked"
	}
	name := upstreamDisplayName(upstream.URL)
	return peers.Record{SchemaVersion: peers.SchemaVersion, ID: name, Name: name, Role: "upstream", Status: status}, true, nil
}

// runPeersUpstreamTrustChange handles block/unblock when peer names or
// matches the configured upstream: it flips the local state file instead of
// calling the (nonexistent, on a laptop with no hub) owner RPC. handled is
// false when peer does not refer to the upstream, so the caller falls
// through to the normal owner-RPC path for a downstream peer.
func runPeersUpstreamTrustChange(deps peersDeps, projectsRoot, verb, peer string, jsonOut bool, out io.Writer) (handled bool, err error) {
	upstream, found, err := wbconfig.LoadPeersUpstream(deps.configPath())
	if err != nil {
		return true, err
	}
	if !found {
		return false, nil
	}
	name := upstreamDisplayName(upstream.URL)
	if !strings.EqualFold(peer, "upstream") && !strings.EqualFold(peer, name) && !strings.EqualFold(peer, upstream.URL) {
		return false, nil
	}
	statePath, err := peerUpstreamStatePath(projectsRoot)
	if err != nil {
		return true, err
	}
	state, err := loadPeerUpstreamState(statePath)
	if err != nil {
		return true, err
	}
	state.Blocked = verb == "Blocked"
	if err := savePeerUpstreamState(statePath, state); err != nil {
		return true, err
	}
	if jsonOut {
		return true, json.NewEncoder(out).Encode(peerTrustResponse{PeerID: name, Name: name, Trust: map[bool]string{true: "blocked", false: "active"}[state.Blocked]})
	}
	_, writeErr := fmt.Fprintf(out, "%s upstream %s\n", verb, name)
	return true, writeErr
}

// ---------------------------------------------------------------- rendering

func writePeersTable(out io.Writer, now time.Time, rows []peers.Record) {
	_, _ = fmt.Fprintf(out, "%-13s %-11s %-10s %-10s %-9s %-6s %s\n", "NAME", "ROLE", "STATUS", "LAST SEEN", "CONNECTED", "CURSOR", "LAG")
	for _, row := range rows {
		lastSeen := "-"
		if row.LastSeenAt != nil {
			lastSeen = publishedAgo(humanAge(now.Sub(*row.LastSeenAt)))
		}
		lag := "-"
		if row.Lag != nil {
			lag = strconv.FormatInt(*row.Lag, 10)
		}
		if row.ResetPending {
			// A pending reset makes the last-known count stale until
			// reconciliation completes, so it takes priority even when a
			// number is also present.
			lag = "reset"
		}
		_, _ = fmt.Fprintf(out, "%-13s %-11s %-10s %-10s %-9s %-6s %s\n", row.Name, row.Role, row.Status, lastSeen, "-", "-", lag)
	}
}

func writePeerDetail(out io.Writer, now time.Time, detail peers.Detail) {
	lastSeen := "-"
	if detail.LastSeenAt != nil {
		lastSeen = publishedAgo(humanAge(now.Sub(*detail.LastSeenAt)))
	}
	nodeID := detail.NodeID
	if nodeID == "" {
		nodeID = "-"
	}
	_, _ = fmt.Fprintf(out, "NAME           %s\n", detail.Name)
	_, _ = fmt.Fprintf(out, "ROLE           %s\n", detail.Role)
	_, _ = fmt.Fprintf(out, "STATUS         %s\n", detail.Status)
	_, _ = fmt.Fprintf(out, "PEER ID        %s\n", detail.ID)
	_, _ = fmt.Fprintf(out, "NODE ID        %s\n", nodeID)
	_, _ = fmt.Fprintf(out, "LAST SEEN      %s\n", lastSeen)
	_, _ = fmt.Fprintf(out, "RESET PENDING  %t\n", detail.ResetPending)
	session := "none"
	if detail.Session != nil {
		session = "connected"
	}
	_, _ = fmt.Fprintf(out, "SESSION        %s\n", session)
	_, _ = fmt.Fprintf(out, "COUNTERS       rx_events=%d tx_events=%d rx_bytes=%d tx_bytes=%d\n",
		detail.Counters.RXEvents, detail.Counters.TXEvents, detail.Counters.RXPayloadBytes, detail.Counters.TXPayloadBytes)
	_, _ = fmt.Fprintf(out, "ADMIN          %t\n", detail.AdminAvailable)
}
