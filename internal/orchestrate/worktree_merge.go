package orchestrate

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const WorktreeMergeSchemaVersion = 1

type WorktreeMergePhase string

const (
	WorktreeMergePhasePrepare WorktreeMergePhase = "prepare"
	WorktreeMergePhaseLand    WorktreeMergePhase = "land"
	WorktreeMergePhaseRevert  WorktreeMergePhase = "revert"
)

type WorktreeMergeStatus string

const (
	WorktreeMergePreparing        WorktreeMergeStatus = "preparing"
	WorktreeMergePrepared         WorktreeMergeStatus = "prepared"
	WorktreeMergeConflict         WorktreeMergeStatus = "conflict"
	WorktreeMergeValidationFailed WorktreeMergeStatus = "validation_failed"
	// WorktreeMergePublished is an intentional non-terminal handoff point: the
	// exact validated candidate is remotely published in an open pull request,
	// but WB has not observed checks or attempted a merge. A later ordinary
	// resume continues the landing journey from this exact receipt.
	WorktreeMergePublished            WorktreeMergeStatus = "published_merge_pending"
	WorktreeMergeChecksPending        WorktreeMergeStatus = "checks_pending"
	WorktreeMergeChecksFailed         WorktreeMergeStatus = "checks_failed"
	WorktreeMergeLanded               WorktreeMergeStatus = "landed_cleanup_pending"
	WorktreeMergeCanonicalSyncBlocked WorktreeMergeStatus = "landed_canonical_sync_blocked"
	WorktreeMergePostTargetCIFailed   WorktreeMergeStatus = "landed_post_target_ci_failed"
	WorktreeMergeComplete             WorktreeMergeStatus = "complete"
)

type WorktreeMergeRoute string

const (
	WorktreeMergeRouteAuto        WorktreeMergeRoute = "auto"
	WorktreeMergeRouteDirect      WorktreeMergeRoute = "direct"
	WorktreeMergeRoutePullRequest WorktreeMergeRoute = "pr"
	WorktreeMergeRouteUnsupported WorktreeMergeRoute = "unsupported"
)

type WorktreeMergeRouteDecision struct {
	Requested WorktreeMergeRoute `json:"requested"`
	Route     WorktreeMergeRoute `json:"route"`
	Reason    string             `json:"reason"`
}

type WorktreeMergeSource struct {
	Task     string `json:"task"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
	Merged   bool   `json:"merged"`
}

type WorktreeMergeCandidate struct {
	Task     string `json:"task"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
}

type WorktreeMergeRebaseReceipt struct {
	CandidateBefore string `json:"candidate_before"`
	TargetBefore    string `json:"target_before"`
	TargetAfter     string `json:"target_after"`
	CandidateAfter  string `json:"candidate_after"`
}

type WorktreeMergeRevertReceipt struct {
	PreviousTargetSHA string `json:"previous_target_sha"`
	LandingSHA        string `json:"landing_sha"`
	CandidateSHA      string `json:"candidate_sha"`
}

type WorktreeMergeSourceRefresh struct {
	RecordedAt time.Time             `json:"recorded_at"`
	Sources    []WorktreeMergeSource `json:"sources"`
}

// WorktreeMergeTargetRefresh records one occasion where the target branch
// advanced past a receipt's recorded target SHA while its candidate was
// already published (an open pull request), and WB refreshed the candidate
// in place by merging the new target into it rather than refusing to rewrite
// the published branch. See refreshPublishedWorktreeMergeCandidateTarget.
type WorktreeMergeTargetRefresh struct {
	RecordedAt           time.Time `json:"recorded_at"`
	PreviousTargetSHA    string    `json:"previous_target_sha"`
	NewTargetSHA         string    `json:"new_target_sha"`
	PreviousCandidateSHA string    `json:"previous_candidate_sha"`
	NewCandidateSHA      string    `json:"new_candidate_sha"`
}

// WorktreeMergeValidationDeferral records that local candidate validation was
// deferred rather than run, because this call resolved the pull-request route
// and the target's required-check policy was read authoritatively, is
// non-empty, and is fenced by a server-enforced strict up-to-date policy (see
// worktreeMergeValidationDeferralEligible). CandidateSHA pins the deferral to
// the exact candidate it was recorded for: an advance past this SHA (a
// further commit, a rebase, a target refresh) makes the deferral stale, and
// every guard that consults it (requireWorktreeMergePublishedValidation,
// preparedValidationStillValid, hostLoadCheckSkippable) requires an exact
// match. See sneat-dev/wb#591.
type WorktreeMergeValidationDeferral struct {
	Route                     WorktreeMergeRoute `json:"route"`
	CandidateSHA              string             `json:"candidate_sha"`
	Reason                    string             `json:"reason"`
	RecordedAt                time.Time          `json:"recorded_at"`
	DirectCIPullRequest       string             `json:"direct_ci_pull_request,omitempty"`
	DirectCIPullRequestNumber int                `json:"direct_ci_pull_request_number,omitempty"`
	DirectCIBase              string             `json:"direct_ci_base,omitempty"`
	DirectCIWorkflowID        int64              `json:"direct_ci_workflow_id,omitempty"`
}

// WorktreeMergeFindingDeferredValidationCheckSkipped is the finding code
// recordDeferredValidationCheckSkippedFinding records: a required check on
// the landed PR-route head concluded "skipped" or "neutral" instead of
// actually running, on a candidate whose own local validation was deferred
// to CI (sneat-dev/wb#591 round 3 red-team follow-up). It is informational
// only — GitHub branch protection's own evaluation is what decided the
// candidate was landable, and this finding never refuses a landing or
// lengthens a wait.
const WorktreeMergeFindingDeferredValidationCheckSkipped = "deferred-validation-check-skipped"

// WorktreeMergeFinding is one non-blocking observation recorded on a
// receipt. See WorktreeMergeReceipt.Findings.
type WorktreeMergeFinding struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Checks  []string `json:"checks,omitempty"`
}

type WorktreeMergeSourcePullRequestReconciliation struct {
	Number       int       `json:"number"`
	URL          string    `json:"url"`
	SourceSHA    string    `json:"source_sha"`
	ObservedSHA  string    `json:"observed_sha,omitempty"`
	ObservedBase string    `json:"observed_base,omitempty"`
	Outcome      string    `json:"outcome"`
	Commented    bool      `json:"commented,omitempty"`
	Closed       bool      `json:"closed,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type WorktreeMergePushGateReceipt struct {
	Remote            string    `json:"remote"`
	RemoteRef         string    `json:"remote_ref"`
	PreviousRemoteSHA string    `json:"previous_remote_sha"`
	LocalSHA          string    `json:"local_sha"`
	Status            string    `json:"status"`
	ObservedAt        time.Time `json:"observed_at"`
}

// WorktreeMergeValidationIdentity binds a successful prepare validation to
// every cheap input that can make rerunning it produce a different result.
// Missing identity is deliberately treated as a cache miss for old receipts.
type WorktreeMergeValidationIdentity struct {
	CandidateSHA     string            `json:"candidate_sha"`
	TargetSHA        string            `json:"target_sha"`
	SourceSHAs       []string          `json:"source_shas"`
	QualityPolicySHA string            `json:"quality_policy_sha"`
	WBBuild          string            `json:"wb_build"`
	WBExecutableSHA  string            `json:"wb_executable_sha"`
	Validators       map[string]string `json:"validators,omitempty"`
}

// WorktreeMergeValidationTimeouts retains explicit validation limits on a
// prepared candidate so a later land/resume repeats the same validation policy.
// The overall prepare deadline is intentionally not retained: it bounds one
// caller's operation rather than the candidate's validation contract.
type WorktreeMergeValidationTimeouts struct {
	Check        time.Duration `json:"check_timeout,omitempty"`
	ShardAttempt time.Duration `json:"shard_attempt_timeout,omitempty"`
}

// WorktreeMergeForwardRepairReceipt preserves the exact landed attempt whose
// failed target CI required a new forward repair. The active lane reuses its
// candidate and receipt instead of abandoning either or pretending the prior
// remote landing never happened.
type WorktreeMergeForwardRepairReceipt struct {
	Status       WorktreeMergeStatus   `json:"status"`
	TargetSHA    string                `json:"target_sha"`
	CandidateSHA string                `json:"candidate_sha"`
	LandingSHA   string                `json:"landing_sha"`
	PullRequest  string                `json:"pull_request,omitempty"`
	Checks       PullRequestWaitResult `json:"checks"`
	Failure      string                `json:"failure"`
}

// WorktreeMergeHostLoadAdmission records one host-load admission check that
// actually ran against a merge verb (prepare/merge/land/resume/revert). A
// receipt with no such check recorded either predates this field or the
// check was skipped because the step it gates never re-runs local CPU-heavy
// validation (see cmd/wb's hostLoadCheckSkippable).
type WorktreeMergeHostLoadAdmission struct {
	Load       float64 `json:"load"`
	Floor      float64 `json:"floor"`
	Overridden bool    `json:"overridden"`
	// SkippedReason names why admission was disabled for this check (never
	// evaluated against Load): "env" (WB_ADMISSION_LOAD_FLOOR<=0), "ci"
	// (CI/GITHUB_ACTIONS declared), or "config" (wb.yaml admission.load_floor:
	// 0). Empty means admission was active — see internal/hostload.Resolve.
	SkippedReason string    `json:"skipped_reason,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
}

