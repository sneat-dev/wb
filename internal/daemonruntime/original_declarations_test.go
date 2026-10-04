package daemonruntime

import (
	"net/http"
	"testing"
)

const (
	cwWtBridgeToken      = "cwWt-bridge-owner-token"
	cwWtBridgeGeneration = "cwWt-generation-1"
)

var cwWtBridgeNoop http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

// cwWtBridgeServer creates a prepared bridge server rooted at a fresh temp dir.
// The explicit projects root determines its private runtime path; TestMain
// isolates ambient user state for configuration and legacy-layout discovery.
func cwWtBridgeServer(t *testing.T, handler http.Handler) (*FileBridgeServer, string) {
	t.Helper()
	if handler == nil {
		handler = cwWtBridgeNoop
	}
	root := daemonTestRoot(t)
	server, err := NewFileBridgeServer(root, cwWtBridgeToken, cwWtBridgeGeneration, handler)
	if err != nil {
		t.Fatalf("cwWt: newDaemonFileBridgeServer: %v", err)
	}
	return server, root
}
