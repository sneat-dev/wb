//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

// The cwWt* helpers below build real, fully prepared file-bridge servers on
// throwaway temp roots. Every test in this file drives the in-process bridge
// code directly, which is where the statement coverage actually lives; the
// package's older bridge tests exec the built binary and therefore record none.

const (
	cwWtBridgeToken      = "cwWt-bridge-owner-token"
	cwWtBridgeGeneration = "cwWt-generation-1"
)

var cwWtBridgeNoop http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

// cwWtBridgeServer creates a prepared bridge server rooted at a fresh temp dir.
// The root is pinned as the daemon home so the bridge's runtime path resolves
// inside the fixture rather than in the developer's real WB home.
func cwWtBridgeServer(t *testing.T, handler http.Handler) (*daemonFileBridgeServer, string) {
	t.Helper()
	if handler == nil {
		handler = cwWtBridgeNoop
	}
	root := daemonTestRoot(t)
	server, err := newDaemonFileBridgeServer(root, cwWtBridgeToken, cwWtBridgeGeneration, handler)
	if err != nil {
		t.Fatalf("cwWt: newDaemonFileBridgeServer: %v", err)
	}
	return server, root
}

// cwWtBridgeDirs returns the bridge base directory and its quarantine child.
func cwWtBridgeDirs(server *daemonFileBridgeServer) (base, quarantine string) {
	base = filepath.Dir(server.requests)
	return base, filepath.Join(base, "quarantine")
}

// cwWtBridgeSigned returns the envelope with a payload digest and MAC that the
// server's own key will verify.
func cwWtBridgeSigned(server *daemonFileBridgeServer, envelope daemonFileEnvelope) daemonFileEnvelope {
	envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
	envelope.MAC = daemonFileEnvelopeMAC(envelope, server.key)
	return envelope
}

// cwWtBridgePut writes a correctly signed envelope into directory.
func cwWtBridgePut(t *testing.T, server *daemonFileBridgeServer, directory string, envelope daemonFileEnvelope) string {
	t.Helper()
	signed := cwWtBridgeSigned(server, envelope)
	if err := writeDaemonFileEnvelope(directory, signed.ID, signed); err != nil {
		t.Fatalf("cwWt: write envelope %s: %v", signed.ID, err)
	}
	return filepath.Join(directory, signed.ID+".json")
}

// cwWtBridgeResponse reads and authenticates the bridge response for an id.
func cwWtBridgeResponse(t *testing.T, server *daemonFileBridgeServer, id string) daemonFileEnvelope {
	t.Helper()
	envelope, err := readDaemonFileEnvelope(filepath.Join(server.responses, id+".json"))
	if err != nil {
		t.Fatalf("cwWt: read bridge response %s: %v", id, err)
	}
	if err := verifyDaemonFileEnvelope(envelope, server.key); err != nil {
		t.Fatalf("cwWt: verify bridge response %s: %v", id, err)
	}
	return envelope
}

// cwWtBridgeDrainError returns the reported bridge error, if any.
func cwWtBridgeDrainError(server *daemonFileBridgeServer) error {
	select {
	case err := <-server.errors:
		return err
	default:
		return nil
	}
}

// cwWtBridgeStat bridges os.Stat for readability in the assertions below.
func cwWtBridgeStat(path string) error {
	_, err := os.Stat(path)
	return err
}