type WorktreeMergeReceipt struct {
	SchemaVersion         int                                `json:"schema_version"`
	ID                    string                             `json:"id"`
	Lane                  string                             `json:"lane"`
	Phase                 WorktreeMergePhase                 `json:"phase"`
	Status                WorktreeMergeStatus                `json:"status"`
	Repository            string                             `json:"repository"`
	Target                string                             `json:"target"`
	TargetSHA             string                             `json:"target_sha"`
	Sources               []WorktreeMergeSource              `json:"sources"`
	Candidate             WorktreeMergeCandidate             `json:"candidate"`
	Rebase                *WorktreeMergeRebaseReceipt        `json:"rebase,omitempty"`
	RevertOf              *WorktreeMergeRevertReceipt        `json:"revert_of,omitempty"`
	Route                 WorktreeMergeRouteDecision         `json:"route,omitempty"`
	PullRequest           string                             `json:"pull_request,omitempty"`
	PublishedCandidateSHA string                             `json:"published_candidate_sha,omitempty"`
	PreviousTargetSHA     string                             `json:"previous_target_sha,omitempty"`
	LandingSHA            string                             `json:"landing_sha,omitempty"`
	CanonicalSync         string                             `json:"canonical_sync,omitempty"`
	LocalSync             string                             `json:"local_sync,omitempty"`
	Validation            quality.VerificationReport         `json:"validation,omitempty"`
	BaselineValidation    quality.VerificationReport         `json:"baseline_validation,omitempty"`
	ImportedMainDeadcode  *WorktreeMergeImportedMainDeadcode `json:"imported_main_deadcode,omitempty"`
	// ValidationDeferral is set exactly when local validation was skipped for
	// the pull-request route rather than run. See
	// WorktreeMergeValidationDeferral.
	ValidationDeferral *WorktreeMergeValidationDeferral `json:"validation_deferral,omitempty"`
	ValidationIdentity *WorktreeMergeValidationIdentity `json:"validation_identity,omitempty"`
	ValidationTimeouts *WorktreeMergeValidationTimeouts `json:"validation_timeouts,omitempty"`
	// Findings are non-blocking observations recorded alongside an otherwise
	// successful outcome (sneat-dev/wb#591 round 3 red-team follow-up): they
	// never cause a refusal and never lengthen a wait. See
	// WorktreeMergeFinding and recordDeferredValidationCheckSkippedFinding.
	Findings       []WorktreeMergeFinding              `json:"findings,omitempty"`
	Checks         PullRequestWaitResult               `json:"checks,omitempty"`
	PushGate       *WorktreeMergePushGateReceipt       `json:"push_gate,omitempty"`
	ForwardRepairs []WorktreeMergeForwardRepairReceipt `json:"forward_repairs,omitempty"`
	Cleanup        bool                                `json:"cleanup_requested"`
	// AllowUnfenced is monotonic landing intent: an interrupted resume keeps
	// the explicit approval to rely on observed exact-head checks when the
	// target has no server-enforced strict up-to-date fence.
	AllowUnfenced      bool                                           `json:"allow_unfenced,omitempty"`
	OnFailure          string                                         `json:"on_failure,omitempty"`
	CleanupReports     []string                                       `json:"cleanup_reports,omitempty"`
	CleanedTasks       []string                                       `json:"cleaned_tasks,omitempty"`
	SourceRefreshes    []WorktreeMergeSourceRefresh                   `json:"source_refreshes,omitempty"`
	TargetRefreshes    []WorktreeMergeTargetRefresh                   `json:"target_refreshes,omitempty"`
	SourcePullRequests []WorktreeMergeSourcePullRequestReconciliation `json:"source_pull_requests,omitempty"`
	// AutoMergeArmed records that the PR route armed GitHub auto-merge for
	// this receipt's pull request. It says nothing about who performed the
	// merge: MergedBy records that when GitHub did.
	AutoMergeArmed bool `json:"auto_merge_armed,omitempty"`
	// MergedBy names who performed the merge when it was not this WB
	// invocation's own merge write — "github auto-merge" when armed
	// auto-merge landed it while WB was waiting, resuming, or absent.
	MergedBy string `json:"merged_by,omitempty"`
	// SupersededPullRequest names a prior candidate's own pull request that
	// a rebatch replacing it closed (red-team finding M6), so its armed
	// auto-merge could not land it alongside this replacement. Empty when
	// this candidate is not a rebatch replacement of a published-unlanded
	// original.
	SupersededPullRequest string `json:"superseded_pull_request,omitempty"`
	// RebatchOf binds this candidate to an immutable prepared receipt whose
	// source set was safely expanded. The old receipt is never rewritten.
	RebatchOf           string                   `json:"rebatch_of,omitempty"`
	RebatchedCandidates []WorktreeMergeCandidate `json:"rebatched_candidates,omitempty"`
	Failure             string                   `json:"failure,omitempty"`
	ResumeArgs          []string                 `json:"resume_args,omitempty"`
	// HostLoadAdmission records the most recent host-load admission check
	// that actually ran for this receipt's lane, so the override is provable
	// from the receipt rather than only from the caller's own log. Set by
	// cmd/wb before the orchestrate call whose validation the check gates.
	HostLoadAdmission *WorktreeMergeHostLoadAdmission `json:"host_load_admission,omitempty"`
	// LaneOwner is the landing-lane record this receipt's session acquired,
	// when the caller populated Lane (see LaneGuardRequest); nil when no
	// guard ran. Recording it on the receipt is what lets `--format json`
	// show a takeover's reason and prior owner, matching what `cmd/wb`'s help
	// and ai/skills/wb-merge/SKILL.md promise.
	LaneOwner   *landinglane.Record `json:"lane_owner,omitempty"`
	ReceiptPath string              `json:"receipt_path"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

type WorktreeMergeLandOptions struct {
	ProjectsRoot  string
	Receipt       string
	Route         WorktreeMergeRoute
	Cleanup       bool
	AllowUnfenced bool
	OnFailure     string
	Timeout       time.Duration
	// WaitSlice, when positive, bounds only the exact-head check wait of one
	// landing call. Zero keeps the historical behaviour in which Timeout is
	// also the wait slice. Timeout still bounds every git and gh command, so
	// a caller that wants a short wait cannot starve those commands: a short
	// Timeout used as a wait budget made a slow host fail the publish step's
	// git commands with conflict instead of stopping at checks_pending.
	WaitSlice time.Duration
	Retry     int
	// ValidateLocally forces the old unconditional behavior: local candidate
	// validation always runs, even when this call resolves the pull-request
	// route and its target's authoritative required-check policy would
	// otherwise be eligible for a validation deferral. See
	// worktreeMergeValidationDeferralEligible and sneat-dev/wb#591.
	ValidateLocally bool
	// DirectCIPullRequest explicitly opts a direct target push into the
	// existing open head PR's exact Go CI in place of local validation.
	DirectCIPullRequest string
	// PrepareTimeout bounds recovery of an interrupted preparing receipt.
	// It does not apply after the candidate has reached prepared state.
	PrepareTimeout time.Duration
	// CheckTimeout and ShardAttemptTimeout override the validation limits stored
	// by an interrupted preparing receipt. The effective limits are persisted
	// before validation so a later retry observes the same policy.
	CheckTimeout        time.Duration
	ShardAttemptTimeout time.Duration
	CheckPollInterval   time.Duration
	Progress            progress.Reporter
	ProgressRequested   bool
	// StopBeforeMerge is the explicit PR-only handoff mode. It validates the
	// preserved candidate and proves the remote open PR identity, then returns
	// before CI observation or any merge operation. It is deliberately not
	// persisted as future landing intent: a bare resume is how the merger takes
	// over from the published handoff.
	StopBeforeMerge bool
	// HostLoadAdmission is the host-load admission check cmd/wb ran, if any,
	// before calling Land/ResumeWorktreeMerge. Nil means the caller decided no
	// check was needed for this step (e.g. it neither validates nor pushes).
	// When set it is copied onto the receipt so the override is provable from
	// the receipt itself, not only from the caller's own log.
	HostLoadAdmission *WorktreeMergeHostLoadAdmission
	// Lane optionally names the acquiring session for the landing-lane
	// ownership guard (see LaneGuardRequest). Left zero, no guard runs.
	Lane LaneGuardRequest
	// CheckoutUpdated is called only after the checked-out canonical target
	// has moved and the exact landed commit is proven reachable. Nil discards.
	CheckoutUpdated func(context.Context, CheckoutUpdate)
	// git overrides this package's Git port (ports.go); nil uses defaultGit.
	// A unit test sets this to a *gitclitest.Fake so a land/resume call that
	// reaches the migrated call sites (spec/plans/coverage-to-100 task-17)
	// never starts a real process, without mutating any shared package
	// state. See resolveGit below and PullRequestLandOptions' identical
	// seam in pr_land.go.
	git Git
	// run overrides this package's generic command runner (ports.go); nil
	// uses defaultRunner. A unit test sets this to a runnertest.Fake for the
	// same reason as git above.
	run runner.Runner
}

// resolveGit returns options.git, falling back to defaultGit (ports.go) when
// the caller left it nil -- production's implicit choice, and every existing
// caller's behaviour before task-17 introduced this seam.
func (options WorktreeMergeLandOptions) resolveGit() Git {
	if options.git != nil {
		return options.git
	}
	return defaultGit
}

// resolveRunner returns options.run, falling back to defaultRunner
// (ports.go) when the caller left it nil.
func (options WorktreeMergeLandOptions) resolveRunner() runner.Runner {
	if options.run != nil {
		return options.run
	}
	return defaultRunner
}

type CheckoutUpdate struct {
	Checkout string
	OldSHA   string
	NewSHA   string
	Cause    string
}

type WorktreeMergePrepareOptions struct {
	ProjectsRoot string
	Sources      []string
	Target       string
	Model        string
	AgentRuntime string
	AgentID      string
	Initiator    string
	CLI          string
	Provider     string
	Timeout      time.Duration
	Retry        int
	// Route lets a caller decide the merge route before preparing (and
	// therefore before deciding whether local validation may be deferred to
	// the pull-request route's authoritative CI). Empty resolves as auto, the
	// same default LandWorktreeMerge uses.
	Route WorktreeMergeRoute
	// ValidateLocally forces local candidate validation during prepare even
	// when the pull-request route would otherwise be eligible to defer it.
	ValidateLocally     bool
	DirectCIPullRequest string
	// PrepareTimeout bounds one prepare invocation. Zero leaves preparation
	// unbounded apart from the existing command timeout.
	PrepareTimeout time.Duration
	// CheckTimeout bounds one logical candidate or baseline validation check.
	// Zero retains the existing per-command behavior.
	CheckTimeout time.Duration
	// ShardAttemptTimeout bounds one process-isolated Go test shard attempt.
	// Zero retains the existing --timeout behavior for shard attempts.
	ShardAttemptTimeout time.Duration
	Progress            progress.Reporter
	ProgressRequested   bool
	// RebatchReceipt is an immutable, still-unlanded prepared or exact published
	// pending/failed-check receipt whose sources are replaced additively.
	RebatchReceipt string
	// HostLoadAdmission is the host-load admission check cmd/wb ran before
	// calling PrepareWorktreeMerge, if any. It is copied onto the new receipt
	// so the override is provable from the receipt itself.
	HostLoadAdmission *WorktreeMergeHostLoadAdmission
	// RequireHostLoadAdmission is cmd/wb's lazy fallback for the gap round
	// 4's minor 2 closes: its combined `wb worktree land`/`wb land` command
	// skips the up-front host-load admission check whenever a cheap
	// PeekWorktreeMergeValidationDeferral call predicts the resolved
	// validation plan will defer to CI (so no local CPU work is coming,
	// and the check would gate nothing). If a transient GitHub read makes
	// the ACTUAL resolve here disagree with that prediction and fall back
	// to local validation after all, HostLoadAdmission is still nil — the
	// up-front check never ran — and running CPU-heavy validation ungated
	// is exactly the saturated-host incident admission exists to prevent.
	// When set, this is invoked exactly once, only when the resolved plan
	// does not defer and HostLoadAdmission is still nil; a non-nil error
	// stops the prepare before validation ever runs, exactly as the
	// up-front check would have. Nil (every other caller) changes nothing.
	RequireHostLoadAdmission func() (*WorktreeMergeHostLoadAdmission, error)
	// Lane optionally names the acquiring session for the landing-lane
	// ownership guard (see LaneGuardRequest). Left zero, no guard runs.
	Lane LaneGuardRequest
	// Sleep is the retry-backoff seam for a rebatch's superseded-pull-request
	// verify retry (see closeSupersededWorktreeMergePullRequest). Nil (every
	// production caller) defaults to time.Sleep; a test supplies a recorder
	// to exercise that retry without a real wait.
	Sleep func(time.Duration)
	// run overrides this package's generic command runner (ports.go); nil
	// uses defaultRunner. A unit test sets this to a runnertest.Fake so a
	// prepare call that reaches the migrated call sites
	// (spec/plans/coverage-to-100 task-17) never starts a real process.
	run runner.Runner
}

// resolveRunner returns options.run, falling back to defaultRunner
// (ports.go) when the caller left it nil.
func (options WorktreeMergePrepareOptions) resolveRunner() runner.Runner {
	if options.run != nil {
		return options.run
	}
	return defaultRunner
}

func PrepareWorktreeMerge(ctx context.Context, options WorktreeMergePrepareOptions) (preparedReceipt WorktreeMergeReceipt, prepareErr error) {
	return prepareWorktreeMerge(ctx, options, readWorktreeMergeReceipt, persistWorktreeMergeReceipt)
}

// prepareWorktreeMerge binds durable reads and writes per invocation. Native
// entry points use the actual receipt owners; failures after exclusivity or
// integration can be observed without racing permissions or global overrides.
func prepareWorktreeMerge(ctx context.Context, options WorktreeMergePrepareOptions, read func(string) (WorktreeMergeReceipt, error), save func(WorktreeMergeReceipt) error) (preparedReceipt WorktreeMergeReceipt, prepareErr error) {
	if options.PrepareTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.PrepareTimeout)
		defer cancel()
	}
	run := options.resolveRunner()
	sleep := options.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	reportWorktreeMergeProgress(options.Progress, "inspect_sources", progress.Started, "validating source worktrees and target")
	projectsRoot, err := filepath.Abs(strings.TrimSpace(options.ProjectsRoot))
	if err != nil || strings.TrimSpace(options.ProjectsRoot) == "" {
		return WorktreeMergeReceipt{}, fmt.Errorf("projects root is required")
	}
	if len(options.Sources) == 0 {
		return WorktreeMergeReceipt{}, fmt.Errorf("at least one source worktree is required")
	}
	target := strings.TrimSpace(options.Target)
	if target == "" {
		canonicalProbe, probeErr := canonicalForMergeSourceWithRunner(ctx, run, options.Sources[0])
		if probeErr != nil {
			return WorktreeMergeReceipt{}, fmt.Errorf("resolve source canonical clone: %w", probeErr)
		}
		target, probeErr = gitops.DefaultBranch(canonicalProbe)
		if probeErr != nil {
			return WorktreeMergeReceipt{}, fmt.Errorf("resolve remote default branch: %w", probeErr)
		}
	}
	sources, repository, canonical, err := inspectWorktreeMergeSourcesWithRunner(ctx, run, projectsRoot, options.Sources, target)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	reportWorktreeMergeProgress(options.Progress, "inspect_sources", progress.Completed, fmt.Sprintf("%s: %d source worktree(s) targeting %s", repository, len(sources), target))
	if !validMergeBranch(ctx, canonical, target) {
		return WorktreeMergeReceipt{}, fmt.Errorf("invalid target branch %q", target)
	}
	// The landing-lane guard runs before any receipt is created: a different
	// live session already driving this (repository, target) lane must be
	// refused before this call does any work it would otherwise have to
	// strand or re-prepare. See LaneGuardRequest.
	laneRecord, laneErr := acquireLandingLane(projectsRoot, repository, target, options.Lane)
	if laneErr != nil {
		return WorktreeMergeReceipt{}, laneErr
	}
	if laneRecord.Owner.WBSessionID != "" {
		// Every return below this point that leaves prepareErr set means no
		// receipt now tracks this session's ownership of the lane (either
		// none was created yet, or the attempt was refused outright), so the
		// lane must not wait out its 30-minute stale timeout before a
		// different session can use it. A successful prepare deliberately
		// keeps the lane: a later `wb worktree merge land`/`resume` for the
		// same receipt still needs it, and cmd/wb releases it itself once the
		// receipt's status says the lane is no longer needed (see
		// WorktreeMergeLaneReleasable).
		defer func() {
			if prepareErr != nil {
				_ = releaseLandingLane(projectsRoot, repository, target, laneRecord.Owner.WBSessionID)
			}
		}()
	}
	var rebatch *WorktreeMergePreparedRebatch
	if strings.TrimSpace(options.RebatchReceipt) != "" {
		rebatch, err = validatePreparedWorktreeMergeRebatch(ctx, projectsRoot, options.RebatchReceipt, repository, target, sources)
		if err != nil {
			return WorktreeMergeReceipt{}, err
		}
	}
	lane := worktreeMergeLaneID(repository, target)
	operation := worktreeMergeOperationID(lane, sources)
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	receiptPath := filepath.Join(reportsDir, operation+".json")
	var prior *WorktreeMergeReceipt
	for {
		existing, readErr := read(receiptPath)
		if errors.Is(readErr, os.ErrNotExist) {
			break
		}
		if readErr != nil {
			return WorktreeMergeReceipt{}, readErr
		}
		if !sameWorktreeMergeSources(existing.Sources, sources) || existing.Repository != repository || existing.Target != target {
			return existing, fmt.Errorf("merger lane %s already owns a different candidate at %s", lane, receiptPath)
		}
		if collisionAcknowledged, collisionErr := hasReceiptCollisionAcknowledgement(existing); collisionErr != nil {
			return existing, fmt.Errorf("validate receipt-collision acknowledgement for %s: %w", receiptPath, collisionErr)
		} else if collisionAcknowledged {
			return existing, fmt.Errorf("merge receipt %s has a receipt-collision acknowledgement and may proceed only as --rebatch-receipt original", receiptPath)
		}
		if acknowledged, ackErr := hasLandedFailureAcknowledgement(existing); ackErr != nil {
			return existing, ackErr
		} else if acknowledged {
			return existing, fmt.Errorf("merge receipt %s was acknowledged as a historical landed failure; prepare a new source candidate", receiptPath)
		}
		if superseded, supersessionErr := hasValidationFailureSupersession(ctx, projectsRoot, existing); supersessionErr != nil {
			return existing, supersessionErr
		} else if superseded {
			// A verified, append-only supersession retires this immutable receipt
			// from lane selection. The same source set may be prepared again, but
			// it must get a successor operation rather than rewrite the historical
			// receipt or reuse its candidate worktree.
			operation = worktreeMergeSupersededOperationID(operation, existing.ReceiptPath)
			receiptPath = filepath.Join(reportsDir, operation+".json")
			continue
		} else {
			if rebatched, rebatchErr := hasPreparedWorktreeMergeRebatch(existing); rebatchErr != nil {
				return existing, rebatchErr
			} else if rebatched {
				return existing, fmt.Errorf("merge receipt %s was rebatched into an audited replacement candidate; prepare the replacement receipt", receiptPath)
			}
			if strandedAcknowledged, strandedErr := hasStrandedLandingAcknowledgement(existing); strandedErr != nil {
				return existing, strandedErr
			} else if strandedAcknowledged {
				return existing, fmt.Errorf("merge receipt %s was acknowledged as a proved stranded landing; prepare a new source candidate", receiptPath)
			}
			if absorbedAcknowledged, absorbedErr := hasAbsorbedConflictAcknowledgement(existing); absorbedErr != nil {
				return existing, absorbedErr
			} else if absorbedAcknowledged {
				return existing, fmt.Errorf("merge receipt %s was acknowledged as a proved absorbed conflict; prepare a new source candidate", receiptPath)
			}
			if retiredAcknowledged, retiredErr := hasRetiredPublicationAcknowledgement(existing); retiredErr != nil {
				return existing, retiredErr
			} else if retiredAcknowledged {
				// A verified, append-only retired-publication acknowledgement proves
				// this receipt's exact published candidate never landed and its
				// publication is gone. Retire this immutable receipt from lane
				// selection exactly like a validation-failure supersession: the same
				// source set is prepared again under a fresh successor operation, a
				// fresh integration branch, and an empty pull request, never by
				// rewriting the historical receipt or reusing its published branch.
				operation = worktreeMergeSupersededOperationID(operation, existing.ReceiptPath)
				receiptPath = filepath.Join(reportsDir, operation+".json")
				continue
			}
			if unpublishedFailureAcknowledged, acknowledgementErr := hasUnpublishedValidationFailureAcknowledgement(existing); acknowledgementErr != nil {
				return existing, acknowledgementErr
			} else if unpublishedFailureAcknowledged {
				// The failed preparation never published or landed and every exact
				// source remains preserved. Keep the receipt immutable and allocate a
				// fresh successor operation for a later attempt.
				operation = worktreeMergeSupersededOperationID(operation, existing.ReceiptPath)
				receiptPath = filepath.Join(reportsDir, operation+".json")
				continue
			}
			if adoption, adopted, adoptionErr := adoptedPublishedCandidate(ctx, existing); adoptionErr != nil {
				return existing, fmt.Errorf("validate published-candidate adoption for %s: %w", receiptPath, adoptionErr)
			} else if adopted {
				existing.PullRequest, existing.PublishedCandidateSHA = adoption.PullRequest, existing.Candidate.SHA
			}
			if rebatch != nil && existing.RebatchOf == rebatch.ReceiptPath && existing.Status == WorktreeMergePrepared {
				lock, lockErr := AcquireOperationLock(projectsRoot, lane, true)
				if lockErr != nil {
					return existing, lockErr
				}
				defer func() { _ = lock.Release() }()
				// Re-read all evidence under the lane lock. This is the only recovery
				// permitted after a crash or write failure between durable replacement
				// receipt creation and append-only acknowledgement persistence.
				current, currentErr := read(receiptPath)
				if currentErr != nil {
					return existing, currentErr
				}
				rechecked, recheckErr := validatePreparedWorktreeMergeRebatch(ctx, projectsRoot, rebatch.ReceiptPath, repository, target, sources)
				if recheckErr != nil {
					return existing, recheckErr
				}
				if current.RebatchOf != rechecked.ReceiptPath || current.TargetSHA != rechecked.CurrentTargetSHA || current.Candidate.SHA == "" || len(current.RebatchedCandidates) != 1 || current.RebatchedCandidates[0] != rechecked.OriginalCandidate || !sameWorktreeMergeSources(current.Sources, sources) {
					return existing, fmt.Errorf("existing replacement receipt %s no longer matches the requested immutable rebatch", receiptPath)
				}
				if _, candidateErr := validateMergeAcknowledgementCandidateWithRunner(ctx, run, projectsRoot, current, current.Candidate); candidateErr != nil {
					return existing, fmt.Errorf("validate replacement candidate before rebatch acknowledgement recovery: %w", candidateErr)
				}
				containsOriginal, ancestorErr := isMergeAncestorWithRunner(ctx, run, current.Candidate.Worktree, rechecked.OriginalCandidate.SHA, current.Candidate.SHA)
				if ancestorErr != nil || !containsOriginal {
					if ancestorErr == nil {
						ancestorErr = fmt.Errorf("replacement candidate %s does not retain original rebatch candidate %s", current.Candidate.SHA, rechecked.OriginalCandidate.SHA)
					}
					return existing, ancestorErr
				}
				if err := ensurePreparedWorktreeMergeRebatch(ctx, rechecked, &current, sleep); err != nil {
					return existing, err
				}
				return current, nil
			}
			if existing.Status == WorktreeMergeValidationFailed {
				// A published PR candidate is already immutable and exact-source retry is
				// idempotent: return its receipt rather than reconstructing or rewriting
				// it. Descendant sources still take the active-lane refusal below.
				replay, replayErr := isExactPublishedValidationFailureReplay(ctx, projectsRoot, existing, sources)
				if replayErr != nil {
					return existing, fmt.Errorf("verify published validation failure replay for %s: %w", receiptPath, replayErr)
				}
				if replay {
					return existing, nil
				}
				return existing, fmt.Errorf("merge receipt %s is validation_failed; only an exact preparing receipt may resume", receiptPath)
			}
			if existing.Status == WorktreeMergePreparing {
				if err := validateExactPreparingWorktreeMergeReceiptWithRunner(ctx, run, existing, lane, operation, sources); err != nil {
					return existing, fmt.Errorf("merge receipt %s cannot resume: %w", receiptPath, err)
				}
				prior = &existing
			} else if existing.Status != WorktreeMergeConflict {
				return existing, nil
			}
		}
		break
	}
	forwardRepair := false
	activeExcept := []string{receiptPath}
	if rebatch != nil {
		activeExcept = append(activeExcept, rebatch.ReceiptPath)
	}
	if active, activeErr := activeWorktreeMergeLaneReceiptWithRunner(ctx, projectsRoot, reportsDir, lane, run, activeExcept...); activeErr != nil {
		return WorktreeMergeReceipt{}, activeErr
	} else if active != nil {
		if collisionAcknowledged, collisionErr := hasReceiptCollisionAcknowledgement(*active); collisionErr != nil {
			return *active, fmt.Errorf("validate receipt-collision acknowledgement for %s: %w", active.ReceiptPath, collisionErr)
		} else if collisionAcknowledged {
			return *active, fmt.Errorf("merge receipt %s has a receipt-collision acknowledgement and may proceed only as --rebatch-receipt original", active.ReceiptPath)
		}
		if adoption, adopted, adoptionErr := adoptedPublishedCandidate(ctx, *active); adoptionErr != nil {
			return *active, fmt.Errorf("validate published-candidate adoption for %s: %w", active.ReceiptPath, adoptionErr)
		} else if adopted {
			active.PullRequest, active.PublishedCandidateSHA = adoption.PullRequest, active.Candidate.SHA
		}
		// This is a read-only replay of an already published candidate, not a
		// resume. It preserves the supported checks-failed forward-repair path
		// after that path has recorded validation_failed, while a descendant
		// source remains a hard validation_failed boundary below.
		replay, replayErr := isExactPublishedValidationFailureReplay(ctx, projectsRoot, *active, sources)
		if replayErr != nil {
			return *active, fmt.Errorf("verify published validation failure replay for %s: %w", active.ReceiptPath, replayErr)
		}
		if replay {
			return *active, nil
		}
		continuation, continuationErr := choosePrepareContinuation(ctx, run, *active, sources)
		if continuationErr != nil {
			return *active, continuationErr
		}
		if continuation != prepareRefresh {
			if continuation != prepareForwardRepair {
				if active.Status == WorktreeMergeValidationFailed {
					return *active, fmt.Errorf("merge receipt %s is validation_failed; only an exact preparing receipt may resume", active.ReceiptPath)
				}
				return *active, fmt.Errorf("merger lane %s is still owned by non-terminal receipt %s with status %s", lane, active.ReceiptPath, active.Status)
			}
			forwardRepair = true
		}
		prior = active
		operation, receiptPath = active.ID, active.ReceiptPath
	}

	branch := "wb/integration/" + target + "/" + mergeOperationSuffix(operation)
	if prior != nil {
		branch = prior.Candidate.Branch
	}
	resume := prior != nil
	listed, listErr := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot, Task: operation, Base: target, Workers: 1})
	if listErr != nil {
		return WorktreeMergeReceipt{}, fmt.Errorf("inspect candidate lane: %w", listErr)
	}
	if len(listed) > 0 {
		resume = true
	}
	reportWorktreeMergeProgress(options.Progress, "acquire_lane", progress.Started, lane)
	lock, err := AcquireOperationLock(projectsRoot, lane, resume)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	defer func() { _ = lock.Release() }()
	reportWorktreeMergeProgress(options.Progress, "acquire_lane", progress.Completed, lane)
	// Re-read the durable lane after acquiring exclusivity: another prepare may
	// have advanced the same resumable candidate before this lock was held.
	if prior != nil {
		current, readErr := read(receiptPath)
		if readErr != nil {
			return WorktreeMergeReceipt{}, readErr
		}
		if collisionAcknowledged, collisionErr := hasReceiptCollisionAcknowledgement(current); collisionErr != nil {
			return current, fmt.Errorf("re-read receipt-collision acknowledgement for %s: %w", receiptPath, collisionErr)
		} else if collisionAcknowledged {
			return current, fmt.Errorf("merge receipt %s has a receipt-collision acknowledgement and may proceed only as --rebatch-receipt original", receiptPath)
		}
		if adoption, adopted, adoptionErr := adoptedPublishedCandidate(ctx, current); adoptionErr != nil {
			return current, fmt.Errorf("re-read published-candidate adoption for %s: %w", receiptPath, adoptionErr)
		} else if adopted {
			current.PullRequest, current.PublishedCandidateSHA = adoption.PullRequest, current.Candidate.SHA
		}
		replay, replayErr := isExactPublishedValidationFailureReplay(ctx, projectsRoot, current, sources)
		if replayErr != nil {
			return current, fmt.Errorf("verify published validation failure replay for %s: %w", receiptPath, replayErr)
		}
		if replay {
			return current, nil
		}
		continuation, continuationErr := choosePrepareContinuation(ctx, run, current, sources)
		if continuationErr != nil {
			return current, continuationErr
		}
		if continuation != prepareRefresh {
			if continuation != prepareForwardRepair {
				if current.Status == WorktreeMergeValidationFailed {
					return current, fmt.Errorf("merge receipt %s is validation_failed; only an exact preparing receipt may resume", receiptPath)
				}
				return current, fmt.Errorf("merger lane %s can no longer refresh receipt %s with the advanced source heads", lane, receiptPath)
			}
			forwardRepair = true
		}
		prior = &current
	}
	if rebatch != nil {
		// The original target and all source identities are re-read only after
		// exclusivity is held. A changed candidate, source, or target invalidates
		// the rebatch rather than silently replacing immutable evidence.
		rebatch, err = validatePreparedWorktreeMergeRebatch(ctx, projectsRoot, rebatch.ReceiptPath, repository, target, sources)
		if err != nil {
			return WorktreeMergeReceipt{}, err
		}
	}
	// A same-source refresh adopts the active receipt path above. Recompute the
	// exclusions after that reassignment so the post-lock owner check does not
	// mistake the resumable prior receipt for a competing lane.
	postLockExcept := []string{receiptPath}
	if rebatch != nil {
		postLockExcept = append(postLockExcept, rebatch.ReceiptPath)
	}
	if active, activeErr := activeWorktreeMergeLaneReceiptWithRunner(ctx, projectsRoot, reportsDir, lane, run, postLockExcept...); activeErr != nil {
		return WorktreeMergeReceipt{}, activeErr
	} else if active != nil {
		return *active, fmt.Errorf("merger lane %s is still owned by non-terminal receipt %s with status %s", lane, active.ReceiptPath, active.Status)
	}
	promptSources := sources
	if prior != nil {
		promptSources = prior.Sources
		if len(prior.SourceRefreshes) > 0 {
			promptSources = prior.SourceRefreshes[0].Sources
		}
	}
	prompt, err := writeWorktreeMergePrompt(repository, target, promptSources)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	defer func() { _ = os.Remove(prompt) }()
	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = "unknown"
	}
	reportWorktreeMergeProgress(options.Progress, "create_candidate", progress.Started, operation)
	created, err := worktrees.Create(ctx, []string{repository}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot,
		Operation:    operation,
		Branch:       branch,
		BranchChosen: true,
		Base:         target,
		Resume:       resume,
		WorkLog: worktrees.WorkLogOptions{
			EffortID: operation, RunID: operation, Initiator: options.Initiator, AgentID: options.AgentID,
			AgentRuntime: options.AgentRuntime, Model: model, CLI: options.CLI, Provider: options.Provider,
			OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		return WorktreeMergeReceipt{}, fmt.Errorf("create candidate worktree: %w", err)
	}
	candidate := created[0]
	reportWorktreeMergeProgress(options.Progress, "create_candidate", progress.Completed, candidate.WorktreeDir)
	if forwardRepair {
		remoteTarget, fetchErr := fetchExactMergeTargetWithRunner(ctx, run, candidate.WorktreeDir, target)
		if fetchErr != nil {
			return *prior, fetchErr
		}
		containsLanding, ancestorErr := isMergeAncestorWithRunner(ctx, run, candidate.WorktreeDir, prior.LandingSHA, remoteTarget)
		if ancestorErr != nil || !containsLanding {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("remote target %s no longer contains prior landing %s", remoteTarget, prior.LandingSHA)
			}
			return *prior, ancestorErr
		}
		absorbedCandidate, targetContainsCandidate, absorptionErr := worktreeMergeCandidateAbsorbed(ctx, candidate.WorktreeDir, *prior, remoteTarget)
		if absorptionErr != nil || !absorbedCandidate {
			if absorptionErr == nil {
				absorptionErr = fmt.Errorf("remote target %s neither contains prior candidate %s nor proves its exact receipted squash landing", remoteTarget, prior.Candidate.SHA)
			}
			return *prior, absorptionErr
		}
		mergeArgs := []string{"merge", "--ff-only", remoteTarget}
		if !targetContainsCandidate {
			mergeArgs = []string{"merge", "--no-ff", "--no-edit", remoteTarget}
		}
		if _, _, mergeErr := runCommand(ctx, run, options.Timeout, options.Retry, candidate.WorktreeDir, "git", mergeArgs...); mergeErr != nil {
			if !targetContainsCandidate {
				_, _, _ = runCommand(ctx, run, options.Timeout, 0, candidate.WorktreeDir, "git", "merge", "--abort")
			}
			return *prior, fmt.Errorf("advance repair candidate to landed target %s: %w", remoteTarget, mergeErr)
		}
		candidate.BaseSHA = remoteTarget
	}

	receipt := newPreparingWorktreeMergeReceipt(preparingReceiptInput{
		options: options, candidate: candidate, sources: sources, prior: prior, rebatch: rebatch,
		laneRecord: laneRecord, forwardRepair: forwardRepair, now: time.Now().UTC(),
		operation: operation, lane: lane, repository: repository, target: target, receiptPath: receiptPath,
	})
	if err := save(receipt); err != nil {
		return receipt, err
	}
	if rebatch != nil && candidate.BaseSHA != rebatch.CurrentTargetSHA {
		// Creation already owns a durable worktree and claim. Preserve its exact
		// identity in a failed receipt so the target race cannot orphan them.
		receipt.Candidate.SHA = candidate.BaseSHA
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("rebatch target changed during candidate creation from %s to %s", rebatch.CurrentTargetSHA, candidate.BaseSHA), save)
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, candidate.WorktreeDir); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	if rebatch != nil {
		// A rebatch is not only a source-list assertion: retain the original
		// candidate in the new candidate DAG. That gives receipt-gated cleanup a
		// real graph/landing proof for the superseded integration branch instead
		// of treating an acknowledgement as if it were a landing.
		containsOriginal, ancestorErr := isMergeAncestorWithRunner(ctx, run, candidate.WorktreeDir, rebatch.OriginalCandidate.SHA, "HEAD")
		if ancestorErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, ancestorErr, save)
		}
		if !containsOriginal {
			if _, _, mergeErr := runCommand(ctx, run, options.Timeout, options.Retry, candidate.WorktreeDir, "git", "merge", "--no-edit", rebatch.OriginalCandidate.SHA); mergeErr != nil {
				_, _, _ = runCommand(ctx, run, options.Timeout, 0, candidate.WorktreeDir, "git", "merge", "--abort")
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("merge original rebatch candidate %s: %w", rebatch.OriginalCandidate.SHA, mergeErr), save)
			}
			receipt.Candidate.SHA, err = mergeRevision(ctx, run, candidate.WorktreeDir, "HEAD")
			if err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
		}
	}

	for index := range receipt.Sources {
		source := &receipt.Sources[index]
		ancestor, ancestorErr := isMergeAncestorWithRunner(ctx, run, candidate.WorktreeDir, source.SHA, "HEAD")
		if ancestorErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, ancestorErr, save)
		}
		if !ancestor {
			if _, _, mergeErr := runCommand(ctx, run, options.Timeout, options.Retry, candidate.WorktreeDir, "git", "merge", "--no-edit", source.SHA); mergeErr != nil {
				_, _, _ = runCommand(ctx, run, options.Timeout, 0, candidate.WorktreeDir, "git", "merge", "--abort")
				conflict := fmt.Errorf("merge conflict while integrating %s at %s: %w", source.Branch, source.SHA, mergeErr)
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, conflict, save)
			}
		}
		source.Merged = true
		progress.Report(options.Progress, progress.Event{Operation: "worktree_merge", Phase: "integrate_sources", Repository: repository,
			State: progress.Running, Completed: index + 1, Total: len(receipt.Sources), Detail: source.Branch + "@" + shortMergeRevision(source.SHA)})
		receipt.Candidate.SHA, err = mergeRevision(ctx, run, candidate.WorktreeDir, "HEAD")
		if err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
		}
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
	}
	if err := recheckWorktreeMergeSourcesWithRunner(ctx, run, receipt.Sources); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	for _, original := range receipt.RebatchedCandidates {
		containsOriginal, ancestorErr := isMergeAncestorWithRunner(ctx, run, candidate.WorktreeDir, original.SHA, receipt.Candidate.SHA)
		if ancestorErr != nil || !containsOriginal {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("replacement candidate %s does not retain rebatched candidate %s", receipt.Candidate.SHA, original.SHA)
			}
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, ancestorErr, save)
		}
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, candidate.WorktreeDir); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	// Validate both the exact target baseline and the integrated candidate,
	// unless this call resolves the pull-request route and the target's
	// authoritative required-check policy is eligible to defer it (M7: "the
	// standalone prepare also counts", sneat-dev/wb#591).
	reportWorktreeMergeProgress(options.Progress, "validate_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
	plan, planErr := resolveWorktreeMergeValidationPlan(ctx, repository, target, options.Route, options.ValidateLocally, false, options.DirectCIPullRequest)
	if planErr != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
	}
	// Round 4, minor 2: cmd/wb's combined `wb worktree land`/`wb land`
	// command skips the up-front host-load admission check whenever a
	// cheap pre-resolution predicts this plan will defer. If that
	// prediction and this actual resolve disagree — most often a
	// transient GitHub read that made the earlier cheap resolution
	// succeed while this one falls back to local validation — the plan
	// above does not defer, admission was never checked, and local
	// CPU-heavy validation is about to run unadmitted. Closing that gap
	// here, once, just-in-time, is cheaper and less invasive than
	// threading admission through every applyOrDeferWorktreeMergeValidation
	// call site; it only ever fires for the one caller that set this hook.
	if !plan.Defer && receipt.HostLoadAdmission == nil && options.RequireHostLoadAdmission != nil {
		admission, admissionErr := options.RequireHostLoadAdmission()
		if admissionErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, admissionErr, save)
		}
		receipt.HostLoadAdmission = admission
	}
	checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
	if validationErr := applyOrDeferWorktreeMergeValidation(ctx, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress); validationErr != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, validationErr, save)
	}
	reportWorktreeMergeProgress(options.Progress, "validate_candidate", progress.Completed, string(receipt.Validation.Status))
	receipt.Status = WorktreeMergePrepared
	receipt.Candidate.SHA, err = mergeRevision(ctx, options.resolveRunner(), candidate.WorktreeDir, "HEAD")
	if err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
	if err := save(receipt); err != nil {
		return receipt, err
	}
	if rebatch != nil {
		if err := ensurePreparedWorktreeMergeRebatch(ctx, rebatch, &receipt, sleep); err != nil {
			return receipt, err
		}
	}
	reportWorktreeMergeProgress(options.Progress, "prepared", progress.Completed, receipt.ReceiptPath)
	return receipt, nil
}

func worktreeMergeValidationTimeouts(check, shardAttempt time.Duration) *WorktreeMergeValidationTimeouts {
	if check <= 0 && shardAttempt <= 0 {
		return nil
	}
	return &WorktreeMergeValidationTimeouts{Check: check, ShardAttempt: shardAttempt}
}

func receiptWorktreeMergeValidationTimeouts(receipt WorktreeMergeReceipt) (time.Duration, time.Duration) {
	if receipt.ValidationTimeouts == nil {
		return 0, 0
	}
	return receipt.ValidationTimeouts.Check, receipt.ValidationTimeouts.ShardAttempt
}

func ResumeWorktreeMerge(ctx context.Context, options WorktreeMergeLandOptions) (WorktreeMergeReceipt, error) {
	return LandWorktreeMerge(ctx, options)
}

func runWorktreeMergePrePushGate(ctx context.Context, worktree, localSHA, remoteRef string, timeout time.Duration, retry int) (*WorktreeMergePushGateReceipt, error) {
	return runWorktreeMergePrePushGateInjected(ctx, worktree, localSHA, remoteRef, timeout, retry, nil)
}

// runWorktreeMergePrePushGateInjected is runWorktreeMergePrePushGate's test
// seam (task-9 PR-9): every production call site reaches it only through
// runWorktreeMergePrePushGate, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged. A test passes
// its own Injector to reach the scratch input file's create/chmod/write/
// close failure branches deterministically.
func runWorktreeMergePrePushGateInjected(ctx context.Context, worktree, localSHA, remoteRef string, timeout time.Duration, retry int, inj *filewrite.Injector) (*WorktreeMergePushGateReceipt, error) {
	remoteOutput, _, err := runCommand(ctx, defaultRunner, timeout, retry, worktree, "git", "ls-remote", "--heads", "origin", remoteRef)
	if err != nil {
		return nil, fmt.Errorf("inspect exact remote ref before pre-push gate: %w", err)
	}
	previousRemoteSHA := strings.Repeat("0", 40)
	if fields := strings.Fields(remoteOutput); len(fields) > 0 {
		previousRemoteSHA = fields[0]
	}
	remoteURL, _, err := runCommand(ctx, defaultRunner, timeout, retry, worktree, "git", "remote", "get-url", "--push", "origin")
	if err != nil {
		return nil, fmt.Errorf("resolve push remote for pre-push gate: %w", err)
	}
	localRef, _, err := runCommand(ctx, defaultRunner, timeout, retry, worktree, "git", "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve local branch for pre-push gate: %w", err)
	}
	body := fmt.Sprintf("%s %s %s %s\n", strings.TrimSpace(localRef), localSHA, remoteRef, previousRemoteSHA)
	inputPath, err := filewrite.CreateScratch("", "wb-worktree-merge-pre-push-*.txt", 0o600, []byte(body), inj)
	if inputPath != "" {
		defer func() { _ = os.Remove(inputPath) }()
	}
	if err != nil {
		return nil, err
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, worktree, "git", "hook", "run", "--ignore-missing", "--to-stdin", inputPath,
		"pre-push", "--", "origin", strings.TrimSpace(remoteURL)); err != nil {
		return nil, fmt.Errorf("managed pre-push gate failed before opening the push connection: %w", err)
	}
	return &WorktreeMergePushGateReceipt{
		Remote: "origin", RemoteRef: remoteRef, PreviousRemoteSHA: previousRemoteSHA,
		LocalSHA: localSHA, Status: "passed", ObservedAt: time.Now().UTC(),
	}, nil
}

func pushWorktreeMergeRef(ctx context.Context, worktree, localSHA, remoteRef string, setUpstream bool, timeout time.Duration, retry int) error {
	args := []string{"push", "--no-verify"}
	if setUpstream {
		args = append(args, "-u")
	}
	args = append(args, "origin", localSHA+":"+remoteRef)
	_, _, err := runCommand(ctx, defaultRunner, timeout, retry, worktree, "git", args...)
	return err
}

// normalizeCompletedWorktreeMergeReceipt permits an exact resume to remove a
// stale prior failure only after the durable receipt independently proves every
// task it owns was cleaned. It never makes an incomplete/error receipt look
// successful: callers reach it only for complete, receipt-gated cleanup and
// any identity mismatch remains an error with Failure preserved.
func normalizeCompletedWorktreeMergeReceipt(receipt *WorktreeMergeReceipt) error {
	if receipt.Failure == "" {
		return nil
	}
	if !receipt.Cleanup {
		return fmt.Errorf("complete receipt %s retains failure but has no cleanup intent", receipt.ReceiptPath)
	}
	expected := sortedUniqueMergeTasks(*receipt)
	if len(expected) == 0 || len(receipt.CleanedTasks) != len(expected) {
		return fmt.Errorf("complete receipt %s retains failure but its cleanup evidence is incomplete", receipt.ReceiptPath)
	}
	cleaned := make(map[string]bool, len(receipt.CleanedTasks))
	for _, task := range receipt.CleanedTasks {
		if task == "" || cleaned[task] {
			return fmt.Errorf("complete receipt %s retains failure but its cleaned task identities are inconsistent", receipt.ReceiptPath)
		}
		cleaned[task] = true
	}
	for _, task := range expected {
		if !cleaned[task] {
			return fmt.Errorf("complete receipt %s retains failure but cleanup did not terminalize task %s", receipt.ReceiptPath, task)
		}
	}
	if err := worktrees.ValidateTerminalCleanupReports(receipt.CleanupReports, receipt.Repository, expected); err != nil {
		return fmt.Errorf("complete receipt %s retains failure but its cleanup reports are inconsistent: %w", receipt.ReceiptPath, err)
	}
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
	return persistWorktreeMergeReceipt(*receipt)
}

// applyRecordedWorktreeMergeRouteBeforeFirstResolve applies a previously
// recorded route (receipt.Route.Requested) to options.Route before the
// FIRST validation-plan resolution in LandWorktreeMerge — not only via
// retainWorktreeMergeLandIntent, which only runs once the receipt already
// has a published PullRequest (minor finding, sneat-dev/wb#591 red-team
// follow-up). Without this, a resume with the default --route auto on a
// not-yet-published receipt that had already recorded an explicit route
// earlier in its own history (an earlier call's route resolution stamps
// receipt.Route.Requested even before publishing — see the "resolve_route"
// progress step) would resolve auto's own policy instead of honoring the
// recorded intent.
func applyRecordedWorktreeMergeRouteBeforeFirstResolve(receipt *WorktreeMergeReceipt, options *WorktreeMergeLandOptions) {
	if receipt.Route.Requested != "" && (options.Route == "" || options.Route == WorktreeMergeRouteAuto) {
		options.Route = receipt.Route.Requested
	}
	if options.DirectCIPullRequest == "" && !options.ValidateLocally && receipt.ValidationDeferral != nil && receipt.ValidationDeferral.Route == WorktreeMergeRouteDirect {
		options.DirectCIPullRequest = receipt.ValidationDeferral.DirectCIPullRequest
		if options.Route == "" || options.Route == WorktreeMergeRouteAuto {
			options.Route = WorktreeMergeRouteDirect
		}
	}
}

// retainWorktreeMergeLandIntent makes a combined command's requested landing
// semantics part of the durable receipt before any target-drift, policy, push,
// or check boundary can interrupt it. A bare resume therefore cannot silently
// downgrade direct to auto, forget cleanup, or lose a requested forward revert.
func retainWorktreeMergeLandIntent(receipt *WorktreeMergeReceipt, options *WorktreeMergeLandOptions) bool {
	requestedRoute := options.Route
	if requestedRoute == "" {
		requestedRoute = WorktreeMergeRouteAuto
	}
	if receipt.Route.Requested != "" && (options.Route == "" || options.Route == WorktreeMergeRouteAuto) {
		requestedRoute = receipt.Route.Requested
	}
	onFailure := strings.TrimSpace(options.OnFailure)
	if onFailure == "" {
		onFailure = "stop"
	}
	if receipt.OnFailure != "" && (strings.TrimSpace(options.OnFailure) == "" || options.OnFailure == "stop") {
		onFailure = receipt.OnFailure
	}
	cleanup := receipt.Cleanup || options.Cleanup
	allowUnfenced := receipt.AllowUnfenced || options.AllowUnfenced
	progressRequested := options.ProgressRequested || stringSliceContains(receipt.ResumeArgs, "--progress")
	resumeArgs := []string{"worktree", "merge", "resume", receipt.ReceiptPath, "--route", string(requestedRoute)}
	if cleanup {
		resumeArgs = append(resumeArgs, "--cleanup")
	}
	if progressRequested {
		resumeArgs = append(resumeArgs, "--progress")
	}
	if allowUnfenced {
		resumeArgs = append(resumeArgs, "--allow-unfenced")
	}
	if options.DirectCIPullRequest != "" {
		resumeArgs = append(resumeArgs, "--defer-direct-ci-pr", options.DirectCIPullRequest)
	}
	resumeArgs = append(resumeArgs, "--on-failure", onFailure)
	changed := receipt.Route.Requested != requestedRoute || receipt.Cleanup != cleanup || receipt.AllowUnfenced != allowUnfenced || receipt.OnFailure != onFailure ||
		strings.Join(receipt.ResumeArgs, "\x00") != strings.Join(resumeArgs, "\x00")
	receipt.Route.Requested = requestedRoute
	receipt.Cleanup = cleanup
	receipt.AllowUnfenced = allowUnfenced
	receipt.OnFailure = onFailure
	receipt.ResumeArgs = resumeArgs
	options.Route = requestedRoute
	options.Cleanup = cleanup
	options.AllowUnfenced = allowUnfenced
	options.OnFailure = onFailure
	options.ProgressRequested = progressRequested
	return changed
}

func worktreeMergePrepareResumeArgs(receiptPath string, progressRequested bool) []string {
	args := []string{"worktree", "merge", "resume", receiptPath}
	if progressRequested {
		args = append(args, "--progress")
	}
	return args
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func RunWorktreeMerge(ctx context.Context, prepare WorktreeMergePrepareOptions, land WorktreeMergeLandOptions) (WorktreeMergeReceipt, error) {
	receipt, err := PrepareWorktreeMerge(ctx, prepare)
	if err != nil {
		return receipt, err
	}
	land.ProjectsRoot, land.Receipt = prepare.ProjectsRoot, receipt.ReceiptPath
	return LandWorktreeMerge(ctx, land)
}

// worktreeMergeReportSidecarSuffixes lists every worktree-merge report
// sidecar/acknowledgement filename suffix that decorates a *.json report file
// but is never itself a receipt. resolveWorktreeMergeReceiptPath and
// activeWorktreeMergeLaneReceipt both scan the same reports directory for
// receipts and must skip exactly this set: a suffix present for one scan but
// not the other lets that scan try to parse a sidecar as a receipt (some
// sidecars carry receipt-shaped fields, including the receipt's own sha256)
// or, for activeWorktreeMergeLaneReceipt, hard-fails the whole lane lookup
// when the sidecar's identity does not check out as a receipt. Registering a
// new sidecar suffix here, once, keeps both scans in sync by construction;
// TestWorktreeMergeReportSidecarSuffixParity guards against a suffix being
// wired into one scan's skip logic without the other.
var worktreeMergeReportSidecarSuffixes = []string{
	worktreeMergeLandedFailureAcknowledgementSuffix,
	worktreeMergeConflictCandidateAdvanceSuffix,
	worktreeMergeValidationFailureSupersessionSuffix,
	worktreeMergeLegacyValidationFailureIdentitySuffix,
	worktreeMergeLegacyConflictIdentitySuffix,
	worktreeMergeSelfSupersessionCorrectionSuffix,
	worktreeMergePreparedRebatchSuffix,
	worktreeMergePublishedCandidateAdoptionSuffix,
	worktreeMergeStrandedLandingAcknowledgementSuffix,
	worktreeMergeReceiptCollisionAcknowledgementSuffix,
	worktreeMergeMissingCleanupAcknowledgementSuffix,
	worktreeMergeAbsorbedConflictAcknowledgementSuffix,
	worktreeMergeRetiredPublicationAcknowledgementSuffix,
	worktreeMergeUnpublishedValidationFailureAcknowledgementSuffix,
}

// isWorktreeMergeReportSidecar reports whether name is a worktree-merge
// report sidecar/acknowledgement file rather than a receipt, per
// worktreeMergeReportSidecarSuffixes.
func isWorktreeMergeReportSidecar(name string) bool {
	for _, suffix := range worktreeMergeReportSidecarSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func resolveWorktreeMergeReceiptPath(projectsRoot, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("candidate worktree or receipt is required")
	}
	absolute, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	reports := filepath.Join(home, "reports", "worktree-merge")
	if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
		if !pathInsideWorktreeMergeReports(reports, absolute) {
			return "", fmt.Errorf("merge receipt %s is outside the authoritative WB report store %s", absolute, reports)
		}
		return absolute, nil
	}
	entries, err := os.ReadDir(reports)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if isWorktreeMergeReportSidecar(entry.Name()) {
			continue
		}
		path := filepath.Join(reports, entry.Name())
		receipt, readErr := readWorktreeMergeReceipt(path)
		if readErr == nil && filepath.Clean(receipt.Candidate.Worktree) == filepath.Clean(absolute) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no worktree merge receipt owns %s", input)
}

func pathInsideWorktreeMergeReports(reports, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(reports), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	resolvedReports, reportsErr := filepath.EvalSymlinks(reports)
	resolvedPath, pathErr := filepath.EvalSymlinks(path)
	if reportsErr != nil || pathErr != nil {
		return false
	}
	resolvedRelative, err := filepath.Rel(resolvedReports, resolvedPath)
	return err == nil && resolvedRelative != "." && resolvedRelative != ".." && !strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator))
}

func fetchExactMergeTarget(ctx context.Context, worktree, target string) (string, error) {
	return fetchExactMergeTargetWithRunner(ctx, defaultRunner, worktree, target)
}

func worktreeMergePRText(ctx context.Context, receipt WorktreeMergeReceipt) (string, string, error) {
	output, _, err := runCommand(ctx, defaultRunner, 0, 0, receipt.Candidate.Worktree, "git", "log", "--format=%s", receipt.TargetSHA+".."+receipt.Candidate.SHA)
	if err != nil {
		return "", "", err
	}
	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			subjects = append(subjects, line)
		}
	}
	title := worktreeMergePRTitle(subjects, len(receipt.Sources))
	var body strings.Builder
	fmt.Fprintf(&body, "Mechanically prepared by `wb worktree merge` from exact source heads.\n\n")
	for _, source := range receipt.Sources {
		fmt.Fprintf(&body, "- `%s` at `%s`\n", source.Branch, source.SHA)
	}
	if len(subjects) > 0 {
		body.WriteString("\nCommits:\n\n")
		for index := len(subjects) - 1; index >= 0; index-- {
			fmt.Fprintf(&body, "- %s\n", subjects[index])
		}
	}
	fmt.Fprintf(&body, "\nCandidate: `%s`\n", receipt.Candidate.SHA)
	return title, body.String(), nil
}

var conventionalWorktreeMergeSubject = regexp.MustCompile(`^([[:alpha:]]+)(\([^)]*\))?(!)?:[[:space:]]+`)

func worktreeMergePRTitle(subjects []string, sourceCount int) string {
	if len(subjects) == 1 {
		return subjects[0]
	}
	if sourceCount == 1 {
		// Git log is newest first. The oldest non-merge subject normally states
		// the source effort's purpose; later commits are review and CI repairs.
		for index := len(subjects) - 1; index >= 0; index-- {
			subject := strings.TrimSpace(subjects[index])
			if subject != "" && !strings.HasPrefix(subject, "Merge ") {
				return subject
			}
		}
	}
	type choice struct {
		prefix   string
		summary  string
		priority int
	}
	selected := choice{prefix: "fix:", priority: 1}
	for _, subject := range subjects {
		match := conventionalWorktreeMergeSubject.FindStringSubmatch(strings.TrimSpace(subject))
		if len(match) == 0 {
			continue
		}
		kind := strings.ToLower(match[1])
		breaking := match[3] == "!"
		candidate := choice{prefix: kind + ":", summary: strings.TrimSpace(strings.TrimPrefix(subject, match[0])), priority: 2}
		switch {
		case breaking:
			candidate.prefix, candidate.priority = kind+"!:", 5
		case kind == "feat":
			candidate.prefix, candidate.priority = "feat:", 4
		case kind == "fix" || kind == "perf" || kind == "revert":
			candidate.prefix, candidate.priority = "fix:", 3
		}
		if candidate.priority > selected.priority {
			selected = candidate
		}
	}
	if selected.summary == "" {
		return fmt.Sprintf("%s apply %d related changes", selected.prefix, sourceCount)
	}
	related := "changes"
	if sourceCount == 2 {
		related = "change"
	}
	return fmt.Sprintf("%s %s and %d related %s", selected.prefix, selected.summary, sourceCount-1, related)
}

// checkWaitSlice is the budget of one exact-head check wait: WaitSlice when
// set, otherwise Timeout, capped at 8 minutes either way.
func (options WorktreeMergeLandOptions) checkWaitSlice() time.Duration {
	slice := options.Timeout
	if options.WaitSlice > 0 {
		slice = options.WaitSlice
	}
	if slice <= 0 || slice > 8*time.Minute {
		slice = 8 * time.Minute
	}
	return slice
}

func waitForWorktreeMergeChecks(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions, pullRequest, head string, allowTargetDescendant bool) (PullRequestWaitResult, error) {
	slice := options.checkWaitSlice()
	interval := options.CheckPollInterval
	if interval <= 0 {
		interval = DefaultCheckPollInterval
	}
	if interval >= slice {
		return PullRequestWaitResult{}, fmt.Errorf("CI poll interval %s must be shorter than wait slice %s", interval, slice)
	}
	// One slice can still run several minutes of CI observation: keep the
	// lane's heartbeat fresh throughout so it never goes stale out from under
	// this still-live session. See startLandingLaneHeartbeat. A no-op when
	// options.Lane was never populated (no guard running for this call).
	stopLaneHeartbeat := startLandingLaneHeartbeat(options.ProjectsRoot, receipt.Repository, receipt.Target, options.Lane.Owner.WBSessionID, 0)
	waitOptions := PullRequestWaitOptions{
		Repository: receipt.Repository, PullRequest: pullRequest, Target: receipt.Target, Head: head, AllowTargetDescendant: allowTargetDescendant,
		// --allow-unfenced is durable because a private repository can expose
		// neither PR nor post-target branch-policy authority. Both phases still
		// require stable exact-head check observations.
		AllowUnfenced: options.AllowUnfenced,
		Slice:         slice, CheckPollInterval: interval, Progress: reportWorktreeMergeCheckProgress(options.Progress, worktreeMergeCheckPhase(pullRequest)),
		OperationProgress: options.Progress,
	}
	var directCI *worktreeMergeDirectCIContract
	if deferral := receipt.ValidationDeferral; pullRequest == "" && deferral != nil && deferral.Route == WorktreeMergeRouteDirect {
		if deferral.CandidateSHA != head || deferral.DirectCIPullRequest == "" || deferral.DirectCIPullRequestNumber <= 0 || deferral.DirectCIBase == "" || deferral.DirectCIWorkflowID <= 0 {
			stopLaneHeartbeat()
			return PullRequestWaitResult{Status: PullRequestWaitFailed}, fmt.Errorf("direct CI deferral is not pinned to exact landed head %s", head)
		}
		directCI = &worktreeMergeDirectCIContract{PullRequest: deferral.DirectCIPullRequest, PullRequestNumber: deferral.DirectCIPullRequestNumber, Base: deferral.DirectCIBase, WorkflowID: deferral.DirectCIWorkflowID}
		waitOptions.AllowTargetDescendant = false
		waitOptions.ExpectedActionChecks = &ExpectedActionChecks{WorkflowID: directCI.WorkflowID, Event: "pull_request", PullRequestNumber: directCI.PullRequestNumber, PullRequestBase: directCI.Base, Names: directCIGoChecks}
	}
	result, err := WaitForCommitChecks(ctx, waitOptions)
	stopLaneHeartbeat()
	if err != nil {
		return result, err
	}
	switch result.Status {
	case PullRequestWaitPassed:
		if directCI != nil {
			if err := verifyWorktreeMergeDirectCIPullRequest(ctx, receipt, *directCI, head); err != nil {
				result.Status = PullRequestWaitFailed
				result.Reason = "direct CI pull request identity changed: " + err.Error()
				return result, fmt.Errorf("%s", result.Reason)
			}
		}
		return result, nil
	case PullRequestWaitPending:
		return result, fmt.Errorf("exact-head checks remain pending: %s; resume with wb worktree merge resume %s", result.Reason, receipt.ReceiptPath)
	default:
		if !options.AllowUnfenced && strings.Contains(result.Reason, "strict up-to-date fence") {
			return result, fmt.Errorf("exact-head checks failed: %s; resume with wb worktree merge resume %s --allow-unfenced", result.Reason, receipt.ReceiptPath)
		}
		// #600: name each failing check and its first error line rather than
		// leaving the caller to hand-roll the same log scraping WB already
		// did while observing the checks.
		if summary := summarizeCheckFailures(result.FailureDetails); summary != "" {
			return result, fmt.Errorf("exact-head checks failed: %s; %s", result.Reason, summary)
		}
		return result, fmt.Errorf("exact-head checks failed: %s", result.Reason)
	}
}

func worktreeMergeCheckPhase(pullRequest string) string {
	if pullRequest != "" {
		return "candidate_checks"
	}
	return "target_checks"
}

func reportWorktreeMergeCheckProgress(reporter progress.Reporter, phase string) func(PullRequestWaitProgress) {
	if reporter == nil {
		return nil
	}
	return func(event PullRequestWaitProgress) {
		passed, pending, failed := 0, 0, 0
		for _, check := range event.Result.Checks {
			switch check.Bucket {
			case "pass", "skipping":
				passed++
			case "fail", "cancel":
				failed++
			default:
				pending++
			}
		}
		var state progress.State
		switch event.Result.Status {
		case PullRequestWaitPending:
			state = progress.Waiting
		case PullRequestWaitPassed:
			state = progress.Completed
		case PullRequestWaitFailed:
			state = progress.Failed
		default:
			state = progress.Running
		}
		detail := fmt.Sprintf("poll %d: %d passed, %d pending, %d failed", event.Observation, passed, pending, failed)
		if event.Result.StableObservations > 0 {
			detail += fmt.Sprintf("; stable %d/2", event.Result.StableObservations)
		}
		if event.NextPoll > 0 {
			detail += "; next poll in " + event.NextPoll.String()
		} else if strings.TrimSpace(event.Result.Reason) != "" {
			detail += "; " + event.Result.Reason
		}
		progress.Report(reporter, progress.Event{Operation: "worktree_merge", Phase: phase, State: state, Detail: detail})
	}
}

func reportWorktreeMergeQualityProgress(reporter progress.Reporter) func(quality.Progress) {
	if reporter == nil {
		return nil
	}
	return func(event quality.Progress) {
		var state progress.State
		if event.State == quality.ProgressStarted || event.State == quality.ProgressRetrying {
			state = progress.Started
			if event.State == quality.ProgressRetrying {
				state = progress.Running
			}
		} else if event.Status == quality.StatusFailed {
			state = progress.Failed
		} else {
			state = progress.Completed
		}
		parts := make([]string, 0, 4)
		if event.Check != "" {
			parts = append(parts, string(event.Check))
		}
		if command := strings.TrimSpace(event.Command); command != "" {
			parts = append(parts, command)
		}
		if detail := strings.TrimSpace(event.Detail); detail != "" && detail != strings.TrimSpace(event.Command) {
			parts = append(parts, detail)
		}
		if event.Attempts > 0 {
			parts = append(parts, fmt.Sprintf("attempt %d", event.Attempts))
		}
		if event.Status != "" {
			parts = append(parts, string(event.Status))
		}
		progress.Report(reporter, progress.Event{Operation: "worktree_merge", Phase: "validate_candidate", State: state,
			Detail: strings.Join(parts, ": "), Completed: event.Completed, Total: event.Total})
	}
}

func reportWorktreeMergeProgress(reporter progress.Reporter, phase string, state progress.State, detail string) {
	progress.Report(reporter, progress.Event{Operation: "worktree_merge", Phase: phase, State: state, Detail: detail})
}

func shortMergeRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func pullRequestLandingReceipt(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions) (string, bool, error) {
	// The pull request URL and --repo already fully qualify this read; an
	// empty dir avoids depending on the candidate worktree, exactly like
	// pullRequestIdentity in ciwait.go. A resume whose worktree was already
	// cleaned up must still be able to observe the server landing result.
	viewOutput, err := githubRead(ctx, "", "pr", "view", receipt.PullRequest,
		"--repo", receipt.Repository, "--json", "state,mergedAt,mergeCommit,headRefOid,baseRefName")
	if err != nil {
		return "", false, fmt.Errorf("read pull-request landing receipt: %w", err)
	}
	var view struct {
		State       string `json:"state"`
		MergedAt    string `json:"mergedAt"`
		HeadRefOID  string `json:"headRefOid"`
		BaseRefName string `json:"baseRefName"`
		MergeCommit struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := json.Unmarshal([]byte(viewOutput), &view); err != nil {
		return "", false, fmt.Errorf("decode pull-request landing receipt: %w", err)
	}
	if view.BaseRefName != receipt.Target {
		return "", false, fmt.Errorf("pull-request landing receipt does not match target %s", receipt.Target)
	}
	if view.HeadRefOID != receipt.Candidate.SHA {
		if view.State == "MERGED" {
			// GitHub's own merged verdict is authoritative once the merged
			// head is proven to descend from our recorded candidate — a
			// foreign push, or an update-branch this receipt never
			// recorded, while auto-merge was armed must not strand an
			// already-landed change behind a conflict receipt (red-team
			// finding M4). It still has to be OUR candidate that landed,
			// not an unrelated head, so the descent is proven, not assumed.
			//
			// This proof is asked of GitHub's own compare API, not the
			// local worktree's git objects: GitHub deletes a merged pull
			// request's source branch by default, and a foreign push may
			// never have been fetched locally at all, so a local
			// `git merge-base --is-ancestor` would fail on a missing
			// object even when the descent genuinely holds.
			descendsFromCandidate, descentReason := candidateContainsTarget(ctx, receipt.Repository, receipt.Candidate.SHA, view.HeadRefOID)
			if !descendsFromCandidate {
				descentErr := fmt.Errorf("pull-request head %s does not match exact candidate %s", view.HeadRefOID, receipt.Candidate.SHA)
				if descentReason != "" {
					descentErr = fmt.Errorf("%w: %s", descentErr, descentReason)
				}
				return "", false, descentErr
			}
		} else {
			advancesPublished, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, view.HeadRefOID, receipt.Candidate.SHA)
			if receipt.PublishedCandidateSHA == "" || view.HeadRefOID != receipt.PublishedCandidateSHA || ancestorErr != nil || !advancesPublished {
				if ancestorErr == nil {
					ancestorErr = fmt.Errorf("pull-request head %s does not match exact candidate %s or its recorded published predecessor %s", view.HeadRefOID, receipt.Candidate.SHA, receipt.PublishedCandidateSHA)
				}
				return "", false, ancestorErr
			}
		}
	}
	if view.State != "MERGED" {
		return "", false, nil
	}
	if view.MergedAt == "" || view.MergeCommit.OID == "" {
		return "", false, fmt.Errorf("merged pull request omitted its time or server merge-result commit")
	}
	return view.MergeCommit.OID, true, nil
}

// findExactOpenWorktreeMergePullRequest discovers an already-open pull request
// through GitHub's immutable commit association. Every mutable identity field
// must still match the prepared candidate before WB adopts it.
func findExactOpenWorktreeMergePullRequest(ctx context.Context, receipt WorktreeMergeReceipt) (string, error) {
	output, err := githubRead(ctx, receipt.Candidate.Worktree, "api", "--paginate",
		"repos/"+receipt.Repository+"/commits/"+receipt.Candidate.SHA+"/pulls")
	if err != nil {
		return "", fmt.Errorf("query pull requests for exact candidate %s: %w", receipt.Candidate.SHA, err)
	}
	var views []struct {
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Head    struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.Unmarshal([]byte(output), &views); err != nil {
		return "", fmt.Errorf("decode pull requests for exact candidate %s: %w", receipt.Candidate.SHA, err)
	}
	var matched string
	for _, view := range views {
		if !strings.EqualFold(view.State, "open") || view.Head.SHA != receipt.Candidate.SHA ||
			view.Head.Ref != receipt.Candidate.Branch || view.Head.Repo.FullName != receipt.Repository ||
			view.Base.Ref != receipt.Target {
			continue
		}
		if strings.TrimSpace(view.HTMLURL) == "" {
			return "", errors.New("exact candidate pull request omitted its URL")
		}
		if matched != "" && matched != view.HTMLURL {
			return "", fmt.Errorf("exact candidate has multiple matching open pull requests: %s and %s", matched, view.HTMLURL)
		}
		matched = view.HTMLURL
	}
	return matched, nil
}

// verifyPublishedWorktreeMergePullRequest proves the exact remote handoff
// identity before an intentional stop. An open pull request at the candidate
// SHA and target has one unambiguous remote diff; verifying just a local
// branch, or a PR number without its current head, is insufficient.
func verifyPublishedWorktreeMergePullRequest(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions) error {
	if receipt.PullRequest == "" {
		return errors.New("published handoff has no pull request")
	}
	remote, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, receipt.Candidate.Worktree,
		"git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return fmt.Errorf("read published candidate ref: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(remote), receipt.Candidate.SHA+"\t") {
		return fmt.Errorf("published candidate ref %s does not match preserved candidate %s", receipt.Candidate.Branch, receipt.Candidate.SHA)
	}

	const maxHeadObservations = 4
	delay := 2 * time.Second
	if options.CheckPollInterval > 0 && options.CheckPollInterval < delay {
		delay = options.CheckPollInterval
	}
	for observation := 1; observation <= maxHeadObservations; observation++ {
		viewOutput, readErr := githubRead(ctx, "", "pr", "view", receipt.PullRequest,
			"--repo", receipt.Repository, "--json", "state,headRefOid,baseRefName")
		if readErr != nil {
			return fmt.Errorf("read published pull-request identity: %w", readErr)
		}
		var view struct {
			State       string `json:"state"`
			HeadRefOID  string `json:"headRefOid"`
			BaseRefName string `json:"baseRefName"`
		}
		if unmarshalErr := json.Unmarshal([]byte(viewOutput), &view); unmarshalErr != nil {
			return fmt.Errorf("decode published pull-request identity: %w", unmarshalErr)
		}
		if view.State != "OPEN" {
			return fmt.Errorf("published pull request %s is %s, not open", receipt.PullRequest, view.State)
		}
		if view.BaseRefName != receipt.Target {
			return fmt.Errorf("published pull-request base %s does not match target %s", view.BaseRefName, receipt.Target)
		}
		if view.HeadRefOID == receipt.Candidate.SHA {
			return nil
		}
		stalePredecessor := receipt.PushGate != nil && receipt.PushGate.Status == "passed" &&
			receipt.PushGate.LocalSHA == receipt.Candidate.SHA && receipt.PushGate.PreviousRemoteSHA != "" &&
			receipt.PushGate.PreviousRemoteSHA != receipt.Candidate.SHA && view.HeadRefOID == receipt.PushGate.PreviousRemoteSHA
		if !stalePredecessor || observation == maxHeadObservations {
			return fmt.Errorf("published pull-request head %s does not match preserved candidate %s", view.HeadRefOID, receipt.Candidate.SHA)
		}
		reportWorktreeMergeProgress(options.Progress, "verify_pull_request_head", progress.Waiting,
			fmt.Sprintf("GitHub still reports published predecessor %s; observation %d/%d, retrying in %s",
				shortMergeRevision(view.HeadRefOID), observation, maxHeadObservations, delay))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for published pull-request head %s: %w", receipt.Candidate.SHA, ctx.Err())
		case <-timer.C:
		}
	}
	return errors.New("published pull-request head verification exhausted without an observation")
}

func cleanupWorktreeMergeAssets(ctx context.Context, projectsRoot string, receipt *WorktreeMergeReceipt) error {
	if err := validateRebatchedWorktreeMergeCleanup(ctx, projectsRoot, *receipt); err != nil {
		return err
	}
	cleaned := make(map[string]bool, len(receipt.CleanedTasks))
	for _, task := range receipt.CleanedTasks {
		cleaned[task] = true
	}
	for _, task := range sortedUniqueMergeTasks(*receipt) {
		if cleaned[task] {
			continue
		}
		// Cleanup owns the single active -> terminal Work Log transition. Calling
		// LogFinalize first would make cleanup attempt the same immutable
		// transition twice and strand otherwise safe landed assets.
		outcome, err := worktrees.Cleanup(ctx, worktrees.CleanupOptions{
			ProjectsRoot: projectsRoot, Task: task, Base: receipt.Target, ExactRepository: receipt.Repository,
			AbsorbedBy: receipt.LandingSHA, MergeReceiptProofs: worktreeMergeCleanupProofs(*receipt, task),
			Apply: true, DeleteRemote: true, OlderThan: 0, Workers: 1,
		})
		if err != nil {
			return fmt.Errorf("cleanup task %s: %w", task, err)
		}
		receipt.CleanupReports = append(receipt.CleanupReports, outcome.ReportPath)
		for _, result := range outcome.Results {
			if !result.Applied {
				return fmt.Errorf("cleanup task %s remained unapplied: %s", task, result.Reason)
			}
		}
		receipt.CleanedTasks = append(receipt.CleanedTasks, task)
		if err := persistWorktreeMergeReceipt(*receipt); err != nil {
			return err
		}
	}
	return nil
}

// validateRebatchedWorktreeMergeCleanup admits retirement of the old candidate
// only after the replacement has an exact remote landing. The acknowledgement
// is not itself a landing claim.
func validateRebatchedWorktreeMergeCleanup(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt) error {
	if receipt.RebatchOf == "" {
		return nil
	}
	original, err := readWorktreeMergeReceipt(receipt.RebatchOf)
	if err != nil {
		return fmt.Errorf("read rebatched receipt before cleanup: %w", err)
	}
	if original.Status == WorktreeMergePreparing {
		if _, ackErr := validateReceiptCollisionAcknowledgement(ctx, projectsRoot, original); ackErr != nil {
			return fmt.Errorf("revalidate receipt-collision acknowledgement before cleanup: %w", ackErr)
		}
	}
	if receipt.LandingSHA == "" {
		return errors.New("rebatched candidate has no remote landing; old candidate cleanup is not yet eligible")
	}
	rebatch, err := readPreparedWorktreeMergeRebatch(rebatchPath(receipt.RebatchOf), original)
	if err != nil {
		return fmt.Errorf("validate rebatched receipt before cleanup: %w", err)
	}
	if rebatch.ReplacementReceiptPath != receipt.ReceiptPath || rebatch.Replacement != receipt.Candidate ||
		!sameWorktreeMergeSources(rebatch.Sources, receipt.Sources) || len(receipt.RebatchedCandidates) != 1 ||
		receipt.RebatchedCandidates[0] != original.Candidate {
		return errors.New("replacement receipt does not carry the exact append-only rebatch cleanup proof")
	}
	if rebatch.ReceiptTargetSHA != rebatch.CurrentTargetSHA {
		advanced, ancestryErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, rebatch.ReceiptTargetSHA, rebatch.CurrentTargetSHA)
		if ancestryErr != nil || !advanced {
			return fmt.Errorf("rebatch cleanup target is not a proven fast-forward: ancestor=%t err=%v", advanced, ancestryErr)
		}
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return err
	}
	containsLanding, err := isMergeAncestor(ctx, receipt.Candidate.Worktree, receipt.LandingSHA, currentTarget)
	if err != nil || !containsLanding {
		if err == nil {
			err = fmt.Errorf("replacement landing %s is not contained in exact current target %s", receipt.LandingSHA, currentTarget)
		}
		return err
	}
	return nil
}

func worktreeMergeCleanupProofs(receipt WorktreeMergeReceipt, task string) []worktrees.MergeReceiptCleanupProof {
	proofs := make([]worktrees.MergeReceiptCleanupProof, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		if source.Task != task || !source.Merged {
			continue
		}
		proofs = append(proofs, worktrees.MergeReceiptCleanupProof{
			Repository: receipt.Repository, Target: receipt.Target,
			SourceTask: source.Task, SourceWorktree: source.Worktree, SourceBranch: source.Branch, SourceSHA: source.SHA,
			CandidateSHA: receipt.Candidate.SHA, LandingSHA: receipt.LandingSHA,
		})
	}
	return proofs
}

// PrepareWorktreeMergeRevert creates a fresh forward candidate which applies
// the inverse landing tree delta onto today's remote target. It never resets or
// force-pushes shared history.
func PrepareWorktreeMergeRevert(ctx context.Context, projectsRoot, input string, timeout time.Duration, retry int) (WorktreeMergeReceipt, error) {
	return prepareWorktreeMergeRevertInjected(ctx, projectsRoot, input, timeout, retry, nil)
}

// prepareWorktreeMergeRevertInjected is PrepareWorktreeMergeRevert's test
// seam (task-9 PR-9): every production call site reaches it only through
// PrepareWorktreeMergeRevert, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged. A test passes its own Injector to
// reach the scratch git-diff patch file's create/write/close failure
// branches deterministically.
func prepareWorktreeMergeRevertInjected(ctx context.Context, projectsRoot, input string, timeout time.Duration, retry int, inj *filewrite.Injector) (WorktreeMergeReceipt, error) {
	path, err := resolveWorktreeMergeReceiptPath(projectsRoot, input)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	receipt, err := readWorktreeMergeReceipt(path)
	if err != nil {
		return receipt, err
	}
	if receipt.Phase == WorktreeMergePhaseRevert {
		if receipt.Status == WorktreeMergePrepared {
			return receipt, nil
		}
		return receipt, fmt.Errorf("receipt already represents a forward revert with status %s", receipt.Status)
	}
	if receipt.PreviousTargetSHA == "" || receipt.LandingSHA == "" {
		return receipt, fmt.Errorf("receipt has no landed before/after target identity to revert")
	}
	revertOf := &WorktreeMergeRevertReceipt{
		PreviousTargetSHA: receipt.PreviousTargetSHA,
		LandingSHA:        receipt.LandingSHA,
		CandidateSHA:      receipt.Candidate.SHA,
	}
	task := "revert-" + receipt.ID
	prompt, err := writeWorktreeMergePromptInjected(receipt.Repository, receipt.Target, receipt.Sources, inj)
	if err != nil {
		return receipt, err
	}
	defer func() { _ = os.Remove(prompt) }()
	created, err := worktrees.Create(ctx, []string{receipt.Repository}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: task, Branch: "wb/revert/" + receipt.ID, BranchChosen: true, Base: receipt.Target,
		WorkLog: worktrees.WorkLogOptions{EffortID: task, RunID: task, Model: "unknown", AgentRuntime: "wb", OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil {
		return receipt, err
	}
	if len(created) != 1 {
		return receipt, fmt.Errorf("revert candidate creation returned %d repositories", len(created))
	}
	patchOutput, _, err := runCommand(ctx, defaultRunner, timeout, retry, created[0].WorktreeDir, "git", "diff", "--binary", revertOf.PreviousTargetSHA, revertOf.LandingSHA)
	if err != nil {
		return receipt, err
	}
	patchPath, err := filewrite.CreateScratch("", "wb-worktree-revert-*.patch", 0, []byte(patchOutput), inj)
	if patchPath != "" {
		defer func() { _ = os.Remove(patchPath) }()
	}
	if err != nil {
		return receipt, err
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, created[0].WorktreeDir, "git", "apply", "--check", "--3way", "--reverse", patchPath); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("forward revert conflicts with current target: %w", err))
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, created[0].WorktreeDir, "git", "apply", "--3way", "--reverse", "--index", patchPath); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, created[0].WorktreeDir, "git", "commit", "-m", "revert: reverse worktree merge "+receipt.ID); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	receipt.Phase = WorktreeMergePhaseRevert
	receipt.Status = WorktreeMergePrepared
	receipt.TargetSHA = created[0].BaseSHA
	receipt.Sources = nil
	receipt.Candidate = WorktreeMergeCandidate{Task: task, Worktree: created[0].WorktreeDir, Branch: created[0].Branch}
	receipt.Candidate.SHA, err = mergeRevision(ctx, defaultRunner, created[0].WorktreeDir, "HEAD")
	receipt.RevertOf = revertOf
	receipt.Rebase = nil
	receipt.Route = WorktreeMergeRouteDecision{}
	receipt.PullRequest = ""
	receipt.PreviousTargetSHA = ""
	receipt.LandingSHA = ""
	receipt.CanonicalSync = ""
	receipt.Checks = PullRequestWaitResult{}
	receipt.Cleanup = false
	receipt.CleanupReports = nil
	receipt.CleanedTasks = nil
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
	if err != nil {
		return receipt, err
	}
	if validationErr := validateWorktreeMergeCandidate(ctx, &receipt, timeout, retry, 0, 0, nil); validationErr != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeValidationFailed, fmt.Errorf("forward revert candidate validation failed: %w", validationErr))
	}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// applyOrDeferWorktreeMergeValidation is the single choke point every
// validation site in LandWorktreeMerge and PrepareWorktreeMerge calls through.
// When plan defers, it records a WorktreeMergeValidationDeferral and marks
// receipt.Validation as skipped instead of running the local suite; otherwise
// it runs validateWorktreeMergeCandidate exactly as before. Callers that need
// receipt.Route current with plan.Route for a downstream guard within the
// same call (finding B1) stamp it themselves at their own call site — see the
// StopBeforeMerge block and the route-resolution/publish-guard step in
// LandWorktreeMerge — rather than this shared helper doing it unconditionally,
// which would also touch PrepareWorktreeMerge's unrelated re-preparation path.
func applyOrDeferWorktreeMergeValidation(ctx context.Context, receipt *WorktreeMergeReceipt, plan worktreeMergeValidationPlan, timeout time.Duration, retry int, checkTimeout, shardAttemptTimeout time.Duration, reporter progress.Reporter) error {
	// Deliberately does not stamp receipt.Route: LandWorktreeMerge's own
	// route-resolution/publish-guard step does that once for the whole call
	// (see the "resolve_route" progress step), and PrepareWorktreeMerge must
	// never overwrite a receipt's previously recorded landing Route with a
	// value freshly re-resolved for an unrelated re-preparation (observed by
	// TestPrepareWorktreeMergeRefreshesPublishedCandidateAfterChecksFail).
	if !plan.Defer {
		// Clear any stale deferral recorded by an earlier call (finding X1):
		// this call is actually validating locally, so a lingering
		// ValidationDeferral would misrepresent this exact Validation record
		// as still-deferred to a later reader of the receipt.
		receipt.ValidationDeferral = nil
		return validateWorktreeMergeCandidate(ctx, receipt, timeout, retry, checkTimeout, shardAttemptTimeout, reporter)
	}
	if plan.DirectCI != nil {
		if err := verifyWorktreeMergeDirectCIInputs(ctx, *receipt); err != nil {
			return err
		}
	}
	receipt.Validation = quality.VerificationReport{
		Repository: receipt.Repository, Path: "git:" + receipt.Candidate.SHA, Revision: receipt.Candidate.SHA,
		WorkspaceClean: true, Status: quality.StatusSkipped,
		Results: []quality.VerificationEntry{{Status: quality.StatusSkipped, Detail: plan.Reason}},
	}
	receipt.BaselineValidation = quality.VerificationReport{}
	receipt.ValidationIdentity = nil
	receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{
		Route: plan.Route.Route, CandidateSHA: receipt.Candidate.SHA, Reason: plan.Reason, RecordedAt: time.Now().UTC(),
	}
	if plan.DirectCI != nil {
		receipt.ValidationDeferral.DirectCIPullRequest = plan.DirectCI.PullRequest
		receipt.ValidationDeferral.DirectCIPullRequestNumber = plan.DirectCI.PullRequestNumber
		receipt.ValidationDeferral.DirectCIBase = plan.DirectCI.Base
		receipt.ValidationDeferral.DirectCIWorkflowID = plan.DirectCI.WorkflowID
	}
	reportWorktreeMergeProgress(reporter, "validate_candidate", progress.Completed, "skipped: "+plan.Reason)
	return nil
}

func validateWorktreeMergeCandidate(ctx context.Context, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int, checkTimeout, shardAttemptTimeout time.Duration, reporter progress.Reporter) error {
	receipt.ImportedMainDeadcode = nil
	runOptions, err := quality.RepositoryRunOptions(receipt.Candidate.Worktree, quality.RunOptions{
		Timeout: timeout, Retry: retry, CheckTimeout: checkTimeout, ShardAttemptTimeout: shardAttemptTimeout,
		Progress: reportWorktreeMergeQualityProgress(reporter),
	})
	if err != nil {
		return fmt.Errorf("load candidate quality policy: %w", err)
	}
	// Keep raw shard failures outside the compact receipt, scoped to this exact
	// candidate. Otherwise a long failure index can hide every process error.
	if receipt.ReceiptPath != "" {
		runOptions.CoverageDiagnosticsDir = filepath.Join(receipt.ReceiptPath+".diagnostics", receipt.Candidate.SHA)
		runOptions.CoverageDiagnosticsRepository = receipt.Repository
	}
	lint := quality.VerifyWithOptions(ctx, receipt.Repository, receipt.Candidate.Worktree, []quality.Check{quality.CheckLint}, runOptions)
	lint.Revision = receipt.Candidate.SHA
	lint.WorkspaceClean = true
	var earlyImportedMain *WorktreeMergeImportedMainDeadcode
	if lint.Status == quality.StatusFailed {
		reportWorktreeMergeProgress(reporter, "validate_target_baseline", progress.Started, shortMergeRevision(receipt.TargetSHA))
		baselineLint, baselineErr := verifyWorktreeMergeTargetChecks(ctx, receipt.Repository, receipt.Candidate.Worktree, receipt.TargetSHA, timeout, retry, checkTimeout, shardAttemptTimeout, []quality.Check{quality.CheckLint})
		if baselineErr != nil {
			return fmt.Errorf("capture exact target lint baseline after candidate failure: %w", baselineErr)
		}
		reportWorktreeMergeProgress(reporter, "validate_target_baseline", progress.Completed, string(baselineLint.Status))
		var regressionErr error
		earlyImportedMain, regressionErr = worktreeMergeValidationWithImportedMainAttestation(baselineLint, lint, func() (*WorktreeMergeImportedMainDeadcode, error) {
			return worktreeMergeImportedMainDeadcode(ctx, receipt, timeout, retry, checkTimeout)
		})
		if regressionErr != nil {
			receipt.Validation = lint
			receipt.BaselineValidation = baselineLint
			return regressionErr
		}
	}
	runOptions.PriorNodeInstallReport = &lint
	rest := quality.VerifyWithOptions(ctx, receipt.Repository, receipt.Candidate.Worktree,
		[]quality.Check{quality.CheckTest, quality.CheckBuild, quality.CheckSpec}, runOptions)
	receipt.Validation = combineWorktreeMergeValidationReports(lint, rest)
	receipt.Validation.Revision = receipt.Candidate.SHA
	receipt.Validation.WorkspaceClean = true
	identity, identityOK := worktreeMergeValidationIdentity(*receipt)
	if identityOK {
		receipt.ValidationIdentity = &identity
	} else {
		// Validation remains authoritative, but an un-fingerprintable
		// environment must be a cache miss rather than a failed prepare.
		receipt.ValidationIdentity = nil
	}
	if receipt.Validation.Status == quality.StatusPassed {
		receipt.BaselineValidation = quality.VerificationReport{
			Repository: receipt.Repository, Path: "git:" + receipt.TargetSHA, Revision: receipt.TargetSHA,
			WorkspaceClean: true, Status: quality.StatusSkipped,
			Results: []quality.VerificationEntry{{Status: quality.StatusSkipped,
				Detail: "candidate passed every configured local check; target baseline was not needed"}},
		}
		return nil
	}
	reportWorktreeMergeProgress(reporter, "validate_target_baseline", progress.Started, shortMergeRevision(receipt.TargetSHA))
	baseline, err := verifyWorktreeMergeTarget(ctx, receipt.Repository, receipt.Candidate.Worktree, receipt.TargetSHA, timeout, retry, checkTimeout, shardAttemptTimeout)
	if err != nil {
		return fmt.Errorf("capture exact target validation baseline after candidate failure: %w", err)
	}
	receipt.BaselineValidation = baseline
	reportWorktreeMergeProgress(reporter, "validate_target_baseline", progress.Completed, string(baseline.Status))
	receipt.ImportedMainDeadcode = nil
	parentEvidence, regressionErr := worktreeMergeValidationWithImportedMainAttestation(baseline, receipt.Validation, func() (*WorktreeMergeImportedMainDeadcode, error) {
		if earlyImportedMain != nil {
			return earlyImportedMain, nil
		}
		return worktreeMergeImportedMainDeadcode(ctx, receipt, timeout, retry, checkTimeout)
	})
	if regressionErr != nil {
		return regressionErr
	}
	receipt.ImportedMainDeadcode = parentEvidence
	if err := worktreeMergeValidationRegressionWithImportedMain(baseline, receipt.Validation, parentEvidence); err != nil {
		return err
	}
	return nil
}

func combineWorktreeMergeValidationReports(first, second quality.VerificationReport) quality.VerificationReport {
	first.Results = append(first.Results, second.Results...)
	if first.Status == quality.StatusFailed || second.Status == quality.StatusFailed {
		first.Status = quality.StatusFailed
	} else if first.Status == quality.StatusPassed || second.Status == quality.StatusPassed {
		first.Status = quality.StatusPassed
	}
	return first
}

func worktreeMergeValidationIdentity(receipt WorktreeMergeReceipt) (WorktreeMergeValidationIdentity, bool) {
	policyPath := filepath.Join(receipt.Candidate.Worktree, ".wb", "quality.yaml")
	policy, err := os.ReadFile(policyPath)
	if errors.Is(err, os.ErrNotExist) {
		policy = []byte("absent")
	} else if err != nil {
		return WorktreeMergeValidationIdentity{}, false
	}
	policyDigest := sha256.Sum256(policy)
	executable, err := os.Executable()
	if err != nil {
		return WorktreeMergeValidationIdentity{}, false
	}
	executableSHA, err := fileSHA256(executable)
	if err != nil {
		return WorktreeMergeValidationIdentity{}, false
	}
	sourceSHAs := make([]string, len(receipt.Sources))
	for index, source := range receipt.Sources {
		sourceSHAs[index] = source.SHA
	}
	var validators map[string]string
	for _, result := range receipt.Validation.Results {
		fields := strings.Fields(result.Command)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if result.Language != "go" && result.Language != "node" && result.Language != "specscore" {
			continue
		}
		path, err := exec.LookPath(name)
		if err != nil {
			return WorktreeMergeValidationIdentity{}, false
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return WorktreeMergeValidationIdentity{}, false
		}
		if validators == nil {
			validators = make(map[string]string)
		}
		validators[name] = digest
	}
	return WorktreeMergeValidationIdentity{
		CandidateSHA: receipt.Candidate.SHA, TargetSHA: receipt.TargetSHA,
		SourceSHAs: sourceSHAs, QualityPolicySHA: hex.EncodeToString(policyDigest[:]),
		WBBuild: buildinfo.Version() + "@" + buildinfo.Revision(), WBExecutableSHA: executableSHA, Validators: validators,
	}, true
}

// requireWorktreeMergePublishedValidation is the single choke point every
// publish and landing transition passes through: push of the candidate
// branch, direct push of the target, pull-request creation or adoption, and
// merging the pull request. It refuses unless the receipt's own operation
// status has left validation_failed for this exact candidate and the
// recorded validation identity still names that exact candidate SHA — so a
// stale, unrevalidated, or drifted candidate can never be pushed for its
// first publish.
//
// One carve-out is deliberate and pre-dates this guard: an already-published
// pull request whose recorded PublishedCandidateSHA still names the exact
// current candidate SHA, and whose status has not itself gone back to
// validation_failed, is proven safe by the earlier push gate and by the
// remote CI checks that ran after that push, not by a fresh local
// validation. This does NOT cover an advance: once Candidate.SHA moves past
// PublishedCandidateSHA (a further commit was added, e.g. by
// advanceResolvedConflictWorktreeMergeCandidate or an --advance resume), the
// new head is unproven and must pass through the ordinary validation check
// below — callers are expected to re-validate that exact SHA (see the
// land-phase re-validation block in LandWorktreeMerge) before ever reaching
// this guard. The existing exact PR-CI validation-reuse path (the "Decide
// validation reuse" CI job backing AC
// pr-land-syncs-and-main-reuses-exact-validation) operates entirely outside
// this function, on an already-landed target commit, and is unaffected by
// it.
func requireWorktreeMergePublishedValidationContext(ctx context.Context, receipt WorktreeMergeReceipt, plan worktreeMergeValidationPlan, timeout time.Duration, retry int, checkTimeout time.Duration) error {
	if err := recheckWorktreeMergeImportedMainDeadcode(ctx, receipt, timeout, retry, checkTimeout); err != nil {
		return fmt.Errorf("recheck imported main deadcode attestation before publish: %w", err)
	}
	if err := worktreeMergeValidationRegressionWithImportedMain(receipt.BaselineValidation, receipt.Validation, receipt.ImportedMainDeadcode); err != nil {
		return fmt.Errorf("recheck candidate deadcode regression before publish: %w", err)
	}
	// Finding B1 (sneat-dev/wb#591): an exact PR-route deferral, or the
	// already-published carve-out below it, must never authorize a publish
	// on a route this call did NOT resolve as the pull-request route.
	// receipt.Route is kept current with the route resolved for this exact
	// call (see applyOrDeferWorktreeMergeValidation and the route resolution
	// in LandWorktreeMerge), so a receipt that deferred validation under a
	// PR-route call and is later resumed with --route direct falls through
	// to the ordinary validated-identity check below and must re-validate.
	//
	// Finding X1 (sneat-dev/wb#591 red-team follow-up): a recorded deferral
	// from an EARLIER call must never authorize a publish on THIS call
	// unless THIS call's freshly-resolved plan also permits deferring
	// (plan.Defer). Without this, `--allow-unfenced` or `--validate-locally`
	// on a resumed call would inherit a stale deferral recorded before the
	// fence was ever removed, or before local validation was requested.
	if receipt.Route.Route == WorktreeMergeRoutePullRequest && plan.Defer {
		if deferral := receipt.ValidationDeferral; deferral != nil &&
			deferral.Route == WorktreeMergeRoutePullRequest &&
			deferral.CandidateSHA == receipt.Candidate.SHA &&
			receipt.Validation.Status == quality.StatusSkipped {
			return nil
		}
		if receipt.PullRequest != "" && receipt.PublishedCandidateSHA != "" &&
			receipt.PublishedCandidateSHA == receipt.Candidate.SHA &&
			receipt.Status != WorktreeMergeValidationFailed {
			return nil
		}
	}
	if receipt.Route.Route == WorktreeMergeRouteDirect && plan.Defer && plan.DirectCI != nil {
		if deferral := receipt.ValidationDeferral; deferral != nil && deferral.Route == WorktreeMergeRouteDirect &&
			deferral.CandidateSHA == receipt.Candidate.SHA && deferral.DirectCIPullRequest == plan.DirectCI.PullRequest && deferral.DirectCIPullRequestNumber == plan.DirectCI.PullRequestNumber &&
			deferral.DirectCIBase == plan.DirectCI.Base && deferral.DirectCIWorkflowID == plan.DirectCI.WorkflowID &&
			receipt.Validation.Status == quality.StatusSkipped && receipt.Validation.Revision == receipt.Candidate.SHA {
			return nil
		}
	}
	identity := receipt.ValidationIdentity
	if receipt.Status != WorktreeMergeValidationFailed &&
		receipt.Validation.Revision == receipt.Candidate.SHA &&
		identity != nil && identity.CandidateSHA == receipt.Candidate.SHA {
		return nil
	}
	return fmt.Errorf(
		"candidate %s has receipt status %q and validation status %q; publish and landing transitions require a validated exact candidate; run `wb worktree merge resume %s` to re-validate",
		shortMergeRevision(receipt.Candidate.SHA), receipt.Status, receipt.Validation.Status, receipt.ReceiptPath,
	)
}

// preparedValidationStillValidContext's plan parameter is THIS call's freshly
// resolved validation plan (finding X1): a prepared receipt's recorded
// deferral is reusable only when THIS call's plan also permits deferring,
// never merely because an earlier call recorded one.
func preparedValidationStillValidContext(ctx context.Context, receipt WorktreeMergeReceipt, plan worktreeMergeValidationPlan, timeout time.Duration, retry int, checkTimeout time.Duration) (bool, error) {
	if err := recheckWorktreeMergeImportedMainDeadcode(ctx, receipt, timeout, retry, checkTimeout); err != nil {
		return false, err
	}
	if err := worktreeMergeValidationRegressionWithImportedMain(receipt.BaselineValidation, receipt.Validation, receipt.ImportedMainDeadcode); err != nil {
		return false, nil
	}
	if deferral := receipt.ValidationDeferral; plan.Defer && receipt.Status == WorktreeMergePrepared && deferral != nil &&
		receipt.Route.Route == WorktreeMergeRoutePullRequest && deferral.Route == WorktreeMergeRoutePullRequest &&
		deferral.CandidateSHA == receipt.Candidate.SHA && receipt.Validation.Status == quality.StatusSkipped &&
		receipt.Validation.Revision == receipt.Candidate.SHA {
		return true, nil
	}
	if deferral := receipt.ValidationDeferral; plan.Defer && plan.DirectCI != nil && receipt.Status == WorktreeMergePrepared && deferral != nil &&
		(receipt.Route.Route == "" || receipt.Route.Route == WorktreeMergeRouteDirect) && deferral.Route == WorktreeMergeRouteDirect &&
		deferral.CandidateSHA == receipt.Candidate.SHA && deferral.DirectCIPullRequest == plan.DirectCI.PullRequest && deferral.DirectCIPullRequestNumber == plan.DirectCI.PullRequestNumber &&
		deferral.DirectCIBase == plan.DirectCI.Base && deferral.DirectCIWorkflowID == plan.DirectCI.WorkflowID &&
		receipt.Validation.Status == quality.StatusSkipped && receipt.Validation.Revision == receipt.Candidate.SHA {
		return true, nil
	}
	if receipt.Status != WorktreeMergePrepared || (receipt.Validation.Status != quality.StatusPassed && receipt.Validation.Status != quality.StatusFailed) ||
		receipt.Validation.Revision != receipt.Candidate.SHA || !receipt.Validation.WorkspaceClean ||
		receipt.ValidationIdentity == nil {
		return false, nil
	}
	if receipt.Validation.Status == quality.StatusFailed {
		baselineAccepted := receipt.BaselineValidation.Status == quality.StatusFailed ||
			(receipt.BaselineValidation.Status == quality.StatusPassed && receipt.ImportedMainDeadcode != nil)
		if receipt.BaselineValidation.Revision != receipt.TargetSHA || !baselineAccepted {
			return false, nil
		}
	}
	identity, fingerprintable := worktreeMergeValidationIdentity(receipt)
	return fingerprintable && reflect.DeepEqual(*receipt.ValidationIdentity, identity), nil
}

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// verifyWorktreeMergeTarget materializes the exact fetched target revision in
// a temporary archive rather than trusting a mutable canonical checkout. This
// keeps the baseline tied to receipt.TargetSHA even while a candidate is being
// rebased for target drift.
func verifyWorktreeMergeTarget(ctx context.Context, repository, repositoryDir, targetSHA string, timeout time.Duration, retry int, checkTimeout, shardAttemptTimeout time.Duration) (quality.VerificationReport, error) {
	return verifyWorktreeMergeTargetChecks(ctx, repository, repositoryDir, targetSHA, timeout, retry, checkTimeout, shardAttemptTimeout,
		[]quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec})
}

func verifyWorktreeMergeTargetChecks(ctx context.Context, repository, repositoryDir, targetSHA string, timeout time.Duration, retry int, checkTimeout, shardAttemptTimeout time.Duration, checks []quality.Check) (quality.VerificationReport, error) {
	targetSHA = strings.TrimSpace(targetSHA)
	if targetSHA == "" {
		return quality.VerificationReport{}, errors.New("target SHA is required for validation baseline")
	}
	temporary, err := os.MkdirTemp("", "wb-worktree-merge-target-*")
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("create target validation snapshot: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	archivePath := filepath.Join(temporary, "target.tar")
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, repositoryDir, "git", "archive", "--format=tar", "--output="+archivePath, targetSHA); err != nil {
		return quality.VerificationReport{}, fmt.Errorf("archive target %s: %w", targetSHA, err)
	}
	snapshot := filepath.Join(temporary, "tree")
	if err := extractWorktreeMergeArchive(archivePath, snapshot); err != nil {
		return quality.VerificationReport{}, fmt.Errorf("materialize target %s: %w", targetSHA, err)
	}
	// Some repository checks, including SpecScore project-host validation,
	// intentionally inspect the checkout's origin remote. An archive has no
	// .git directory, so recreate only that read-only context from the
	// candidate before comparing failure identities. The snapshot remains an
	// exact target tree: no commits, refs, index, or candidate files are used.
	if err := configureWorktreeMergeBaselineRemote(ctx, repositoryDir, snapshot, timeout, retry); err != nil {
		return quality.VerificationReport{}, err
	}
	runOptions, err := quality.RepositoryRunOptions(snapshot, quality.RunOptions{Timeout: timeout, Retry: retry, CheckTimeout: checkTimeout, ShardAttemptTimeout: shardAttemptTimeout})
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("load target quality policy: %w", err)
	}
	cacheKey, err := quality.NewValidationCacheKey(repository, targetSHA, snapshot, buildinfo.Revision(), checks, validationCacheValidatorSHAs(checks), runOptions)
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("fingerprint target validation baseline: %w", err)
	}
	cacheRoot, err := os.UserHomeDir()
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("resolve WB validation cache: %w", err)
	}
	cacheDir := quality.ValidationCacheDir(filepath.Join(cacheRoot, ".wb"))
	if len(checks) == 1 && checks[0] == quality.CheckLint {
		fullChecks := []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec}
		fullKey, keyErr := quality.NewValidationCacheKey(repository, targetSHA, snapshot, buildinfo.Revision(), fullChecks, validationCacheValidatorSHAs(fullChecks), runOptions)
		if keyErr != nil {
			return quality.VerificationReport{}, fmt.Errorf("fingerprint full target validation baseline: %w", keyErr)
		}
		if cached, ok, cacheErr := quality.LoadValidationCache(cacheDir, fullKey); cacheErr != nil {
			return quality.VerificationReport{}, fmt.Errorf("read full target validation baseline cache: %w", cacheErr)
		} else if ok {
			return worktreeMergeLintEvidence(cached), nil
		}
	}
	if cached, ok, cacheErr := quality.LoadValidationCache(cacheDir, cacheKey); cacheErr != nil {
		return quality.VerificationReport{}, fmt.Errorf("read target validation baseline cache: %w", cacheErr)
	} else if ok {
		return cached, nil
	}
	report := quality.VerifyWithOptions(ctx, repository, snapshot, checks, runOptions)
	// The transient snapshot is intentionally removed before this durable
	// receipt is written. The exact revision remains the useful evidence.
	report.Path = "git:" + targetSHA
	report.Revision = targetSHA
	report.WorkspaceClean = true
	if report.Status != quality.StatusSkipped {
		if cacheErr := quality.SaveValidationCache(cacheDir, cacheKey, report); cacheErr != nil {
			return quality.VerificationReport{}, fmt.Errorf("save target validation baseline cache: %w", cacheErr)
		}
	}
	return report, nil
}

func worktreeMergeLintEvidence(report quality.VerificationReport) quality.VerificationReport {
	entries := make([]quality.VerificationEntry, 0, len(report.Results))
	report.Status = quality.StatusSkipped
	for _, entry := range report.Results {
		if entry.Check != quality.CheckLint && entry.Check != "install" && entry.Check != "" {
			continue
		}
		entries = append(entries, entry)
		if entry.Status == quality.StatusFailed {
			report.Status = quality.StatusFailed
		} else if report.Status == quality.StatusSkipped && entry.Status == quality.StatusPassed {
			report.Status = quality.StatusPassed
		}
	}
	report.Results = entries
	return report
}

// validationCacheValidatorSHAs prevents a baseline report from being reused
// after an installed external validator changes. The candidate receipt already
// records validator identities; the baseline cache must carry the same guard.
func validationCacheValidatorSHAs(checks []quality.Check) map[string]string {
	for _, check := range checks {
		if check != quality.CheckSpec {
			continue
		}
		path, err := exec.LookPath("specscore")
		if err != nil {
			return map[string]string{"specscore": "unresolved"}
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return map[string]string{"specscore": "unreadable"}
		}
		return map[string]string{"specscore": digest}
	}
	return nil
}

func configureWorktreeMergeBaselineRemote(ctx context.Context, candidateWorktree, snapshot string, timeout time.Duration, retry int) error {
	remote, _, err := runCommand(ctx, defaultRunner, timeout, retry, candidateWorktree, "git", "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("read candidate origin remote for target baseline: %w", err)
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return errors.New("candidate origin remote for target baseline is empty")
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, snapshot, "git", "init", "--quiet"); err != nil {
		return fmt.Errorf("initialize target baseline Git context: %w", err)
	}
	if _, _, err := runCommand(ctx, defaultRunner, timeout, retry, snapshot, "git", "remote", "add", "origin", remote); err != nil {
		return fmt.Errorf("configure target baseline origin remote: %w", err)
	}
	return nil
}

func extractWorktreeMergeArchive(archivePath, destination string) error {
	return extractWorktreeMergeArchiveInjected(archivePath, destination, nil)
}

// extractWorktreeMergeArchiveInjected is extractWorktreeMergeArchive's test
// seam (task-9 PR-4): every production call site reaches it only through
// extractWorktreeMergeArchive, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/write/close failure branch
// on a tar.TypeReg entry deterministically. Each regular-file entry is
// created with a fixed name (from the archive) that is always fully
// overwritten (O_CREATE|O_TRUNC, never O_EXCL), so this uses
// CreateOrTruncatePath rather than CreateExclusivePath. The streaming copy
// from the tar reader goes through io.Copy(filewrite.Writer(file, path,
// inj), reader) (review-t9-pr6 N1): filewrite.Writer is an io.Writer
// adapter, not a []byte-at-a-time primitive, so it fits an arbitrary-size
// streaming source exactly as well as it fits a small in-memory payload,
// making io.Copy's write failures injectable without changing the chunks
// io.Copy writes or how it wraps their errors.
func extractWorktreeMergeArchiveInjected(archivePath, destination string, inj *filewrite.Injector) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archived path %q", header.Name)
		}
		path := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// git archive emits PAX metadata before regular entries on some
			// platforms. The tar reader applies it to the following header.
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(path, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			file, err := filewrite.CreateOrTruncatePath(path, os.FileMode(header.Mode), inj)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(filewrite.Writer(file, path, inj), reader)
			closeErr := filewrite.Close(file, path, inj)
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("unsafe archived symlink %q -> %q", header.Name, header.Linkname)
			}
			linkTarget := filepath.Clean(filepath.Join(filepath.Dir(path), header.Linkname))
			relativeTarget, err := filepath.Rel(destination, linkTarget)
			if err != nil || relativeTarget == ".." || strings.HasPrefix(relativeTarget, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe archived symlink %q -> %q", header.Name, header.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archived entry %q", header.Name)
		}
	}
}

func worktreeMergeValidationRegression(baseline, candidate quality.VerificationReport) error {
	return worktreeMergeValidationRegressionWithImportedMain(baseline, candidate, nil)
}

func worktreeMergeValidationRegressionWithImportedMain(baseline, candidate quality.VerificationReport, imported *WorktreeMergeImportedMainDeadcode) error {
	baselineFailures := failedWorktreeMergeVerificationEntries(baseline)
	candidateFailures := failedWorktreeMergeVerificationEntries(candidate)
	if candidate.Status == quality.StatusFailed && len(candidateFailures) == 0 {
		return errors.New("candidate validation reported failure without failed check evidence")
	}
	matched := make([]bool, len(baselineFailures))
	for _, candidateFailure := range candidateFailures {
		if candidateFailure.Deadcode != nil {
			if matchDeadcodeBaselineFailure(baselineFailures, candidateFailure) || matchImportedMainDeadcodeFailure(baseline.Results, candidateFailure, imported) {
				continue
			}
			if imported == nil {
				if delta, ok := worktreeMergeDeadcodeTargetDelta(baseline.Results, candidateFailure); ok {
					return fmt.Errorf("candidate validation has %d deadcode finding(s) absent from exact target: %s", len(delta), strings.Join(delta, ", "))
				}
			}
			return fmt.Errorf("candidate validation introduced or changed deadcode failure: %s", candidateFailure.Command)
		}
		if candidateFailure.Language == "go" && candidateFailure.Check == quality.CheckTest {
			if matchGoCoverageBaselineFailure(baselineFailures, candidateFailure) {
				continue
			}
		}
		if candidateFailure.Language == "specscore" {
			if matchSpecScoreBaselineFailure(baselineFailures, candidateFailure) {
				continue
			}
			return fmt.Errorf("candidate validation introduced or changed failure: %s %s %s", candidateFailure.Language, candidateFailure.Check, candidateFailure.Command)
		}
		found := false
		for index, baselineFailure := range baselineFailures {
			if !matched[index] && sameWorktreeMergeFailure(baselineFailure, candidateFailure) {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("candidate validation introduced or changed failure: %s %s %s", candidateFailure.Language, candidateFailure.Check, candidateFailure.Command)
		}
	}
	return nil
}

func hasWorktreeMergeDeadcodeFailure(report quality.VerificationReport) bool {
	for _, entry := range report.Results {
		if entry.Deadcode != nil {
			return true
		}
	}
	return false
}

func worktreeMergeNonDeadcodeRegression(baseline, candidate quality.VerificationReport) error {
	withoutDeadcode := func(report quality.VerificationReport) quality.VerificationReport {
		results := make([]quality.VerificationEntry, 0, len(report.Results))
		for _, entry := range report.Results {
			if entry.Deadcode == nil {
				results = append(results, entry)
			}
		}
		report.Results = results
		return report
	}
	baseline, candidate = withoutDeadcode(baseline), withoutDeadcode(candidate)
	if candidate.Status == quality.StatusFailed && len(failedWorktreeMergeVerificationEntries(candidate)) == 0 {
		candidate.Status = quality.StatusPassed
	}
	return worktreeMergeValidationRegression(baseline, candidate)
}

func worktreeMergeValidationWithImportedMainAttestation(baseline, candidate quality.VerificationReport, attest func() (*WorktreeMergeImportedMainDeadcode, error)) (*WorktreeMergeImportedMainDeadcode, error) {
	targetErr := worktreeMergeValidationRegression(baseline, candidate)
	if targetErr == nil {
		return nil, nil
	}
	if !hasWorktreeMergeDeadcodeFailure(candidate) || worktreeMergeNonDeadcodeRegression(baseline, candidate) != nil {
		return nil, targetErr
	}
	evidence, err := attest()
	if err != nil {
		return nil, fmt.Errorf("attest imported main deadcode baseline: %w", err)
	}
	if err := worktreeMergeValidationRegressionWithImportedMain(baseline, candidate, evidence); err != nil {
		return evidence, err
	}
	return evidence, nil
}

func matchImportedMainDeadcodeFailure(baseline []quality.VerificationEntry, candidate quality.VerificationEntry, imported *WorktreeMergeImportedMainDeadcode) bool {
	if imported == nil || candidate.Language != "go" || candidate.Check != quality.CheckLint || candidate.Command != worktreeMergeDeadcodeCommand || !candidate.Deadcode.Valid() {
		return false
	}
	target, valid := worktreeMergeDeadcodeIdentitySet(baseline, candidate.Language, candidate.Check, candidate.Command, candidate.Module)
	if !valid {
		return false
	}
	parent, valid := importedMainDeadcodeIdentities(imported.Validation, candidate.Check, candidate.Command, candidate.Module)
	if !valid {
		return false
	}
	for id := range parent {
		target[id] = true
	}
	for _, id := range candidate.Deadcode.Identities {
		if !target[id] {
			return false
		}
	}
	return true
}

func worktreeMergeDeadcodeIdentitySet(entries []quality.VerificationEntry, language string, check quality.Check, command, module string) (map[string]bool, bool) {
	var matching *quality.VerificationEntry
	for index := range entries {
		entry := &entries[index]
		if entry.Language != language || entry.Check != check || entry.Command != command || entry.Module != module {
			continue
		}
		if matching != nil {
			return nil, false
		}
		matching = entry
	}
	if matching == nil {
		return nil, false
	}
	if matching.Status == quality.StatusPassed && matching.Deadcode == nil {
		return map[string]bool{}, true
	}
	if matching.Status != quality.StatusFailed || !matching.Deadcode.Valid() {
		return nil, false
	}
	identities := make(map[string]bool, len(matching.Deadcode.Identities))
	for _, identity := range matching.Deadcode.Identities {
		identities[identity] = true
	}
	return identities, true
}

// worktreeMergeDeadcodeTargetDelta is diagnostic only. An incomplete or
// ambiguous exact-target report cannot supply a trustworthy difference.
func worktreeMergeDeadcodeTargetDelta(target []quality.VerificationEntry, candidate quality.VerificationEntry) ([]string, bool) {
	if !candidate.Deadcode.Valid() {
		return nil, false
	}
	known, valid := worktreeMergeDeadcodeIdentitySet(target, candidate.Language, candidate.Check, candidate.Command, candidate.Module)
	if !valid {
		return nil, false
	}
	var delta []string
	for _, identity := range candidate.Deadcode.Identities {
		if !known[identity] {
			delta = append(delta, identity)
		}
	}
	return delta, len(delta) > 0
}

// matchDeadcodeBaselineFailure compares complete function identities, not the
// bounded human diagnostic or its count alone. An older baseline receipt
// without structured evidence retains the strict historical detail match.
func matchDeadcodeBaselineFailure(baseline []quality.VerificationEntry, candidate quality.VerificationEntry) bool {
	if candidate.Language != "go" || candidate.Check != quality.CheckLint || candidate.Command != "go run ./cmd/wb deadcode" || !candidate.Deadcode.Valid() {
		return false
	}
	for _, previous := range baseline {
		if previous.Language != candidate.Language || previous.Module != candidate.Module || previous.Check != candidate.Check || previous.Command != candidate.Command {
			continue
		}
		if previous.Deadcode == nil {
			return sameWorktreeMergeFailure(previous, candidate)
		}
		if !previous.Deadcode.Valid() {
			return false
		}
		known := make(map[string]bool, len(previous.Deadcode.Identities))
		for _, identity := range previous.Deadcode.Identities {
			known[identity] = true
		}
		for _, identity := range candidate.Deadcode.Identities {
			if !known[identity] {
				return false
			}
		}
		return true
	}
	return false
}

func failedWorktreeMergeVerificationEntries(report quality.VerificationReport) []quality.VerificationEntry {
	entries := make([]quality.VerificationEntry, 0, len(report.Results))
	for _, entry := range report.Results {
		if entry.Status == quality.StatusFailed {
			entries = append(entries, entry)
		}
	}
	return entries
}

func sameWorktreeMergeFailure(baseline, candidate quality.VerificationEntry) bool {
	// The discriminating comparison is the exact check identity plus the
	// normalized diagnostic. Any identity mismatch or remaining normalized
	// detail difference falsifies equivalence.
	return baseline.Language == candidate.Language && baseline.Module == candidate.Module && baseline.Check == candidate.Check &&
		baseline.Command == candidate.Command && normalizeWorktreeMergeFailureDetail(baseline.Detail) == normalizeWorktreeMergeFailureDetail(candidate.Detail)
}

var (
	worktreeMergeFailureTimestampPattern     = regexp.MustCompile(`(?m)^((?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9])(\s+\[[^\r\n]*\]|\s+[✓├])`)
	worktreeMergeFailureGeneratedPattern     = regexp.MustCompile(`(?i)\b(Generated)\s+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)\b`)
	worktreeMergeFailureBuiltPattern         = regexp.MustCompile(`(?i)\b(built in|Completed in)\s+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)\b`)
	worktreeMergeFailureParenthesizedPattern = regexp.MustCompile(`\(\+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)\)`)
	worktreeMergeFailureTruncationPattern    = regexp.MustCompile(`(?s)(… output truncated; final [0-9]+ bytes:\r?\n)([^\r\n]*)`)
	worktreeMergeFailurePartialTimingPattern = regexp.MustCompile(`(?i)^([[:alpha:]]+)\s+in\s+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)$`)
)

func normalizeWorktreeMergeFailureDetail(detail string) string {
	// Quality command output can include the ephemeral checkout path. It is not
	// behavior, so compare a whitespace-normalized form after erasing absolute
	// paths. The path may be quoted or wrapped in diagnostic punctuation, so
	// normalize it inside the field rather than requiring the whole field to be
	// an absolute path. All command, check, module, and error text still has to
	// match.
	detail = worktreeMergeFailureTimestampPattern.ReplaceAllString(detail, `<timestamp>${2}`)
	detail = worktreeMergeFailureGeneratedPattern.ReplaceAllString(detail, `${1} <duration>`)
	detail = worktreeMergeFailureBuiltPattern.ReplaceAllString(detail, `${1} <duration>`)
	detail = worktreeMergeFailureParenthesizedPattern.ReplaceAllString(detail, `(+<duration>)`)
	detail = worktreeMergeFailureTruncationPattern.ReplaceAllStringFunc(detail, normalizeWorktreeMergeFailureTruncatedTail)
	fields := strings.Fields(detail)
	for index, field := range fields {
		fields[index] = normalizeWorktreeMergeFailureField(field)
	}
	return strings.Join(fields, " ")
}

func normalizeWorktreeMergeFailureTruncatedTail(match string) string {
	const markerEnd = "\n"
	lineStart := strings.Index(match, markerEnd)
	if lineStart < 0 || lineStart+len(markerEnd) >= len(match) {
		return match
	}
	line := strings.TrimSpace(match[lineStart+len(markerEnd):])
	partial := worktreeMergeFailurePartialTimingPattern.FindStringSubmatch(line)
	if len(partial) != 2 {
		return match
	}
	word := strings.ToLower(partial[1])
	for _, complete := range []string{"built", "completed"} {
		if strings.HasSuffix(complete, word) {
			return match[:lineStart+len(markerEnd)] + complete + " in <duration>"
		}
	}
	return match
}

const worktreeMergeFailurePathPunctuation = "\"'`()[]{}<>,;."

