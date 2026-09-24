//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

func TestDaemonFileBridgeTargetsOneOfTwoWorkersAndCancelsWithoutLease(t *testing.T) {
	root := daemonTestRoot(t)
	client, stop := startTestDaemonFileBridge(t, root, "bridge-token", "7")
	defer stop()
	ctx := context.Background()
	operation := submitTestWorkerOperation(t, ctx, client, root, "worker-a", "target-a")
	workerA := registerTestBridgeWorker(t, ctx, client, root, "worker-a")
	workerB := registerTestBridgeWorker(t, ctx, client, root, "worker-b")

	wrong, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{WorkerId: workerB.WorkerId, WorkerGeneration: workerB.WorkerGeneration, WaitMilliseconds: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Msg.Assignment.OperationId != "" {
		t.Fatalf("worker-b received worker-a operation: %#v", wrong.Msg.Assignment)
	}
	cancelled, err := client.CancelOperation(ctx, connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: operation.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Msg.State != daemonv1.OperationState_OPERATION_STATE_CANCELLED {
		t.Fatalf("cancelled state = %s", cancelled.Msg.State)
	}
	afterCancel, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{WorkerId: workerA.WorkerId, WorkerGeneration: workerA.WorkerGeneration, WaitMilliseconds: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if afterCancel.Msg.Assignment.OperationId != "" {
		t.Fatalf("cancelled operation was leased: %#v", afterCancel.Msg.Assignment)
	}
	running := submitTestWorkerOperation(t, ctx, client, root, "worker-a", "cancel-running")
	runningLease, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{WorkerId: workerA.WorkerId, WorkerGeneration: workerA.WorkerGeneration, WaitMilliseconds: 1}))
	if err != nil || runningLease.Msg.Assignment.OperationId != running.OperationId {
		t.Fatalf("running cancellation lease = %#v, %v", runningLease.Msg.Assignment, err)
	}
	if _, err := client.CancelOperation(ctx, connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: running.OperationId})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.HeartbeatOperation(ctx, connect.NewRequest(&daemonv1.HeartbeatOperationRequest{WorkerId: workerA.WorkerId, WorkerGeneration: workerA.WorkerGeneration, OperationId: running.OperationId, LeaseId: runningLease.Msg.Assignment.LeaseId, Progress: "must stop"})); err == nil {
		t.Fatal("cancelled running lease accepted another heartbeat")
	}
}

