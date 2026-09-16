//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
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
	root := t.TempDir()
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

func TestCwWtDaemonFileBridgeHTTPClientRejectsBadConfiguration(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "cwWt-root-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonFileBridgeHTTPClientWithTimeout(fileRoot, cwWtBridgeGeneration, time.Second); err == nil {
		t.Fatal("client accepted a regular-file projects root")
	}

	prepared := t.TempDir()
	if _, _, err := prepareDaemonFileBridge(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonFileBridgeHTTPClientWithTimeout(prepared, "   ", time.Second); err == nil || !strings.Contains(err.Error(), "requires scheduler generation") {
		t.Fatalf("blank generation error = %v", err)
	}
	if _, err := newDaemonFileBridgeHTTPClientWithTimeout(prepared, cwWtBridgeGeneration, time.Second); err == nil || !strings.Contains(err.Error(), "inspect daemon file bridge key") {
		t.Fatalf("missing key error = %v", err)
	}

	root := cwWtBridgeClientRoot(t)
	if _, err := newDaemonFileBridgeHTTPClientWithTimeout(root, cwWtBridgeGeneration, 0); err == nil || !strings.Contains(err.Error(), "timeout must be positive") {
		t.Fatalf("zero timeout error = %v", err)
	}
	client, err := newDaemonFileBridgeHTTPClient(root, cwWtBridgeGeneration)
	if err != nil || client == nil {
		t.Fatalf("valid client = %v, %v", client, err)
	}
}

func TestCwWtDaemonFileBridgeRoundTripRejectsBadRequests(t *testing.T) {
	t.Run("unreadable body", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 2*time.Second)
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, cwWtBridgeErrorReader{})
		if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "read daemon RPC for file bridge") {
			t.Fatalf("unreadable body error = %v", err)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 2*time.Second)
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, bytes.NewReader(make([]byte, daemonFileBridgeMaxBytes+64)))
		if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized body error = %v", err)
		}
	})

	t.Run("unreadable request directory", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 2*time.Second)
		body, err := proto.Marshal(&daemonv1.SubmitOperationRequest{WorkingDirectory: "/tmp", Argv: []string{"go", "version"}, TargetWorkerId: "cwWt-worker"})
		if err != nil {
			t.Fatal(err)
		}
		transport.requests = filepath.Join(t.TempDir(), "cwWt-absent-requests")
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceSubmitOperationProcedure, bytes.NewReader(body))
		if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("unreadable request directory error = %v", err)
		}
	})

	t.Run("unknown procedure", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 2*time.Second)
		request := httptest.NewRequest(http.MethodPost, "/cwWt.unknown.Service/DoThing", bytes.NewReader(nil))
		if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "refused an unknown RPC procedure") {
			t.Fatalf("unknown procedure error = %v", err)
		}
	})

	t.Run("unwritable request directory", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 2*time.Second)
		blocked := filepath.Join(t.TempDir(), "cwWt-requests-file")
		if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		transport.requests = blocked
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, bytes.NewReader(nil))
		if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "create daemon file bridge envelope") {
			t.Fatalf("unwritable request directory error = %v", err)
		}
	})
}

func TestCwWtDaemonFileBridgeRoundTripValidatesResponses(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 6*time.Second)
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, bytes.NewReader(nil))
		done := cwWtBridgeRoundTripAsync(transport, request)
		id := cwWtBridgeResolveRequestID(t, transport.requests)
		forged := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: transport.generation, StatusCode: http.StatusOK}
		forged.PayloadSHA256 = daemonFilePayloadDigest(forged)
		forged.MAC = daemonFileEnvelopeMAC(forged, "cwWt-someone-elses-key")
		if err := writeDaemonFileEnvelope(transport.responses, id, forged); err != nil {
			t.Fatal(err)
		}
		if err := cwWtBridgeAwaitRoundTrip(t, done); err == nil || !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("unauthenticated response error = %v", err)
		}
	})

	t.Run("worker fence", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 6*time.Second)
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, bytes.NewReader(nil))
		done := cwWtBridgeRoundTripAsync(transport, request)
		id := cwWtBridgeResolveRequestID(t, transport.requests)
		cwWtBridgeReply(t, transport, id, func(response *daemonFileEnvelope) {
			response.TargetWorkerID = "cwWt-intruder"
		})
		if err := cwWtBridgeAwaitRoundTrip(t, done); err == nil || !strings.Contains(err.Error(), "fence does not match the request") {
			t.Fatalf("worker fence error = %v", err)
		}
	})

	t.Run("scheduler generation", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 6*time.Second)
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, bytes.NewReader(nil))
		done := cwWtBridgeRoundTripAsync(transport, request)
		id := cwWtBridgeResolveRequestID(t, transport.requests)
		cwWtBridgeReply(t, transport, id, func(response *daemonFileEnvelope) {
			response.SchedulerGeneration = "cwWt-other-generation"
		})
		if err := cwWtBridgeAwaitRoundTrip(t, done); err == nil || !strings.Contains(err.Error(), "scheduler generation response changed") {
			t.Fatalf("scheduler generation error = %v", err)
		}
	})

	t.Run("unreadable response", func(t *testing.T) {
		transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 6*time.Second)
		request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, bytes.NewReader(nil))
		done := cwWtBridgeRoundTripAsync(transport, request)
		id := cwWtBridgeResolveRequestID(t, transport.requests)
		if err := os.Mkdir(filepath.Join(transport.responses, id+".json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := cwWtBridgeAwaitRoundTrip(t, done); err == nil || !strings.Contains(err.Error(), "not a bounded regular file") {
			t.Fatalf("unreadable response error = %v", err)
		}
	})
}