func normalizeWorktreeMergeFailureField(field string) string {
	start := 0
	for start < len(field) && strings.ContainsRune(worktreeMergeFailurePathPunctuation, rune(field[start])) {
		start++
	}
	end := len(field)
	for end > start && strings.ContainsRune(worktreeMergeFailurePathPunctuation, rune(field[end-1])) {
		end--
	}
	if start != end && filepath.IsAbs(field[start:end]) {
		field = field[:start] + "<workspace>" + field[end:]
	}
	return field
}

func activeRuleCount(pages [][]githubActiveBranchRule) int {
	count := 0
	for _, page := range pages {
		count += len(page)
	}
	return count
}

func worktreeMergeLaneID(repository, target string) string {
	hash := sha256.Sum256([]byte(repository + "\x00" + target))
	readable := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(repository + "-" + target)
	readable = strings.Trim(readable, "-")
	if len(readable) > 42 {
		readable = readable[:42]
	}
	return "merge-" + readable + "-" + hex.EncodeToString(hash[:6])
}

func worktreeMergeOperationID(lane string, sources []WorktreeMergeSource) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(lane))
	for _, source := range sources {
		_, _ = hash.Write([]byte{'\x00'})
		_, _ = hash.Write([]byte(source.SHA))
	}
	return lane + "-" + hex.EncodeToString(hash.Sum(nil)[:6])
}

