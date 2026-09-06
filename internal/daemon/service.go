// Package daemon owns WB's durable local operation queue.
package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/runqueue"
)

const (
	ProtocolVersion       = 1
	QueueSchema           = 1
	outputTailLimit       = 64 << 10
	maxIdempotencyKeySize = 256
	maxArgumentCount      = 1024
	maxArgumentBytes      = 128 << 10
	executionModeWorker   = "worker"
	executionModeRaw      = "trusted_raw"
)

type record struct {
	Schema        int                 `json:"schema"`
	Generation    string              `json:"scheduler_generation"`
	RequestSHA    string              `json:"request_sha256"`
	Operation     *daemonv1.Operation `json:"operation"`
	WorkingDir    string              `json:"working_directory"`
	Argv          []string            `json:"argv"`
	Environment   map[string]string   `json:"environment,omitempty"`
	ExecutionMode string              `json:"execution_mode,omitempty"`
	LeaseID       string              `json:"lease_id,omitempty"`
}

type active struct {
	cancel context.CancelFunc
}

// Service implements the generated ConnectRPC service and persists every
// state transition before it is returned to the caller.
type Service struct {
	mu         sync.Mutex
	projects   string
	directory  string
	build      string
	generation string
	records    map[string]*record
	byKey      map[string]string
	active     map[string]active
	workers    map[string]*workerState
	changed    chan struct{}
	persist    func(*record) error
	authorize  func() error
	now        func() time.Time
}

func NewService(projectsRoot, build, generation string, authorizeRaw func() error) (*Service, error) {
	if authorizeRaw == nil {
		authorizeRaw = func() error { return errors.New("raw daemon execution authorization is not configured") }
	}
	directory := filepath.Join(projectsRoot, ".wb", "runtime", "daemon", "operations")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create daemon operation store: %w", err)
	}
	if generation == "" {
		var err error
		generation, err = randomID("wbg-")
		if err != nil {
			return nil, err
		}
	}
	service := &Service{
		projects: projectsRoot, directory: directory, build: build,
		generation: generation, records: map[string]*record{},
		byKey: map[string]string{}, active: map[string]active{}, workers: map[string]*workerState{}, changed: make(chan struct{}), authorize: authorizeRaw,
		now: func() time.Time { return time.Now().UTC() },
	}
	service.persist = service.persistRecord
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read daemon operation store: %w", err)
	}
	queued := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("read daemon operation %s: %w", entry.Name(), readErr)
		}
		var item record
		if err := json.Unmarshal(contents, &item); err != nil {
			return nil, fmt.Errorf("parse daemon operation %s: %w", entry.Name(), err)
		}
		if item.Schema != QueueSchema || item.Operation == nil || item.Operation.OperationId == "" {
			return nil, fmt.Errorf("daemon operation %s has unsupported or incomplete schema", entry.Name())
		}
		if item.ExecutionMode == "" {
			// Every operation written before the worker protocol was a protected
			// raw operation. Preserve that boundary across the schema addition.
			item.ExecutionMode = executionModeRaw
		}
		service.records[item.Operation.OperationId] = &item
		if item.Operation.IdempotencyKey != "" {
			if item.RequestSHA == "" {
				item.RequestSHA = requestDigest(item.WorkingDir, item.Argv, item.Environment, item.Operation.CpuUnits, item.ExecutionMode)
			}
			service.byKey[item.Operation.IdempotencyKey] = item.Operation.OperationId
		}
		switch item.Operation.State {
		case daemonv1.OperationState_OPERATION_STATE_RUNNING:
			item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
			item.Operation.Error = "daemon restarted while the operation was running; inspect side effects before retrying"
			item.Operation.Cursor = nextCursor(item.Operation.Cursor)
			item.Operation.FinishedUnixMilli = service.now().UnixMilli()
			if err := service.persistLocked(&item); err != nil {
				return nil, err
			}
		case daemonv1.OperationState_OPERATION_STATE_QUEUED:
			if item.ExecutionMode == executionModeWorker {
				queued = append(queued, item.Operation.OperationId)
				continue
			}
			if authorizeErr := service.authorize(); authorizeErr != nil {
				item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
				item.Operation.Error = "raw daemon execution authorization failed before queued work could resume: " + authorizeErr.Error()
				item.Operation.Cursor = nextCursor(item.Operation.Cursor)
				item.Operation.FinishedUnixMilli = time.Now().UnixMilli()
				if err := service.persistLocked(&item); err != nil {
					return nil, err
				}
				continue
			}
			queued = append(queued, item.Operation.OperationId)
		}
	}
	for _, id := range queued {
		if service.records[id].ExecutionMode == executionModeRaw {
			go service.execute(id)
		}
	}
	return service, nil
}