func TestCwWtDaemonFileBridgeKeyRejectsUnsafeFiles(t *testing.T) {
	root := daemonTestRoot(t)
	if _, _, err := prepareDaemonFileBridge(root); err != nil {
		t.Fatal(err)
	}
	keyPath := mustDaemonPath(t, daemonFileBridgeKeyPath, root)

	if _, err := daemonFileBridgeKey(root, false); err == nil || !strings.Contains(err.Error(), "inspect daemon file bridge key") {
		t.Fatalf("missing key error = %v", err)
	}

	if err := os.Mkdir(keyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonFileBridgeKey(root, false); err == nil || !strings.Contains(err.Error(), "not a regular 32-byte hex key") {
		t.Fatalf("directory key error = %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(keyPath, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonFileBridgeKey(root, false); err == nil || !strings.Contains(err.Error(), "not a regular 32-byte hex key") {
		t.Fatalf("short key error = %v", err)
	}

	hexKey := strings.Repeat("ab", 32)
	if err := os.WriteFile(keyPath, []byte(hexKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonFileBridgeKey(root, false); err == nil || !strings.Contains(err.Error(), "has mode") {
		t.Fatalf("world-readable key error = %v", err)
	}

	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("zz", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonFileBridgeKey(root, false); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("non-hex key error = %v", err)
	}

	if err := os.WriteFile(keyPath, []byte(hexKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := daemonFileBridgeKey(root, false)
	if err != nil || got != hexKey {
		t.Fatalf("valid key = %q, %v", got, err)
	}
}

func TestCwWtDaemonFileBridgeKeyRejectsSymlinkedRuntime(t *testing.T) {
	root := daemonTestRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(escape, filepath.Join(root, ".wb", "runtime")); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonFileBridgeKey(root, true); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("symlinked runtime key error = %v", err)
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Fatalf("symlink escape target was created: %v", err)
	}
}

func TestCwWtDaemonFileBridgeSecureRuntimeRejectsBadRoots(t *testing.T) {
	if err := secureDaemonRuntime(filepath.Join("relative", "cwWt-root")); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative root error = %v", err)
	}

	fileRoot := filepath.Join(t.TempDir(), "cwWt-root-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureDaemonRuntime(fileRoot); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("regular-file root error = %v", err)
	}

	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "cwWt-root-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if err := secureDaemonRuntime(linkRoot); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("symlinked root error = %v", err)
	}

	blocked := daemonTestRoot(t)
	if err := os.WriteFile(filepath.Join(blocked, ".wb"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureDaemonRuntime(blocked); err == nil || !strings.Contains(err.Error(), "daemon file bridge parent is not a real directory") {
		t.Fatalf("regular-file .wb error = %v", err)
	}
}

func TestCwWtDaemonFileBridgeParentDirectoryReportsCreationFailure(t *testing.T) {
	dir := t.TempDir()
	dangling := filepath.Join(dir, "cwWt-dangling")
	if err := os.Symlink(filepath.Join(dir, "cwWt-missing"), dangling); err != nil {
		t.Fatal(err)
	}
	err := secureBridgeParentDirectory(filepath.Join(dangling, ".wb"), false)
	if err == nil || !strings.Contains(err.Error(), "create daemon file bridge parent") {
		t.Fatalf("unmakeable parent error = %v", err)
	}
}

func TestCwWtDaemonFileBridgeSecureDirectoryRejectsBadPaths(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "cwWt-regular")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	dangling := filepath.Join(dir, "cwWt-dangling")
	if err := os.Symlink(filepath.Join(dir, "cwWt-missing", "child"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := secureBridgeDirectory(filepath.Join(dangling, "sub")); err == nil || !strings.Contains(err.Error(), "create daemon file bridge directory") {
		t.Fatalf("unmakeable directory error = %v", err)
	}

	if err := secureBridgeDirectory(filepath.Join(regular, "sub")); err == nil || !strings.Contains(err.Error(), "inspect daemon file bridge directory") {
		t.Fatalf("inspect under file error = %v", err)
	}

	if err := secureBridgeDirectory(regular); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("regular-file directory error = %v", err)
	}

	link := filepath.Join(dir, "cwWt-link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := secureBridgeDirectory(link); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("symlinked directory error = %v", err)
	}

	wrongMode := filepath.Join(dir, "cwWt-wrong-mode")
	if err := os.Mkdir(wrongMode, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wrongMode, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := secureBridgeDirectory(wrongMode); err == nil || !strings.Contains(err.Error(), "has mode") {
		t.Fatalf("wrong-mode directory error = %v", err)
	}
}

func TestCwWtDaemonFileBridgePrepareAndServerRejectBadFixtures(t *testing.T) {
	root := daemonTestRoot(t)
	if _, _, err := prepareDaemonFileBridge(root); err != nil {
		t.Fatal(err)
	}
	base := mustDaemonPath(t, daemonFileBridgeDirectory, root)
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareDaemonFileBridge(root); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("blocked bridge base error = %v", err)
	}

	fileRoot := filepath.Join(t.TempDir(), "cwWt-root-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonFileBridgeServer(fileRoot, cwWtBridgeToken, cwWtBridgeGeneration, cwWtBridgeNoop); err == nil {
		t.Fatal("server accepted a regular-file projects root")
	}

	incomplete := daemonTestRoot(t)
	for name, call := range map[string]func() (*daemonFileBridgeServer, error){
		"blank token": func() (*daemonFileBridgeServer, error) {
			return newDaemonFileBridgeServer(incomplete, "  ", cwWtBridgeGeneration, cwWtBridgeNoop)
		},
		"blank generation": func() (*daemonFileBridgeServer, error) {
			return newDaemonFileBridgeServer(incomplete, cwWtBridgeToken, "  ", cwWtBridgeNoop)
		},
		"nil handler": func() (*daemonFileBridgeServer, error) {
			return newDaemonFileBridgeServer(incomplete, cwWtBridgeToken, cwWtBridgeGeneration, nil)
		},
	} {
		if _, err := call(); err == nil || !strings.Contains(err.Error(), "requires token, scheduler generation, and handler") {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	blockedInflight := daemonTestRoot(t)
	if _, _, err := prepareDaemonFileBridge(blockedInflight); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mustDaemonPath(t, daemonFileBridgeDirectory, blockedInflight), "inflight"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonFileBridgeServer(blockedInflight, cwWtBridgeToken, cwWtBridgeGeneration, cwWtBridgeNoop); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("blocked inflight error = %v", err)
	}

	blockedKey := daemonTestRoot(t)
	if _, _, err := prepareDaemonFileBridge(blockedKey); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mustDaemonPath(t, daemonFileBridgeKeyPath, blockedKey), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonFileBridgeServer(blockedKey, cwWtBridgeToken, cwWtBridgeGeneration, cwWtBridgeNoop); err == nil || !strings.Contains(err.Error(), "not a regular 32-byte hex key") {
		t.Fatalf("blocked key error = %v", err)
	}
}

func TestCwWtDaemonFileBridgeServeReportsScanAndInternalErrors(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	injected := errors.New("cwWt: injected bridge failure")
	server.report(injected)
	err := server.Serve(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("Serve error = %v, want injected failure", err)
	}

	broken, _ := cwWtBridgeServer(t, nil)
	if err := os.RemoveAll(broken.responses); err != nil {
		t.Fatal(err)
	}
	if err := broken.Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "cleanup directory") {
		t.Fatalf("Serve scan error = %v", err)
	}
}

func TestCwWtDaemonFileBridgeScanQuarantinesUnauthenticatedResponse(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	server.lastCleanup = time.Now()
	_, quarantine := cwWtBridgeDirs(server)
	const id = "cwWt-unauthenticated"
	cwWtBridgePut(t, server, server.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration,
		TargetWorkerID: "cwWt-worker", Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	forged := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration, TargetWorkerID: "cwWt-worker", StatusCode: http.StatusOK}
	forged.PayloadSHA256 = daemonFilePayloadDigest(forged)
	forged.MAC = daemonFileEnvelopeMAC(forged, "cwWt-someone-elses-key")
	if err := writeDaemonFileEnvelope(server.responses, id, forged); err != nil {
		t.Fatal(err)
	}

	if err := server.scan(); err != nil {
		t.Fatal(err)
	}
	if err := cwWtBridgeStat(filepath.Join(server.requests, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("unauthenticated request survived quarantine: %v", err)
	}
	response := cwWtBridgeResponse(t, server, id)
	if !strings.Contains(response.Error, "failed authentication") {
		t.Fatalf("unauthenticated response error = %q", response.Error)
	}
	entries, err := os.ReadDir(quarantine)
	if err != nil || len(entries) != 2 {
		t.Fatalf("quarantine entries = %d, %v", len(entries), err)
	}
}

func TestCwWtDaemonFileBridgeScanQuarantinesUnreadableResponse(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	server.lastCleanup = time.Now()
	_, quarantine := cwWtBridgeDirs(server)
	const id = "cwWt-unreadable"
	cwWtBridgePut(t, server, server.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	if err := os.WriteFile(filepath.Join(server.responses, id+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := server.scan(); err != nil {
		t.Fatal(err)
	}
	response := cwWtBridgeResponse(t, server, id)
	if !strings.Contains(response.Error, "unreadable response was quarantined") {
		t.Fatalf("unreadable response error = %q", response.Error)
	}
	entries, err := os.ReadDir(quarantine)
	if err != nil || len(entries) != 2 {
		t.Fatalf("quarantine entries = %d, %v", len(entries), err)
	}
}

func TestCwWtDaemonFileBridgeScanSkipsActiveAndSaturatedWork(t *testing.T) {
	active, _ := cwWtBridgeServer(t, nil)
	active.lastCleanup = time.Now()
	const activeID = "cwWt-active"
	cwWtBridgePut(t, active, active.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: activeID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	active.active[activeID] = true
	if err := active.scan(); err != nil {
		t.Fatal(err)
	}
	if err := cwWtBridgeStat(filepath.Join(active.requests, activeID+".json")); err != nil {
		t.Fatalf("active request disappeared: %v", err)
	}
	if err := cwWtBridgeStat(filepath.Join(active.responses, activeID+".json")); !os.IsNotExist(err) {
		t.Fatalf("active request produced a response: %v", err)
	}

	saturated, _ := cwWtBridgeServer(t, nil)
	saturated.lastCleanup = time.Now()
	const saturatedID = "cwWt-saturated"
	cwWtBridgePut(t, saturated, saturated.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: saturatedID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	for index := 0; index < daemonFileBridgeWorkers; index++ {
		saturated.sem <- struct{}{}
	}
	if err := saturated.scan(); err != nil {
		t.Fatal(err)
	}
	if err := cwWtBridgeStat(filepath.Join(saturated.requests, saturatedID+".json")); err != nil {
		t.Fatalf("saturated request disappeared: %v", err)
	}
	if err := cwWtBridgeStat(filepath.Join(saturated.responses, saturatedID+".json")); !os.IsNotExist(err) {
		t.Fatalf("saturated request produced a response: %v", err)
	}
	for index := 0; index < daemonFileBridgeWorkers; index++ {
		<-saturated.sem
	}
	if len(saturated.active) != 0 {
		t.Fatalf("saturated scan tracked active work: %#v", saturated.active)
	}
}

func TestCwWtDaemonFileBridgeScanRejectsBacklogAndUnreadableRequests(t *testing.T) {
	backlog, _ := cwWtBridgeServer(t, nil)
	if err := backlog.scan(); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < daemonFileBridgeBacklogLimit+1; index++ {
		name := fmt.Sprintf("cwWt-backlog-%05d.json", index)
		if err := os.WriteFile(filepath.Join(backlog.requests, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := backlog.scan(); err == nil || !strings.Contains(err.Error(), "backlog exceeds") {
		t.Fatalf("backlog error = %v", err)
	}

	unreadable, _ := cwWtBridgeServer(t, nil)
	if err := unreadable.scan(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(unreadable.requests); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unreadable.requests, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unreadable.scan(); err == nil || !strings.Contains(err.Error(), "read daemon file bridge requests") {
		t.Fatalf("unreadable requests error = %v", err)
	}
}

func TestCwWtDaemonFileBridgeScanReportsQuarantineFailure(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	server.lastCleanup = time.Now()
	_, quarantine := cwWtBridgeDirs(server)
	const id = "cwWt-quarantine-blocked"
	cwWtBridgePut(t, server, server.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	forged := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration}
	forged.PayloadSHA256 = daemonFilePayloadDigest(forged)
	forged.MAC = daemonFileEnvelopeMAC(forged, "cwWt-someone-elses-key")
	if err := writeDaemonFileEnvelope(server.responses, id, forged); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := server.scan()
	if err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("blocked quarantine scan error = %v", err)
	}
	if reported := cwWtBridgeDrainError(server); reported == nil {
		t.Fatal("blocked quarantine was not reported")
	}
}

func TestCwWtDaemonFileBridgeCleanupCompletedReportsRemovalFailures(t *testing.T) {
	now := time.Now()
	stale := now.Add(-daemonFileBridgeCompletedAge - time.Hour)

	requestBlocked, _ := cwWtBridgeServer(t, nil)
	responsePath := filepath.Join(requestBlocked.responses, "cwWt-cleanup.json")
	if err := os.WriteFile(responsePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(responsePath, stale, stale); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(requestBlocked.requests, "cwWt-cleanup.json")
	if err := os.Mkdir(requestPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(requestPath, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	requestBlocked.cleanupCompleted(requestPath, responsePath, filepath.Join(requestBlocked.inflight, "cwWt-cleanup.json"), now)
	if reported := cwWtBridgeDrainError(requestBlocked); reported == nil || !strings.Contains(reported.Error(), "remove stale daemon file bridge file") {
		t.Fatalf("blocked request removal report = %v", reported)
	}

	markerBlocked, _ := cwWtBridgeServer(t, nil)
	responsePath = filepath.Join(markerBlocked.responses, "cwWt-cleanup.json")
	if err := os.WriteFile(responsePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(responsePath, stale, stale); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(markerBlocked.inflight, "cwWt-cleanup.json")
	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerPath, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerBlocked.cleanupCompleted(filepath.Join(markerBlocked.requests, "cwWt-cleanup.json"), responsePath, markerPath, now)
	if reported := cwWtBridgeDrainError(markerBlocked); reported == nil || !strings.Contains(reported.Error(), "remove stale daemon file bridge file") {
		t.Fatalf("blocked marker removal report = %v", reported)
	}

	responseBlocked, _ := cwWtBridgeServer(t, nil)
	responseDir := filepath.Join(responseBlocked.responses, "cwWt-cleanup.json")
	if err := os.Mkdir(responseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(responseDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(responseDir, stale, stale); err != nil {
		t.Fatal(err)
	}
	responseBlocked.cleanupCompleted(filepath.Join(responseBlocked.requests, "cwWt-cleanup.json"), responseDir, filepath.Join(responseBlocked.inflight, "cwWt-cleanup.json"), now)
	if reported := cwWtBridgeDrainError(responseBlocked); reported == nil || !strings.Contains(reported.Error(), "remove stale daemon file bridge file") {
		t.Fatalf("blocked response removal report = %v", reported)
	}
}

func TestCwWtDaemonFileBridgeCleanupStaleErrorPaths(t *testing.T) {
	now := time.Now()

	missingResponses, _ := cwWtBridgeServer(t, nil)
	if err := os.RemoveAll(missingResponses.responses); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(missingResponses.responses, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := missingResponses.cleanupStale(now); err == nil || !strings.Contains(err.Error(), "cleanup directory") {
		t.Fatalf("missing responses cleanup error = %v", err)
	}

	directoryEntries, _ := cwWtBridgeServer(t, nil)
	if err := os.Mkdir(filepath.Join(directoryEntries.responses, "cwWt-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directoryEntries.inflight, "cwWt-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := directoryEntries.cleanupStale(now); err != nil {
		t.Fatalf("directory entries cleanup error = %v", err)
	}
	for _, path := range []string{filepath.Join(directoryEntries.responses, "cwWt-dir.json"), filepath.Join(directoryEntries.inflight, "cwWt-dir.json")} {
		if err := cwWtBridgeStat(path); err != nil {
			t.Fatalf("directory entry removed by cleanup: %v", err)
		}
	}

	unreadableRequests, _ := cwWtBridgeServer(t, nil)
	staleResponse := filepath.Join(unreadableRequests.responses, "cwWt-stale.json")
	if err := os.WriteFile(staleResponse, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-daemonFileBridgeCompletedAge - time.Hour)
	if err := os.Chtimes(staleResponse, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(unreadableRequests.requests); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unreadableRequests.requests, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unreadableRequests.cleanupStale(now); err == nil || !strings.Contains(err.Error(), "request during cleanup") {
		t.Fatalf("unreadable requests cleanup error = %v", err)
	}

	missingRequests, _ := cwWtBridgeServer(t, nil)
	if err := os.RemoveAll(missingRequests.requests); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(missingRequests.requests, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := missingRequests.cleanupStale(now); err == nil || !strings.Contains(err.Error(), "inspect stale daemon file bridge requests") {
		t.Fatalf("missing requests cleanup error = %v", err)
	}
}

func TestCwWtDaemonFileBridgeCleanupStaleExpiresRequests(t *testing.T) {
	now := time.Now()
	expired := now.Add(-daemonFileBridgeRequestAge - time.Hour)

	active, _ := cwWtBridgeServer(t, nil)
	const activeID = "cwWt-expired-active"
	cwWtBridgePut(t, active, active.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: activeID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	activePath := filepath.Join(active.requests, activeID+".json")
	if err := os.Chtimes(activePath, expired, expired); err != nil {
		t.Fatal(err)
	}
	active.active[activeID] = true
	if err := active.cleanupStale(now); err != nil {
		t.Fatal(err)
	}
	if err := cwWtBridgeStat(activePath); err != nil {
		t.Fatalf("active expired request was quarantined: %v", err)
	}
	if err := cwWtBridgeStat(filepath.Join(active.responses, activeID+".json")); !os.IsNotExist(err) {
		t.Fatalf("active expired request produced a response: %v", err)
	}

	unverifiable, _ := cwWtBridgeServer(t, nil)
	garbagePath := filepath.Join(unverifiable.requests, "cwWt-garbage.json")
	if err := os.WriteFile(garbagePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(garbagePath, expired, expired); err != nil {
		t.Fatal(err)
	}
	forged := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-forged", SchedulerGeneration: cwWtBridgeGeneration, Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure}
	forged.PayloadSHA256 = daemonFilePayloadDigest(forged)
	forged.MAC = daemonFileEnvelopeMAC(forged, "cwWt-someone-elses-key")
	forgedPath := filepath.Join(unverifiable.requests, "cwWt-forged.json")
	if err := writeDaemonFileEnvelope(unverifiable.requests, forged.ID, forged); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(forgedPath, expired, expired); err != nil {
		t.Fatal(err)
	}
	if err := unverifiable.cleanupStale(now); err != nil {
		t.Fatalf("unverifiable cleanup error = %v", err)
	}
	for _, path := range []string{garbagePath, forgedPath} {
		if err := cwWtBridgeStat(path); err != nil {
			t.Fatalf("unverifiable expired request was removed: %v", err)
		}
	}
	if err := cwWtBridgeStat(filepath.Join(unverifiable.responses, "cwWt-forged.json")); !os.IsNotExist(err) {
		t.Fatalf("unverifiable request produced a response: %v", err)
	}
}

func TestCwWtDaemonFileBridgeCleanupStaleReportsQuarantineAndRemovalFailures(t *testing.T) {
	now := time.Now()
	expired := now.Add(-daemonFileBridgeRequestAge - time.Hour)

	quarantineBlocked, _ := cwWtBridgeServer(t, nil)
	_, quarantine := cwWtBridgeDirs(quarantineBlocked)
	const blockedID = "cwWt-quarantine-blocked"
	cwWtBridgePut(t, quarantineBlocked, quarantineBlocked.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: blockedID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	blockedPath := filepath.Join(quarantineBlocked.requests, blockedID+".json")
	if err := os.Chtimes(blockedPath, expired, expired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := quarantineBlocked.cleanupStale(now); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("blocked quarantine cleanup error = %v", err)
	}

	inflightBlocked, _ := cwWtBridgeServer(t, nil)
	const inflightID = "cwWt-inflight-blocked"
	cwWtBridgePut(t, inflightBlocked, inflightBlocked.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: inflightID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	inflightPath := filepath.Join(inflightBlocked.requests, inflightID+".json")
	if err := os.Chtimes(inflightPath, expired, expired); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(inflightBlocked.inflight, inflightID+".json")
	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerPath, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := inflightBlocked.cleanupStale(now)
	if err == nil || !strings.Contains(err.Error(), "remove stale daemon file bridge file") {
		t.Fatalf("blocked inflight removal cleanup error = %v", err)
	}
	if err := cwWtBridgeStat(filepath.Join(inflightBlocked.requests, inflightID+".json")); !os.IsNotExist(err) {
		t.Fatalf("expired request was not quarantined: %v", err)
	}
}

func TestCwWtDaemonFileBridgeRemoveBridgeFileToleratesMissingOnly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "cwWt-missing.json")
	if err := removeBridgeFile(missing); err != nil {
		t.Fatalf("missing file removal error = %v", err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeBridgeFile(directory); err == nil || !strings.Contains(err.Error(), "remove stale daemon file bridge file") {
		t.Fatalf("non-empty directory removal error = %v", err)
	}
}

func TestCwWtDaemonFileBridgeProcessRejectsUnreadableAndMismatchedRequests(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)

	server.process("cwWt-missing")
	missing := cwWtBridgeResponse(t, server, "cwWt-missing")
	if missing.Error == "" || !strings.Contains(missing.Error, "no such file") {
		t.Fatalf("missing request error = %q", missing.Error)
	}

	const mismatchedID = "cwWt-mismatched"
	mismatched := cwWtBridgeSigned(server, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: "cwWt-actual", SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	if err := writeDaemonFileEnvelope(server.requests, mismatchedID, mismatched); err != nil {
		t.Fatal(err)
	}
	server.process(mismatchedID)
	response := cwWtBridgeResponse(t, server, mismatchedID)
	if !strings.Contains(response.Error, "does not match its atomic filename") {
		t.Fatalf("mismatched ID error = %q", response.Error)
	}
}

func TestCwWtDaemonFileBridgeProcessRefusesUnauthenticatedRequest(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	_, quarantine := cwWtBridgeDirs(server)
	const id = "cwWt-forged-request"
	forged := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration, Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure}
	forged.PayloadSHA256 = daemonFilePayloadDigest(forged)
	forged.MAC = daemonFileEnvelopeMAC(forged, "cwWt-someone-elses-key")
	if err := writeDaemonFileEnvelope(server.requests, id, forged); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	server.process(id)
	if err := cwWtBridgeStat(filepath.Join(server.requests, id+".json")); err != nil {
		t.Fatalf("unquarantinable forged request was removed: %v", err)
	}
	if err := cwWtBridgeStat(filepath.Join(server.responses, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("unquarantinable forged request produced a response: %v", err)
	}
	if reported := cwWtBridgeDrainError(server); reported == nil {
		t.Fatal("blocked quarantine was not reported")
	}
}

func TestCwWtDaemonFileBridgeProcessRejectsBadDispatch(t *testing.T) {
	unknown, _ := cwWtBridgeServer(t, nil)
	const unknownID = "cwWt-unknown-procedure"
	cwWtBridgePut(t, unknown, unknown.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: unknownID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: "/cwWt.unknown.Service/DoThing",
	})
	unknown.process(unknownID)
	if response := cwWtBridgeResponse(t, unknown, unknownID); !strings.Contains(response.Error, "refused an unknown RPC procedure") {
		t.Fatalf("unknown procedure error = %q", response.Error)
	}

	mismatch, _ := cwWtBridgeServer(t, nil)
	const mismatchID = "cwWt-target-mismatch"
	body, err := proto.Marshal(&daemonv1.SubmitOperationRequest{WorkingDirectory: "/tmp", Argv: []string{"go", "version"}, TargetWorkerId: "cwWt-worker-a"})
	if err != nil {
		t.Fatal(err)
	}
	cwWtBridgePut(t, mismatch, mismatch.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: mismatchID, SchedulerGeneration: cwWtBridgeGeneration,
		TargetWorkerID: "cwWt-worker-b", Procedure: daemonv1connect.DaemonServiceSubmitOperationProcedure, Body: body,
	})
	mismatch.process(mismatchID)
	if response := cwWtBridgeResponse(t, mismatch, mismatchID); !strings.Contains(response.Error, "target worker fence does not match") {
		t.Fatalf("target mismatch error = %q", response.Error)
	}
}

func TestCwWtDaemonFileBridgeProcessRejectsUnreadableOrUnwritableMarker(t *testing.T) {
	unreadable, _ := cwWtBridgeServer(t, nil)
	const unreadableID = "cwWt-marker-unreadable"
	cwWtBridgePut(t, unreadable, unreadable.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: unreadableID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	if err := os.Mkdir(filepath.Join(unreadable.inflight, unreadableID+".json"), 0o700); err != nil {
		t.Fatal(err)
	}
	unreadable.process(unreadableID)
	if response := cwWtBridgeResponse(t, unreadable, unreadableID); !strings.Contains(response.Error, "dispatch marker is unreadable") {
		t.Fatalf("unreadable marker error = %q", response.Error)
	}

	unwritable, _ := cwWtBridgeServer(t, nil)
	unwritable.inflight = filepath.Join(t.TempDir(), "cwWt-missing-inflight")
	const unwritableID = "cwWt-marker-unwritable"
	cwWtBridgePut(t, unwritable, unwritable.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: unwritableID, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	unwritable.process(unwritableID)
	if response := cwWtBridgeResponse(t, unwritable, unwritableID); !strings.Contains(response.Error, "persist bridge dispatch fence") {
		t.Fatalf("unwritable marker error = %q", response.Error)
	}
}

func TestCwWtDaemonFileBridgeProcessBoundsResponseSize(t *testing.T) {
	server, _ := cwWtBridgeServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(make([]byte, daemonFileBridgeMaxBytes+64))
	}))
	const id = "cwWt-oversized-response"
	cwWtBridgePut(t, server, server.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	server.process(id)
	if response := cwWtBridgeResponse(t, server, id); !strings.Contains(response.Error, "response exceeded its bound") {
		t.Fatalf("oversized response error = %q", response.Error)
	}
}

func TestCwWtDaemonFileBridgeQuarantineReportsBadPaths(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	_, quarantine := cwWtBridgeDirs(server)

	if err := server.quarantine("cwWt-absent", filepath.Join(server.requests, "cwWt-absent.json"), "request"); err != nil {
		t.Fatalf("absent quarantine target error = %v", err)
	}

	regular := filepath.Join(t.TempDir(), "cwWt-regular")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.quarantine("cwWt-enotdir", filepath.Join(regular, "cwWt.json"), "request"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("uninspectable quarantine target error = %v", err)
	}

	blocked := filepath.Join(server.requests, "cwWt-blocked.json")
	if err := os.WriteFile(blocked, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.quarantine("cwWt-blocked", blocked, "request"); err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("blocked quarantine directory error = %v", err)
	}
	if reported := cwWtBridgeDrainError(server); reported == nil {
		t.Fatal("blocked quarantine directory was not reported")
	}
}

func TestCwWtDaemonFileBridgeQuarantineReportsRenameFailure(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	base, quarantine := cwWtBridgeDirs(server)
	if err := secureBridgeDirectory(quarantine); err != nil {
		t.Fatal(err)
	}
	// Renaming the bridge base into its own quarantine child is rejected by the
	// kernel, which is the only portable way to make os.Rename fail on demand.
	err := server.quarantine("cwWt-rename", base, "request")
	if err == nil || !strings.Contains(err.Error(), "quarantine daemon file bridge request") {
		t.Fatalf("rename failure quarantine error = %v", err)
	}
	if reported := cwWtBridgeDrainError(server); reported == nil {
		t.Fatal("rename failure was not reported")
	}
	if err := cwWtBridgeStat(base); err != nil {
		t.Fatalf("bridge base was moved: %v", err)
	}
}

func TestCwWtDaemonFileBridgeQuarantineCleanupBoundsAndOrders(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "cwWt-missing-quarantine")
	if err := cleanupDaemonFileQuarantine(missing, time.Now()); err == nil || !strings.Contains(err.Error(), "inspect daemon file bridge quarantine") {
		t.Fatalf("missing quarantine error = %v", err)
	}

	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "cwWt-subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	same := time.Now().Add(-time.Hour)
	names := make([]string, 0, daemonFileBridgeQuarantineLimit+2)
	for index := 0; index < daemonFileBridgeQuarantineLimit+2; index++ {
		name := fmt.Sprintf("cwWt-%04d.json", index)
		names = append(names, name)
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("quarantined"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, same, same); err != nil {
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
	if len(entries) != daemonFileBridgeQuarantineLimit+1 {
		t.Fatalf("quarantine entries = %d, want %d", len(entries), daemonFileBridgeQuarantineLimit+1)
	}
	for index, name := range names {
		_, statErr := os.Stat(filepath.Join(directory, name))
		if index < 2 && !os.IsNotExist(statErr) {
			t.Fatalf("quarantine did not evict the oldest %s: %v", name, statErr)
		}
		if index >= 2 && statErr != nil {
			t.Fatalf("quarantine evicted retained %s: %v", name, statErr)
		}
	}
	if err := cwWtBridgeStat(filepath.Join(directory, "cwWt-subdir")); err != nil {
		t.Fatalf("quarantine cleanup removed a directory: %v", err)
	}
}

func TestCwWtDaemonFileBridgeWriteErrorReportsPersistenceFailure(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	if err := os.Rename(server.responses, server.responses+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server.responses, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.writeError("cwWt-recovery", cwWtBridgeGeneration, "cwWt-worker", errors.New("cwWt: boom"))
	reported := cwWtBridgeDrainError(server)
	if reported == nil || !strings.Contains(reported.Error(), "persist daemon file bridge recovery response") {
		t.Fatalf("writeError persistence report = %v", reported)
	}
}

func TestCwWtDaemonFileBridgeWriteEnvelopeRejectsOversizedAndUnpublishablePayloads(t *testing.T) {
	directory := t.TempDir()
	oversized := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-oversized", Body: make([]byte, daemonFileBridgeMaxBytes+1)}
	if err := writeDaemonFileEnvelope(directory, oversized.ID, oversized); err == nil || !strings.Contains(err.Error(), "envelope exceeds") {
		t.Fatalf("oversized envelope error = %v", err)
	}

	blockedPath := filepath.Join(directory, "cwWt-publish.json")
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedPath, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDaemonFileEnvelope(directory, "cwWt-publish", daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-publish"}); err == nil || !strings.Contains(err.Error(), "publish daemon file bridge envelope") {
		t.Fatalf("blocked publish error = %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".cwWt-publish.") {
			t.Fatalf("unpublished temporary file leaked: %s", entry.Name())
		}
	}
}

func TestCwWtDaemonFileBridgeReadEnvelopeRejectsUnsafeFiles(t *testing.T) {
	directory := t.TempDir()

	asDirectory := filepath.Join(directory, "cwWt-directory.json")
	if err := os.Mkdir(asDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readDaemonFileEnvelope(asDirectory); err == nil || !strings.Contains(err.Error(), "not a bounded regular file") {
		t.Fatalf("directory envelope error = %v", err)
	}

	oversized := filepath.Join(directory, "cwWt-oversized.json")
	if err := os.WriteFile(oversized, make([]byte, daemonFileBridgeMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDaemonFileEnvelope(oversized); err == nil || !strings.Contains(err.Error(), "not a bounded regular file") {
		t.Fatalf("oversized envelope error = %v", err)
	}

	wrongMode := filepath.Join(directory, "cwWt-mode.json")
	if err := os.WriteFile(wrongMode, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wrongMode, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDaemonFileEnvelope(wrongMode); err == nil || !strings.Contains(err.Error(), "has mode") {
		t.Fatalf("world-readable envelope error = %v", err)
	}

	undecodable := filepath.Join(directory, "cwWt-undecodable.json")
	if err := os.WriteFile(undecodable, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDaemonFileEnvelope(undecodable); err == nil || !strings.Contains(err.Error(), "decode daemon file bridge envelope") {
		t.Fatalf("undecodable envelope error = %v", err)
	}

	want := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-round-trip", SchedulerGeneration: cwWtBridgeGeneration}
	if err := writeDaemonFileEnvelope(directory, want.ID, want); err != nil {
		t.Fatal(err)
	}
	got, err := readDaemonFileEnvelope(filepath.Join(directory, want.ID+".json"))
	if err != nil || got.ID != want.ID || got.Schema != want.Schema {
		t.Fatalf("envelope round trip = %#v, %v", got, err)
	}
}

func TestCwWtDaemonFileBridgeVerifyEnvelopeRejectsForgedHeaders(t *testing.T) {
	if err := verifyDaemonFileEnvelope(daemonFileEnvelope{Schema: 99, ID: "cwWt-id"}, "cwWt-key"); err == nil || !strings.Contains(err.Error(), "unsupported schema") {
		t.Fatalf("schema error = %v", err)
	}
	if err := verifyDaemonFileEnvelope(daemonFileEnvelope{Schema: daemonFileBridgeSchema}, "cwWt-key"); err == nil || !strings.Contains(err.Error(), "unsupported schema") {
		t.Fatalf("empty ID error = %v", err)
	}
	if err := verifyDaemonFileEnvelope(daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt/escape"}, "cwWt-key"); err == nil || !strings.Contains(err.Error(), "unsupported schema") {
		t.Fatalf("unsafe ID error = %v", err)
	}

	envelope := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "cwWt-forged"}
	envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
	envelope.MAC = "not-hexadecimal"
	if err := verifyDaemonFileEnvelope(envelope, "cwWt-key"); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("non-hex MAC error = %v", err)
	}

	envelope.MAC = daemonFileEnvelopeMAC(envelope, "cwWt-other-key")
	if err := verifyDaemonFileEnvelope(envelope, "cwWt-key"); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("wrong-key MAC error = %v", err)
	}

	envelope.MAC = daemonFileEnvelopeMAC(envelope, "cwWt-key")
	if err := verifyDaemonFileEnvelope(envelope, "cwWt-key"); err != nil {
		t.Fatalf("valid envelope error = %v", err)
	}
}

func TestCwWtDaemonFileBridgePrepareRequestRejectsUnknownAndMalformedPayloads(t *testing.T) {
	if _, _, err := daemonFilePrepareRequest("/cwWt.unknown.Service/DoThing", nil, "cwWt-id"); err == nil || !strings.Contains(err.Error(), "refused an unknown RPC procedure") {
		t.Fatalf("unknown procedure error = %v", err)
	}
	if _, _, err := daemonFilePrepareRequest(daemonv1connect.DaemonServiceSubmitOperationProcedure, []byte{0x0f}, "cwWt-id"); err == nil || !strings.Contains(err.Error(), "decode bridged submit request") {
		t.Fatalf("submit decode error = %v", err)
	}
	for name, procedure := range map[string]string{
		"register":   daemonv1connect.DaemonServiceRegisterWorkerProcedure,
		"lease":      daemonv1connect.DaemonServiceLeaseOperationProcedure,
		"heartbeat":  daemonv1connect.DaemonServiceHeartbeatOperationProcedure,
		"complete":   daemonv1connect.DaemonServiceCompleteOperationProcedure,
		"disconnect": daemonv1connect.DaemonServiceDisconnectWorkerProcedure,
	} {
		if _, _, err := daemonFilePrepareRequest(procedure, []byte{0x0f}, "cwWt-id"); err == nil {
			t.Fatalf("%s accepted an undecodable payload", name)
		}
	}

	body, err := proto.Marshal(&daemonv1.RegisterWorkerRequest{WorkerId: "   "})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := daemonFilePrepareRequest(daemonv1connect.DaemonServiceRegisterWorkerProcedure, body, "cwWt-id"); err == nil || !strings.Contains(err.Error(), "requires an explicit target worker identity") {
		t.Fatalf("blank target error = %v", err)
	}

	submitBody, err := proto.Marshal(&daemonv1.SubmitOperationRequest{WorkingDirectory: "/tmp", Argv: []string{"go", "version"}, TargetWorkerId: "cwWt-worker"})
	if err != nil {
		t.Fatal(err)
	}
	target, bound, err := daemonFilePrepareRequest(daemonv1connect.DaemonServiceSubmitOperationProcedure, submitBody, "cwWt-idempotency")
	if err != nil || target != "cwWt-worker" {
		t.Fatalf("bound submit = %q, %v", target, err)
	}
	decoded := &daemonv1.SubmitOperationRequest{}
	if err := proto.Unmarshal(bound, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.IdempotencyKey != "cwWt-idempotency" {
		t.Fatalf("bound idempotency key = %q", decoded.IdempotencyKey)
	}
}

func TestCwWtDaemonFileBridgeCleanupStaleReportsUnremovableResponse(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	stale := time.Now().Add(-daemonFileBridgeCompletedAge - time.Hour)
	stalePath := filepath.Join(server.responses, "cwWt-unremovable.json")
	if err := os.WriteFile(stalePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stalePath, stale, stale); err != nil {
		t.Fatal(err)
	}
	// A directory that can be listed but not written is how the bridge's
	// "cleanup candidate could not be removed" branch becomes observable.
	if err := os.Chmod(server.responses, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(server.responses, 0o700) })

	err := server.cleanupStale(time.Now())
	if err == nil || !strings.Contains(err.Error(), "remove stale daemon file bridge file") {
		t.Fatalf("unremovable cleanup candidate error = %v", err)
	}
	if statErr := cwWtBridgeStat(stalePath); statErr != nil {
		t.Fatalf("cleanup removed a file it reported as unremovable: %v", statErr)
	}
}

func TestCwWtDaemonFileBridgeQuarantineCleanupReportsUnremovableEntry(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "cwWt-quarantine")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(directory, "cwWt-expired.json")
	if err := os.WriteFile(stalePath, []byte("quarantined"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-daemonFileBridgeQuarantineAge - time.Hour)
	if err := os.Chtimes(stalePath, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	err := cleanupDaemonFileQuarantine(directory, time.Now())
	if err == nil || !strings.Contains(err.Error(), "remove stale daemon file bridge file") {
		t.Fatalf("unremovable quarantine entry error = %v", err)
	}
	if statErr := cwWtBridgeStat(stalePath); statErr != nil {
		t.Fatalf("quarantine cleanup removed an entry it reported as unremovable: %v", statErr)
	}
}

func TestCwWtDaemonFileBridgeScanReportsQuarantineFailureForUnreadableResponse(t *testing.T) {
	server, _ := cwWtBridgeServer(t, nil)
	server.lastCleanup = time.Now()
	_, quarantine := cwWtBridgeDirs(server)
	const id = "cwWt-unreadable-quarantine-blocked"
	cwWtBridgePut(t, server, server.requests, daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: cwWtBridgeGeneration,
		Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure,
	})
	if err := os.WriteFile(filepath.Join(server.responses, id+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := server.scan()
	if err == nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("blocked unreadable-response quarantine error = %v", err)
	}
	if reported := cwWtBridgeDrainError(server); reported == nil {
		t.Fatal("blocked unreadable-response quarantine was not reported")
	}
}
