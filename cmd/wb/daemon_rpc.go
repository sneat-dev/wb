package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

const daemonRPCBaseURL = "http://wb.local"

type daemonAuthenticatedTransport struct {
	token string
	base  http.RoundTripper
}

func (transport daemonAuthenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(request)
}

func authenticatedDaemonHandler(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if provided == "" || len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			http.Error(response, "unauthorized local daemon request", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func newDaemonOperationClient(root, token string) (daemonv1connect.DaemonServiceClient, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("daemon lifecycle state has no authentication token")
	}
	client, err := daemonLocalHTTPClient(root, token)
	if err != nil {
		return nil, err
	}
	return daemonv1connect.NewDaemonServiceClient(client, daemonRPCBaseURL), nil
}

func daemonOperationClient(ctx context.Context, deps daemonDependencies, root string) (daemonv1connect.DaemonServiceClient, error) {
	controller := newDaemonController(deps, root)
	result, err := controller.Start(ctx, daemonDefaultListen)
	if err != nil {
		return nil, fmt.Errorf("start local daemon: %w", err)
	}
	state, found, err := controller.store.Load()
	if err != nil {
		return nil, err
	}
	if !found || state.Status != "ready" || !result.ProcessManagerRunning {
		return nil, fmt.Errorf("local daemon is not ready")
	}
	client, err := newDaemonOperationClient(root, state.OwnerToken)
	if err != nil {
		return nil, err
	}
	if _, err := client.GetDaemonInfo(ctx, connect.NewRequest(&daemonv1.GetDaemonInfoRequest{})); err != nil {
		return nil, fmt.Errorf("connect to authenticated local daemon: %w", err)
	}
	return client, nil
}
