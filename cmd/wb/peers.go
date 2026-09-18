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
	"strings"
	"time"

	"github.com/spf13/cobra"

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

func newPeersCmd() *cobra.Command {
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
	command.AddCommand(newPeersInviteCmd())
	command.AddCommand(newPeersJoinCmd())
	command.AddCommand(newPeersListCmd())
	command.AddCommand(newPeersGetCmd())
	command.AddCommand(newPeersBlockCmd())
	command.AddCommand(newPeersUnblockCmd())
	command.AddCommand(newPeersDisconnectCmd())
	return command
}

// ---------------------------------------------------------------- invite

func newPeersInviteCmd() *cobra.Command {
	var rotate, jsonOut bool
	var tokenFile string
	command := &cobra.Command{
		Use:   "invite <name>",
		Short: "Mint a one-time peer credential on this hub",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersInvite(command.Context(), defaultPeersDeps(), projectsRoot, args[0], rotate, tokenFile, jsonOut, command.OutOrStdout())
		},
	}
	command.Flags().BoolVar(&rotate, "rotate", false, "reissue the credential for an existing peer name")
	command.Flags().StringVar(&tokenFile, "token-file", "", "write the one-time token here (mode 0600) instead of printing it")
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers invite laptop admit token credential mint one-time hub connect")
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
	client, err := deps.adminClient(ctx, deps.daemonDeps, projectsRoot)
	if err != nil {
		return err
	}
	var response peerInviteResponse
	if err := client.call(ctx, peersRPCPrefix+"invite", peerInviteRequest{Name: name, Rotate: rotate}, &response); err != nil {
		return err
	}
	result := peersInviteResult{PeerID: response.PeerID, Name: response.Name, Token: response.Token, CreatedAt: response.CreatedAt, Rotated: response.Rotated}
	if strings.TrimSpace(tokenFile) != "" {
		absolute, err := filepath.Abs(tokenFile)
		if err != nil {
			return fmt.Errorf("resolve token file: %w", err)
		}
		if err := writeOneTimeToken(absolute, response.Token); err != nil {
			return err
		}
		result.Token = ""
		result.TokenFile = absolute
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

func writeOneTimeToken(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- block / unblock / disconnect

func newPeersBlockCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "block <peer>",
		Short: "Refuse a peer's sessions and HTTP calls, keeping its history",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersTrustChange(command.Context(), defaultPeersDeps(), projectsRoot, peersRPCPrefix+"block", "Blocked", args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers block refuse deny cut off laptop vm hub connect")
	return command
}

func newPeersUnblockCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "unblock <peer>",
		Short: "Allow a blocked peer's sessions again",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersTrustChange(command.Context(), defaultPeersDeps(), projectsRoot, peersRPCPrefix+"unblock", "Unblocked", args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers unblock allow readmit laptop vm hub connect")
	return command
}

func runPeersTrustChange(ctx context.Context, deps peersDeps, projectsRoot, path, verb, peer string, jsonOut bool, out io.Writer) error {
	if handled, err := runPeersUpstreamTrustChange(deps, projectsRoot, verb, peer, jsonOut, out); handled {
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

func newPeersDisconnectCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "disconnect <peer>",
		Short: "Close a peer's live session only; it stays trusted and reconnects itself",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersDisconnect(command.Context(), defaultPeersDeps(), projectsRoot, args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers disconnect close session live laptop vm hub connect")
	return command
}

func runPeersDisconnect(ctx context.Context, deps peersDeps, projectsRoot, peer string, jsonOut bool, out io.Writer) error {
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

func newPeersListCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List every peer, plus the upstream hub when this machine has joined one",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runPeersList(command.Context(), defaultPeersDeps(), projectsRoot, jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers list laptop vm hub connect status connected offline blocked")
	return command
}

func newPeersGetCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "get <peer>",
		Short: "Show one peer's full record",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersGet(command.Context(), defaultPeersDeps(), projectsRoot, args[0], jsonOut, command.OutOrStdout())
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers get show detail laptop vm hub connect")
	return command
}

func runPeersList(ctx context.Context, deps peersDeps, projectsRoot string, jsonOut bool, out io.Writer) error {
	list, err := readPeersList(ctx, deps, projectsRoot)
	if err != nil {
		return err
	}
	rows := append([]peers.Record(nil), list.Peers...)
	if upstream, found, err := resolveUpstreamRow(deps, projectsRoot); err != nil {
		return err
	} else if found {
		rows = append(rows, upstream)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if jsonOut {
		return json.NewEncoder(out).Encode(peers.ListResponse{SchemaVersion: peers.SchemaVersion, Peers: rows})
	}
	writePeersTable(out, deps.now(), rows)
	return nil
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

// readPeersList reads the local daemon's peers API, best-effort: a daemon
// that is not running or not self-hosting a hub yet is reported as "no
// downstream peers" rather than failing the command outright, since `wb
// peers list` is also the way a fresh laptop-only install (no hub, maybe an
// upstream) checks its own state.
func readPeersList(ctx context.Context, deps peersDeps, projectsRoot string) (peers.ListResponse, error) {
	empty := peers.ListResponse{SchemaVersion: peers.SchemaVersion}
	listen, err := deps.listenAddress(deps.daemonDeps, projectsRoot)
	if err != nil {
		return empty, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listen+"/api/v1/peers", nil)
	if err != nil {
		return empty, nil
	}
	response, err := deps.httpClient.Do(request)
	if err != nil {
		return empty, nil
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return empty, nil
	}
	var body peers.ListResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return empty, err
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

func savePeerUpstreamState(path string, state peerUpstreamState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create upstream peer state directory: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write upstream peer state: %w", err)
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
		if row.ResetPending {
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
