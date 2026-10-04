//go:build !windows

package daemonruntime

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

// All writes, chmods, quarantine and cleanup remain in private TempDirs.
// Cases that change HOME remain serial; TestMain isolates ambient user state.
func TestFileBridgeBoundariesRejectUnresolvableHomeBeforePublication(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(home, []byte("private home sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for name, call := range map[string]func() error{
		"key path":   func() error { _, err := daemonFileBridgeKeyPath(root); return err },
		"key create": func() error { _, err := daemonFileBridgeKey(root, true); return err },
		"directory":  func() error { _, err := daemonFileBridgeDirectory(root); return err },
		"runtime":    func() error { return secureDaemonRuntime(root) },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "resolve WB home") {
			t.Fatalf("%s error = %v, want native home-resolution refusal", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".wb")); !os.IsNotExist(err) {
		t.Fatalf("refused home created state: %v", err)
	}
	if body, err := os.ReadFile(home); err != nil || string(body) != "private home sentinel" {
		t.Fatalf("home sentinel changed: %q, %v", body, err)
	}
}

func TestFileBridgeBoundariesRejectBlockedNativeDirectories(t *testing.T) {
	root := daemonTestRoot(t)
	blocked := filepath.Join(root, ".wb")
	if err := os.WriteFile(blocked, []byte("private blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureDaemonRuntime(root); err == nil || !strings.Contains(err.Error(), "parent is not a real directory") {
		t.Fatalf("runtime parent refusal = %v", err)
	}
	if err := secureBridgeDirectory(filepath.Join(root, "invalid\x00directory")); err == nil || !strings.Contains(err.Error(), "inspect daemon file bridge directory") {
		t.Fatalf("invalid native directory error = %v", err)
	}
	link := filepath.Join(root, "missing-parent")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if err := secureBridgeDirectory(filepath.Join(link, "child")); err == nil || !strings.Contains(err.Error(), "create daemon file bridge directory") {
		t.Fatalf("unmakeable native directory error = %v", err)
	}
	if err := secureBridgeParentDirectory(filepath.Join(link, "child"), true); err == nil || !strings.Contains(err.Error(), "create daemon file bridge parent") {
		t.Fatalf("unmakeable native parent error = %v", err)
	}
}

func TestFileBridgeBoundariesStopAfterResponseQuarantineIfRequestCannotMove(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("native directory write refusal requires an unprivileged process")
	}
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unauthenticated response", true: "unreadable response"}[malformed], func(t *testing.T) {
			var calls atomic.Int32
			server, _ := cwWtBridgeServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			server.lastCleanup = time.Now()
			const id = "boundary-request"
			requestPath := cwWtBridgePut(t, server, server.requests, daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: server.generation, Procedure: daemonv1connect.DaemonServiceGetDaemonInfoProcedure})
			responsePath := filepath.Join(server.responses, id+".json")
			if malformed {
				if err := os.WriteFile(responsePath, []byte("{not-json"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				response := cwWtBridgeSigned(server, daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: server.generation})
				response.MAC = strings.Repeat("00", 32)
				if err := writeDaemonFileEnvelope(server.responses, id, response); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(server.requests, 0o500); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chmod(server.requests, 0o700); err != nil {
					t.Error(err)
				}
			}()
			if err := server.scan(); err == nil || !strings.Contains(err.Error(), "quarantine daemon file bridge request") {
				t.Fatalf("request quarantine refusal = %v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("refused recovery replayed the handler %d times", calls.Load())
			}
			if _, err := os.Stat(requestPath); err != nil {
				t.Fatalf("unmoved request lost: %v", err)
			}
			if _, err := os.Stat(responsePath); !os.IsNotExist(err) {
				t.Fatalf("response was not quarantined first: %v", err)
			}
			_, directory := cwWtBridgeDirs(server)
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Name(), ".response.") {
				t.Fatalf("response quarantine = %v, %v", entries, err)
			}
			if err := cwWtBridgeDrainError(server); err == nil {
				t.Fatal("native quarantine failure was not reported")
			}
		})
	}
}

