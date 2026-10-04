package cmdworktree

import (
	"context"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/retiredcandidateack"
	"github.com/spf13/cobra"
	"io"
	"time"
)

type MergeOperations struct {
	AcknowledgeAbsorbedConflict                    func(ctx context.Context, options orchestrate.WorktreeMergeAbsorbedConflictAcknowledgementOptions) (orchestrate.WorktreeMergeAbsorbedConflictAcknowledgement, error)
	AcknowledgeLandedMergeFailure                  func(ctx context.Context, options orchestrate.WorktreeMergeLandedFailureAcknowledgementOptions) (orchestrate.WorktreeMergeLandedFailureAcknowledgement, error)
	AcknowledgeMissingWorktreeMergeCleanup         func(ctx context.Context, options orchestrate.WorktreeMergeMissingCleanupAcknowledgementOptions) (orchestrate.WorktreeMergeMissingCleanupAcknowledgement, error)
	AcknowledgeRetiredPrepareCandidate             func(ctx context.Context, options orchestrate.WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions) (retiredcandidateack.Acknowledgement, error)
	AcknowledgeRetiredPublication                  func(ctx context.Context, options orchestrate.WorktreeMergeRetiredPublicationAcknowledgementOptions) (orchestrate.WorktreeMergeRetiredPublicationAcknowledgement, error)
	AcknowledgeStrandedPullRequestLanding          func(ctx context.Context, options orchestrate.WorktreeMergeStrandedLandingAcknowledgementOptions) (orchestrate.WorktreeMergeStrandedLandingAcknowledgement, error)
	AcknowledgeUnpublishedValidationFailure        func(ctx context.Context, options orchestrate.WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions) (orchestrate.WorktreeMergeUnpublishedValidationFailureAcknowledgement, error)
	AcknowledgeWorktreeMergeReceiptCollision       func(ctx context.Context, options orchestrate.WorktreeMergeReceiptCollisionAcknowledgementOptions) (orchestrate.WorktreeMergeReceiptCollisionAcknowledgement, error)
	AdoptPublishedWorktreeMergeCandidate           func(ctx context.Context, options orchestrate.WorktreeMergePublishedCandidateAdoptionOptions) (orchestrate.WorktreeMergePublishedCandidateAdoption, error)
	CorrectValidationFailedSelfSupersession        func(ctx context.Context, options orchestrate.WorktreeMergeSelfSupersessionCorrectionOptions) (orchestrate.WorktreeMergeSelfSupersessionCorrection, error)
	LandWorktreeMerge                              func(ctx context.Context, options orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error)
	PeekWorktreeMergeReceipt                       func(projectsRoot, input string) (orchestrate.WorktreeMergeReceipt, error)
	PeekWorktreeMergeValidationDeferral            func(ctx context.Context, projectsRoot string, sources []string, target string, requestedRoute orchestrate.WorktreeMergeRoute, validateLocally, allowUnfenced bool, directCIPullRequest ...string) (bool, error)
	PrepareConflictWorktreeMergeReplacement        func(ctx context.Context, options orchestrate.WorktreeMergeConflictCandidateRefreshOptions) (result orchestrate.WorktreeMergeConflictCandidateRefresh, retErr error)
	PreparePublishedValidationFailureForwardRepair func(ctx context.Context, options orchestrate.WorktreeMergePublishedForwardRepairOptions) (result orchestrate.WorktreeMergePublishedForwardRepair, retErr error)
	PrepareValidationFailedWorktreeMergeSeal       func(ctx context.Context, options orchestrate.WorktreeMergeValidationFailureSealOptions) (orchestrate.WorktreeMergeValidationFailureSeal, error)
	PrepareWorktreeMerge                           func(ctx context.Context, options orchestrate.WorktreeMergePrepareOptions) (preparedReceipt orchestrate.WorktreeMergeReceipt, prepareErr error)
	PrepareWorktreeMergeRevert                     func(ctx context.Context, projectsRoot, input string, timeout time.Duration, retry int) (orchestrate.WorktreeMergeReceipt, error)
	ResumeWorktreeMerge                            func(ctx context.Context, options orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error)
	RunWorktreeMerge                               func(ctx context.Context, prepare orchestrate.WorktreeMergePrepareOptions, land orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error)
	SupersedeValidationFailedWorktreeMerge         func(ctx context.Context, options orchestrate.WorktreeMergeValidationFailureSupersessionOptions) (orchestrate.WorktreeMergeValidationFailureSupersession, error)
}