func (service *Service) GetDaemonInfo(_ context.Context, _ *connect.Request[daemonv1.GetDaemonInfoRequest]) (*connect.Response[daemonv1.GetDaemonInfoResponse], error) {
	return connect.NewResponse(&daemonv1.GetDaemonInfoResponse{
		Build: service.build, ProtocolVersion: ProtocolVersion, QueueSchema: QueueSchema,
		SchedulerGeneration: service.generation,
		State:               daemonv1.DaemonState_DAEMON_STATE_READY,
		CpuBudget:           uint32(runqueue.Budget()),
	}), nil
}

func (service *Service) SubmitOperation(_ context.Context, request *connect.Request[daemonv1.SubmitOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	input := request.Msg
	executionMode := executionModeWorker
	if input.LocalRawCommand {
		executionMode = executionModeRaw
		if err := service.authorize(); err != nil {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("raw daemon execution authorization failed: %w", err))
		}
	}
	if len(input.Argv) == 0 || strings.TrimSpace(input.Argv[0]) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("argv must name a command"))
	}
	if len(input.IdempotencyKey) > maxIdempotencyKeySize {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("idempotency_key exceeds %d bytes", maxIdempotencyKeySize))
	}
	if len(input.Argv) > maxArgumentCount {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("argv exceeds %d arguments", maxArgumentCount))
	}
	if !filepath.IsAbs(input.WorkingDirectory) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("working_directory must be absolute"))
	}
	argumentBytes := 0
	for _, argument := range input.Argv {
		argumentBytes += len(argument)
		if argumentBytes > maxArgumentBytes {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("argv exceeds %d aggregate bytes", maxArgumentBytes))
		}
		if strings.IndexByte(argument, 0) >= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("argv must not contain NUL bytes"))
		}
	}
	var environment map[string]string
	if executionMode == executionModeWorker {
		if len(input.Environment) != 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker operations inherit environment inside the worker; environment must not enter the daemon request"))
		}
	} else {
		var err error
		environment, err = allowedEnvironment(input.Environment)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	requestSHA := requestDigest(input.WorkingDirectory, input.Argv, environment, input.CpuUnits, executionMode)
	service.mu.Lock()
	if input.IdempotencyKey != "" {
		if id := service.byKey[input.IdempotencyKey]; id != "" {
			existing := service.records[id]
			if existing.RequestSHA != requestSHA {
				service.mu.Unlock()
				return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency key belongs to a different operation payload"))
			}
			operation := cloneOperation(existing.Operation)
			service.mu.Unlock()
			return connect.NewResponse(operation), nil
		}
	}
	id, err := randomID("wbo-")
	if err != nil {
		service.mu.Unlock()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	now := service.now()
	operation := &daemonv1.Operation{
		SchemaVersion: QueueSchema, OperationId: id, IdempotencyKey: input.IdempotencyKey,
		State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "1",
		CommandKind: classify(input.Argv), ArgsSha256: digest(input.Argv),
		ArgumentCount: uint32(len(input.Argv)), CpuUnits: input.CpuUnits,
		SubmittedUnixMilli: now.UnixMilli(),
	}
	if executionMode == executionModeWorker && operation.CpuUnits == 0 {
		operation.CpuUnits = uint32(runqueue.Units(input.Argv, runqueue.Budget()))
	}
	item := &record{Schema: QueueSchema, Generation: service.generation, RequestSHA: requestSHA, Operation: operation, WorkingDir: input.WorkingDirectory, Argv: append([]string(nil), input.Argv...), Environment: environment, ExecutionMode: executionMode}
	service.records[id] = item
	if input.IdempotencyKey != "" {
		service.byKey[input.IdempotencyKey] = id
	}
	if err := service.persistLocked(item); err != nil {
		delete(service.records, id)
		delete(service.byKey, input.IdempotencyKey)
		service.mu.Unlock()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	service.notifyLocked()
	result := cloneOperation(operation)
	service.mu.Unlock()
	if executionMode == executionModeRaw {
		go service.execute(id)
	}
	return connect.NewResponse(result), nil
}

func (service *Service) GetOperation(_ context.Context, request *connect.Request[daemonv1.GetOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	operation, err := service.get(request.Msg.OperationId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(operation), nil
}

func (service *Service) WaitOperation(ctx context.Context, request *connect.Request[daemonv1.WaitOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	wait := time.Duration(request.Msg.WaitMilliseconds) * time.Millisecond
	if wait <= 0 || wait > 30*time.Second {
		wait = 30 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		service.mu.Lock()
		service.reapExpiredWorkerLeasesLocked(service.now())
		item := service.records[request.Msg.OperationId]
		if item == nil {
			service.mu.Unlock()
			return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
		}
		operation := cloneOperation(item.Operation)
		changed := service.changed
		service.mu.Unlock()
		if operation.Cursor != request.Msg.AfterCursor || terminal(operation.State) {
			return connect.NewResponse(operation), nil
		}
		select {
		case <-ctx.Done():
			return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
		case <-deadline.C:
			return connect.NewResponse(operation), nil
		case <-changed:
		}
	}
}

func (service *Service) CancelOperation(_ context.Context, request *connect.Request[daemonv1.CancelOperationRequest]) (*connect.Response[daemonv1.Operation], error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.reapExpiredWorkerLeasesLocked(service.now())
	item := service.records[request.Msg.OperationId]
	if item == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	if terminal(item.Operation.State) {
		return connect.NewResponse(cloneOperation(item.Operation)), nil
	}
	previous := cloneOperation(item.Operation)
	item.Operation.State = daemonv1.OperationState_OPERATION_STATE_CANCELLED
	item.Operation.Error = "cancelled by caller"
	item.Operation.FinishedUnixMilli = time.Now().UnixMilli()
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	if err := service.persistLocked(item); err != nil {
		item.Operation = previous
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if running := service.active[item.Operation.OperationId]; running.cancel != nil {
		running.cancel()
	}
	service.notifyLocked()
	return connect.NewResponse(cloneOperation(item.Operation)), nil
}

func (service *Service) get(id string) (*daemonv1.Operation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.reapExpiredWorkerLeasesLocked(service.now())
	item := service.records[id]
	if item == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	return cloneOperation(item.Operation), nil
}

func (service *Service) execute(id string) {
	service.mu.Lock()
	item := service.records[id]
	if item == nil || item.ExecutionMode != executionModeRaw || item.Operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		service.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.active[id] = active{cancel: cancel}
	service.mu.Unlock()

	units := int(item.Operation.CpuUnits)
	if units == 0 {
		units = runqueue.Units(item.Argv, runqueue.Budget())
	}
	lease, waited, err := runqueue.Acquire(ctx, service.projects, units, runqueue.Budget())
	if err != nil {
		service.finish(id, daemonv1.OperationState_OPERATION_STATE_CANCELLED, 1, nil, nil, waited, err)
		return
	}
	defer lease.Release()
	if authorizeErr := service.authorize(); authorizeErr != nil {
		service.failAuthorization(id, authorizeErr)
		return
	}

	service.mu.Lock()
	item = service.records[id]
	if item == nil || item.Operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		delete(service.active, id)
		service.mu.Unlock()
		return
	}
	item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RUNNING
	item.Generation = service.generation
	item.Operation.CpuUnits = uint32(units)
	item.Operation.QueueWaitMilliseconds = waited.Milliseconds()
	item.Operation.StartedUnixMilli = time.Now().UnixMilli()
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	if err := service.persistLocked(item); err != nil {
		delete(service.active, id)
		service.markPersistenceFailureLocked(item, "persist running transition", err)
		service.notifyLocked()
		service.mu.Unlock()
		return
	}
	service.notifyLocked()
	argv := append([]string(nil), item.Argv...)
	workingDir := item.WorkingDir
	environment := governedChildEnvironment(os.Environ(), item.Environment, id, units)
	service.mu.Unlock()

	var stdout, stderr tailBuffer
	child := process.CommandContext(ctx, argv[0], argv[1:]...)
	child.Dir = workingDir
	child.Stdout = &stdout
	child.Stderr = &stderr
	child.Env = environment
	err = child.Run()
	exitCode := 0
	state := daemonv1.OperationState_OPERATION_STATE_SUCCEEDED
	if err != nil {
		exitCode = 1
		state = daemonv1.OperationState_OPERATION_STATE_FAILED
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	service.finish(id, state, exitCode, stdout.Bytes(), stderr.Bytes(), waited, err)
}

func (service *Service) failAuthorization(id string, authorizeErr error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	item := service.records[id]
	if item == nil || item.Operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		delete(service.active, id)
		return
	}
	delete(service.active, id)
	item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
	item.Operation.Error = "raw daemon execution authorization failed immediately before launch: " + authorizeErr.Error()
	item.Operation.FinishedUnixMilli = time.Now().UnixMilli()
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	if err := service.persistLocked(item); err != nil {
		service.markPersistenceFailureLocked(item, "persist authorization failure", err)
	}
	service.notifyLocked()
}

func (service *Service) finish(id string, state daemonv1.OperationState, exitCode int, stdout, stderr []byte, waited time.Duration, runErr error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	item := service.records[id]
	if item == nil {
		return
	}
	delete(service.active, id)
	if item.Operation.State == daemonv1.OperationState_OPERATION_STATE_CANCELLED {
		return
	}
	now := time.Now()
	item.Operation.State = state
	item.Operation.ExitCode = int32(exitCode)
	item.Operation.StdoutTail = append([]byte(nil), stdout...)
	item.Operation.StderrTail = append([]byte(nil), stderr...)
	item.Operation.QueueWaitMilliseconds = waited.Milliseconds()
	item.Operation.FinishedUnixMilli = now.UnixMilli()
	if item.Operation.StartedUnixMilli != 0 {
		item.Operation.WallMilliseconds = now.UnixMilli() - item.Operation.StartedUnixMilli
	}
	if runErr != nil {
		item.Operation.Error = runErr.Error()
	}
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	if err := service.persistLocked(item); err != nil {
		service.markPersistenceFailureLocked(item, "persist terminal transition", err)
	}
	service.notifyLocked()
}

func (service *Service) persistLocked(item *record) error {
	return service.persist(item)
}

func (service *Service) persistRecord(item *record) error {
	contents, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon operation: %w", err)
	}
	path := filepath.Join(service.directory, item.Operation.OperationId+".json")
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write daemon operation: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write daemon operation: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync daemon operation: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close daemon operation: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish daemon operation: %w", err)
	}
	return nil
}

func (service *Service) markPersistenceFailureLocked(item *record, transition string, persistErr error) {
	item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
	item.Operation.Error = transition + ": " + persistErr.Error()
	item.Operation.FinishedUnixMilli = time.Now().UnixMilli()
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	// A transient failure can still preserve the explicit recovery disposition.
	// If storage remains unavailable, the in-memory result reports the failure
	// and the on-disk RUNNING record becomes recovery-required on next startup.
	_ = service.persist(item)
}

func (service *Service) notifyLocked() {
	close(service.changed)
	service.changed = make(chan struct{})
}

func terminal(state daemonv1.OperationState) bool {
	return state == daemonv1.OperationState_OPERATION_STATE_SUCCEEDED ||
		state == daemonv1.OperationState_OPERATION_STATE_FAILED ||
		state == daemonv1.OperationState_OPERATION_STATE_CANCELLED ||
		state == daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
}

func cloneOperation(operation *daemonv1.Operation) *daemonv1.Operation {
	return proto.Clone(operation).(*daemonv1.Operation)
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate daemon ID: %w", err)
	}
	return prefix + hex.EncodeToString(value), nil
}

func nextCursor(cursor string) string {
	switch cursor {
	case "", "0":
		return "1"
	case "1":
		return "2"
	case "2":
		return "3"
	default:
		return cursor + ".1"
	}
}

func digest(argv []string) string {
	encoded, _ := json.Marshal(argv)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func requestDigest(workingDirectory string, argv []string, environment map[string]string, cpuUnits uint32, executionMode string) string {
	encoded, _ := json.Marshal(struct {
		WorkingDirectory string            `json:"working_directory"`
		Argv             []string          `json:"argv"`
		Environment      map[string]string `json:"environment,omitempty"`
		CPUUnits         uint32            `json:"cpu_units,omitempty"`
		ExecutionMode    string            `json:"execution_mode"`
	}{
		WorkingDirectory: workingDirectory,
		Argv:             argv,
		Environment:      environment,
		CPUUnits:         cpuUnits,
		ExecutionMode:    executionMode,
	})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func classify(argv []string) string {
	if len(argv) == 0 {
		return "unknown"
	}
	tool := filepath.Base(argv[0])
	if len(argv) > 1 && !strings.HasPrefix(argv[1], "-") {
		return tool + "/" + argv[1]
	}
	return tool
}

var durableEnvironmentAllowlist = map[string]bool{
	"CI": true, "COLORTERM": true, "NO_COLOR": true, "TERM": true,
}

func allowedEnvironment(input map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(input))
	for key, value := range input {
		if !durableEnvironmentAllowlist[key] {
			return nil, fmt.Errorf("environment override %q is not allowed in durable operations", key)
		}
		if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("environment override %q has an invalid value", key)
		}
		result[key] = value
	}
	return result, nil
}

func governedChildEnvironment(base []string, additions map[string]string, operationID string, units int) []string {
	result := mergeEnvironment(base, additions)
	result = mergeEnvironment(result, map[string]string{
		"GOMAXPROCS":      fmt.Sprint(units),
		"NX_PARALLEL":     fmt.Sprint(units),
		"WB_CPU_UNITS":    fmt.Sprint(units),
		"WB_OPERATION_ID": operationID,
	})
	return result
}

func mergeEnvironment(base []string, additions map[string]string) []string {
	result := append([]string(nil), base...)
	for key, value := range additions {
		prefix := key + "="
		filtered := result[:0]
		for _, entry := range result {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, prefix+value)
	}
	return result
}

type tailBuffer struct{ bytes.Buffer }

func (buffer *tailBuffer) Write(contents []byte) (int, error) {
	written := len(contents)
	_, _ = buffer.Buffer.Write(contents)
	if buffer.Len() > outputTailLimit {
		kept := append([]byte(nil), buffer.Bytes()[buffer.Len()-outputTailLimit:]...)
		buffer.Reset()
		_, _ = buffer.Buffer.Write(kept)
	}
	return written, nil
}