func TestCwWtDaemonFileBridgeRoundTripHonoursCancellation(t *testing.T) {
	transport := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 6*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, daemonv1connect.DaemonServiceGetDaemonInfoProcedure, bytes.NewReader(nil)).WithContext(ctx)
	_, err := transport.RoundTrip(request)
	if err == nil || !strings.Contains(err.Error(), "was interrupted") {
		t.Fatalf("cancelled round trip error = %v", err)
	}
}

func TestCwWtDaemonFileBridgePendingSubmitSkipsUnrelatedEnvelopes(t *testing.T) {
	root := cwWtBridgeClientRoot(t)
	transport := cwWtBridgeTransport(t, root, 2*time.Second)
	key, err := daemonFileBridgeKey(root, false)
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.Marshal(&daemonv1.SubmitOperationRequest{WorkingDirectory: "/tmp", Argv: []string{"go", "version"}, TargetWorkerId: "cwWt-worker"})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(transport.requests, "cwWt-subdir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transport.requests, "cwWt-note.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transport.requests, "cwWt-garbage.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherProcedure := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-other-procedure", SchedulerGeneration: cwWtBridgeGeneration, Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure}
	otherProcedure.PayloadSHA256 = daemonFilePayloadDigest(otherProcedure)
	otherProcedure.MAC = daemonFileEnvelopeMAC(otherProcedure, key)
	if err := writeDaemonFileEnvelope(transport.requests, otherProcedure.ID, otherProcedure); err != nil {
		t.Fatal(err)
	}
	undecodableBody := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-undecodable-body", SchedulerGeneration: cwWtBridgeGeneration, Procedure: daemonv1connect.DaemonServiceSubmitOperationProcedure, Body: []byte{0x0f}}
	undecodableBody.PayloadSHA256 = daemonFilePayloadDigest(undecodableBody)
	undecodableBody.MAC = daemonFileEnvelopeMAC(undecodableBody, key)
	if err := writeDaemonFileEnvelope(transport.requests, undecodableBody.ID, undecodableBody); err != nil {
		t.Fatal(err)
	}
	forged := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-forged-submit", SchedulerGeneration: cwWtBridgeGeneration, Procedure: daemonv1connect.DaemonServiceSubmitOperationProcedure, Body: body}
	forged.PayloadSHA256 = daemonFilePayloadDigest(forged)
	forged.MAC = daemonFileEnvelopeMAC(forged, "cwWt-someone-elses-key")
	if err := writeDaemonFileEnvelope(transport.requests, forged.ID, forged); err != nil {
		t.Fatal(err)
	}

	id, recovered, generation, err := transport.pendingSubmit(daemonv1connect.DaemonServiceSubmitOperationProcedure, body)
	if err != nil || id != "" || recovered != nil || generation != "" {
		t.Fatalf("pendingSubmit with no eligible envelope = %q, %v, %q, %v", id, recovered, generation, err)
	}
	if _, _, _, err := transport.pendingSubmit(daemonv1connect.DaemonServiceGetDaemonInfoProcedure, body); err != nil {
		t.Fatalf("non-submit pendingSubmit error = %v", err)
	}

	matching := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-matching-submit", SchedulerGeneration: cwWtBridgeGeneration, Procedure: daemonv1connect.DaemonServiceSubmitOperationProcedure, Body: body}
	matching.PayloadSHA256 = daemonFilePayloadDigest(matching)
	matching.MAC = daemonFileEnvelopeMAC(matching, key)
	if err := writeDaemonFileEnvelope(transport.requests, matching.ID, matching); err != nil {
		t.Fatal(err)
	}
	id, recovered, generation, err = transport.pendingSubmit(daemonv1connect.DaemonServiceSubmitOperationProcedure, body)
	if err != nil || id != matching.ID || generation != cwWtBridgeGeneration || !bytes.Equal(recovered, body) {
		t.Fatalf("pendingSubmit recovery = %q, %v, %q, %v", id, recovered, generation, err)
	}

	broken := cwWtBridgeTransport(t, cwWtBridgeClientRoot(t), 2*time.Second)
	broken.requests = filepath.Join(t.TempDir(), "cwWt-absent-requests")
	if _, _, _, err := broken.pendingSubmit(daemonv1connect.DaemonServiceSubmitOperationProcedure, body); err == nil {
		t.Fatal("pendingSubmit accepted an unreadable request directory")
	}
}