func TestFileBridgeBoundariesCleanupRefusesAnUninspectableRequestParent(t *testing.T) {
	server, root := cwWtBridgeServer(t, nil)
	path := cwWtBridgePut(t, server, server.responses, daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: "boundary-old-response", SchedulerGeneration: server.generation})
	old := time.Now().Add(-2 * daemonFileBridgeCompletedAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "request-parent-file")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.requests = blocker
	if err := server.cleanupStale(time.Now()); err == nil || !strings.Contains(err.Error(), "inspect daemon file bridge request during cleanup") {
		t.Fatalf("native request inspection refusal = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("response removed despite request inspection failure: %v", err)
	}
}

func TestFileBridgeBoundariesQuarantineReportsNativeNameLimit(t *testing.T) {
	server, root := cwWtBridgeServer(t, nil)
	path := filepath.Join(root, "private-quarantine-candidate")
	if err := os.WriteFile(path, []byte("preserve candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.quarantine(strings.Repeat("x", 255), path, "response"); err == nil || !strings.Contains(err.Error(), "quarantine daemon file bridge response") {
		t.Fatalf("native rename name-limit error = %v", err)
	}
	if err := cwWtBridgeDrainError(server); err == nil {
		t.Fatal("native rename failure was not reported")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "preserve candidate" {
		t.Fatalf("candidate changed after refused rename: %q, %v", data, err)
	}
}

type fileBridgeBoundaryReader struct{ err error }

func (reader fileBridgeBoundaryReader) Read([]byte) (int, error) { return 0, reader.err }
func (fileBridgeBoundaryReader) Close() error                    { return nil }

func TestFileBridgeBoundariesRejectReaderFailureBeforeRequestPersistence(t *testing.T) {
	t.Parallel()
	failure := errors.New("private request reader failure")
	request, err := http.NewRequest(http.MethodPost, "http://wb.local"+daemonv1connect.DaemonServiceSubmitOperationProcedure, fileBridgeBoundaryReader{err: failure})
	if err != nil {
		t.Fatal(err)
	}
	transport := &daemonFileBridgeTransport{requests: t.TempDir()}
	response, err := transport.RoundTrip(request)
	if response != nil || !errors.Is(err, failure) {
		t.Fatalf("response = %v, error = %v, want original read failure", response, err)
	}
	entries, err := os.ReadDir(transport.requests)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reader refusal persisted a request: %v, %v", entries, err)
	}
}

func TestFileBridgeBoundariesRejectUnsafeProtoTextDuringIdempotencyBinding(t *testing.T) {
	t.Parallel()
	body, err := proto.Marshal(&daemonv1.SubmitOperationRequest{Argv: []string{"go", "version"}})
	if err != nil {
		t.Fatal(err)
	}
	// This private helper can receive arbitrary request IDs. An invalid UTF-8
	// identifier must not produce a publishable protobuf request.
	_, bound, err := daemonFilePrepareRequest(daemonv1connect.DaemonServiceSubmitOperationProcedure, body, string([]byte{0xff}))
	if err == nil || bound != nil {
		t.Fatalf("unsafe id produced request bytes %x, error %v", bound, err)
	}
}

func TestFileBridgeBoundariesRequireNativeCurrentUserOwnership(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("requires a native file owned by a different user")
	}
	// Only metadata is observed. No system file is opened for contents, written,
	// chmodded, linked or adopted into a bridge fixture.
	info, err := os.Lstat("/etc/hosts")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyBridgePathSecurity("/etc/hosts", info, info.Mode().Perm(), false); err == nil || !strings.Contains(err.Error(), "not owned by the current user") {
		t.Fatalf("native foreign ownership refusal = %v", err)
	}
	private := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(private, bytes.Repeat([]byte("x"), 64), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyBridgePathSecurity(private, info, 0o600, true); err != nil {
		t.Fatalf("private current-user file refused: %v", err)
	}
	file, err := os.Open(private)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if data, err := io.ReadAll(file); err != nil || len(data) != 64 {
		t.Fatalf("native private file = %d bytes, %v", len(data), err)
	}
}