// worktreeMergeOperationIDMatchesRecordedSourceSet accepts the original
// source-derived operation identity when a later append-only source refresh
// replaced the current source heads. No identity other than the current set
// or one complete recorded refresh set is accepted.
func worktreeMergeOperationIDMatchesRecordedSourceSet(receipt WorktreeMergeReceipt) bool {
	if receipt.ID == worktreeMergeOperationID(receipt.Lane, receipt.Sources) {
		return true
	}
	for _, refresh := range receipt.SourceRefreshes {
		if len(refresh.Sources) == 0 || refresh.RecordedAt.IsZero() {
			continue
		}
		complete := true
		for _, source := range refresh.Sources {
			if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
				complete = false
				break
			}
		}
		if complete && receipt.ID == worktreeMergeOperationID(receipt.Lane, refresh.Sources) {
			return true
		}
	}
	return false
}

func worktreeMergeSupersededOperationID(operation, receiptPath string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(operation))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(filepath.Clean(receiptPath)))
	return operation + "-superseded-" + hex.EncodeToString(hash.Sum(nil)[:6])
}

// validateWorktreeMergeSupersededOperationID proves that an unpublished
// conflict receipt is either the source-derived root operation or a chain of
// deterministic successors. Each successor names the exact predecessor path
// from the same report directory, so an arbitrary suffix cannot impersonate a
// receipt that PrepareWorktreeMerge would have created.
func validateWorktreeMergeSupersededOperationID(operation, receiptPath, lane string, sources []WorktreeMergeSource) error {
	root := worktreeMergeOperationID(lane, sources)
	path := filepath.Clean(receiptPath)
	if filepath.Base(path) != operation+".json" {
		return fmt.Errorf("receipt path %s does not name operation %s", receiptPath, operation)
	}
	reportsDir := filepath.Dir(path)
	for operation != root {
		const marker = "-superseded-"
		index := strings.LastIndex(operation, marker)
		if index <= 0 {
			return fmt.Errorf("operation %s does not descend from source-derived operation %s", operation, root)
		}
		suffix := operation[index+len(marker):]
		if len(suffix) != 12 || suffix != strings.ToLower(suffix) {
			return fmt.Errorf("operation %s has an invalid supersession suffix", operation)
		}
		if _, err := hex.DecodeString(suffix); err != nil {
			return fmt.Errorf("operation %s has an invalid supersession suffix: %w", operation, err)
		}
		predecessor := operation[:index]
		predecessorPath := filepath.Join(reportsDir, predecessor+".json")
		if want := worktreeMergeSupersededOperationID(predecessor, predecessorPath); operation != want {
			return fmt.Errorf("operation %s is not the deterministic successor of %s", operation, predecessorPath)
		}
		operation = predecessor
	}
	return nil
}

