package sessioncourier

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
)

const (
	synchestraExecutableName             = "synchestra"
	synchestraSessionAcceptHandler       = sessionmove.SynchestraSessionAcceptHandler
	synchestraInvocationProtocolVersion  = "synchestra.handler-invocation.v1"
	synchestraDispatchProtocolVersion    = "synchestra.dispatch.v1"
	synchestraReceiptArtifactVersion     = "synchestra.wb-handler-receipt-artifact.v1"
	synchestraReceiptArtifactPrefix      = "synchestra-artifact:wb-handler-receipt.v1:"
	synchestraDeliveryTimeout            = 2 * time.Minute
	synchestraPollInterval               = time.Second
	maxSynchestraStatusPolls             = 120
	maxSynchestraRequestBytes            = 1 << 20
	maxSynchestraCommandStdoutBytes      = 2 << 20
	maxSynchestraCommandStderrBytes      = 64 << 10
	maxSynchestraReceiptBytes            = 256 << 10
	maxSynchestraReceiptArtifactBytes    = ((maxSynchestraReceiptBytes + 2) / 3 * 4) + 4096
	maxSynchestraReceiptArtifactRefBytes = ((maxSynchestraReceiptArtifactBytes + 2) / 3 * 4) + len(synchestraReceiptArtifactPrefix)
)

var synchestraDispatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// SynchestraOptions binds the adapter to a previously accepted dispatch and
// durably records a newly accepted one before the first status poll.
type SynchestraOptions struct {
	Dispatch     *sessionmove.SynchestraDispatch
	SaveDispatch func(sessionmove.SynchestraDispatch) error
}

type synchestraDeliverer struct {
	config     sessionmove.SynchestraConfig
	options    SynchestraOptions
	executable string
	runner     commandRunner
	sleep      func(context.Context, time.Duration) error
}

// NewSynchestraDeliverer resolves the trusted local CLI and constructs the
// typed fixed-handler adapter. It never falls back to SSH.
func NewSynchestraDeliverer(config sessionmove.SynchestraConfig, options SynchestraOptions) (Deliverer, error) {
	return newSynchestraDeliverer(config, options, exec.LookPath, execCommandRunner{}, sleepWithContext)
}