func TestDaemonFileBridgeCompletesWorkerOperationEndToEndWithoutSocket(t *testing.T) {
	root := daemonTestRoot(t)
	client, stop := startTestDaemonFileBridge(t, root, "bridge-token", "8")
	defer stop()
	ctx := context.Background()
	operation := submitTestWorkerOperation(t, ctx, client, root, "worker-a", "complete")
	registration := registerTestBridgeWorker(t, ctx, client, root, "worker-a")
	leased, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1}))
	if err != nil {
		t.Fatal(err)
	}
	assignment := leased.Msg.Assignment
	if assignment.OperationId != operation.OperationId || assignment.WorkerId != "worker-a" || assignment.SchedulerGeneration != "8" {
		t.Fatalf("assignment = %#v", assignment)
	}
	if _, err := client.HeartbeatOperation(ctx, connect.NewRequest(&daemonv1.HeartbeatOperationRequest{WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, Progress: "running"})); err != nil {
		t.Fatal(err)
	}
	completed, err := client.CompleteOperation(ctx, connect.NewRequest(&daemonv1.CompleteOperationRequest{WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, StdoutTail: []byte("done\n")}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.Operation.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED || string(completed.Msg.Operation.StdoutTail) != "done\n" {
		t.Fatalf("completion = %#v", completed.Msg.Operation)
	}
	journal, err := os.ReadFile(filepath.Join(root, ".wb", "runtime", "daemon", "operations", operation.OperationId+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journal, []byte(`"environment"`)) || bytes.Contains(journal, []byte("bridge-token")) {
		t.Fatalf("journal persisted environment or bridge credential: %s", journal)
	}
}

func TestDaemonFileBridgeQueuedWorkSurvivesRestartAndSameWorkerReconnects(t *testing.T) {
	root := daemonTestRoot(t)
	client1, stop1 := startTestDaemonFileBridge(t, root, "first-token", "10")
	operation := submitTestWorkerOperation(t, context.Background(), client1, root, "stable-worker", "restart")
	stop1()

	client2, stop2 := startTestDaemonFileBridge(t, root, "second-token", "11")
	defer stop2()
	registration := registerTestBridgeWorker(t, context.Background(), client2, root, "stable-worker")
	leased, err := client2.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if leased.Msg.Assignment.OperationId != operation.OperationId || leased.Msg.Assignment.SchedulerGeneration != "11" {
		t.Fatalf("restarted assignment = %#v", leased.Msg.Assignment)
	}

	staleHTTP, err := newDaemonFileBridgeHTTPClient(root, "10")
	if err != nil {
		t.Fatal(err)
	}
	stale := daemonv1connect.NewDaemonServiceClient(staleHTTP, daemonRPCBaseURL)
	if _, err := stale.RegisterWorker(context.Background(), connect.NewRequest(&daemonv1.RegisterWorkerRequest{WorkerId: "stable-worker", Build: "old", ProtocolVersion: daemon.ProtocolVersion, Os: "test", Arch: "test", CpuCapacity: 1, PermittedRoots: []string{root}})); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("stale generation error = %v", err)
	}
}

// TestDaemonFileBridgeRetryRecoversSubmitAcrossTokenAndGenerationRotation
// proves that a submit whose response is lost can be retried and recovered
// once the bridge's owner token and scheduler generation rotate.
//
// It used to force the "lost response" case with a 600ms client timeout
// racing the bridge's 250ms poll interval, then read the durable operations
// directory once, immediately, hoping the bridge had already written the
// record (sneat-dev/wb#504: flaky under CI load, because that race assumed
// scheduling was fast enough on an idle machine).
//
// The race is not in the client's timeout itself: the file bridge always
// drops this procedure's response, so the client is always going to time
// out, deterministically, once its own deadline elapses -- cancelling the
// request context instead does not help, because connectrpc.com/connect's
// unary send path checks ctx.Err() after the round trip and, if it is
// non-nil, discards whatever detailed error the transport returned in favor
// of a bare "context canceled"/"context deadline exceeded" (see
// duplexHTTPCall.sendUnary / wrapIfContextError in
// connectrpc.com/connect@v1.20.0/duplex_http_call.go and error.go), so a
// cancelled context here would silently break the "wbfb-...retry the exact
// command" assertion instead of fixing a flake. The real race was in
// checking the durable operations directory immediately after that timeout,
// hoping the bridge had already noticed the request and written the record
// within the same short window.
//
// server.dispatch in daemon_file_bridge.go runs server.handler.ServeHTTP
// (which durably writes the operation record) *before* it consults
// dropResponse (the dropResponse check follows the ServeHTTP call). So the
// moment the test's dropResponse hook is invoked for SubmitOperation is
// itself the exact, race-free signal that the record already exists. The
// test captures the operations directory listing right there, event-driven,
// instead of polling for it after the fact; the client is left to reach its
// own real (generous, non-racing) deadline on an unmodified background
// context, which is what preserves the informative timeout error connect
// would otherwise discard.
func TestDaemonFileBridgeRetryRecoversSubmitAcrossTokenAndGenerationRotation(t *testing.T) {
	root := daemonTestRoot(t)
	service1, err := daemonTestService(t, root, "old-build", "30", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	responseDropped := make(chan struct{})
	var dropOnce sync.Once
	var operationEntries []os.DirEntry
	var operationsErr error
	first, stop1 := startTestDaemonFileBridgeWithServiceAndTimeout(t, root, "old-owner-token", "30", service1, func(procedure string) bool {
		if procedure != daemonv1connect.DaemonServiceSubmitOperationProcedure {
			return false
		}
		dropOnce.Do(func() {
			// The bridge has already durably written the operation record for
			// this submit by this point (see the function comment), so this
			// snapshot is race-free: no poll can ever observe a partial write.
			operationEntries, operationsErr = os.ReadDir(filepath.Join(root, ".wb", "runtime", "daemon", "operations"))
			close(responseDropped)
		})
		return true
	}, 2*time.Second)
	_, firstErr := first.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"go", "version"}, TargetWorkerId: "stable-worker"}))
	if firstErr == nil {
		t.Fatal("dropped submission response unexpectedly succeeded")
	}
	if !strings.Contains(firstErr.Error(), "wbfb-") || !strings.Contains(firstErr.Error(), "retry the exact command") {
		t.Fatalf("lost-submit recovery guidance = %v", firstErr)
	}
	// The client only stops waiting after its own deadline elapses with no
	// response ever written, and the bridge only ever reaches that state by
	// first calling dropResponse (there is no other way for this procedure
	// to resolve), so this receive cannot block: it exists to establish the
	// happens-before edge for the snapshot below, not to wait for anything.
	<-responseDropped
	if operationsErr != nil || len(operationEntries) != 1 {
		t.Fatalf("old daemon operation count = %d, %v", len(operationEntries), operationsErr)
	}
	wantOperationID := strings.TrimSuffix(operationEntries[0].Name(), ".json")
	stop1()

	service2, err := daemonTestService(t, root, "new-build", "31", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	second, stop := startTestDaemonFileBridgeWithService(t, root, "rotated-owner-token", "31", service2, nil)
	defer stop()
	recovered, err := second.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"go", "version"}, TargetWorkerId: "stable-worker"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recovered.Msg.IdempotencyKey, "wbfb-") {
		t.Fatalf("recovered idempotency key = %q", recovered.Msg.IdempotencyKey)
	}
	if recovered.Msg.OperationId != wantOperationID {
		t.Fatalf("recovered operation = %q, want %q", recovered.Msg.OperationId, wantOperationID)
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".wb", "runtime", "daemon", "operations")); err != nil || len(entries) != 1 {
		t.Fatalf("durable operation count = %d, %v", len(entries), err)
	}
}

