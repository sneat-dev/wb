package cmdpeers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/peersrun"
)

func runInvite(ctx context.Context, deps Dependencies, root, name string, rotate bool, tokenFile string, jsonOut bool, out io.Writer) error {
	result, err := deps.Invite(ctx, peersrun.InviteRequest{Root: root, Name: name, Rotate: rotate, TokenFile: tokenFile})
	if err != nil {
		return err
	}
	output := result.Output
	if result.TokenWriteError != nil {
		if jsonOut {
			if err := json.NewEncoder(out).Encode(output); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(out, "wb: could not write token file %s: %v\n", result.AttemptedTokenFile, result.TokenWriteError); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(out, "Token (copy this now; it cannot be shown again):"); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(out, output.Token); err != nil {
				return err
			}
		}
		return &peersrun.Refusal{Kind: peersrun.Findings, Message: fmt.Sprintf("could not write token file %s: %v", result.AttemptedTokenFile, result.TokenWriteError)}
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(output)
	}
	verb := "Invited"
	if output.Rotated {
		verb = "Rotated"
	}
	if _, err := fmt.Fprintf(out, "%s %s (%s)\n", verb, output.Name, output.PeerID); err != nil {
		return err
	}
	if output.TokenFile != "" {
		_, err = fmt.Fprintf(out, "Token written to %s (mode 0600). It cannot be shown again.\n", output.TokenFile)
		return err
	}
	_, err = fmt.Fprintf(out, "Token (copy this now; it cannot be shown again):\n%s\n", output.Token)
	return err
}
func runJoin(ctx context.Context, deps Dependencies, root, hubURL, tokenFile string, tokenStdin, restartDaemon, jsonOut bool, in io.Reader, out, errOut io.Writer) error {
	result, err := deps.Join(ctx, peersrun.JoinRequest{Root: root, HubURL: hubURL, TokenFile: tokenFile, TokenStdin: tokenStdin, RestartDaemon: restartDaemon}, in, func(err error) { _, _ = fmt.Fprintln(errOut, "wb: node identity unavailable:", err) })
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	_, err = fmt.Fprintf(out, "Joined %s\n  config      %s\n  credential  %s\n  verified    yes\n  daemon      %s\n", result.HubURL, result.ConfigPath, result.TokenFile, map[bool]string{true: "restarted if running", false: "restart skipped"}[result.DaemonRestart])
	return err
}
func runTrustChange(ctx context.Context, deps Dependencies, root string, action peersrun.TrustAction, verb, peer string, jsonOut bool, out io.Writer) error {
	result, err := deps.TrustChange(ctx, peersrun.TrustRequest{Root: root, Peer: peer, Action: action})
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(result.Response)
	}
	if result.Upstream {
		_, err = fmt.Fprintf(out, "%s upstream %s\n", verb, result.Response.Name)
		return err
	}
	_, err = fmt.Fprintf(out, "%s %s (%s): trust=%s reset_pending=%t\n", verb, result.Response.Name, result.Response.PeerID, result.Response.Trust, result.Response.ResetPending)
	return err
}
func runDisconnect(ctx context.Context, deps Dependencies, root, peer string, jsonOut bool, out io.Writer) error {
	result, err := deps.Disconnect(ctx, peersrun.DisconnectRequest{Root: root, Peer: peer})
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	_, err = fmt.Fprintf(out, "%s (%s): %s\n", result.Name, result.PeerID, result.Message)
	return err
}
func runList(ctx context.Context, deps Dependencies, root string, jsonOut bool, out, errOut io.Writer) error {
	result, err := deps.List(ctx, peersrun.ListRequest{Root: root}, func(err error) { _, _ = fmt.Fprintln(errOut, "wb: downstream peers unavailable:", err) })
	if err != nil {
		return err
	}
	if jsonOut {
		if err := json.NewEncoder(out).Encode(result.Response); err != nil {
			return err
		}
	} else {
		writePeersTable(out, deps.Now(), result.Response.Peers)
	}
	return result.Finding
}
func runGet(ctx context.Context, deps Dependencies, root, peer string, jsonOut bool, out io.Writer) error {
	result, err := deps.Get(ctx, peersrun.GetRequest{Root: root, Peer: peer})
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	writePeerDetail(out, deps.Now(), result)
	return nil
}

func writePeersTable(out io.Writer, now time.Time, rows []peers.Record) {
	_, _ = fmt.Fprintf(out, "%-13s %-11s %-10s %-10s %-9s %-6s %s\n", "NAME", "ROLE", "STATUS", "LAST SEEN", "CONNECTED", "CURSOR", "LAG")
	for _, row := range rows {
		lastSeen := "-"
		if row.LastSeenAt != nil {
			lastSeen = shared.PublishedAgo(shared.HumanAge(now.Sub(*row.LastSeenAt)))
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
		lastSeen = shared.PublishedAgo(shared.HumanAge(now.Sub(*detail.LastSeenAt)))
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
