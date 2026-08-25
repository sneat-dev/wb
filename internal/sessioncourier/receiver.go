package sessioncourier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
)

// validateReceiverRequest applies the byte contract shared by every courier:
// one bounded canonical WB request whose exact bytes are transported intact.
func validateReceiverRequest(raw []byte, maximum int) (sessionmove.Request, error) {
	if len(raw) == 0 {
		return sessionmove.Request{}, fmt.Errorf("session request must not be empty")
	}
	if len(raw) > maximum {
		return sessionmove.Request{}, fmt.Errorf("session request exceeds %d bytes", maximum)
	}
	request, err := sessionmove.DecodeRequest(raw)
	if err != nil {
		return sessionmove.Request{}, err
	}
	canonical, err := sessionmove.EncodeRequest(request)
	if err != nil {
		return sessionmove.Request{}, fmt.Errorf("encode canonical session request: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return sessionmove.Request{}, fmt.Errorf("session request must use WB's canonical JSON encoding")
	}
	return request, nil
}

func decodeReceiverResult(raw []byte) (sessionreceive.Result, error) {
	var result sessionreceive.Result
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode one session receive result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return sessionreceive.Result{}, fmt.Errorf("session receive result has a trailing JSON value")
		}
		return sessionreceive.Result{}, fmt.Errorf("decode trailing session receive output: %w", err)
	}
	return result, nil
}

// validateReceiverResult is deliberately courier-neutral. SSH stdout and the
// Synchestra receipt artifact both pass through this exact validation so the
// courier boundary cannot change receipt identity or lineage semantics.
func validateReceiverResult(result sessionreceive.Result, request sessionmove.Request, raw []byte) error {
	wantRequest, err := sessionmove.EncodeRequest(request)
	if err != nil {
		return err
	}
	gotRequest, err := sessionmove.EncodeRequest(result.Request)
	if err != nil {
		return fmt.Errorf("response request is invalid: %w", err)
	}
	if !bytes.Equal(gotRequest, wantRequest) {
		return fmt.Errorf("response request does not match the delivered handoff")
	}
	wantDigest := sessionmove.DigestBytes(raw)
	if result.Digest != wantDigest {
		return fmt.Errorf("response request_digest %q does not match exact delivered bytes %q", result.Digest, wantDigest)
	}
	if result.Phase != sessionmove.PhaseCompleted {
		return fmt.Errorf("response phase %q is not %q", result.Phase, sessionmove.PhaseCompleted)
	}
	if result.Worktree != nil && result.Worktree.Commit != request.BundleCommit {
		return fmt.Errorf("response pinned worktree commit does not match bundle_commit %q", request.BundleCommit)
	}
	receipt := result.Receipt
	if receipt == nil {
		return fmt.Errorf("response phase %q does not include a completion receipt", result.Phase)
	}
	if err := sessionmove.ValidateReceiptForRequest(*receipt, request, wantDigest); err != nil {
		return fmt.Errorf("response receipt is invalid: %w", err)
	}
	successor := result.Successor
	if successor == nil {
		if !result.Replay {
			return fmt.Errorf("fresh completed response does not include a successor launch result")
		}
		return nil
	}
	if successor.HandoffID != request.HandoffID {
		return fmt.Errorf("response successor handoff_id %q does not match %q", successor.HandoffID, request.HandoffID)
	}
	if successor.WBSessionID != request.SuccessorWBSessionID {
		return fmt.Errorf("response successor wb_session_id %q does not match %q", successor.WBSessionID, request.SuccessorWBSessionID)
	}
	if successor.PredecessorWBSessionID != request.PredecessorWBSessionID {
		return fmt.Errorf("response successor predecessor_wb_session_id %q does not match %q", successor.PredecessorWBSessionID, request.PredecessorWBSessionID)
	}
	if successor.TargetMachine != request.TargetMachine {
		return fmt.Errorf("response successor target_machine %q does not match %q", successor.TargetMachine, request.TargetMachine)
	}
	wantRuntime := strings.TrimSpace(request.RequestedHarness)
	if wantRuntime == "" {
		wantRuntime = strings.TrimSpace(request.SourceRuntime)
	}
	if successor.Runtime != wantRuntime {
		return fmt.Errorf("response successor runtime %q does not match requested runtime %q", successor.Runtime, wantRuntime)
	}
	wantModel := ""
	if wantRuntime == strings.TrimSpace(request.SourceRuntime) {
		wantModel = strings.TrimSpace(request.SourceModel)
	}
	if successor.Model != wantModel {
		return fmt.Errorf("response successor model %q does not match expected model %q", successor.Model, wantModel)
	}
	wantTmuxName := "wb-session-" + request.SuccessorWBSessionID
	if successor.TmuxName != wantTmuxName {
		return fmt.Errorf("response successor tmux_name %q does not match %q", successor.TmuxName, wantTmuxName)
	}
	if successor.PinnedCommit != request.BundleCommit {
		return fmt.Errorf("response successor pinned_commit %q does not match bundle_commit %q", successor.PinnedCommit, request.BundleCommit)
	}
	if !filepath.IsAbs(successor.WorktreeDir) || filepath.Clean(successor.WorktreeDir) != successor.WorktreeDir {
		return fmt.Errorf("response successor worktree_dir %q is not a clean absolute path", successor.WorktreeDir)
	}
	if successor.PID <= 0 || successor.StartedAt.IsZero() {
		return fmt.Errorf("response successor does not identify one live started process")
	}
	if result.Worktree != nil && successor.WorktreeDir != result.Worktree.WorktreeDir {
		return fmt.Errorf("response successor worktree %q does not match received worktree %q", successor.WorktreeDir, result.Worktree.WorktreeDir)
	}
	if successor.TargetWorkLogRef != receipt.TargetWorkLogReference {
		return fmt.Errorf("response successor target_work_log_ref %q does not match receipt target_work_log_reference %q", successor.TargetWorkLogRef, receipt.TargetWorkLogReference)
	}
	if successor.AttemptID != receipt.AttemptID || successor.AttemptIndex != receipt.AttemptIndex || successor.PID != receipt.PID {
		return fmt.Errorf("response successor launch attempt does not match completion receipt")
	}
	if successor.HandoffID != receipt.HandoffID || successor.WBSessionID != receipt.SuccessorWBSessionID ||
		successor.PredecessorWBSessionID != receipt.PredecessorWBSessionID || successor.TargetMachine != receipt.TargetMachine {
		return fmt.Errorf("response successor identity does not match completion receipt")
	}
	if successor.TmuxName != receipt.TmuxName {
		return fmt.Errorf("response successor tmux_name %q does not match receipt %q", successor.TmuxName, receipt.TmuxName)
	}
	if successor.Runtime != receipt.Runtime || successor.Model != receipt.Model || successor.NativeHarnessID != receipt.NativeHarnessID {
		return fmt.Errorf("response successor harness identity does not match completion receipt")
	}
	if successor.PinnedCommit != receipt.PinnedCommit {
		return fmt.Errorf("response successor pinned_commit %q does not match receipt %q", successor.PinnedCommit, receipt.PinnedCommit)
	}
	if !successor.StartedAt.Equal(receipt.StartedAt) {
		return fmt.Errorf("response receipt started_at %s does not match successor %s", receipt.StartedAt, successor.StartedAt)
	}
	return nil
}