func TestDaemonFileBridgeIdenticalSubmitReusesUnresolvedEnvelope(t *testing.T) {
	root := daemonTestRoot(t)
	if _, err := daemonFileBridgeKey(root, true); err != nil {
		t.Fatal(err)
	}
	httpClient, err := newDaemonFileBridgeHTTPClientWithTimeout(root, "32", 600*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	client := daemonv1connect.NewDaemonServiceClient(httpClient, daemonRPCBaseURL)
	request := func(key string) error {
		_, callErr := client.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{IdempotencyKey: key, WorkingDirectory: root, Argv: []string{"go", "version"}, TargetWorkerId: "stable-worker"}))
		return callErr
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- request("") }()
	waitForBridgeRequest(t, root)
	secondDone := make(chan error, 1)
	go func() { secondDone <- request("") }()
	for _, done := range []chan error{firstDone, secondDone} {
		if err := <-done; err == nil || !strings.Contains(err.Error(), "retry the exact command") {
			t.Fatalf("identical unresolved submit error = %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(mustDaemonPath(t, daemonFileBridgeDirectory, root), "requests"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("identical unresolved request count = %d, %v", len(entries), err)
	}
	if err := request("intentional-second"); err == nil {
		t.Fatal("explicitly distinct submit unexpectedly received a daemon response")
	}
	entries, err = os.ReadDir(filepath.Join(mustDaemonPath(t, daemonFileBridgeDirectory, root), "requests"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("explicitly distinct request count = %d, %v", len(entries), err)
	}
}

func TestDaemonFileBridgeRunningLeaseBecomesRecoveryRequiredAfterRestart(t *testing.T) {
	root := daemonTestRoot(t)
	client1, stop1 := startTestDaemonFileBridge(t, root, "lease-token", "20")
	operation := submitTestWorkerOperation(t, context.Background(), client1, root, "stable-worker", "leased-before-restart")
	registration1 := registerTestBridgeWorker(t, context.Background(), client1, root, "stable-worker")
	leased, err := client1.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{WorkerId: registration1.WorkerId, WorkerGeneration: registration1.WorkerGeneration, WaitMilliseconds: 1}))
	if err != nil || leased.Msg.Assignment.OperationId != operation.OperationId {
		t.Fatalf("initial lease = %#v, %v", leased.Msg.Assignment, err)
	}
	stop1()

	client2, stop2 := startTestDaemonFileBridge(t, root, "replacement-token", "21")
	defer stop2()
	recovered, err := client2.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: operation.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED {
		t.Fatalf("restarted running operation = %#v", recovered.Msg)
	}
	registration2 := registerTestBridgeWorker(t, context.Background(), client2, root, "stable-worker")
	retry, err := client2.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{WorkerId: registration2.WorkerId, WorkerGeneration: registration2.WorkerGeneration, WaitMilliseconds: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if retry.Msg.Assignment.OperationId != "" {
		t.Fatalf("recovery-required operation was leased twice: %#v", retry.Msg.Assignment)
	}
}

func TestDaemonFileBridgeRequestSurvivesBridgeProcessRestart(t *testing.T) {
	root := daemonTestRoot(t)
	if _, err := daemonFileBridgeKey(root, true); err != nil {
		t.Fatal(err)
	}
	httpClient, err := newDaemonFileBridgeHTTPClient(root, "12")
	if err != nil {
		t.Fatal(err)
	}
	client := daemonv1connect.NewDaemonServiceClient(httpClient, daemonRPCBaseURL)
	type result struct {
		response *connect.Response[daemonv1.GetDaemonInfoResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, callErr := client.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{}))
		done <- result{response: response, err: callErr}
	}()
	waitForBridgeRequest(t, root)
	_, stop := startTestDaemonFileBridge(t, root, "restart-token", "12")
	defer stop()
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.response.Msg.SchedulerGeneration != "12" {
			t.Fatalf("recovered request = %#v, %v", outcome.response, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("persisted bridge request was not recovered")
	}
}

func TestDaemonFileBridgeRejectsTamperedDigestAndRawOrEnvironmentRequests(t *testing.T) {
	root := daemonTestRoot(t)
	requests, responses, err := prepareDaemonFileBridge(root)
	if err != nil {
		t.Fatal(err)
	}
	key, err := daemonFileBridgeKey(root, true)
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.Marshal(&daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"go", "test"}, TargetWorkerId: "worker-a"})
	if err != nil {
		t.Fatal(err)
	}
	envelope := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "tampered", SchedulerGeneration: "13", TargetWorkerID: "worker-a", Procedure: daemonv1connect.DaemonServiceSubmitOperationProcedure, Header: map[string][]string{"Content-Type": {"application/proto"}}, Body: body}
	envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
	envelope.MAC = daemonFileEnvelopeMAC(envelope, key)
	envelope.Body[0] ^= 0xff
	if err := writeDaemonFileEnvelope(requests, envelope.ID, envelope); err != nil {
		t.Fatal(err)
	}
	_, stop := startTestDaemonFileBridge(t, root, "tamper-token", "13")
	defer stop()
	responsePath := filepath.Join(responses, envelope.ID+".json")
	response := waitForBridgeResponse(t, responsePath)
	if err := verifyDaemonFileEnvelope(response, key); err != nil || !strings.Contains(response.Error, "digest mismatch") {
		t.Fatalf("tamper response = %#v, verify=%v", response, err)
	}

	for name, message := range map[string]*daemonv1.SubmitOperationRequest{
		"raw":         {WorkingDirectory: root, Argv: []string{"go", "test"}, LocalRawCommand: true},
		"environment": {WorkingDirectory: root, Argv: []string{"go", "test"}, TargetWorkerId: "worker-a", Environment: map[string]string{"TOKEN": "secret"}},
	} {
		payload, marshalErr := proto.Marshal(message)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, targetErr := daemonFileRequestTarget(daemonv1connect.DaemonServiceSubmitOperationProcedure, payload); targetErr == nil {
			t.Fatalf("%s request entered file bridge", name)
		}
	}
}

