package cmdpeers

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/peersrun"
	"github.com/spf13/cobra"
)

type fixtureExitError struct {
	code    int
	message string
}

func (e *fixtureExitError) Error() string { return e.message }
func peersOriginalCommand(root string) *cobra.Command {
	refused := &peersrun.Refusal{Kind: peersrun.Usage, Message: "no hub is configured"}
	return New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }, ExitError: func(code int, message string) error { return &fixtureExitError{code: code, message: message} }}, Dependencies{Invite: func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error) {
		return peersrun.InviteResult{}, refused
	}, TrustChange: func(context.Context, peersrun.TrustRequest) (peersrun.TrustResult, error) {
		return peersrun.TrustResult{}, refused
	}, Disconnect: func(context.Context, peersrun.DisconnectRequest) (peers.DisconnectResponse, error) {
		return peers.DisconnectResponse{}, refused
	}, Join: func(context.Context, peersrun.JoinRequest, io.Reader, func(error)) (peersrun.JoinOutput, error) {
		return peersrun.JoinOutput{}, &peersrun.Refusal{Kind: peersrun.Usage, Message: "http"}
	}, Get: func(context.Context, peersrun.GetRequest) (peers.Detail, error) {
		return peers.Detail{}, &peersrun.Refusal{Kind: peersrun.Findings, Message: "no upstream is configured"}
	}, List: func(context.Context, peersrun.ListRequest, func(error)) (peersrun.ListResult, error) {
		return peersrun.ListResult{Response: peers.ListResponse{SchemaVersion: peers.SchemaVersion}}, nil
	}, Now: time.Now, SetDiscoveryTerms: func(*cobra.Command, string) {}})
}
func peersOriginalExecute(t *testing.T, command *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	command.SetArgs(args)
	command.SetOut(&out)
	command.SetErr(&out)
	command.SilenceErrors = true
	command.SilenceUsage = true
	err := command.Execute()
	return out.String(), err
}
func TestPeersJoinCLIWiresIntoRunPeersJoin(t *testing.T) {
	root := t.TempDir()
	out, err := peersOriginalExecute(t, peersOriginalCommand(root), "join", "not-a-url")
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("wb peers join wiring: err=%v out=%q", err, out)
	}
}
func TestPeersInviteCLIWiresIntoRunPeersInvite(t *testing.T) {
	root := t.TempDir()
	out, err := peersOriginalExecute(t, peersOriginalCommand(root), "invite", "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers invite wiring: err=%v out=%q", err, out)
	}
}
func TestPeersBlockCLIWiresIntoRunPeersTrustChange(t *testing.T) {
	root := t.TempDir()
	out, err := peersOriginalExecute(t, peersOriginalCommand(root), "block", "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers block wiring: err=%v out=%q", err, out)
	}
}
func TestPeersUnblockCLIWiresIntoRunPeersTrustChange(t *testing.T) {
	root := t.TempDir()
	out, err := peersOriginalExecute(t, peersOriginalCommand(root), "unblock", "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers unblock wiring: err=%v out=%q", err, out)
	}
}
func TestPeersDisconnectCLIWiresIntoRunPeersDisconnect(t *testing.T) {
	root := t.TempDir()
	out, err := peersOriginalExecute(t, peersOriginalCommand(root), "disconnect", "laptop")
	if err == nil || !strings.Contains(err.Error(), "no hub is configured") {
		t.Fatalf("wb peers disconnect wiring: err=%v out=%q", err, out)
	}
}
func TestPeersGetCLIWiresIntoRunPeersGet(t *testing.T) {
	root := t.TempDir()
	out, err := peersOriginalExecute(t, peersOriginalCommand(root), "get", "upstream")
	if err == nil || !strings.Contains(err.Error(), "no upstream is configured") {
		t.Fatalf("wb peers get wiring: err=%v out=%q", err, out)
	}
}
func TestPeersListCLIWiresIntoRunPeersList(t *testing.T) {
	root := t.TempDir()
	out, err := peersOriginalExecute(t, peersOriginalCommand(root), "list")
	if err != nil {
		t.Fatalf("wb peers list wiring: err=%v out=%q", err, out)
	}
	if !strings.Contains(out, "{") && !strings.Contains(out, "no peers") && !strings.Contains(out, "\n") {
		t.Fatalf("wb peers list produced no observable output: %q", out)
	}
}