func newSynchestraDeliverer(
	config sessionmove.SynchestraConfig,
	options SynchestraOptions,
	lookPath func(string) (string, error),
	runner commandRunner,
	sleep func(context.Context, time.Duration) error,
) (*synchestraDeliverer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if lookPath == nil {
		return nil, fmt.Errorf("resolve synchestra executable: executable lookup is unavailable")
	}
	executable, err := lookPath(synchestraExecutableName)
	if err != nil {
		return nil, fmt.Errorf("resolve synchestra executable: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, fmt.Errorf("resolve synchestra executable: %q is not a clean absolute path", executable)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve synchestra executable %s: %w", executable, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("resolve synchestra executable: %q is not a regular executable file", executable)
	}
	if runner == nil {
		return nil, fmt.Errorf("run synchestra courier: command runner is unavailable")
	}
	if sleep == nil {
		return nil, fmt.Errorf("poll synchestra courier: clock is unavailable")
	}
	if options.Dispatch == nil && options.SaveDispatch == nil {
		return nil, fmt.Errorf("construct synchestra courier: durable dispatch recorder is unavailable")
	}
	if options.Dispatch != nil {
		copy := *options.Dispatch
		options.Dispatch = &copy
	}
	return &synchestraDeliverer{config: config, options: options, executable: executable, runner: runner, sleep: sleep}, nil
}

func (d *synchestraDeliverer) Deliver(ctx context.Context, raw []byte) (sessionreceive.Result, error) {
	var result sessionreceive.Result
	request, err := validateReceiverRequest(raw, maxSynchestraRequestBytes)
	if err != nil {
		return result, fmt.Errorf("validate synchestra session request: %w", err)
	}
	deliveryContext, cancel := context.WithTimeout(ctx, synchestraDeliveryTimeout)
	defer cancel()

	var output synchestraInvocationOutput
	if d.options.Dispatch == nil {
		args := []string{
			"runner", "invoke", "@/dev/stdin", "--runner", d.config.Runner,
			"--handler", synchestraSessionAcceptHandler, "--invocation-id", request.HandoffID,
			"--format", "json",
		}
		rawOutput, runErr := d.run(deliveryContext, args, raw, "invoke handler")
		if runErr != nil {
			return result, runErr
		}
		output, err = decodeSynchestraInvocationOutput(rawOutput)
		if err != nil {
			return result, fmt.Errorf("validate synchestra invoke response: %w", err)
		}
		identity, identityErr := validateSynchestraInvocationOutput(output, d.config.Runner, request, raw, "")
		if identityErr != nil {
			return result, fmt.Errorf("validate synchestra invoke response: %w", identityErr)
		}
		if d.options.SaveDispatch != nil {
			if saveErr := d.options.SaveDispatch(identity); saveErr != nil {
				return result, fmt.Errorf("persist synchestra dispatch identity: %w", saveErr)
			}
		}
		d.options.Dispatch = &identity
	} else {
		if err := validateSynchestraResumeIdentity(*d.options.Dispatch, d.config.Runner, request, raw); err != nil {
			return result, fmt.Errorf("validate persisted synchestra dispatch identity: %w", err)
		}
		rawOutput, runErr := d.run(deliveryContext, synchestraStatusArgs(d.options.Dispatch.DispatchID), nil, "observe dispatch")
		if runErr != nil {
			return result, runErr
		}
		output, err = decodeSynchestraInvocationOutput(rawOutput)
		if err != nil {
			return result, fmt.Errorf("validate synchestra status response: %w", err)
		}
		if _, err := validateSynchestraInvocationOutput(output, d.config.Runner, request, raw, d.options.Dispatch.DispatchID); err != nil {
			return result, fmt.Errorf("validate synchestra status response: %w", err)
		}
	}

	for polls := 0; ; polls++ {
		terminalReceipt, pending, terminalErr := synchestraTerminalReceipt(output, request, raw)
		if terminalErr != nil {
			return result, terminalErr
		}
		if !pending {
			return terminalReceipt, nil
		}
		if polls >= maxSynchestraStatusPolls {
			return result, fmt.Errorf("synchestra dispatch %s did not complete after %d bounded status polls", d.options.Dispatch.DispatchID, maxSynchestraStatusPolls)
		}
		if err := d.sleep(deliveryContext, synchestraPollInterval); err != nil {
			return result, fmt.Errorf("wait to observe synchestra dispatch %s: %w", d.options.Dispatch.DispatchID, err)
		}
		rawOutput, runErr := d.run(deliveryContext, synchestraStatusArgs(d.options.Dispatch.DispatchID), nil, "observe dispatch")
		if runErr != nil {
			return result, runErr
		}
		output, err = decodeSynchestraInvocationOutput(rawOutput)
		if err != nil {
			return result, fmt.Errorf("validate synchestra status response: %w", err)
		}
		if _, err := validateSynchestraInvocationOutput(output, d.config.Runner, request, raw, d.options.Dispatch.DispatchID); err != nil {
			return result, fmt.Errorf("validate synchestra status response: %w", err)
		}
	}
}

func (d *synchestraDeliverer) run(ctx context.Context, args []string, stdin []byte, operation string) ([]byte, error) {
	var stdout, stderr boundedBuffer
	stdout.limit = maxSynchestraCommandStdoutBytes
	stderr.limit = maxSynchestraCommandStderrBytes
	if err := d.runner.Run(ctx, d.executable, args, stdin, &stdout, &stderr); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, fmt.Errorf("synchestra session delivery: %w", contextErr)
		}
		diagnostic := sanitizeDiagnostic(stderr.Bytes(), stderr.exceeded)
		if diagnostic == "" {
			return nil, fmt.Errorf("synchestra %s: %w", operation, err)
		}
		return nil, fmt.Errorf("synchestra %s: %w: %s", operation, err, diagnostic)
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("synchestra %s response exceeds %d bytes", operation, maxSynchestraCommandStdoutBytes)
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func synchestraStatusArgs(dispatchID string) []string {
	return []string{"runner", "dispatch", "status", dispatchID, "--format", "json"}
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type synchestraInvocationMetadata struct {
	ProtocolVersion string     `json:"protocol_version"`
	ID              string     `json:"id"`
	Handler         string     `json:"handler"`
	PayloadDigest   string     `json:"payload_digest"`
	PayloadSize     int64      `json:"payload_size"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	Deadline        *time.Time `json:"deadline,omitempty"`
}

type synchestraResolvedOutput struct {
	Operation          string                        `json:"operation"`
	DispatchID         string                        `json:"dispatch_id,omitempty"`
	Cursor             int64                         `json:"cursor,omitempty"`
	Source             *struct{}                     `json:"source,omitempty"`
	Repository         *synchestraRepositoryOutput   `json:"repository,omitempty"`
	RequestedExecution *struct{}                     `json:"requested_execution,omitempty"`
	Runner             string                        `json:"runner,omitempty"`
	Invocation         *synchestraInvocationMetadata `json:"invocation,omitempty"`
}

type synchestraRepositoryOutput struct {
	CanonicalID   string `json:"canonical_id"`
	CloneURL      string `json:"clone_url"`
	BaseRevision  string `json:"base_revision"`
	BaseRef       string `json:"base_ref,omitempty"`
	Subdirectory  string `json:"subdirectory,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	CredentialRef string `json:"credential_ref,omitempty"`
}

type synchestraCancellationOutput struct {
	RequestedAt    time.Time  `json:"requested_at"`
	RequestedBy    string     `json:"requested_by"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

type synchestraDispatchOutput struct {
	ProtocolVersion string                        `json:"protocol_version"`
	ID              string                        `json:"id"`
	Status          string                        `json:"status"`
	AttemptIDs      []string                      `json:"attempt_ids"`
	ActiveAttemptID string                        `json:"active_attempt_id,omitempty"`
	Cancellation    *synchestraCancellationOutput `json:"cancellation,omitempty"`
	CreatedAt       time.Time                     `json:"created_at"`
	UpdatedAt       time.Time                     `json:"updated_at"`
}

type synchestraAttemptResult struct {
	ArtifactReferences []string  `json:"artifact_references"`
	PublishedAt        time.Time `json:"published_at"`
}

type synchestraAttemptFailure struct {
	Stage     string               `json:"stage"`
	Code      string               `json:"code"`
	Retryable bool                 `json:"retryable"`
	Logs      *synchestraLogOutput `json:"logs,omitempty"`
}

type synchestraWorkerOutput struct {
	WorkerID string `json:"worker_id"`
	HostID   string `json:"host_id"`
	RunnerID string `json:"runner_id,omitempty"`
}

type synchestraLeaseOutput struct {
	Owner           synchestraWorkerOutput `json:"owner"`
	Generation      int64                  `json:"generation"`
	AcquiredAt      time.Time              `json:"acquired_at"`
	ExpiresAt       time.Time              `json:"expires_at"`
	LastHeartbeatAt time.Time              `json:"last_heartbeat_at"`
}

type synchestraLogOutput struct {
	SessionID      string     `json:"session_id"`
	StreamID       string     `json:"stream_id"`
	LastSequence   int64      `json:"last_sequence"`
	Href           string     `json:"href,omitempty"`
	RetentionUntil *time.Time `json:"retention_until,omitempty"`
}

type synchestraSessionOutput struct {
	ID        string               `json:"id"`
	Runtime   string               `json:"runtime"`
	StartedAt time.Time            `json:"started_at"`
	EndedAt   *time.Time           `json:"ended_at,omitempty"`
	Logs      *synchestraLogOutput `json:"logs,omitempty"`
}

type synchestraAttemptOutput struct {
	ProtocolVersion string                    `json:"protocol_version"`
	ID              string                    `json:"id"`
	DispatchID      string                    `json:"dispatch_id"`
	Number          int                       `json:"number"`
	Status          string                    `json:"status"`
	Worker          *synchestraWorkerOutput   `json:"worker,omitempty"`
	Lease           *synchestraLeaseOutput    `json:"lease,omitempty"`
	Session         *synchestraSessionOutput  `json:"session,omitempty"`
	Logs            *synchestraLogOutput      `json:"logs,omitempty"`
	Result          *synchestraAttemptResult  `json:"result,omitempty"`
	Failure         *synchestraAttemptFailure `json:"failure,omitempty"`
	CancellationAck *time.Time                `json:"cancellation_acknowledged_at,omitempty"`
	CreatedAt       time.Time                 `json:"created_at"`
	StartedAt       *time.Time                `json:"started_at,omitempty"`
	FinishedAt      *time.Time                `json:"finished_at,omitempty"`
}

type synchestraInvocationOutput struct {
	Resolved synchestraResolvedOutput   `json:"resolved"`
	Dispatch *synchestraDispatchOutput  `json:"dispatch,omitempty"`
	Attempts []synchestraAttemptOutput  `json:"attempts"`
	Created  *bool                      `json:"created,omitempty"`
	Error    *synchestraInvocationError `json:"error,omitempty"`
}

type synchestraInvocationError struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

func decodeSynchestraInvocationOutput(raw []byte) (synchestraInvocationOutput, error) {
	var output synchestraInvocationOutput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return output, fmt.Errorf("decode one runner invocation result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return synchestraInvocationOutput{}, fmt.Errorf("runner invocation result has a trailing JSON value")
		}
		return synchestraInvocationOutput{}, fmt.Errorf("decode trailing runner invocation output: %w", err)
	}
	if output.Error != nil {
		return synchestraInvocationOutput{}, fmt.Errorf("runner invocation reported an error")
	}
	return output, nil
}

func validateSynchestraInvocationOutput(
	output synchestraInvocationOutput,
	runner string,
	request sessionmove.Request,
	raw []byte,
	expectedDispatchID string,
) (sessionmove.SynchestraDispatch, error) {
	if output.Dispatch == nil || output.Resolved.Invocation == nil {
		return sessionmove.SynchestraDispatch{}, fmt.Errorf("runner output lacks typed invocation and dispatch identity")
	}
	dispatch := output.Dispatch
	invocation := output.Resolved.Invocation
	if dispatch.ProtocolVersion != synchestraDispatchProtocolVersion {
		return sessionmove.SynchestraDispatch{}, fmt.Errorf("dispatch protocol_version %q is unsupported", dispatch.ProtocolVersion)
	}
	if !synchestraDispatchIDPattern.MatchString(dispatch.ID) {
		return sessionmove.SynchestraDispatch{}, fmt.Errorf("dispatch id is invalid")
	}
	if expectedDispatchID != "" && dispatch.ID != expectedDispatchID {
		return sessionmove.SynchestraDispatch{}, fmt.Errorf("dispatch id %q does not match persisted dispatch %q", dispatch.ID, expectedDispatchID)
	}
	expectedOperation := "invoke"
	expectedResolvedDispatchID := ""
	if expectedDispatchID != "" {
		expectedOperation = "status"
		expectedResolvedDispatchID = dispatch.ID
	}
	if output.Resolved.Operation != expectedOperation || output.Resolved.DispatchID != expectedResolvedDispatchID ||
		output.Resolved.Runner != runner || output.Resolved.Source != nil || output.Resolved.RequestedExecution != nil {
		return sessionmove.SynchestraDispatch{}, fmt.Errorf("resolved runner or dispatch identity does not match the selected route")
	}
	if invocation.ProtocolVersion != synchestraInvocationProtocolVersion || invocation.ID != request.HandoffID ||
		invocation.Handler != synchestraSessionAcceptHandler || invocation.PayloadDigest != string(sessionmove.DigestBytes(raw)) ||
		invocation.PayloadSize != int64(len(raw)) || invocation.Deadline != nil {
		return sessionmove.SynchestraDispatch{}, fmt.Errorf("typed invocation identity, handler, payload_digest, or payload_size does not match the delivered request")
	}
	for _, attempt := range output.Attempts {
		if attempt.ProtocolVersion != synchestraDispatchProtocolVersion || attempt.DispatchID != dispatch.ID {
			return sessionmove.SynchestraDispatch{}, fmt.Errorf("attempt identity does not match dispatch %s", dispatch.ID)
		}
	}
	if err := validateSynchestraAttemptHistory(*dispatch, output.Attempts); err != nil {
		return sessionmove.SynchestraDispatch{}, err
	}
	return sessionmove.SynchestraDispatch{
		SchemaVersion: sessionmove.SynchestraDispatchSchemaVersion,
		HandoffID:     request.HandoffID, RequestDigest: sessionmove.DigestBytes(raw), Runner: runner,
		InvocationID: request.HandoffID, Handler: synchestraSessionAcceptHandler, DispatchID: dispatch.ID,
	}, nil
}

func validateSynchestraAttemptHistory(dispatch synchestraDispatchOutput, attempts []synchestraAttemptOutput) error {
	if len(dispatch.AttemptIDs) != len(attempts) {
		return fmt.Errorf("dispatch attempt history cardinality does not match returned attempts")
	}
	declared := make(map[string]struct{}, len(dispatch.AttemptIDs))
	for _, id := range dispatch.AttemptIDs {
		if id == "" {
			return fmt.Errorf("dispatch attempt history contains an empty attempt id")
		}
		if _, duplicate := declared[id]; duplicate {
			return fmt.Errorf("dispatch attempt history contains duplicate attempt id %q", id)
		}
		declared[id] = struct{}{}
	}
	returned := make(map[string]struct{}, len(attempts))
	numbers := make(map[int]struct{}, len(attempts))
	for _, attempt := range attempts {
		if _, ok := declared[attempt.ID]; !ok {
			return fmt.Errorf("returned attempt %q is absent from dispatch attempt history", attempt.ID)
		}
		if _, duplicate := returned[attempt.ID]; duplicate {
			return fmt.Errorf("returned attempts contain duplicate id %q", attempt.ID)
		}
		if attempt.Number <= 0 {
			return fmt.Errorf("returned attempt %q has invalid number %d", attempt.ID, attempt.Number)
		}
		if _, duplicate := numbers[attempt.Number]; duplicate {
			return fmt.Errorf("returned attempts contain duplicate number %d", attempt.Number)
		}
		switch attempt.Status {
		case "queued", "leased", "running", "completed", "failed", "cancelled", "abandoned":
		default:
			return fmt.Errorf("returned attempt %q has unsupported status %q", attempt.ID, attempt.Status)
		}
		returned[attempt.ID] = struct{}{}
		numbers[attempt.Number] = struct{}{}
	}
	if dispatch.ActiveAttemptID != "" {
		if _, ok := declared[dispatch.ActiveAttemptID]; !ok {
			return fmt.Errorf("active attempt %q is absent from dispatch attempt history", dispatch.ActiveAttemptID)
		}
	}
	return nil
}

func validateSynchestraResumeIdentity(identity sessionmove.SynchestraDispatch, runner string, request sessionmove.Request, raw []byte) error {
	if identity.SchemaVersion != sessionmove.SynchestraDispatchSchemaVersion || identity.HandoffID != request.HandoffID ||
		identity.RequestDigest != sessionmove.DigestBytes(raw) || identity.Runner != runner ||
		identity.InvocationID != request.HandoffID || identity.Handler != synchestraSessionAcceptHandler ||
		!synchestraDispatchIDPattern.MatchString(identity.DispatchID) {
		return fmt.Errorf("persisted invocation/dispatch identity does not match exact request and runner")
	}
	return nil
}

func synchestraTerminalReceipt(output synchestraInvocationOutput, request sessionmove.Request, raw []byte) (sessionreceive.Result, bool, error) {
	if output.Dispatch == nil {
		return sessionreceive.Result{}, false, fmt.Errorf("synchestra response has no dispatch")
	}
	switch output.Dispatch.Status {
	case "queued", "leased", "running":
		return sessionreceive.Result{}, true, nil
	case "failed", "cancelled":
		return sessionreceive.Result{}, false, fmt.Errorf("synchestra dispatch %s ended %s without a WB receipt", output.Dispatch.ID, output.Dispatch.Status)
	case "completed":
	default:
		return sessionreceive.Result{}, false, fmt.Errorf("synchestra dispatch status %q is unsupported", output.Dispatch.Status)
	}

	completed := 0
	artifact := ""
	for _, attempt := range output.Attempts {
		if attempt.Status != "completed" {
			continue
		}
		completed++
		if attempt.Result == nil || len(attempt.Result.ArtifactReferences) != 1 {
			return sessionreceive.Result{}, false, fmt.Errorf("completed synchestra attempt must contain exactly one WB receipt artifact")
		}
		artifact = attempt.Result.ArtifactReferences[0]
	}
	if completed != 1 || artifact == "" {
		return sessionreceive.Result{}, false, fmt.Errorf("completed synchestra dispatch must contain exactly one completed WB receipt attempt")
	}
	receiptBytes, err := decodeSynchestraReceiptArtifact(artifact, request, raw)
	if err != nil {
		return sessionreceive.Result{}, false, fmt.Errorf("validate synchestra WB receipt artifact: %w", err)
	}
	result, err := decodeReceiverResult(receiptBytes)
	if err != nil {
		return sessionreceive.Result{}, false, err
	}
	if err := validateReceiverResult(result, request, raw); err != nil {
		return sessionreceive.Result{}, false, err
	}
	return result, false, nil
}

type synchestraReceiptArtifact struct {
	ProtocolVersion string    `json:"protocol_version"`
	InvocationID    string    `json:"invocation_id"`
	Handler         string    `json:"handler"`
	PayloadDigest   string    `json:"payload_digest"`
	ReceiptDigest   string    `json:"receipt_digest"`
	Receipt         []byte    `json:"receipt"`
	CompletedAt     time.Time `json:"completed_at"`
}

func decodeSynchestraReceiptArtifact(reference string, request sessionmove.Request, raw []byte) ([]byte, error) {
	if !strings.HasPrefix(reference, synchestraReceiptArtifactPrefix) || len(reference) > maxSynchestraReceiptArtifactRefBytes {
		return nil, fmt.Errorf("receipt artifact reference is invalid")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(reference, synchestraReceiptArtifactPrefix))
	if err != nil || len(encoded) == 0 || len(encoded) > maxSynchestraReceiptArtifactBytes {
		return nil, fmt.Errorf("receipt artifact reference is invalid")
	}
	var artifact synchestraReceiptArtifact
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return nil, fmt.Errorf("receipt artifact is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("receipt artifact is invalid")
	}
	canonical, err := json.Marshal(artifact)
	if err != nil || synchestraReceiptArtifactPrefix+base64.RawURLEncoding.EncodeToString(canonical) != reference {
		return nil, fmt.Errorf("receipt artifact is not canonical")
	}
	if artifact.ProtocolVersion != synchestraReceiptArtifactVersion {
		return nil, fmt.Errorf("protocol_version is unsupported")
	}
	if artifact.InvocationID != request.HandoffID {
		return nil, fmt.Errorf("invocation_id does not match handoff")
	}
	if artifact.Handler != synchestraSessionAcceptHandler {
		return nil, fmt.Errorf("handler does not match fixed receiver")
	}
	if artifact.PayloadDigest != string(sessionmove.DigestBytes(raw)) {
		return nil, fmt.Errorf("payload_digest does not match exact request bytes")
	}
	if artifact.ReceiptDigest != string(sessionmove.DigestBytes(artifact.Receipt)) {
		return nil, fmt.Errorf("receipt_digest does not match exact receipt bytes")
	}
	trimmed := bytes.TrimSpace(artifact.Receipt)
	if len(artifact.Receipt) == 0 || len(artifact.Receipt) > maxSynchestraReceiptBytes || len(trimmed) < 2 ||
		trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return nil, fmt.Errorf("receipt is not one bounded JSON object")
	}
	_, offset := artifact.CompletedAt.Zone()
	if artifact.CompletedAt.IsZero() || offset != 0 {
		return nil, fmt.Errorf("completed_at is not canonical UTC")
	}
	return append([]byte(nil), artifact.Receipt...), nil
}