func TestDaemonFileBridgeRefusesAmbiguousWorkerRPCReplay(t *testing.T) {
	root := daemonTestRoot(t)
	requests, responses, err := prepareDaemonFileBridge(root)
	if err != nil {
		t.Fatal(err)
	}
	key, err := daemonFileBridgeKey(root, true)
	if err != nil {
		t.Fatal(err)
	}
	types := []struct {
		name      string
		procedure string
		message   proto.Message
	}{
		{"register", daemonv1connect.DaemonServiceRegisterWorkerProcedure, &daemonv1.RegisterWorkerRequest{WorkerId: "worker-a"}},
		{"lease", daemonv1connect.DaemonServiceLeaseOperationProcedure, &daemonv1.LeaseOperationRequest{WorkerId: "worker-a"}},
		{"heartbeat", daemonv1connect.DaemonServiceHeartbeatOperationProcedure, &daemonv1.HeartbeatOperationRequest{WorkerId: "worker-a"}},
		{"complete", daemonv1connect.DaemonServiceCompleteOperationProcedure, &daemonv1.CompleteOperationRequest{WorkerId: "worker-a"}},
		{"disconnect", daemonv1connect.DaemonServiceDisconnectWorkerProcedure, &daemonv1.DisconnectWorkerRequest{WorkerId: "worker-a"}},
	}
	called := 0
	server, err := newDaemonFileBridgeServer(root, "owner-token", "40", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range types {
		body, marshalErr := proto.Marshal(item.message)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		envelope := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: item.name, SchedulerGeneration: "40", TargetWorkerID: "worker-a", Procedure: item.procedure, Header: map[string][]string{"Content-Type": {"application/proto"}}, Body: body}
		envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
		envelope.MAC = daemonFileEnvelopeMAC(envelope, key)
		if err := writeDaemonFileEnvelope(requests, item.name, envelope); err != nil {
			t.Fatal(err)
		}
		marker := envelope
		marker.Body, marker.Header = nil, nil
		marker.PayloadSHA256 = daemonFilePayloadDigest(marker)
		marker.MAC = daemonFileEnvelopeMAC(marker, key)
		if err := writeDaemonFileEnvelope(server.inflight, item.name, marker); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	for _, item := range types {
		response := waitForBridgeResponse(t, filepath.Join(responses, item.name+".json"))
		if verifyDaemonFileEnvelope(response, key) != nil || !strings.Contains(response.Error, "ambiguous") {
			t.Fatalf("%s ambiguous response = %#v", item.name, response)
		}
	}
	cancel()
	<-done
	if called != 0 {
		t.Fatalf("ambiguous worker RPCs reached handler %d times", called)
	}
}

// A symlinked *home* is deliberately not this test's subject. WB_HOME is
// resolved — symlinks followed — by the one home resolver every WB subsystem
// shares, before the daemon ever sees the path, so the daemon's runtime
// directory can no longer contain a symlinked home component. What the daemon
// still owns is the runtime directory below that resolved home: it must be a
// real directory, and a symlink planted there must not become a write path
// into whatever it points at.
func TestDaemonFileBridgeRejectsSymlinkedRuntimeParent(t *testing.T) {
	root := daemonTestRoot(t)
	escape := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(root, ".wb", "runtime")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareDaemonFileBridge(root); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("symlinked runtime error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(escape, "file-bridge")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape target was modified: %v", err)
	}
}

