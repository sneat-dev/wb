// Package sessionparkreceive owns target-side admission and atomic execution
// of one parked multi-worktree bundle.
package sessionparkreceive

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	PhaseReceived         = "received"
	PhaseMembersReady     = "members_ready"
	PhaseClaimsReady      = "claims_ready"
	PhaseSuccessorStarted = "successor_started"
	PhaseCompleted        = "completed"
)

type Options struct {
	Store        sessionpark.TargetStore
	ProjectsRoot string
	LocalMachine string
	RawEnvelope  []byte
	Now          func() time.Time

	ReceiveMember    func(context.Context, worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error)
	VerifyMember     func(context.Context, worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error)
	StartSuccessor   func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	InspectSuccessor func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error)
	PrepareMember    func(context.Context, worktrees.ParkedSessionWorkLogPrepareOptions) (worktrees.ParkedSessionWorkLogPrepareResult, error)
	CompleteMember   func(worktrees.ParkedTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error)

	AfterMembersReady     func() error
	AfterClaimsReady      func() error
	AfterSuccessorStarted func() error
	AfterReceipt          func() error
}

// Result intentionally omits the request and continuation. Normal JSON/text
// output may expose only the durable receipt and high-level phase.
type Result struct {
	ResumeID string               `json:"resume_id"`
	Digest   sessionmove.Digest   `json:"request_digest"`
	Phase    string               `json:"phase"`
	Receipt  *sessionpark.Receipt `json:"receipt,omitempty"`
	Replay   bool                 `json:"replay"`
}

