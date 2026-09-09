package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

const daemonRPCBaseURL = "http://wb.local"

const daemonStatusBridgeTimeout = 2 * time.Second

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

func daemonOperationClient(ctx context.Context, deps daemonDependencies, root string, progress io.Writer) (daemonv1connect.DaemonServiceClient, error) {
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
	localClient := deps.localClient
	if localClient == nil {
		localClient = daemonLocalHTTPClient
	}
	httpClient, err := localClient(root, state.OwnerToken)
	if err != nil {
		return nil, err
	}
	client := daemonv1connect.NewDaemonServiceClient(httpClient, daemonRPCBaseURL)
	deadline := time.Now().Add(time.Second)
	var probeErr error
	for {
		if _, probeErr = client.GetDaemonInfo(ctx, connect.NewRequest(&daemonv1.GetDaemonInfoRequest{})); probeErr == nil {
			return client, nil
		}
		if time.Now().After(deadline) {
			if !daemonFileBridgeFallbackAllowed(probeErr) {
				return nil, fmt.Errorf("connect to authenticated local daemon: %w", probeErr)
			}
			bridgeHTTPClient, bridgeErr := newDaemonFileBridgeHTTPClient(root, fmt.Sprint(state.Queue.Generation))
			if bridgeErr != nil {
				return nil, fmt.Errorf("local daemon socket unavailable (%v) and protected file bridge unavailable: %w", probeErr, bridgeErr)
			}
			if progress != nil {
				_, _ = fmt.Fprintln(progress, "wb: local daemon socket unavailable; using protected project-root file bridge")
			}
			bridgeClient := daemonv1connect.NewDaemonServiceClient(bridgeHTTPClient, daemonRPCBaseURL)
			if _, bridgeErr = bridgeClient.GetDaemonInfo(ctx, connect.NewRequest(&daemonv1.GetDaemonInfoRequest{})); bridgeErr != nil {
				return nil, fmt.Errorf("connect to daemon through protected file bridge: %w", bridgeErr)
			}
			return bridgeClient, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect to authenticated local daemon: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func daemonFileBridgeHealthy(ctx context.Context, root, generation string) error {
	httpClient, err := newDaemonFileBridgeHTTPClientWithTimeout(root, generation, daemonStatusBridgeTimeout)
	if err != nil {
		return err
	}
	client := daemonv1connect.NewDaemonServiceClient(httpClient, daemonRPCBaseURL)
	response, err := client.GetDaemonInfo(ctx, connect.NewRequest(&daemonv1.GetDaemonInfoRequest{}))
	if err != nil {
		return err
	}
	if response.Msg.SchedulerGeneration != generation {
		return fmt.Errorf("daemon file bridge scheduler generation is %s, want %s", response.Msg.SchedulerGeneration, generation)
	}
	return nil
}

func daemonFileBridgeFallbackAllowed(err error) bool {
	if err == nil {
		return false
	}
	if code := connect.CodeOf(err); code != connect.CodeUnknown && code != connect.CodeUnavailable {
		return false
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, fragment := range []string{"operation not permitted", "permission denied", "connection refused", "no such file or directory"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}
