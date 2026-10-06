package main

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmdpeers"
	"github.com/sneat-dev/wb/internal/nodeidentity"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/peersrun"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/spf13/cobra"
)

func newPeersCmd(inv *invocation) *cobra.Command {
	service := newPeersService()
	return cmdpeers.New(newCLIRuntime(inv), cmdpeers.Dependencies{Invite: service.Invite, Join: service.Join, List: service.List, Get: service.Get, TrustChange: service.TrustChange, Disconnect: service.Disconnect, Now: func() time.Time { return time.Now().UTC() }, SetDiscoveryTerms: setDiscoveryTerms})
}
func newPeersService() peersrun.Service {
	return peersrun.New(peersrun.Dependencies{ConfigPath: wbconfig.DefaultPath, Abs: filepath.Abs, Do: (&http.Client{Timeout: 5 * time.Second}).Do,
		ListenAddress: func(root string) (string, error) { return daemonListenAddress(daemonDependencies{}, root) },
		Admin: func(ctx context.Context, root string) (peersrun.AdminOperations, error) {
			return newPeerAdminOperations(ctx, defaultDaemonDependencies(), root)
		}},
		peersrun.JoinDependencies{ConfigPath: wbconfig.DefaultPath, Verify: peersrun.Verify, Restart: restartDaemonAfterRemoteEnroll, EnsureNodeIdentity: func(root string) error { _, err := nodeidentity.Load(root, nil); return err }})
}
func newPeerAdminOperations(ctx context.Context, deps daemonDependencies, root string) (peersrun.AdminOperations, error) {
	client, err := newPeerAdminClient(ctx, deps, root)
	if err != nil {
		return peersrun.AdminOperations{}, err
	}
	return peerAdminOperations(client), nil
}
func peerAdminOperations(client *peerAdminClient) peersrun.AdminOperations {
	return peersrun.AdminOperations{
		Invite: func(ctx context.Context, request peers.InviteRequest) (peers.InviteResponse, error) {
			var response peers.InviteResponse
			err := client.call(ctx, peers.RPCPrefix+"invite", request, &response)
			return response, err
		},
		Trust: func(ctx context.Context, action peersrun.TrustAction, peer string) (peers.TrustResponse, error) {
			path := "block"
			if action == peersrun.Unblock {
				path = "unblock"
			}
			var response peers.TrustResponse
			err := client.call(ctx, peers.RPCPrefix+path, peers.NameOrIDRequest{Peer: peer}, &response)
			return response, err
		},
		Disconnect: func(ctx context.Context, peer string) (peers.DisconnectResponse, error) {
			var response peers.DisconnectResponse
			err := client.call(ctx, peers.RPCPrefix+"disconnect", peers.NameOrIDRequest{Peer: peer}, &response)
			return response, err
		},
	}
}
