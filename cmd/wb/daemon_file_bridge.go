package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

const (
	daemonFileBridgeSchema          = 1
	daemonFileBridgeMaxBytes        = 512 << 10
	daemonFileBridgePoll            = 250 * time.Millisecond
	daemonFileBridgeTimeout         = 12 * time.Second
	daemonFileBridgeWorkers         = 16
	daemonFileBridgeQuarantineLimit = 128
	daemonFileBridgeQuarantineAge   = 7 * 24 * time.Hour
	daemonFileBridgeCompletedAge    = 24 * time.Hour
	daemonFileBridgeRequestAge      = 7 * 24 * time.Hour
	daemonFileBridgeCleanupInterval = time.Minute
	daemonFileBridgeBacklogLimit    = 1024
)

type daemonFileEnvelope struct {
	Schema              int                 `json:"schema"`
	ID                  string              `json:"id"`
	SchedulerGeneration string              `json:"scheduler_generation"`
	TargetWorkerID      string              `json:"target_worker_id,omitempty"`
	Procedure           string              `json:"procedure,omitempty"`
	ContentType         string              `json:"content_type,omitempty"`
	Header              map[string][]string `json:"header,omitempty"`
	Body                []byte              `json:"body,omitempty"`
	PayloadSHA256       string              `json:"payload_sha256"`
	StatusCode          int                 `json:"status_code,omitempty"`
	Error               string              `json:"error,omitempty"`
	MAC                 string              `json:"mac"`
}

func daemonFileBridgeKeyPath(root string) string {
	return filepath.Join(root, ".wb", "runtime", "file-bridge.key")
}

func daemonFileBridgeKey(root string, create bool) (string, error) {
	path := daemonFileBridgeKeyPath(root)
	if create {
		if err := secureBridgeRuntime(root); err != nil {
			return "", err
		}
		value := make([]byte, 32)
		if _, err := rand.Read(value); err != nil {
			return "", fmt.Errorf("generate daemon file bridge key: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err = file.Write([]byte(hex.EncodeToString(value))); err == nil {
				err = file.Sync()
			}
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(path)
				return "", fmt.Errorf("persist daemon file bridge key: %w", err)
			}
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("create daemon file bridge key: %w", err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect daemon file bridge key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != 64 {
		return "", errors.New("daemon file bridge key is not a regular 32-byte hex key")
	}
	if err := verifyBridgePathSecurity(path, info, 0o600); err != nil {
		return "", err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read daemon file bridge key: %w", err)
	}
	if _, err := hex.DecodeString(string(contents)); err != nil {
		return "", errors.New("daemon file bridge key is malformed")
	}
	return string(contents), nil
}

func daemonFileBridgeDirectory(root string) string {
	return filepath.Join(root, ".wb", "runtime", "file-bridge")
}

func prepareDaemonFileBridge(root string) (requests, responses string, err error) {
	if err := secureBridgeRuntime(root); err != nil {
		return "", "", err
	}
	base := daemonFileBridgeDirectory(root)
	for _, directory := range []string{base, filepath.Join(base, "requests"), filepath.Join(base, "responses")} {
		if err = secureBridgeDirectory(directory); err != nil {
			return "", "", err
		}
	}
	return filepath.Join(base, "requests"), filepath.Join(base, "responses"), nil
}

func secureBridgeRuntime(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("daemon file bridge projects root must be absolute")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("daemon file bridge projects root is not a real directory: %s", root)
	}
	if err := verifyBridgePathSecurity(root, info, info.Mode().Perm()); err != nil {
		return err
	}
	wbDirectory := filepath.Join(root, ".wb")
	if err := secureBridgeParentDirectory(wbDirectory, false); err != nil {
		return err
	}
	return secureBridgeParentDirectory(filepath.Join(wbDirectory, "runtime"), true)
}

func secureBridgeParentDirectory(path string, private bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create daemon file bridge parent: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("daemon file bridge parent is not a real directory: %s", path)
	}
	want := info.Mode().Perm()
	if private {
		want = 0o700
	}
	return verifyBridgePathSecurity(path, info, want)
}

