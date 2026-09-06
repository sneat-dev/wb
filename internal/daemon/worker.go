package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

const (
	workerHeartbeatInterval = 5 * time.Second
	workerLeaseDuration     = 20 * time.Second
	maxWorkerIDBytes        = 128
	maxWorkerRoots          = 64
	maxWorkerProgressBytes  = 512
	maxWorkerErrorBytes     = 4 << 10
)

type workerState struct {
	id         string
	generation string
	build      string
	os         string
	arch       string
	capacity   uint32
	roots      []string
	lastSeen   time.Time
}

// StartLeaseRecovery watches running worker leases even when no client is
// polling an operation. The daemon owns only this recovery clock; execution
// remains in the connected worker process.
func (service *Service) StartLeaseRecovery(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(workerHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				service.mu.Lock()
				service.reapExpiredWorkerLeasesLocked(service.now())
				service.mu.Unlock()
			}
		}
	}()
}

func (service *Service) RegisterWorker(_ context.Context, request *connect.Request[daemonv1.RegisterWorkerRequest]) (*connect.Response[daemonv1.RegisterWorkerResponse], error) {
	input := request.Msg
	id := strings.TrimSpace(input.WorkerId)
	if id == "" || len(id) > maxWorkerIDBytes || strings.ContainsAny(id, "/\\\x00\r\n") {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id must be 1-%d safe bytes", maxWorkerIDBytes))
	}
	if input.ProtocolVersion != ProtocolVersion {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker protocol %d is incompatible with daemon protocol %d", input.ProtocolVersion, ProtocolVersion))
	}
	if strings.TrimSpace(input.Build) == "" || strings.TrimSpace(input.Os) == "" || strings.TrimSpace(input.Arch) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker build, os, and arch are required"))
	}
	if input.CpuCapacity == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker cpu_capacity must be positive"))
	}
	roots, err := canonicalWorkerRoots(input.PermittedRoots)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	generation, err := randomID("wbwg-")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	now := service.now()
	service.mu.Lock()
	defer service.mu.Unlock()
	service.reapExpiredWorkerLeasesLocked(now)
	if previous := service.workers[id]; previous != nil {
		if err := service.recoverWorkerLocked(previous, "worker reconnected with a new generation; inspect the interrupted operation before retrying", now); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	service.workers[id] = &workerState{
		id: id, generation: generation, build: strings.TrimSpace(input.Build), os: strings.TrimSpace(input.Os), arch: strings.TrimSpace(input.Arch),
		capacity: input.CpuCapacity, roots: roots, lastSeen: now,
	}
	service.notifyLocked()
	return connect.NewResponse(&daemonv1.RegisterWorkerResponse{Registration: &daemonv1.WorkerRegistration{
		WorkerId: id, WorkerGeneration: generation, SchedulerGeneration: service.generation,
		HeartbeatMilliseconds: uint32(workerHeartbeatInterval.Milliseconds()), LeaseMilliseconds: uint32(workerLeaseDuration.Milliseconds()),
	}}), nil
}

func (service *Service) LeaseOperation(ctx context.Context, request *connect.Request[daemonv1.LeaseOperationRequest]) (*connect.Response[daemonv1.LeaseOperationResponse], error) {
	wait := time.Duration(request.Msg.WaitMilliseconds) * time.Millisecond
	if wait <= 0 || wait > 10*time.Second {
		wait = 10 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		now := service.now()
		service.mu.Lock()
		service.reapExpiredWorkerLeasesLocked(now)
		worker, err := service.workerLocked(request.Msg.WorkerId, request.Msg.WorkerGeneration)
		if err != nil {
			service.mu.Unlock()
			return nil, err
		}
		worker.lastSeen = now
		item := service.compatibleQueuedOperationLocked(worker)
		if item != nil {
			assignment, assignErr := service.assignLocked(worker, item, now)
			service.mu.Unlock()
			if assignErr != nil {
				return nil, connect.NewError(connect.CodeInternal, assignErr)
			}
			return connect.NewResponse(&daemonv1.LeaseOperationResponse{Assignment: assignment}), nil
		}
		changed := service.changed
		empty := &daemonv1.WorkerAssignment{WorkerId: worker.id, WorkerGeneration: worker.generation, SchedulerGeneration: service.generation}
		service.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
		case <-deadline.C:
			return connect.NewResponse(&daemonv1.LeaseOperationResponse{Assignment: empty}), nil
		case <-changed:
		}
	}
}