func TestDaemonFileBridgeQuarantineIsBounded(t *testing.T) {
	root := daemonTestRoot(t)
	requests, _, err := prepareDaemonFileBridge(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(filepath.Dir(requests), "quarantine")
	if err := secureBridgeDirectory(directory); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < daemonFileBridgeQuarantineLimit+12; index++ {
		path := filepath.Join(directory, fmt.Sprintf("old-%03d.json", index))
		if err := os.WriteFile(path, []byte("quarantined"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupDaemonFileQuarantine(directory, time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != daemonFileBridgeQuarantineLimit {
		t.Fatalf("quarantine entries = %d, want %d", len(entries), daemonFileBridgeQuarantineLimit)
	}
}

func TestDaemonFileBridgeCleansStaleValidAndOrphanedEnvelopes(t *testing.T) {
	root := daemonTestRoot(t)
	server, err := newDaemonFileBridgeServer(root, "owner-token", "50", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeSigned := func(directory, id, procedure string, modified time.Time) {
		t.Helper()
		envelope := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: "50", Procedure: procedure}
		envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
		envelope.MAC = daemonFileEnvelopeMAC(envelope, server.key)
		if err := writeDaemonFileEnvelope(directory, id, envelope); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, id+".json")
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	completed := now.Add(-daemonFileBridgeCompletedAge - time.Hour)
	expired := now.Add(-daemonFileBridgeRequestAge - time.Hour)
	writeSigned(server.requests, "completed", daemonv1connect.DaemonServiceGetDaemonInfoProcedure, completed)
	writeSigned(server.responses, "completed", "", completed)
	writeSigned(server.responses, "orphan-response", "", completed)
	writeSigned(server.inflight, "orphan-marker", "", completed)
	writeSigned(server.requests, "expired", daemonv1connect.DaemonServiceGetDaemonInfoProcedure, expired)

	if err := server.scan(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(server.requests, "completed.json"),
		filepath.Join(server.responses, "completed.json"),
		filepath.Join(server.responses, "orphan-response.json"),
		filepath.Join(server.inflight, "orphan-marker.json"),
		filepath.Join(server.requests, "expired.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale bridge file remains at %s: %v", path, err)
		}
	}
	recovery := waitForBridgeResponse(t, filepath.Join(server.responses, "expired.json"))
	if verifyDaemonFileEnvelope(recovery, server.key) != nil || !strings.Contains(recovery.Error, "request expired") {
		t.Fatalf("expired request recovery = %#v", recovery)
	}
	quarantine, err := os.ReadDir(filepath.Join(filepath.Dir(server.requests), "quarantine"))
	if err != nil || len(quarantine) != 0 {
		t.Fatalf("expired request quarantine cleanup = %d, %v", len(quarantine), err)
	}
}

func TestDaemonFileBridgeStaleRequestSweepLeavesNonRequestEntries(t *testing.T) {
	root := daemonTestRoot(t)
	server, err := newDaemonFileBridgeServer(root, "owner-token", "50", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	// The requests directory can also hold diagnostics and nested directories.
	// Neither is a request envelope, even when it is older than the sweep age.
	textPath := filepath.Join(server.requests, "notes.txt")
	if err := os.WriteFile(textPath, []byte("keep diagnostics\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	directoryPath := filepath.Join(server.requests, "nested.json")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-daemonFileBridgeRequestAge - time.Hour)
	if err := os.Chtimes(textPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(directoryPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := server.cleanupStale(time.Now()); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(textPath); err != nil || string(content) != "keep diagnostics\n" {
		t.Fatalf("non-JSON diagnostic changed during request sweep: %q, %v", content, err)
	}
	if info, err := os.Stat(directoryPath); err != nil || !info.IsDir() {
		t.Fatalf("nested directory changed during request sweep: %v, %v", info, err)
	}
}

func TestDaemonFileBridgeReportsResponsePersistenceFailure(t *testing.T) {
	root := daemonTestRoot(t)
	called := 0
	server, err := newDaemonFileBridgeServer(root, "owner-token", "51", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		called++
		writer.WriteHeader(http.StatusOK)
	}))
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.Marshal(&daemonv1.GetDaemonInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	envelope := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "persist-failure", SchedulerGeneration: "51", Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure, Header: map[string][]string{"Content-Type": {"application/proto"}}, Body: body}
	envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
	envelope.MAC = daemonFileEnvelopeMAC(envelope, server.key)
	if err := writeDaemonFileEnvelope(server.requests, envelope.ID, envelope); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(server.responses, server.responses+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server.responses, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.process(envelope.ID)
	select {
	case observed := <-server.errors:
		if !strings.Contains(observed.Error(), "persist daemon file bridge response") {
			t.Fatalf("reported persistence error = %v", observed)
		}
	default:
		t.Fatal("response persistence failure was not observable")
	}
	if called != 1 {
		t.Fatalf("handler calls = %d, want 1", called)
	}
}

func TestDaemonOperationClientFallsBackOnlyForUnreachableSocket(t *testing.T) {
	root := daemonTestRoot(t)
	service, err := daemonTestService(t, root, "test-build", "1", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, authenticatedDaemonHandler("fallback-token", handler))
	bridge, err := newDaemonFileBridgeServer(root, "fallback-token", "1", mux)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- bridge.Serve(ctx) }()
	defer func() { cancel(); <-bridgeDone }()

	deps := daemonTestDependencies(t, root)
	provenance, err := newDaemonController(deps, root).provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, daemonDefaultListen, provenance, "fallback-token", deps.now())
	state.MarkReady(777, deps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 777 }
	deps.health = func(context.Context, string) error { return syscall.EPERM }
	deps.localClient = func(string, string) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, syscall.EPERM })}, nil
	}
	var progress bytes.Buffer
	client, err := daemonOperationClient(context.Background(), deps, root, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), "protected project-root file bridge") {
		t.Fatalf("fallback progress = %q", progress.String())
	}
	if _, err := client.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{})); err != nil {
		t.Fatal(err)
	}
	if daemonFileBridgeFallbackAllowed(connect.NewError(connect.CodeUnauthenticated, errors.New("bad token"))) {
		t.Fatal("authentication failure selected file bridge")
	}
}