func secureBridgeDirectory(path string) error {
	info, err := os.Lstat(path)
	created := false
	if os.IsNotExist(err) {
		if err = os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create daemon file bridge directory: %w", err)
		}
		created = true
	} else if err != nil {
		return fmt.Errorf("inspect daemon file bridge directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("daemon file bridge path is not a real directory: %s", path)
	}
	if created {
		err = os.Chmod(path, 0o700)
	}
	if err != nil {
		return fmt.Errorf("protect daemon file bridge directory: %w", err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect daemon file bridge directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("daemon file bridge path is not a real directory: %s", path)
	}
	return verifyBridgePathSecurity(path, info, 0o700)
}

type daemonFileBridgeServer struct {
	requests     string
	responses    string
	inflight     string
	ownerToken   string
	key          string
	generation   string
	handler      http.Handler
	mu           sync.Mutex
	active       map[string]bool
	wg           sync.WaitGroup
	sem          chan struct{}
	errors       chan error
	lastCleanup  time.Time
	dropResponse func(string) bool
}

func newDaemonFileBridgeServer(root, ownerToken, generation string, handler http.Handler) (*daemonFileBridgeServer, error) {
	requests, responses, err := prepareDaemonFileBridge(root)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(ownerToken) == "" || strings.TrimSpace(generation) == "" || handler == nil {
		return nil, errors.New("daemon file bridge requires token, scheduler generation, and handler")
	}
	inflight := filepath.Join(filepath.Dir(requests), "inflight")
	if err := secureBridgeDirectory(inflight); err != nil {
		return nil, err
	}
	key, err := daemonFileBridgeKey(root, true)
	if err != nil {
		return nil, err
	}
	return &daemonFileBridgeServer{requests: requests, responses: responses, inflight: inflight, ownerToken: ownerToken, key: key, generation: generation, handler: handler, active: map[string]bool{}, sem: make(chan struct{}, daemonFileBridgeWorkers), errors: make(chan error, 1)}, nil
}

func (server *daemonFileBridgeServer) Serve(ctx context.Context) error {
	ticker := time.NewTicker(daemonFileBridgePoll)
	defer ticker.Stop()
	for {
		if err := server.scan(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			server.wg.Wait()
			return nil
		case err := <-server.errors:
			server.wg.Wait()
			return err
		case <-ticker.C:
		}
	}
}

func (server *daemonFileBridgeServer) scan() error {
	now := time.Now()
	if server.lastCleanup.IsZero() || now.Sub(server.lastCleanup) >= daemonFileBridgeCleanupInterval {
		if err := server.cleanupStale(now); err != nil {
			return err
		}
		server.lastCleanup = now
	}
	entries, err := os.ReadDir(server.requests)
	if err != nil {
		return fmt.Errorf("read daemon file bridge requests: %w", err)
	}
	if len(entries) > daemonFileBridgeBacklogLimit {
		return fmt.Errorf("daemon file bridge request backlog exceeds %d entries; inspect recovery dispositions before restarting", daemonFileBridgeBacklogLimit)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		responsePath := filepath.Join(server.responses, name)
		if response, err := readDaemonFileEnvelope(responsePath); err == nil {
			if verifyDaemonFileEnvelope(response, server.key) == nil {
				server.cleanupCompleted(filepath.Join(server.requests, name), responsePath, filepath.Join(server.inflight, name), now)
				continue
			}
			if err := server.quarantine(id, responsePath, "response"); err != nil {
				return err
			}
			if err := server.quarantine(id, filepath.Join(server.requests, name), "request"); err != nil {
				return err
			}
			server.writeError(id, server.generation, response.TargetWorkerID, errors.New("recovery_required: an existing response failed authentication; the mutation was not replayed"))
			continue
		} else if !os.IsNotExist(err) {
			if err := server.quarantine(id, responsePath, "response"); err != nil {
				return err
			}
			if err := server.quarantine(id, filepath.Join(server.requests, name), "request"); err != nil {
				return err
			}
			server.writeError(id, server.generation, "", errors.New("recovery_required: an unreadable response was quarantined; the mutation was not replayed"))
			continue
		}
		server.mu.Lock()
		if server.active[id] {
			server.mu.Unlock()
			continue
		}
		select {
		case server.sem <- struct{}{}:
		default:
			server.mu.Unlock()
			continue
		}
		server.active[id] = true
		server.wg.Add(1)
		server.mu.Unlock()
		go func() {
			defer server.wg.Done()
			defer func() { <-server.sem }()
			defer func() {
				server.mu.Lock()
				delete(server.active, id)
				server.mu.Unlock()
			}()
			server.process(id)
		}()
	}
	return nil
}

func (server *daemonFileBridgeServer) cleanupCompleted(requestPath, responsePath, markerPath string, now time.Time) {
	info, err := os.Stat(responsePath)
	if err != nil || now.Sub(info.ModTime()) <= daemonFileBridgeCompletedAge {
		return
	}
	if err := removeBridgeFile(requestPath); err != nil {
		server.report(err)
		return
	}
	if err := removeBridgeFile(markerPath); err != nil {
		server.report(err)
	}
	if err := removeBridgeFile(responsePath); err != nil {
		server.report(err)
	}
}

func (server *daemonFileBridgeServer) cleanupStale(now time.Time) error {
	for _, directory := range []string{server.responses, server.inflight} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return fmt.Errorf("inspect daemon file bridge cleanup directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect daemon file bridge cleanup candidate: %w", err)
			}
			if now.Sub(info.ModTime()) <= daemonFileBridgeCompletedAge {
				continue
			}
			if _, err := os.Lstat(filepath.Join(server.requests, entry.Name())); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect daemon file bridge request during cleanup: %w", err)
			}
			if err := removeBridgeFile(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
		}
	}

	entries, err := os.ReadDir(server.requests)
	if err != nil {
		return fmt.Errorf("inspect stale daemon file bridge requests: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect stale daemon file bridge request: %w", err)
		}
		if now.Sub(info.ModTime()) <= daemonFileBridgeRequestAge {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		server.mu.Lock()
		active := server.active[id]
		server.mu.Unlock()
		if active {
			continue
		}
		requestPath := filepath.Join(server.requests, entry.Name())
		request, err := readDaemonFileEnvelope(requestPath)
		if err != nil || verifyDaemonFileEnvelope(request, server.key) != nil {
			continue
		}
		if err := server.quarantine(id, requestPath, "expired-request"); err != nil {
			return err
		}
		if err := removeBridgeFile(filepath.Join(server.inflight, entry.Name())); err != nil {
			return err
		}
		server.writeError(id, request.SchedulerGeneration, request.TargetWorkerID, errors.New("recovery_required: file bridge request expired before a durable response; retry the exact submission with its idempotency key"))
	}
	return nil
}

func removeBridgeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale daemon file bridge file: %w", err)
	}
	return nil
}

func (server *daemonFileBridgeServer) process(id string) {
	requestPath := filepath.Join(server.requests, id+".json")
	request, err := readDaemonFileEnvelope(requestPath)
	if err != nil {
		server.writeError(id, "", "", err)
		return
	}
	if request.ID != id {
		server.writeError(id, request.SchedulerGeneration, request.TargetWorkerID, errors.New("request ID does not match its atomic filename"))
		return
	}
	if err := verifyDaemonFileEnvelope(request, server.key); err != nil {
		if quarantineErr := server.quarantine(id, requestPath, "request"); quarantineErr != nil {
			return
		}
		server.writeError(id, server.generation, request.TargetWorkerID, fmt.Errorf("recovery_required: request authentication failed and was not replayed: %w", err))
		return
	}
	target, dispatchBody, err := daemonFilePrepareRequest(request.Procedure, request.Body, id)
	if err != nil {
		server.writeError(id, server.generation, request.TargetWorkerID, err)
		return
	}
	if target != request.TargetWorkerID {
		server.writeError(id, request.SchedulerGeneration, request.TargetWorkerID, errors.New("target worker fence does not match the RPC payload"))
		return
	}
	markerPath := filepath.Join(server.inflight, id+".json")
	marker, markerErr := readDaemonFileEnvelope(markerPath)
	ambiguous := markerErr == nil && verifyDaemonFileEnvelope(marker, server.key) == nil
	if markerErr != nil && !os.IsNotExist(markerErr) {
		server.writeError(id, request.SchedulerGeneration, request.TargetWorkerID, errors.New("recovery_required: dispatch marker is unreadable; request was not replayed"))
		return
	}
	safeReplay := request.Procedure == daemonv1connect.DaemonServiceSubmitOperationProcedure || daemonFileReadOnlyProcedure(request.Procedure)
	if ambiguous && !safeReplay {
		server.writeError(id, request.SchedulerGeneration, request.TargetWorkerID, errors.New("recovery_required: worker RPC outcome is ambiguous; reconnect without replaying it"))
		return
	}
	if request.SchedulerGeneration != server.generation && !safeReplay {
		server.writeError(id, request.SchedulerGeneration, request.TargetWorkerID, fmt.Errorf("recovery_required: scheduler generation changed to %s; reconnect without replaying the worker RPC", server.generation))
		return
	}
	if !ambiguous {
		marker = request
		marker.Body = nil
		marker.Header = nil
		marker.ContentType = ""
		marker.PayloadSHA256 = daemonFilePayloadDigest(marker)
		marker.MAC = daemonFileEnvelopeMAC(marker, server.key)
		if err := writeDaemonFileEnvelope(server.inflight, id, marker); err != nil {
			server.writeError(id, request.SchedulerGeneration, request.TargetWorkerID, fmt.Errorf("persist bridge dispatch fence: %w", err))
			return
		}
	}
	req := httptest.NewRequest(http.MethodPost, daemonRPCBaseURL+request.Procedure, bytes.NewReader(dispatchBody))
	for key, values := range request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+server.ownerToken)
	recorder := httptest.NewRecorder()
	server.handler.ServeHTTP(recorder, req)
	if server.dropResponse != nil && server.dropResponse(request.Procedure) {
		return
	}
	result := recorder.Result()
	defer func() { _ = result.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(result.Body, daemonFileBridgeMaxBytes+1))
	if err != nil || len(body) > daemonFileBridgeMaxBytes {
		server.writeError(id, server.generation, request.TargetWorkerID, errors.New("daemon file bridge response exceeded its bound"))
		return
	}
	response := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: request.SchedulerGeneration, TargetWorkerID: request.TargetWorkerID, StatusCode: result.StatusCode, Header: bridgeResponseHeaders(result.Header), Body: body}
	response.PayloadSHA256 = daemonFilePayloadDigest(response)
	response.MAC = daemonFileEnvelopeMAC(response, server.key)
	if err := writeDaemonFileEnvelope(server.responses, id, response); err == nil {
		_ = os.Remove(markerPath)
	} else {
		server.report(fmt.Errorf("persist daemon file bridge response %s: %w", id, err))
	}
}