func (service *Service) HeartbeatOperation(_ context.Context, request *connect.Request[daemonv1.HeartbeatOperationRequest]) (*connect.Response[daemonv1.HeartbeatOperationResponse], error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now()
	service.reapExpiredWorkerLeasesLocked(now)
	worker, err := service.workerLocked(request.Msg.WorkerId, request.Msg.WorkerGeneration)
	if err != nil {
		return nil, err
	}
	item, err := service.workerLeaseLocked(worker, request.Msg.OperationId, request.Msg.LeaseId)
	if err != nil {
		return nil, err
	}
	progress := strings.TrimSpace(request.Msg.Progress)
	if len(progress) > maxWorkerProgressBytes || strings.IndexByte(progress, 0) >= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("progress must be at most %d bytes without NUL", maxWorkerProgressBytes))
	}
	previous := cloneOperation(item.Operation)
	item.Operation.LastProgressUnixMilli = now.UnixMilli()
	item.Operation.LeaseExpiresUnixMilli = now.Add(workerLeaseDuration).UnixMilli()
	item.Operation.Progress = progress
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	worker.lastSeen = now
	if err := service.persistLocked(item); err != nil {
		item.Operation = previous
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	service.notifyLocked()
	return connect.NewResponse(&daemonv1.HeartbeatOperationResponse{Operation: cloneOperation(item.Operation)}), nil
}

func (service *Service) CompleteOperation(_ context.Context, request *connect.Request[daemonv1.CompleteOperationRequest]) (*connect.Response[daemonv1.CompleteOperationResponse], error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now()
	service.reapExpiredWorkerLeasesLocked(now)
	worker, err := service.workerLocked(request.Msg.WorkerId, request.Msg.WorkerGeneration)
	if err != nil {
		return nil, err
	}
	item, err := service.workerLeaseLocked(worker, request.Msg.OperationId, request.Msg.LeaseId)
	if err != nil {
		return nil, err
	}
	previous := cloneOperation(item.Operation)
	state := daemonv1.OperationState_OPERATION_STATE_SUCCEEDED
	if request.Msg.ExitCode != 0 || strings.TrimSpace(request.Msg.Error) != "" {
		state = daemonv1.OperationState_OPERATION_STATE_FAILED
	}
	item.Operation.State = state
	item.Operation.ExitCode = request.Msg.ExitCode
	item.Operation.StdoutTail = boundedTail(request.Msg.StdoutTail, outputTailLimit)
	item.Operation.StderrTail = boundedTail(request.Msg.StderrTail, outputTailLimit)
	item.Operation.Error = boundedString(strings.TrimSpace(request.Msg.Error), maxWorkerErrorBytes)
	item.Operation.FinishedUnixMilli = now.UnixMilli()
	item.Operation.LastProgressUnixMilli = now.UnixMilli()
	item.Operation.LeaseExpiresUnixMilli = 0
	item.Operation.Progress = "completed"
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	if item.Operation.StartedUnixMilli != 0 {
		item.Operation.WallMilliseconds = now.UnixMilli() - item.Operation.StartedUnixMilli
	}
	if err := service.persistLocked(item); err != nil {
		item.Operation = previous
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	worker.lastSeen = now
	service.notifyLocked()
	return connect.NewResponse(&daemonv1.CompleteOperationResponse{Operation: cloneOperation(item.Operation)}), nil
}

func (service *Service) DisconnectWorker(_ context.Context, request *connect.Request[daemonv1.DisconnectWorkerRequest]) (*connect.Response[daemonv1.DisconnectWorkerResponse], error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	worker, err := service.workerLocked(request.Msg.WorkerId, request.Msg.WorkerGeneration)
	if err != nil {
		return nil, err
	}
	now := service.now()
	count := service.workerRunningCountLocked(worker)
	if err := service.recoverWorkerLocked(worker, "worker disconnected while the operation was running; inspect side effects before retrying", now); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	delete(service.workers, worker.id)
	service.notifyLocked()
	return connect.NewResponse(&daemonv1.DisconnectWorkerResponse{RecoveryRequiredOperations: uint32(count)}), nil
}

func canonicalWorkerRoots(input []string) ([]string, error) {
	if len(input) == 0 || len(input) > maxWorkerRoots {
		return nil, fmt.Errorf("worker requires 1-%d explicitly permitted roots", maxWorkerRoots)
	}
	seen := map[string]bool{}
	roots := make([]string, 0, len(input))
	for _, raw := range input {
		if !filepath.IsAbs(raw) {
			return nil, fmt.Errorf("worker permitted root must be absolute: %s", raw)
		}
		root, err := filepath.EvalSymlinks(filepath.Clean(raw))
		if err != nil {
			return nil, fmt.Errorf("resolve worker permitted root %s: %w", raw, err)
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("worker permitted root is not a directory: %s", raw)
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return roots, nil
}

func (service *Service) workerLocked(id, generation string) (*workerState, error) {
	worker := service.workers[strings.TrimSpace(id)]
	if worker == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("worker is not registered"))
	}
	if worker.generation != strings.TrimSpace(generation) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("worker generation was superseded; reconnect before accepting work"))
	}
	return worker, nil
}

