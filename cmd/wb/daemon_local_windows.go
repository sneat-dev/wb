//go:build windows

package main

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
)

const daemonLocalNetwork = "npipe"

func daemonLocalAddress(root string) string {
	// Keep the command/client contract compatible with a current-user Windows
	// named-pipe adapter without falling back to an unauthenticated TCP port.
	return `\\.\pipe\wb-` + filepath.Base(filepath.Clean(root))
}

func listenDaemonLocal(string) (net.Listener, error) {
	return nil, fmt.Errorf("WB daemon named-pipe listener is unavailable in this Windows build")
}

func daemonLocalHTTPClient(string, string) (*http.Client, error) {
	return nil, fmt.Errorf("WB daemon named-pipe client is unavailable in this Windows build")
}