func Receive(ctx context.Context, options Options) (Result, error) {
	envelope, err := sessionpark.DecodeEnvelope(options.RawEnvelope)
	if err != nil {
		return Result{}, err
	}
	request := envelope.Request
	if options.LocalMachine == "" {
		return Result{}, fmt.Errorf("validated local remote.machine identity is required")
	}
	if request.TargetMachine != options.LocalMachine {
		return Result{}, fmt.Errorf("park resume %s targets machine %q, but this receiver is configured as %q", request.ResumeID, request.TargetMachine, options.LocalMachine)
	}
	admission, err := options.Store.Admit(options.RawEnvelope)
	if err != nil {
		return Result{}, err
	}
	digest := admission.Digest
	result := Result{ResumeID: request.ResumeID, Digest: digest, Replay: admission.Replay}
	lock, err := options.Store.Acquire(ctx, request.ResumeID, digest)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close() }()
	if lock.Envelope().Request.ResumeID != request.ResumeID {
		return Result{}, fmt.Errorf("retained park resume envelope changed after admission")
	}
	now := time.Now().UTC
	if options.Now != nil {
		now = options.Now
	}
	receipt, err := options.Store.LoadReceiptUnderLock(lock, request, digest)
	if err != nil {
		return Result{}, err
	}
	events, err := options.Store.EventsUnderLock(lock, request, digest)
	if err != nil {
		return Result{}, err
	}
	has := func(phase string) bool {
		for _, event := range events {
			if event.Phase == phase {
				return true
			}
		}
		return false
	}
	if receipt != nil {
		if err := sessionpark.ValidateReceipt(*receipt, request, digest); err != nil {
			return Result{}, err
		}
		if !has(PhaseCompleted) {
			if _, err := options.Store.AppendEventUnderLock(lock, request, digest, PhaseCompleted, now()); err != nil {
				return Result{}, err
			}
		}
		result.Phase, result.Receipt, result.Replay = PhaseCompleted, receipt, true
		return result, nil
	}
	if !has(PhaseReceived) {
		if _, err := options.Store.AppendEventUnderLock(lock, request, digest, PhaseReceived, now()); err != nil {
			return Result{}, err
		}
	}
	memberResults := make([]worktrees.SessionReceiveResult, len(request.Members))
	receiptMembers := make([]sessionpark.ReceiptMember, len(request.Members))
	for index, member := range request.Members {
		spec := worktrees.SessionReceiveSpec{
			AuthorityID: request.ResumeID, AuthorityDigest: digest, AuthorityStore: options.Store.Root, Fence: lock,
			OperationID: request.ResumeID + "-" + member.MemberID, MemberKey: member.MemberID,
			RepositoryRemote: member.RepositoryRemote, Branch: member.Branch, Commit: member.Commit,
			PinBranch: sessionpark.MemberPin(request.ResumeID, member.MemberID),
		}
		memberOptions := worktrees.SessionMemberReceiveOptions{ProjectsRoot: options.ProjectsRoot, Spec: spec}
		receive := options.ReceiveMember
		if has(PhaseMembersReady) {
			receive = options.VerifyMember
			if receive == nil {
				receive = worktrees.VerifyReceivedSessionMember
			}
		} else if receive == nil {
			receive = worktrees.ReceiveSessionMember
		}
		memberResult, err := receive(ctx, memberOptions)
		if err != nil {
			return Result{}, fmt.Errorf("prepare parked member %s: %w", member.MemberID, err)
		}
		if memberResult.Repository != member.Repository || memberResult.Commit != member.Commit {
			return Result{}, fmt.Errorf("prepared parked member %s conflicts with admitted repository or commit", member.MemberID)
		}
		targetReference, err := sessionpark.TargetWorkLogReference(request, digest, member)
		if err != nil {
			return Result{}, err
		}
		memberResults[index] = memberResult
		receiptMembers[index] = sessionpark.ReceiptMember{
			MemberID: member.MemberID, Repository: member.Repository, TargetPath: memberResult.WorktreeDir,
			Pin: sessionpark.MemberPin(request.ResumeID, member.MemberID), Commit: member.Commit,
			TargetWorkLogReference: targetReference,
		}
	}
	if !has(PhaseMembersReady) {
		if _, err := options.Store.AppendEventUnderLock(lock, request, digest, PhaseMembersReady, now()); err != nil {
			return Result{}, err
		}
		if options.AfterMembersReady != nil {
			if err := options.AfterMembersReady(); err != nil {
				return Result{}, err
			}
		}
	}
	continuationPath, successorContext, err := options.Store.EnsureSuccessorContextUnderLock(lock, request, digest, receiptMembers)
	if err != nil {
		return Result{}, err
	}
	authority, err := sessionpark.LaunchAuthority(request, digest, continuationPath, successorContext)
	if err != nil {
		return Result{}, err
	}
	prepare := options.PrepareMember
	if prepare == nil {
		prepare = worktrees.PrepareParkedSessionWorkLog
	}
	beforeRelease := func(ctx context.Context, prepared sessionlaunch.Prepared) (string, error) {
		for index, member := range request.Members {
			preparedMember, err := prepare(ctx, worktrees.ParkedSessionWorkLogPrepareOptions{
				ProjectsRoot: options.ProjectsRoot, Request: request, RequestDigest: digest, Member: member,
				ReceivedAt: request.CreatedAt, Session: prepared.Session, AttemptID: prepared.AttemptID,
				AttemptIndex: prepared.AttemptIndex, WorktreeDir: memberResults[index].WorktreeDir,
				PinnedCommit: member.Commit,
			})
			if err != nil {
				return "", fmt.Errorf("prepare parked member %s target claim: %w", member.MemberID, err)
			}
			if preparedMember.WorkLogReference != receiptMembers[index].TargetWorkLogReference {
				return "", fmt.Errorf("prepared parked member %s returned conflicting target Work Log reference", member.MemberID)
			}
		}
		if !has(PhaseClaimsReady) {
			if _, err := options.Store.AppendEventUnderLock(lock, request, digest, PhaseClaimsReady, now()); err != nil {
				return "", err
			}
			if options.AfterClaimsReady != nil {
				if err := options.AfterClaimsReady(); err != nil {
					return "", err
				}
			}
		}
		return receiptMembers[0].TargetWorkLogReference, nil
	}
	launchOptions := sessionlaunch.Options{
		ProjectsRoot: options.ProjectsRoot, Authority: &authority, StoreRoot: options.Store.Root, Fence: lock,
		WorktreeDir: memberResults[0].WorktreeDir, PinnedCommit: request.Members[0].Commit, BeforeRelease: beforeRelease,
	}
	var successor sessionlaunch.Result
	inspect := options.InspectSuccessor
	if inspect == nil {
		inspect = sessionlaunch.Inspect
	}
	if has(PhaseSuccessorStarted) || has(PhaseMembersReady) {
		successor, err = inspect(ctx, launchOptions)
		if err != nil && !errors.Is(err, sessionlaunch.ErrNotReleased) && !errors.Is(err, sessionlaunch.ErrRetryableLaunch) {
			return Result{}, err
		}
	}
	if err != nil || successor.WBSessionID == "" {
		start := options.StartSuccessor
		if start == nil {
			start = sessionlaunch.Start
		}
		successor, err = start(ctx, launchOptions)
		if err != nil {
			return Result{}, err
		}
	}
	if successor.HandoffID != request.ResumeID || successor.WBSessionID != request.SuccessorWBSessionID ||
		successor.PredecessorWBSessionID != request.PredecessorWBSessionID || successor.TargetMachine != request.TargetMachine ||
		successor.WorktreeDir != memberResults[0].WorktreeDir || successor.PinnedCommit != request.Members[0].Commit {
		return Result{}, fmt.Errorf("live parked successor conflicts with admitted bundle identity")
	}
	if !has(PhaseSuccessorStarted) {
		if _, err := options.Store.AppendEventUnderLock(lock, request, digest, PhaseSuccessorStarted, now()); err != nil {
			return Result{}, err
		}
		if options.AfterSuccessorStarted != nil {
			if err := options.AfterSuccessorStarted(); err != nil {
				return Result{}, err
			}
		}
	}
	complete := options.CompleteMember
	if complete == nil {
		complete = worktrees.RecordParkedTargetCompleted
	}
	for index, member := range request.Members {
		if _, err := complete(worktrees.ParkedTargetCompletionOptions{
			ProjectsRoot: options.ProjectsRoot, Request: request, RequestDigest: digest, Member: member,
			WorktreeDir: memberResults[index].WorktreeDir, Successor: successor,
		}); err != nil {
			return Result{}, fmt.Errorf("complete parked member %s target custody: %w", member.MemberID, err)
		}
	}
	receiptValue := sessionpark.Receipt{
		SchemaVersion: sessionpark.ReceiptSchemaVersion, ResumeID: request.ResumeID, RequestDigest: digest,
		ParkedSessionID: request.ParkedSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
		TmuxName: successor.TmuxName, Runtime: successor.Runtime, Model: successor.Model, NativeHarnessID: successor.NativeHarnessID,
		AttemptID: successor.AttemptID, AttemptIndex: successor.AttemptIndex, PID: successor.PID,
		StartedAt: successor.StartedAt, Members: receiptMembers,
	}
	if err := sessionpark.ValidateReceipt(receiptValue, request, digest); err != nil {
		return Result{}, err
	}
	durable, replay, err := options.Store.SaveReceiptUnderLock(lock, request, digest, receiptValue)
	if err != nil {
		return Result{}, err
	}
	if options.AfterReceipt != nil {
		if err := options.AfterReceipt(); err != nil {
			return Result{}, err
		}
	}
	if !has(PhaseCompleted) {
		if _, err := options.Store.AppendEventUnderLock(lock, request, digest, PhaseCompleted, now()); err != nil {
			return Result{}, err
		}
	}
	result.Phase, result.Receipt, result.Replay = PhaseCompleted, &durable, result.Replay || replay || has(PhaseMembersReady)
	return result, nil
}

func TargetStoreRoot(home string) string { return filepath.Join(home, sessionpark.TargetDirName) }