// validateWorktreeMergeSupersededOperationIDMatchesRecordedSourceSet accepts
// a deterministic supersession chain rooted in either the current sources or
// one complete historical source set retained by an append-only refresh. The
// chain validator still proves every predecessor path hash and suffix.
func validateWorktreeMergeSupersededOperationIDMatchesRecordedSourceSet(receipt WorktreeMergeReceipt, receiptPath string) error {
	currentErr := validateWorktreeMergeSupersededOperationID(receipt.ID, receiptPath, receipt.Lane, receipt.Sources)
	if currentErr == nil {
		return nil
	}
	for _, refresh := range receipt.SourceRefreshes {
		if len(refresh.Sources) == 0 || refresh.RecordedAt.IsZero() {
			continue
		}
		complete := true
		for _, source := range refresh.Sources {
			if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
				complete = false
				break
			}
		}
		if complete && validateWorktreeMergeSupersededOperationID(receipt.ID, receiptPath, receipt.Lane, refresh.Sources) == nil {
			return nil
		}
	}
	return currentErr
}

func mergeOperationSuffix(operation string) string {
	if index := strings.LastIndex(operation, "-"); index >= 0 && index+1 < len(operation) {
		return operation[index+1:]
	}
	return operation
}

func activeWorktreeMergeLaneReceipt(ctx context.Context, projectsRoot, reportsDir, lane string, except ...string) (*WorktreeMergeReceipt, error) {
	return activeWorktreeMergeLaneReceiptWithRunner(ctx, projectsRoot, reportsDir, lane, defaultRunner, except...)
}