func DefaultMergeOperations() MergeOperations {
	return MergeOperations{
		AcknowledgeAbsorbedConflict:                    orchestrate.AcknowledgeAbsorbedConflict,
		AcknowledgeLandedMergeFailure:                  orchestrate.AcknowledgeLandedMergeFailure,
		AcknowledgeMissingWorktreeMergeCleanup:         orchestrate.AcknowledgeMissingWorktreeMergeCleanup,
		AcknowledgeRetiredPrepareCandidate:             orchestrate.AcknowledgeRetiredPrepareCandidate,
		AcknowledgeRetiredPublication:                  orchestrate.AcknowledgeRetiredPublication,
		AcknowledgeStrandedPullRequestLanding:          orchestrate.AcknowledgeStrandedPullRequestLanding,
		AcknowledgeUnpublishedValidationFailure:        orchestrate.AcknowledgeUnpublishedValidationFailure,
		AcknowledgeWorktreeMergeReceiptCollision:       orchestrate.AcknowledgeWorktreeMergeReceiptCollision,
		AdoptPublishedWorktreeMergeCandidate:           orchestrate.AdoptPublishedWorktreeMergeCandidate,
		CorrectValidationFailedSelfSupersession:        orchestrate.CorrectValidationFailedSelfSupersession,
		LandWorktreeMerge:                              orchestrate.LandWorktreeMerge,
		PeekWorktreeMergeReceipt:                       orchestrate.PeekWorktreeMergeReceipt,
		PeekWorktreeMergeValidationDeferral:            orchestrate.PeekWorktreeMergeValidationDeferral,
		PrepareConflictWorktreeMergeReplacement:        orchestrate.PrepareConflictWorktreeMergeReplacement,
		PreparePublishedValidationFailureForwardRepair: orchestrate.PreparePublishedValidationFailureForwardRepair,
		PrepareValidationFailedWorktreeMergeSeal:       orchestrate.PrepareValidationFailedWorktreeMergeSeal,
		PrepareWorktreeMerge:                           orchestrate.PrepareWorktreeMerge,
		PrepareWorktreeMergeRevert:                     orchestrate.PrepareWorktreeMergeRevert,
		ResumeWorktreeMerge:                            orchestrate.ResumeWorktreeMerge,
		RunWorktreeMerge:                               orchestrate.RunWorktreeMerge,
		SupersedeValidationFailedWorktreeMerge:         orchestrate.SupersedeValidationFailedWorktreeMerge,
	}
}

type MergeBindings struct {
	Admission       JournalAdmission
	Initiator       func(*cobra.Command) string
	Discovery       func(*cobra.Command, string)
	Quiet           func(*cobra.Command)
	Landing         func(*cobra.Command, string) *cobra.Command
	RefusePaths     func(string, []string) error
	RefuseReceipt   func(string, string) error
	LaneRequest     func(string, string, string, bool) orchestrate.LaneGuardRequest
	ReleaseLane     func(string, orchestrate.WorktreeMergeReceipt)
	CheckoutUpdated func(io.Writer) func(context.Context, orchestrate.CheckoutUpdate)
}
type mergeHostLoad struct {
	resolve func() (float64, string)
	check   func(float64, bool) error
	reader  func() hostload.Reader
	now     func() time.Time
}

func defaultMergeHostLoad() mergeHostLoad {
	return mergeHostLoad{resolve: func() (float64, string) { return hostload.Resolve("") }, check: func(floor float64, allow bool) error { return hostload.Check(nil, floor, allow) }, reader: func() hostload.Reader { return hostload.System }, now: time.Now}
}

type mergeInvocation struct {
	runtime    shared.Runtime
	operations MergeOperations
	bindings   MergeBindings
	hostLoad   mergeHostLoad
}

func NewMerge(runtime shared.Runtime, operations MergeOperations, bindings MergeBindings) *cobra.Command {
	return newWorktreeMergeCmd(&mergeInvocation{runtime, operations, bindings, defaultMergeHostLoad()})
}
func NewLand(runtime shared.Runtime, operations MergeOperations, bindings MergeBindings) *cobra.Command {
	return newWorktreeLandCmd(&mergeInvocation{runtime, operations, bindings, defaultMergeHostLoad()})
}
