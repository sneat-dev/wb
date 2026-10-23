package cmdpeers

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/peersrun"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	Invite            func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error)
	Join              func(context.Context, peersrun.JoinRequest, io.Reader, func(error)) (peersrun.JoinOutput, error)
	List              func(context.Context, peersrun.ListRequest, func(error)) (peersrun.ListResult, error)
	Get               func(context.Context, peersrun.GetRequest) (peers.Detail, error)
	TrustChange       func(context.Context, peersrun.TrustRequest) (peersrun.TrustResult, error)
	Disconnect        func(context.Context, peersrun.DisconnectRequest) (peers.DisconnectResponse, error)
	Now               func() time.Time
	SetDiscoveryTerms func(*cobra.Command, string)
}

func commandError(runtime shared.Runtime, err error) error {
	var refused *peersrun.Refusal
	if errors.As(err, &refused) {
		code := shared.ExitUsage
		if refused.Kind == peersrun.Findings {
			code = shared.ExitFindings
		}
		return runtime.ExitError(code, refused.Message)
	}
	return err
}
func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
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
	command.AddCommand(newInviteCmd(runtime, deps))
	command.AddCommand(newJoinCmd(runtime, deps))
	command.AddCommand(newListCmd(runtime, deps))
	command.AddCommand(newGetCmd(runtime, deps))
	command.AddCommand(newBlockCmd(runtime, deps))
	command.AddCommand(newUnblockCmd(runtime, deps))
	command.AddCommand(newDisconnectCmd(runtime, deps))
	return command
}
func newInviteCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var rotate, jsonOut bool
	var tokenFile string
	command := &cobra.Command{
		Use:   "invite <name>",
		Short: "Mint a one-time peer credential on this hub",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return commandError(runtime, runInvite(command.Context(), deps, runtime.Flags().ProjectsRoot, args[0], rotate, tokenFile, jsonOut, command.OutOrStdout()))
		},
	}
	command.Flags().BoolVar(&rotate, "rotate", false, "reissue the credential for an existing peer name")
	command.Flags().StringVar(&tokenFile, "token-file", "", "write the one-time token here (mode 0600) instead of printing it")
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.SetDiscoveryTerms(command, "peers invite laptop vm admit token credential mint one-time hub connect block")
	return command
}
func newJoinCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var tokenFile string
	var tokenStdin, restartDaemon, jsonOut bool
	command := &cobra.Command{
		Use:   "join <hub-url>",
		Short: "Join a hub as its peer, verifying the invite token first",
		Long: `wb peers join reads the one-time token an operator minted with
"wb peers invite", verifies it against the hub, stores it as a private
credential, writes peers.upstream in wb.yaml (never touching remote:), and
restarts a running daemon so the peer session starts immediately.

  wb peers invite laptop --token-file token.txt   # on the hub
  cat token.txt | wb peers join https://vm1.sneat.dev --token-stdin   # on the laptop`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return commandError(runtime, runJoin(command.Context(), deps, runtime.Flags().ProjectsRoot, args[0], tokenFile, tokenStdin, restartDaemon, jsonOut, command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr()))
		},
	}
	command.Flags().StringVar(&tokenFile, "token-file", "", "read the one-time token from this absolute path")
	command.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the one-time token from stdin")
	command.Flags().BoolVar(&restartDaemon, "restart-daemon", true, "restart WB daemon if it is running")
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.SetDiscoveryTerms(command, "peers join laptop connect hub upstream token dial vm block")
	return command
}
func newListCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List every peer, plus the upstream hub when this machine has joined one",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return commandError(runtime, runList(command.Context(), deps, runtime.Flags().ProjectsRoot, jsonOut, command.OutOrStdout(), command.ErrOrStderr()))
		},
	}
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.SetDiscoveryTerms(command, "peers list laptop vm hub connect block status connected offline blocked")
	return command
}
func newGetCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "get <peer>",
		Short: "Show one peer's full record",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return commandError(runtime, runGet(command.Context(), deps, runtime.Flags().ProjectsRoot, args[0], jsonOut, command.OutOrStdout()))
		},
	}
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.SetDiscoveryTerms(command, "peers get show detail laptop vm hub connect block")
	return command
}
func newBlockCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "block <peer>",
		Short: "Refuse a peer's sessions and HTTP calls, keeping its history",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return commandError(runtime, runTrustChange(command.Context(), deps, runtime.Flags().ProjectsRoot, peersrun.Block, "Blocked", args[0], jsonOut, command.OutOrStdout()))
		},
	}
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.SetDiscoveryTerms(command, "peers block refuse deny cut off laptop vm hub connect")
	return command
}
func newUnblockCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "unblock <peer>",
		Short: "Allow a blocked peer's sessions again",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return commandError(runtime, runTrustChange(command.Context(), deps, runtime.Flags().ProjectsRoot, peersrun.Unblock, "Unblocked", args[0], jsonOut, command.OutOrStdout()))
		},
	}
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.SetDiscoveryTerms(command, "peers unblock allow readmit laptop vm hub connect block")
	return command
}
func newDisconnectCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "disconnect <peer>",
		Short: "Close a peer's live session only; it stays trusted and reconnects itself",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return commandError(runtime, runDisconnect(command.Context(), deps, runtime.Flags().ProjectsRoot, args[0], jsonOut, command.OutOrStdout()))
		},
	}
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.SetDiscoveryTerms(command, "peers disconnect close session live laptop vm hub connect block")
	return command
}