func activeWorktreeMergeLaneReceiptWithRunner(ctx context.Context, projectsRoot, reportsDir, lane string, remoteRunner runner.Runner, except ...string) (*WorktreeMergeReceipt, error) {
	entries, err := os.ReadDir(reportsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if isWorktreeMergeReportSidecar(entry.Name()) {
			continue
		}
		if entry.Name() != lane+".json" && !strings.HasPrefix(entry.Name(), lane+"-") {
			continue
		}
		path := filepath.Join(reportsDir, entry.Name())
		excluded := false
		for _, ignored := range except {
			if filepath.Clean(path) == filepath.Clean(ignored) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		receipt, readErr := readWorktreeMergeReceipt(path)
		if readErr != nil {
			return nil, readErr
		}
		receiptLane := receipt.Lane
		if receiptLane == "" {
			receiptLane = worktreeMergeLaneID(receipt.Repository, receipt.Target)
		}
		if receiptLane == lane && receipt.Status != WorktreeMergeComplete {
			// A valid immutable missing-cleanup acknowledgement proves the old
			// landed receipt's assets are already terminal. It releases lane
			// ownership even when nobody resumed the historical receipt merely to
			// rewrite its derived status to complete.
			if receipt.Status == WorktreeMergeLanded && receipt.LandingSHA != "" && receipt.Cleanup {
				ackPath := receipt.ReceiptPath + worktreeMergeMissingCleanupAcknowledgementSuffix
				if _, statErr := os.Stat(ackPath); statErr == nil {
					if _, ackErr := validateMissingCleanupAcknowledgement(ctx, projectsRoot, receipt, ackPath, 0, 0); ackErr != nil {
						return nil, fmt.Errorf("validate missing-cleanup acknowledgement for %s: %w", receipt.ReceiptPath, ackErr)
					}
					continue
				} else if !os.IsNotExist(statErr) {
					return nil, fmt.Errorf("inspect missing-cleanup acknowledgement %s: %w", ackPath, statErr)
				}
			}
			acknowledged, ackErr := hasLandedFailureAcknowledgement(receipt)
			if ackErr != nil {
				return nil, ackErr
			}
			if acknowledged {
				continue
			}
			superseded, supersessionErr := hasValidationFailureSupersession(ctx, projectsRoot, receipt)
			if supersessionErr != nil {
				return nil, supersessionErr
			}
			if superseded {
				continue
			}
			rebatched, rebatchErr := hasPreparedWorktreeMergeRebatch(receipt)
			if rebatchErr != nil {
				// A sidecar that cannot be authenticated is not evidence of an
				// in-flight merge. Skipping it keeps one abandoned
				// `.prepared.rebatched.ack.json` from taking the whole
				// (repository, target) lane offline (wb#319). Land and other
				// verbs that name this receipt still see the same error.
				continue
			}
			if rebatched {
				continue
			}
			strandedAcknowledged, strandedErr := hasStrandedLandingAcknowledgement(receipt)
			if strandedErr != nil {
				return nil, strandedErr
			}
			if strandedAcknowledged {
				continue
			}
			absorbedAcknowledged, absorbedErr := hasAbsorbedConflictAcknowledgement(receipt)
			if absorbedErr != nil {
				return nil, absorbedErr
			}
			if absorbedAcknowledged {
				continue
			}
			retiredAcknowledged, retiredErr := hasRetiredPublicationAcknowledgement(receipt)
			if retiredErr != nil {
				return nil, retiredErr
			}
			if retiredAcknowledged {
				continue
			}
			unpublishedFailureAcknowledged, acknowledgementErr := hasUnpublishedValidationFailureAcknowledgement(receipt)
			if acknowledgementErr != nil {
				return nil, acknowledgementErr
			}
			if unpublishedFailureAcknowledged {
				continue
			}
			// A prepare-time conflict releases this lane only after proving its
			// candidate was not published. Publication fields can be lost after
			// an ambiguous push, so the receipt alone is not enough evidence.
			if receipt.Status == WorktreeMergeConflict && receipt.Phase == WorktreeMergePhasePrepare &&
				receipt.PullRequest == "" && receipt.PublishedCandidateSHA == "" && receipt.LandingSHA == "" && receipt.Candidate.Branch != "" {
				if _, adopted, adoptionErr := adoptedPublishedCandidate(ctx, receipt); adoptionErr != nil {
					return nil, fmt.Errorf("validate published-candidate adoption for %s: %w", receipt.ReceiptPath, adoptionErr)
				} else if !adopted {
					canonical, canonicalErr := worktrees.CanonicalRepositoryPath(projectsRoot, receipt.Repository)
					if canonicalErr != nil {
						return nil, canonicalErr
					}
					remote, _, remoteErr := runCommand(ctx, remoteRunner, 0, 0, canonical, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
					if remoteErr != nil {
						return nil, fmt.Errorf("verify unpublished conflict candidate %s: %w", receipt.ReceiptPath, remoteErr)
					}
					if strings.TrimSpace(remote) == "" {
						continue
					}
				}
			}
			return &receipt, nil
		}
	}
	return nil, nil
}

// isExactPublishedValidationFailureReplay proves the only read-only retry
// allowed after validation_failed. It additionally accepts the founder-approved
// recorded forward-repair shape only after every immutable ancestry root is
// re-read from the exact clean candidate.
func isExactPublishedValidationFailureReplay(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, sources []WorktreeMergeSource) (bool, error) {
	if receipt.Status != WorktreeMergeValidationFailed || receipt.PullRequest == "" ||
		receipt.PublishedCandidateSHA == "" || receipt.Candidate.SHA == "" ||
		!sameWorktreeMergeSources(receipt.Sources, sources) {
		return false, nil
	}
	if receipt.PublishedCandidateSHA == receipt.Candidate.SHA {
		return true, nil
	}
	if len(receipt.SourceRefreshes) == 0 {
		return false, nil
	}
	claim, err := validateMergeAcknowledgementCandidate(ctx, projectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return false, err
	}
	if err := recheckWorktreeMergeSources(ctx, receipt.Sources); err != nil {
		return false, err
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return false, err
	}
	roots := []string{claim.BaseSHA, receipt.TargetSHA, currentTarget, receipt.PublishedCandidateSHA}
	for _, refresh := range receipt.SourceRefreshes {
		for _, source := range refresh.Sources {
			roots = append(roots, source.SHA)
		}
	}
	for _, source := range receipt.Sources {
		roots = append(roots, source.SHA)
	}
	for _, root := range roots {
		if root == "" {
			return false, errors.New("published repair replay has an incomplete immutable ancestry root")
		}
		contains, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, root, receipt.Candidate.SHA)
		if ancestorErr != nil {
			return false, ancestorErr
		}
		if !contains {
			return false, fmt.Errorf("candidate %s does not contain immutable replay root %s", receipt.Candidate.SHA, root)
		}
	}
	remote, _, err := runCommand(ctx, defaultRunner, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(strings.TrimSpace(remote), receipt.PublishedCandidateSHA+"\t"), nil
}

func writeWorktreeMergePrompt(repository, target string, sources []WorktreeMergeSource) (string, error) {
	return writeWorktreeMergePromptInjected(repository, target, sources, nil)
}

// writeWorktreeMergePromptInjected is writeWorktreeMergePrompt's test seam
// (task-9 PR-9): every production call site reaches it only through
// writeWorktreeMergePrompt, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged. A test passes its own Injector to
// reach the scratch prompt file's create/chmod/write/close failure branches
// deterministically.
func writeWorktreeMergePromptInjected(repository, target string, sources []WorktreeMergeSource, inj *filewrite.Injector) (string, error) {
	var body strings.Builder
	fmt.Fprintf(&body, "WB mechanically prepares an integration candidate for %s target %s from these exact source heads:\n", repository, target)
	for _, source := range sources {
		fmt.Fprintf(&body, "- %s %s %s\n", source.Branch, source.SHA, source.Worktree)
	}
	return writeWorktreeMergeScratchPromptInjected("wb-worktree-merge-prompt-*.txt", body.String(), inj)
}

// writeWorktreeMergeScratchPromptInjected is task-9 PR-9's shared shape for
// the four worktree-merge prompt writers in this package
// (writeWorktreeMergePrompt, writeConflictCandidateRefreshPrompt,
// writePublishedForwardRepairPrompt, writeValidationFailureSealPrompt): a
// private (0600) scratch temp file, written once with body and returned by
// path for a single worktrees.Create call to consume, then removed by the
// caller once that call returns. On any failure -- create, chmod, write, or
// close -- the reservation is removed before returning, matching all four
// original inline sequences' cleanup-on-any-error behaviour exactly.
func writeWorktreeMergeScratchPromptInjected(pattern, body string, inj *filewrite.Injector) (string, error) {
	path, err := filewrite.CreateScratch("", pattern, 0o600, []byte(body), inj)
	if err != nil {
		if path != "" {
			_ = os.Remove(path)
		}
		return "", err
	}
	return path, nil
}

func persistWorktreeMergeReceipt(receipt WorktreeMergeReceipt) error {
	return persistWorktreeMergeReceiptInjected(receipt, nil)
}

// persistWorktreeMergeReceiptInjected is persistWorktreeMergeReceipt's test
// seam (task-9 PR-4): every production call site reaches it only through
// persistWorktreeMergeReceipt, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged; a test passes
// its own Injector directly to reach a create/chmod/write/sync/close/rename
// failure branch deterministically.
func persistWorktreeMergeReceiptInjected(receipt WorktreeMergeReceipt, inj *filewrite.Injector) error {
	if receipt.ReceiptPath == "" {
		return fmt.Errorf("merge receipt path is required")
	}
	if err := os.MkdirAll(filepath.Dir(receipt.ReceiptPath), 0o700); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporary, err := filewrite.CreateTemp(filepath.Dir(receipt.ReceiptPath), ".merge-receipt-*.tmp", inj)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryPath, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Write(temporary, contents, temporaryPath, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Sync(temporary, temporaryPath, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Close(temporary, temporaryPath, inj); err != nil {
		return err
	}
	return filewrite.Rename(temporaryPath, receipt.ReceiptPath, inj)
}

// PeekWorktreeMergeReceipt resolves input (a candidate worktree or a receipt
// path/task, per resolveWorktreeMergeReceiptPath) and reads the receipt it
// names, read-only. It exists so a landing guard — such as cmd/wb's
// host-load admission check — can inspect a receipt's exact state before
// deciding whether the step it gates will re-run local CPU-heavy validation,
// without duplicating receipt-resolution logic outside this package.
func PeekWorktreeMergeReceipt(projectsRoot, input string) (WorktreeMergeReceipt, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(projectsRoot, input)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	return readWorktreeMergeReceipt(receiptPath)
}

func readWorktreeMergeReceipt(path string) (WorktreeMergeReceipt, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	var receipt WorktreeMergeReceipt
	if err := json.Unmarshal(contents, &receipt); err != nil {
		return WorktreeMergeReceipt{}, fmt.Errorf("decode merge receipt %s: %w", path, err)
	}
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.ReceiptPath != path {
		return WorktreeMergeReceipt{}, fmt.Errorf("merge receipt %s has invalid identity", path)
	}
	return receipt, nil
}

func failWorktreeMergeReceipt(receipt WorktreeMergeReceipt, status WorktreeMergeStatus, failure error) (WorktreeMergeReceipt, error) {
	return failWorktreeMergeReceiptWithSave(receipt, status, failure, persistWorktreeMergeReceipt)
}

func requireCleanMergeWorktree(ctx context.Context, path string) error {
	return requireCleanMergeWorktreeWithRunner(ctx, defaultRunner, path)
}

func recheckWorktreeMergeSources(ctx context.Context, sources []WorktreeMergeSource) error {
	return recheckWorktreeMergeSourcesWithRunner(ctx, defaultRunner, sources)
}

// mergeRevision resolves revision to a commit SHA in path. Unlike the Git
// port's RevParse (ports.go), this is `rev-parse --verify revision^{commit}`:
// --verify makes an ambiguous or unresolvable revision a plain error instead
// of printing usage, and ^{commit} requires the result to be a commit
// object -- neither of which RevParse's own argv reproduces, so this stays
// its own call through runCommand rather than a Git port method
// (spec/plans/coverage-to-100 task-17).
func mergeRevision(ctx context.Context, run runner.Runner, path, revision string) (string, error) {
	output, _, err := runCommand(ctx, run, 0, 0, path, "git", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func mergeTreeRevision(ctx context.Context, run runner.Runner, path, revision string) (string, error) {
	output, _, err := runCommand(ctx, run, 0, 0, path, "git", "rev-parse", "--verify", revision+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func worktreeMergeCandidateAbsorbed(ctx context.Context, path string, prior WorktreeMergeReceipt, remoteTarget string) (absorbed, graphContained bool, err error) {
	containsCandidate, err := isMergeAncestor(ctx, path, prior.Candidate.SHA, remoteTarget)
	if err != nil || containsCandidate {
		return containsCandidate, containsCandidate, err
	}
	if prior.PullRequest == "" || prior.PublishedCandidateSHA == "" || prior.PublishedCandidateSHA != prior.Candidate.SHA || prior.LandingSHA == "" {
		return false, false, nil
	}
	containsLanding, err := isMergeAncestor(ctx, path, prior.LandingSHA, remoteTarget)
	if err != nil || !containsLanding {
		return false, false, err
	}
	candidateTree, err := mergeTreeRevision(ctx, defaultRunner, path, prior.Candidate.SHA)
	if err != nil {
		return false, false, fmt.Errorf("resolve prior candidate tree %s: %w", prior.Candidate.SHA, err)
	}
	landingTree, err := mergeTreeRevision(ctx, defaultRunner, path, prior.LandingSHA)
	if err != nil {
		return false, false, fmt.Errorf("resolve prior landing tree %s: %w", prior.LandingSHA, err)
	}
	return candidateTree == landingTree, false, nil
}

func isMergeAncestor(ctx context.Context, path, ancestor, descendant string) (bool, error) {
	return isMergeAncestorWithRunner(ctx, defaultRunner, path, ancestor, descendant)
}

func validMergeBranch(ctx context.Context, path, branch string) bool {
	_, _, err := runCommand(ctx, defaultRunner, 0, 0, path, "git", "check-ref-format", "--branch", branch)
	return err == nil
}

func sameWorktreeMergeSources(left, right []WorktreeMergeSource) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Task != right[index].Task || left[index].Worktree != right[index].Worktree || left[index].Branch != right[index].Branch || left[index].SHA != right[index].SHA {
			return false
		}
	}
	return true
}