func (server *daemonFileBridgeServer) quarantine(id, path, kind string) error {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	directory := filepath.Join(filepath.Dir(server.requests), "quarantine")
	if err := secureBridgeDirectory(directory); err != nil {
		server.report(err)
		return err
	}
	name := fmt.Sprintf("%s.%s.%d.json", id, kind, time.Now().UnixNano())
	if err := os.Rename(path, filepath.Join(directory, name)); err != nil {
		err = fmt.Errorf("quarantine daemon file bridge %s %s: %w", kind, id, err)
		server.report(err)
		return err
	}
	if err := cleanupDaemonFileQuarantine(directory, time.Now()); err != nil {
		server.report(err)
		return err
	}
	return nil
}

func cleanupDaemonFileQuarantine(directory string, now time.Time) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("inspect daemon file bridge quarantine: %w", err)
	}
	type candidate struct {
		name     string
		modified time.Time
	}
	files := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("inspect daemon file bridge quarantine entry: %w", infoErr)
		}
		files = append(files, candidate{name: entry.Name(), modified: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].name < files[j].name
		}
		return files[i].modified.Before(files[j].modified)
	})
	for index, file := range files {
		if now.Sub(file.modified) > daemonFileBridgeQuarantineAge || len(files)-index > daemonFileBridgeQuarantineLimit {
			if err := removeBridgeFile(filepath.Join(directory, file.name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (server *daemonFileBridgeServer) writeError(id, generation, target string, bridgeErr error) {
	response := daemonFileEnvelope{Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: generation, TargetWorkerID: target, Error: bridgeErr.Error()}
	response.PayloadSHA256 = daemonFilePayloadDigest(response)
	response.MAC = daemonFileEnvelopeMAC(response, server.key)
	if err := writeDaemonFileEnvelope(server.responses, id, response); err != nil {
		server.report(fmt.Errorf("persist daemon file bridge recovery response %s: %w", id, err))
	}
}

func (server *daemonFileBridgeServer) report(err error) {
	select {
	case server.errors <- err:
	default:
	}
}

func bridgeResponseHeaders(input http.Header) map[string][]string {
	result := map[string][]string{}
	for _, name := range []string{"Content-Type", "Content-Encoding", "Connect-Content-Encoding", "Grpc-Encoding"} {
		if values := input.Values(name); len(values) != 0 {
			result[name] = append([]string(nil), values...)
		}
	}
	return result
}

func daemonFileReadOnlyProcedure(procedure string) bool {
	switch procedure {
	case daemonv1connect.DaemonServiceGetDaemonInfoProcedure, daemonv1connect.DaemonServiceGetOperationProcedure, daemonv1connect.DaemonServiceWaitOperationProcedure:
		return true
	default:
		return false
	}
}

type daemonFileBridgeTransport struct {
	requests   string
	responses  string
	inflight   string
	key        string
	generation string
	timeout    time.Duration
}

func newDaemonFileBridgeHTTPClient(root, generation string) (*http.Client, error) {
	return newDaemonFileBridgeHTTPClientWithTimeout(root, generation, daemonFileBridgeTimeout)
}

func newDaemonFileBridgeHTTPClientWithTimeout(root, generation string, timeout time.Duration) (*http.Client, error) {
	requests, responses, err := prepareDaemonFileBridge(root)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(generation) == "" {
		return nil, errors.New("daemon file bridge client requires scheduler generation")
	}
	key, err := daemonFileBridgeKey(root, false)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, errors.New("daemon file bridge client timeout must be positive")
	}
	return &http.Client{Transport: &daemonFileBridgeTransport{requests: requests, responses: responses, inflight: filepath.Join(filepath.Dir(requests), "inflight"), key: key, generation: generation, timeout: timeout}}, nil
}

func (transport *daemonFileBridgeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, daemonFileBridgeMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read daemon RPC for file bridge: %w", err)
	}
	if len(body) > daemonFileBridgeMaxBytes {
		return nil, fmt.Errorf("daemon file bridge request exceeds %d bytes", daemonFileBridgeMaxBytes)
	}
	id, recoveredBody, requestGeneration, err := transport.pendingSubmit(request.URL.Path, body)
	if err != nil {
		return nil, err
	}
	recovered := id != ""
	if recovered {
		body = recoveredBody
	} else {
		id, err = daemonFileBridgeID()
		if err != nil {
			return nil, err
		}
		requestGeneration = transport.generation
	}
	target, body, err := daemonFilePrepareRequest(request.URL.Path, body, id)
	if err != nil {
		return nil, err
	}
	envelope := daemonFileEnvelope{
		Schema: daemonFileBridgeSchema, ID: id, SchedulerGeneration: requestGeneration, TargetWorkerID: target,
		Procedure: request.URL.Path, ContentType: request.Header.Get("Content-Type"),
		Header: map[string][]string{"Content-Type": request.Header.Values("Content-Type"), "Accept-Encoding": request.Header.Values("Accept-Encoding"), "Connect-Protocol-Version": request.Header.Values("Connect-Protocol-Version")}, Body: body,
	}
	envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
	envelope.MAC = daemonFileEnvelopeMAC(envelope, transport.key)
	if !recovered {
		if err := writeDaemonFileEnvelope(transport.requests, id, envelope); err != nil {
			return nil, err
		}
	}
	responsePath := filepath.Join(transport.responses, id+".json")
	deadline := time.NewTimer(transport.timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(daemonFileBridgePoll)
	defer ticker.Stop()
	for {
		response, readErr := readDaemonFileEnvelope(responsePath)
		if readErr == nil {
			if verifyErr := verifyDaemonFileEnvelope(response, transport.key); verifyErr != nil {
				return nil, verifyErr
			}
			if response.ID != id || response.TargetWorkerID != target {
				return nil, errors.New("daemon file bridge response fence does not match the request")
			}
			if response.SchedulerGeneration != requestGeneration {
				return nil, fmt.Errorf("daemon file bridge scheduler generation response changed from %s to %s", requestGeneration, response.SchedulerGeneration)
			}
			_ = os.Remove(filepath.Join(transport.requests, id+".json"))
			_ = os.Remove(filepath.Join(transport.inflight, id+".json"))
			_ = os.Remove(responsePath)
			if response.Error != "" {
				return nil, errors.New(response.Error)
			}
			return &http.Response{StatusCode: response.StatusCode, Status: fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode)), Header: http.Header(response.Header), Body: io.NopCloser(bytes.NewReader(response.Body)), Request: request}, nil
		}
		if !os.IsNotExist(readErr) {
			return nil, readErr
		}
		select {
		case <-request.Context().Done():
			return nil, fmt.Errorf("daemon file bridge request %s was interrupted: %w; retry the exact command to recover its existing operation", id, request.Context().Err())
		case <-deadline.C:
			return nil, fmt.Errorf("daemon file bridge request %s timed out; retry the exact command to recover its existing operation, or inspect the file-bridge quarantine disposition", id)
		case <-ticker.C:
		}
	}
}

func (transport *daemonFileBridgeTransport) pendingSubmit(procedure string, originalBody []byte) (string, []byte, string, error) {
	if procedure != daemonv1connect.DaemonServiceSubmitOperationProcedure {
		return "", nil, "", nil
	}
	want := &daemonv1.SubmitOperationRequest{}
	if err := proto.Unmarshal(originalBody, want); err != nil || want.IdempotencyKey != "" {
		return "", nil, "", nil
	}
	entries, err := os.ReadDir(transport.requests)
	if err != nil {
		return "", nil, "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		envelope, readErr := readDaemonFileEnvelope(filepath.Join(transport.requests, entry.Name()))
		if readErr != nil || verifyDaemonFileEnvelope(envelope, transport.key) != nil || envelope.Procedure != procedure {
			continue
		}
		candidate := &daemonv1.SubmitOperationRequest{}
		if proto.Unmarshal(envelope.Body, candidate) != nil {
			continue
		}
		candidate.IdempotencyKey = ""
		if proto.Equal(want, candidate) {
			return envelope.ID, envelope.Body, envelope.SchedulerGeneration, nil
		}
	}
	return "", nil, "", nil
}

func writeDaemonFileEnvelope(directory, id string, envelope daemonFileEnvelope) error {
	contents, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode daemon file bridge envelope: %w", err)
	}
	if len(contents) > daemonFileBridgeMaxBytes {
		return fmt.Errorf("daemon file bridge envelope exceeds %d bytes", daemonFileBridgeMaxBytes)
	}
	path := filepath.Join(directory, id+".json")
	temporaryID, err := daemonFileBridgeID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, "."+id+"."+temporaryID+".tmp")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create daemon file bridge envelope: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if _, err = file.Write(contents); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("persist daemon file bridge envelope: %w", err)
	}
	if err = os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish daemon file bridge envelope: %w", err)
	}
	remove = false
	return nil
}