func (service *Service) compatibleQueuedOperationLocked(worker *workerState) *record {
	items := make([]*record, 0)
	for _, item := range service.records {
		if item.ExecutionMode != executionModeWorker || item.Operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED || item.Operation.CpuUnits > worker.capacity {
			continue
		}
		if permitted, _ := pathWithinAny(worker.roots, item.WorkingDir); !permitted {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Operation.SubmittedUnixMilli == items[j].Operation.SubmittedUnixMilli {
			return items[i].Operation.OperationId < items[j].Operation.OperationId
		}
		return items[i].Operation.SubmittedUnixMilli < items[j].Operation.SubmittedUnixMilli
	})
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func (service *Service) assignLocked(worker *workerState, item *record, now time.Time) (*daemonv1.WorkerAssignment, error) {
	leaseID, err := randomID("wbwl-")
	if err != nil {
		return nil, err
	}
	previous := cloneOperation(item.Operation)
	item.LeaseID = leaseID
	item.Generation = service.generation
	item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RUNNING
	item.Operation.WorkerId = worker.id
	item.Operation.WorkerGeneration = worker.generation
	item.Operation.StartedUnixMilli = now.UnixMilli()
	item.Operation.QueueWaitMilliseconds = now.UnixMilli() - item.Operation.SubmittedUnixMilli
	item.Operation.LastProgressUnixMilli = now.UnixMilli()
	item.Operation.LeaseExpiresUnixMilli = now.Add(workerLeaseDuration).UnixMilli()
	item.Operation.Progress = "leased"
	item.Operation.Cursor = nextCursor(item.Operation.Cursor)
	if err := service.persistLocked(item); err != nil {
		item.Operation = previous
		item.LeaseID = ""
		return nil, err
	}
	service.notifyLocked()
	return &daemonv1.WorkerAssignment{
		WorkerId: worker.id, WorkerGeneration: worker.generation, SchedulerGeneration: service.generation,
		LeaseId: leaseID, LeaseExpiresUnixMilli: item.Operation.LeaseExpiresUnixMilli,
		OperationId: item.Operation.OperationId, WorkingDirectory: item.WorkingDir,
		Argv: append([]string(nil), item.Argv...), CpuUnits: item.Operation.CpuUnits,
	}, nil
}

func (service *Service) workerLeaseLocked(worker *workerState, operationID, leaseID string) (*record, error) {
	item := service.records[strings.TrimSpace(operationID)]
	if item == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	if item.Operation.State != daemonv1.OperationState_OPERATION_STATE_RUNNING || item.Operation.WorkerId != worker.id || item.Operation.WorkerGeneration != worker.generation || item.LeaseID != strings.TrimSpace(leaseID) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("operation lease is no longer owned by this worker generation"))
	}
	return item, nil
}

func (service *Service) recoverWorkerLocked(worker *workerState, reason string, now time.Time) error {
	for _, item := range service.records {
		if item.Operation.State != daemonv1.OperationState_OPERATION_STATE_RUNNING || item.Operation.WorkerId != worker.id || item.Operation.WorkerGeneration != worker.generation {
			continue
		}
		item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
		item.Operation.Error = reason
		item.Operation.FinishedUnixMilli = now.UnixMilli()
		item.Operation.LeaseExpiresUnixMilli = 0
		item.Operation.Progress = "worker unavailable"
		item.Operation.Cursor = nextCursor(item.Operation.Cursor)
		if err := service.persistLocked(item); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) workerRunningCountLocked(worker *workerState) int {
	count := 0
	for _, item := range service.records {
		if item.Operation.State == daemonv1.OperationState_OPERATION_STATE_RUNNING && item.Operation.WorkerId == worker.id && item.Operation.WorkerGeneration == worker.generation {
			count++
		}
	}
	return count
}

func (service *Service) reapExpiredWorkerLeasesLocked(now time.Time) {
	for _, item := range service.records {
		if item.ExecutionMode != executionModeWorker || item.Operation.State != daemonv1.OperationState_OPERATION_STATE_RUNNING || item.Operation.LeaseExpiresUnixMilli <= 0 || now.UnixMilli() <= item.Operation.LeaseExpiresUnixMilli {
			continue
		}
		item.Operation.State = daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
		item.Operation.Error = "worker heartbeat lease expired; inspect side effects before retrying"
		item.Operation.FinishedUnixMilli = now.UnixMilli()
		item.Operation.LeaseExpiresUnixMilli = 0
		item.Operation.Progress = "worker unavailable"
		item.Operation.Cursor = nextCursor(item.Operation.Cursor)
		if err := service.persistLocked(item); err != nil {
			service.markPersistenceFailureLocked(item, "persist expired worker lease", err)
		}
		service.notifyLocked()
	}
}

func pathWithinAny(roots []string, candidate string) (bool, error) {
	if !filepath.IsAbs(candidate) {
		return false, errors.New("assigned working directory must be absolute")
	}
	candidate, err := filepath.EvalSymlinks(filepath.Clean(candidate))
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		relative, relErr := filepath.Rel(root, candidate)
		if relErr == nil && (relative == "." || (relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return true, nil
		}
	}
	return false, nil
}

func boundedTail(value []byte, limit int) []byte {
	if len(value) <= limit {
		return append([]byte(nil), value...)
	}
	return append([]byte(nil), value[len(value)-limit:]...)
}

func boundedString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
