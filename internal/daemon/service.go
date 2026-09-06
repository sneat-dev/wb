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
	ProtocolVersion = 1
	QueueSchema     = 1
	outputTailLimit = 64 << 10
)

type record struct {
	Schema      int                 `json:"schema"`
	Operation   *daemonv1.Operation `json:"operation"`
	WorkingDir  string              `json:"working_directory"`
	Argv        []string            `json:"argv"`
	Environment map[string]string   `json:"environment,omitempty"`
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
	changed    chan struct{}
}

func NewService(projectsRoot, build string) (*Service, error) {
	directory := filepath.Join(projectsRoot, ".wb", "runtime", "daemon", "operations")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create daemon operation store: %w", err)
	}
	generation, err := randomID("wbg-")
	if err != nil {
		return nil, err
	}
	service := &Service{
		projects: projectsRoot, directory: directory, build: build,
		generation: generation, records: map[string]*record{},
		byKey: map[string]string{}, active: map[string]active{}, changed: make(chan struct{}),
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read daemon operation store: %w", err)
	}
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
		if item.Schema > QueueSchema || item.Operation == nil || item.Operation.OperationId == "" {
			return nil, fmt.Errorf("daemon operation %s has unsupported or incomplete schema", entry.Name())
		}
		service.records[item.Operation.OperationId] = &item
		if item.Operation.IdempotencyKey != "" {
			service.byKey[item.Operation.IdempotencyKey] = item.Operation.OperationId
		}
		switch item.Operation.State {
		case daemonv1.OperationState_OPERATION_STATE_RUNNING:
			item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
			item.Operation.Error = "daemon restarted while the operation was running; inspect side effects before retrying"
			item.Operation.Cursor = nextCursor(item.Operation.Cursor)
			item.Operation.FinishedUnixMilli = time.Now().UnixMilli()
			if err := service.persistLocked(&item); err != nil {
				return nil, err
			}
		case daemonv1.OperationState_OPERATION_STATE_QUEUED:
			go service.execute(item.Operation.OperationId)
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
	if !input.LocalRawCommand {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("raw commands are accepted only on the local daemon channel"))
	}
	if len(input.Argv) == 0 || strings.TrimSpace(input.Argv[0]) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("argv must name a command"))
	}
	if !filepath.IsAbs(input.WorkingDirectory) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("working_directory must be absolute"))
	}
	for _, argument := range input.Argv {
		if strings.IndexByte(argument, 0) >= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("argv must not contain NUL bytes"))
		}
	}
	service.mu.Lock()
	if input.IdempotencyKey != "" {
		if id := service.byKey[input.IdempotencyKey]; id != "" {
			operation := cloneOperation(service.records[id].Operation)
			service.mu.Unlock()
			return connect.NewResponse(operation), nil
		}
	}
	id, err := randomID("wbo-")
	if err != nil {
		service.mu.Unlock()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	now := time.Now()
	operation := &daemonv1.Operation{
		SchemaVersion: QueueSchema, OperationId: id, IdempotencyKey: input.IdempotencyKey,
		State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "1",
		CommandKind: classify(input.Argv), ArgsSha256: digest(input.Argv),
		ArgumentCount: uint32(len(input.Argv)), CpuUnits: input.CpuUnits,
		SubmittedUnixMilli: now.UnixMilli(),
	}
	item := &record{Schema: QueueSchema, Operation: operation, WorkingDir: input.WorkingDirectory, Argv: append([]string(nil), input.Argv...), Environment: input.Environment}
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
	go service.execute(id)
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
	item := service.records[request.Msg.OperationId]
	if item == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	if terminal(item.Operation.State) {
		return connect.NewResponse(cloneOperation(item.Operation)), nil
	}
	if running := service.active[item.Operation.OperationId]; running.cancel != nil {
		running.cancel()
	}
	item.Operation.State = daemonv1.OperationState_OPERATION_STATE_CANCELLED
	item.Operation.Error = "cancelled by caller"
	item.Operation.FinishedUnixMilli = time.Now().UnixMilli()
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	if err := service.persistLocked(item); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	service.notifyLocked()
	return connect.NewResponse(cloneOperation(item.Operation)), nil
}

func (service *Service) get(id string) (*daemonv1.Operation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	item := service.records[id]
	if item == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	return cloneOperation(item.Operation), nil
}

func (service *Service) execute(id string) {
	service.mu.Lock()
	item := service.records[id]
	if item == nil || item.Operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
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

	service.mu.Lock()
	item = service.records[id]
	if item == nil || item.Operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		delete(service.active, id)
		service.mu.Unlock()
		return
	}
	item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RUNNING
	item.Operation.CpuUnits = uint32(units)
	item.Operation.QueueWaitMilliseconds = waited.Milliseconds()
	item.Operation.StartedUnixMilli = time.Now().UnixMilli()
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	_ = service.persistLocked(item)
	service.notifyLocked()
	argv := append([]string(nil), item.Argv...)
	workingDir := item.WorkingDir
	environment := cloneMap(item.Environment)
	service.mu.Unlock()

	var stdout, stderr tailBuffer
	child := process.CommandContext(ctx, argv[0], argv[1:]...)
	child.Dir = workingDir
	child.Stdout = &stdout
	child.Stderr = &stderr
	child.Env = mergeEnvironment(os.Environ(), environment)
	started := time.Now()
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
	_ = started
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
	_ = service.persistLocked(item)
	service.notifyLocked()
}

func (service *Service) persistLocked(item *record) error {
	contents, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon operation: %w", err)
	}
	path := filepath.Join(service.directory, item.Operation.OperationId+".json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return fmt.Errorf("write daemon operation: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish daemon operation: %w", err)
	}
	return nil
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

func cloneMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
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
		buffer.Buffer.Reset()
		_, _ = buffer.Buffer.Write(kept)
	}
	return written, nil
}