func readDaemonFileEnvelope(path string) (daemonFileEnvelope, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return daemonFileEnvelope{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > daemonFileBridgeMaxBytes {
		return daemonFileEnvelope{}, errors.New("daemon file bridge envelope is not a bounded regular file")
	}
	if err := verifyBridgePathSecurity(path, info, 0o600); err != nil {
		return daemonFileEnvelope{}, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return daemonFileEnvelope{}, err
	}
	var envelope daemonFileEnvelope
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return daemonFileEnvelope{}, fmt.Errorf("decode daemon file bridge envelope: %w", err)
	}
	return envelope, nil
}

func verifyDaemonFileEnvelope(envelope daemonFileEnvelope, token string) error {
	if envelope.Schema != daemonFileBridgeSchema || envelope.ID == "" || strings.ContainsAny(envelope.ID, "/\\\x00\r\n") {
		return errors.New("daemon file bridge envelope has an unsupported schema or unsafe ID")
	}
	if envelope.PayloadSHA256 != daemonFilePayloadDigest(envelope) {
		return errors.New("daemon file bridge payload digest mismatch")
	}
	want, err := hex.DecodeString(envelope.MAC)
	if err != nil || !hmac.Equal(want, mustDecodeHex(daemonFileEnvelopeMAC(envelope, token))) {
		return errors.New("daemon file bridge authentication failed")
	}
	return nil
}