func TestDaemonFileBridgeHealthVerifiesSchedulerGeneration(t *testing.T) {
	root := daemonTestRoot(t)
	_, stop := startTestDaemonFileBridge(t, root, "health-token", "61")
	defer stop()
	if err := daemonFileBridgeHealthy(context.Background(), root, "61"); err != nil {
		t.Fatal(err)
	}
	if err := daemonFileBridgeHealthy(context.Background(), root, "60"); err == nil || !strings.Contains(err.Error(), "generation is 61, want 60") {
		t.Fatalf("stale health generation error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func startTestDaemonFileBridge(t *testing.T, root, token, generation string) (daemonv1connect.DaemonServiceClient, func()) {
	t.Helper()
	service, err := daemonTestService(t, root, "test-build", generation, func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	return startTestDaemonFileBridgeWithService(t, root, token, generation, service, nil)
}

func startTestDaemonFileBridgeWithService(t *testing.T, root, token, generation string, service *daemon.Service, dropResponse func(string) bool) (daemonv1connect.DaemonServiceClient, func()) {
	return startTestDaemonFileBridgeWithServiceAndTimeout(t, root, token, generation, service, dropResponse, daemonFileBridgeTimeout)
}

func startTestDaemonFileBridgeWithServiceAndTimeout(t *testing.T, root, token, generation string, service *daemon.Service, dropResponse func(string) bool, timeout time.Duration) (daemonv1connect.DaemonServiceClient, func()) {
	t.Helper()
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, authenticatedDaemonHandler(token, handler))
	server, err := newDaemonFileBridgeServer(root, token, generation, mux)
	if err != nil {
		t.Fatal(err)
	}
	server.dropResponse = dropResponse
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	httpClient, err := newDaemonFileBridgeHTTPClientWithTimeout(root, generation, timeout)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	return daemonv1connect.NewDaemonServiceClient(httpClient, daemonRPCBaseURL), stop
}

func submitTestWorkerOperation(t *testing.T, ctx context.Context, client daemonv1connect.DaemonServiceClient, root, workerID, key string) *daemonv1.Operation {
	t.Helper()
	response, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{IdempotencyKey: key, WorkingDirectory: root, Argv: []string{"go", "version"}, TargetWorkerId: workerID}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg
}

func registerTestBridgeWorker(t *testing.T, ctx context.Context, client daemonv1connect.DaemonServiceClient, root, workerID string) *daemonv1.WorkerRegistration {
	t.Helper()
	response, err := client.RegisterWorker(ctx, connect.NewRequest(&daemonv1.RegisterWorkerRequest{WorkerId: workerID, Build: "test-build", ProtocolVersion: daemon.ProtocolVersion, Os: "test", Arch: "test", CpuCapacity: 1, PermittedRoots: []string{root}}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg.Registration
}

func waitForBridgeRequest(t *testing.T, root string) {
	t.Helper()
	requests := filepath.Join(mustDaemonPath(t, daemonFileBridgeDirectory, root), "requests")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(requests)
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				return
			}
		}
		time.Sleep(daemonFileBridgePoll)
	}
	t.Fatal("file bridge request did not appear")
}

func waitForBridgeResponse(t *testing.T, path string) daemonFileEnvelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := readDaemonFileEnvelope(path)
		if err == nil {
			return response
		}
		time.Sleep(daemonFileBridgePoll)
	}
	t.Fatalf("file bridge response did not appear: %s", path)
	return daemonFileEnvelope{}
}
