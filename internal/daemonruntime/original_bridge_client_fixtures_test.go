//go:build !windows

package daemonruntime

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// cwWtBridgeErrorReader fails every read, which is how RoundTrip's "the RPC
// body could not be read" branch is reached without a real network.
type cwWtBridgeErrorReader struct{}

func (cwWtBridgeErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("cwWt: injected body read failure")
}

// cwWtBridgeClientRoot prepares a bridge root with a key, as the client side
// requires.
func cwWtBridgeClientRoot(t *testing.T) string {
	t.Helper()
	root := daemonTestRoot(t)
	if _, _, err := prepareDaemonFileBridge(root); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonFileBridgeKey(root, true); err != nil {
		t.Fatal(err)
	}
	return root
}

// cwWtBridgeTransport builds the file-bridge transport through the public
// constructor so the test exercises the same wiring the CLI uses.
func cwWtBridgeTransport(t *testing.T, root string, timeout time.Duration) *daemonFileBridgeTransport {
	t.Helper()
	client, err := newDaemonFileBridgeHTTPClientWithTimeout(root, cwWtBridgeGeneration, timeout)
	if err != nil {
		t.Fatalf("cwWt: newDaemonFileBridgeHTTPClientWithTimeout: %v", err)
	}
	transport, ok := client.Transport.(*daemonFileBridgeTransport)
	if !ok {
		t.Fatalf("cwWt: unexpected transport %T", client.Transport)
	}
	return transport
}

// cwWtBridgeResolveRequestID waits for the bridged request envelope and returns
// its id, so the test can answer the request the way a daemon would.
func cwWtBridgeResolveRequestID(t *testing.T, directory string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
					return strings.TrimSuffix(entry.Name(), ".json")
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cwWt: bridged request envelope did not appear")
	return ""
}

func cwWtBridgeRoundTripAsync(transport *daemonFileBridgeTransport, request *http.Request) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := transport.RoundTrip(request)
		done <- err
	}()
	return done
}

func cwWtBridgeAwaitRoundTrip(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("cwWt: bridged round trip did not finish")
		return nil
	}
}

// cwWtBridgeReply writes an envelope the transport will try to consume, after
// applying mutate to control which validation fence it trips.
func cwWtBridgeReply(t *testing.T, transport *daemonFileBridgeTransport, id string, mutate func(*daemonFileEnvelope)) {
	t.Helper()
	response := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: transport.generation, StatusCode: http.StatusOK}
	if mutate != nil {
		mutate(&response)
	}
	response.PayloadSHA256 = daemonFilePayloadDigest(response)
	response.MAC = daemonFileEnvelopeMAC(response, transport.key)
	if err := writeDaemonFileEnvelope(transport.responses, id, response); err != nil {
		t.Fatal(err)
	}
}