func daemonFilePayloadDigest(envelope daemonFileEnvelope) string {
	digest := sha256.Sum256(append([]byte(envelope.Procedure+"\x00"+envelope.SchedulerGeneration+"\x00"+envelope.TargetWorkerID+"\x00"), envelope.Body...))
	return hex.EncodeToString(digest[:])
}

func daemonFileEnvelopeMAC(envelope daemonFileEnvelope, token string) string {
	envelope.MAC = ""
	contents, _ := json.Marshal(envelope)
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write(contents)
	return hex.EncodeToString(mac.Sum(nil))
}

func mustDecodeHex(value string) []byte {
	decoded, _ := hex.DecodeString(value)
	return decoded
}

func daemonFileBridgeID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate daemon file bridge ID: %w", err)
	}
	return "wbfb-" + hex.EncodeToString(value), nil
}

func daemonFileRequestTarget(procedure string, body []byte) (string, error) {
	target, _, err := daemonFilePrepareRequest(procedure, body, "")
	return target, err
}

func daemonFilePrepareRequest(procedure string, body []byte, requestID string) (string, []byte, error) {
	var target string
	switch procedure {
	case daemonv1connect.DaemonServiceGetDaemonInfoProcedure, daemonv1connect.DaemonServiceGetOperationProcedure, daemonv1connect.DaemonServiceWaitOperationProcedure, daemonv1connect.DaemonServiceCancelOperationProcedure:
		return "", body, nil
	case daemonv1connect.DaemonServiceSubmitOperationProcedure:
		message := &daemonv1.SubmitOperationRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			return "", nil, fmt.Errorf("decode bridged submit request: %w", err)
		}
		if message.LocalRawCommand || len(message.Environment) != 0 {
			return "", nil, errors.New("file bridge accepts only normal worker execution without environment overrides")
		}
		target = strings.TrimSpace(message.TargetWorkerId)
		if requestID != "" && message.IdempotencyKey == "" {
			message.IdempotencyKey = requestID
			var err error
			body, err = proto.Marshal(message)
			if err != nil {
				return "", nil, fmt.Errorf("bind bridged submit idempotency: %w", err)
			}
		}
	case daemonv1connect.DaemonServiceRegisterWorkerProcedure:
		message := &daemonv1.RegisterWorkerRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			return "", nil, err
		}
		target = strings.TrimSpace(message.WorkerId)
	case daemonv1connect.DaemonServiceLeaseOperationProcedure:
		message := &daemonv1.LeaseOperationRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			return "", nil, err
		}
		target = strings.TrimSpace(message.WorkerId)
	case daemonv1connect.DaemonServiceHeartbeatOperationProcedure:
		message := &daemonv1.HeartbeatOperationRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			return "", nil, err
		}
		target = strings.TrimSpace(message.WorkerId)
	case daemonv1connect.DaemonServiceCompleteOperationProcedure:
		message := &daemonv1.CompleteOperationRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			return "", nil, err
		}
		target = strings.TrimSpace(message.WorkerId)
	case daemonv1connect.DaemonServiceDisconnectWorkerProcedure:
		message := &daemonv1.DisconnectWorkerRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			return "", nil, err
		}
		target = strings.TrimSpace(message.WorkerId)
	default:
		return "", nil, errors.New("daemon file bridge refused an unknown RPC procedure")
	}
	if target == "" {
		return "", nil, errors.New("daemon file bridge worker RPC requires an explicit target worker identity")
	}
	return target, body, nil
}
