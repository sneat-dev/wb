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

// daemonSocketPathIn reports that this platform's endpoint is a named pipe
// rather than a path inside the runtime directory, so a directory-based probe
// has nothing to dial.
func daemonSocketPathIn(string) (string, bool) { return "", false }

func listenDaemonLocal(string) (net.Listener, error) {
	return nil, fmt.Errorf("WB daemon named-pipe listener is unavailable in this Windows build")
}

func daemonLocalHTTPClient(string, string) (*http.Client, error) {
	return nil, fmt.Errorf("WB daemon named-pipe client is unavailable in this Windows build")
}
