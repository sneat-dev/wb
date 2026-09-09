//go:build !windows

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const daemonLocalNetwork = "unix"

type removingListener struct {
	net.Listener
	path string
}

func (listener *removingListener) Close() error {
	err := listener.Listener.Close()
	if removeErr := os.Remove(listener.path); err == nil && !os.IsNotExist(removeErr) {
		err = removeErr
	}
	return err
}

func daemonLocalAddress(root string) string {
	return filepath.Join(root, ".wb", "runtime", "daemon.sock")
}

func listenDaemonLocal(root string) (net.Listener, error) {
	path := daemonLocalAddress(root)
	// Darwin's sockaddr_un path limit is 104 bytes; Linux allows slightly more.
	// Fail before net.Listen so configuration errors name the durable endpoint.
	if len(path) >= 104 {
		return nil, fmt.Errorf("daemon socket path is too long (%d bytes; must be below 104): %s", len(path), path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon socket directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refuse to replace non-socket daemon path %s", path)
		}
		connection, dialErr := net.DialTimeout(daemonLocalNetwork, path, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, fmt.Errorf("daemon socket %s is already accepting connections", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale daemon socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect daemon socket: %w", err)
	}
	listener, err := net.Listen(daemonLocalNetwork, path)
	if err != nil {
		return nil, fmt.Errorf("listen on daemon socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("protect daemon socket: %w", err)
	}
	return &removingListener{Listener: listener, path: path}, nil
}

func daemonLocalHTTPClient(root, token string) (*http.Client, error) {
	path := daemonLocalAddress(root)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, daemonLocalNetwork, path)
		},
	}
	return &http.Client{Transport: daemonAuthenticatedTransport{token: token, base: transport}}, nil
}
