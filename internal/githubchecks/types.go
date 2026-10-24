package githubchecks

import (
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

// RemoteCheck is the normalized GitHub check state observed before merge.
type RemoteCheck struct {
	Name              string `json:"name" yaml:"name"`
	Bucket            string `json:"bucket" yaml:"bucket"`
	WorkflowID        int64  `json:"workflow_id,omitempty" yaml:"workflow_id,omitempty"`
	WorkflowRunID     int64  `json:"workflow_run_id,omitempty" yaml:"workflow_run_id,omitempty"`
	WorkflowEvent     string `json:"workflow_event,omitempty" yaml:"workflow_event,omitempty"`
	PullRequestNumber int    `json:"pull_request_number,omitempty" yaml:"pull_request_number,omitempty"`
	PullRequestBase   string `json:"pull_request_base,omitempty" yaml:"pull_request_base,omitempty"`
	// Conclusion is the raw GitHub check-run/workflow-run conclusion (e.g.
	// "success", "skipped", "neutral", "failure"), kept alongside Bucket so a
	// strict deferral-satisfaction check (sneat-dev/wb#591 red-team finding
	// X2) can tell an actually-executed pass ("success") apart from a check
	// that never ran ("skipped" or "neutral") even though checkRunBucket
	// buckets both "success" and "neutral" the same, as an ordinary "pass"
	// (only "skipped" gets its own "skipping" bucket) for the overall
	// pass/fail loop. Empty for a commit-status-derived check, which has no
	// conclusion.
	Conclusion string `json:"conclusion,omitempty" yaml:"conclusion,omitempty"`
	Link       string `json:"link,omitempty" yaml:"link,omitempty"`
	AppID      int64  `json:"app_id,omitempty" yaml:"app_id,omitempty"`
	CheckRunID int64  `json:"check_run_id,omitempty" yaml:"check_run_id,omitempty"`
	// WorkflowName is the GitHub Actions workflow name that produced this
	// check (sneat-dev/wb#627): the workflow backing a check-run's suite, or
	// a synthetic workflow-run entry's own name. Empty for a third-party
	// check-run app or a commit-status-derived check, neither of which
	// belongs to an Actions workflow, so --workflow can never select them.
	WorkflowName string `json:"workflow_name,omitempty" yaml:"workflow_name,omitempty"`
	// WorkflowRunConclusion is the owning GitHub Actions run's own top-level
	// conclusion (e.g. "failure", "success", "cancelled"). A "skipping" bucket
	// whose run failed or was cancelled is treated as failed by a filtered
	// wait (sneat-dev/wb#627). Empty wherever there is no owning Actions run.
	WorkflowRunConclusion string `json:"workflow_run_conclusion,omitempty" yaml:"workflow_run_conclusion,omitempty"`
}

// CIFailureDetail is a bounded diagnostic for one failed GitHub Actions job.
// It deliberately carries an excerpt rather than the raw job log so a machine
// receipt remains compact and does not become an accidental log archive.
type CIFailureDetail struct {
	Check       string                `json:"check" yaml:"check"`
	RunURL      string                `json:"run_url,omitempty" yaml:"run_url,omitempty"`
	JobURL      string                `json:"job_url,omitempty" yaml:"job_url,omitempty"`
	Annotations []CIFailureAnnotation `json:"annotations,omitempty" yaml:"annotations,omitempty"`
	Excerpt     string                `json:"excerpt,omitempty" yaml:"excerpt,omitempty"`
	Reason      string                `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// CIFailureAnnotation is a compact, deduplicated GitHub check-run finding.
// It is deliberately narrower than GitHub's annotation payload so CI receipts
// remain useful to machines without becoming a copy of the Actions log.
type CIFailureAnnotation struct {
	Path      string `json:"path" yaml:"path"`
	StartLine int    `json:"start_line" yaml:"start_line"`
	EndLine   int    `json:"end_line,omitempty" yaml:"end_line,omitempty"`
	Message   string `json:"message" yaml:"message"`
}

// RequiredRemoteCheck is GitHub's target-policy expectation. IntegrationID
// is non-zero when a ruleset pins the context to one GitHub App; every receipt
// must then observe the matching exact-head check-run producer, not merely a
// same-named PR summary or legacy status from another actor.
type RequiredRemoteCheck struct {
	Name          string `json:"name" yaml:"name"`
	IntegrationID int64  `json:"integration_id,omitempty" yaml:"integration_id,omitempty"`
}

// CheckWaitFilter records that a check wait was scoped to a --workflow/--check
// subset of the exact head's checks and required checks (sneat-dev/wb#627),
// and how many each selected. Present in a JSON receipt only when a filter
// was requested.
type CheckWaitFilter struct {
	Workflows []string `json:"workflows,omitempty" yaml:"workflows,omitempty"`
	Checks    []string `json:"checks,omitempty" yaml:"checks,omitempty"`
	// MatchedChecks is how many observed checks the filter selected on the
	// observation this receipt reports.
	MatchedChecks int `json:"matched_checks" yaml:"matched_checks"`
	// RequiredChecks is how many of the target's required checks the filter
	// selected; completeness is evaluated only over this subset.
	RequiredChecks int `json:"matched_required_checks" yaml:"matched_required_checks"`
}

// PullRequestWaitOptions identifies exactly one direct-push or pull-request
// head whose observed checks are read by a bounded foreground invocation. A
// caller resumes a pending result with the same repository, target, PR (when
// supplied), and head; any later head is a distinct integration candidate.
type PullRequestWaitOptions struct {
	Repository  string
	PullRequest string
	Target      string
	Head        string
	// ExpectedActionChecks makes an opt-in CI wait require executed jobs from
	// one exact GitHub Actions workflow and event, even on an unprotected target.
	ExpectedActionChecks *ExpectedActionChecks
	// AllowTargetDescendant is only for post-landing target CI: the exact
	// landed Head must remain an ancestor of the observed target. Pre-landing
	// candidate and pull-request waits retain exact target-head freshness.
	AllowTargetDescendant bool
	// AllowUnfenced permits a validation-only PR check receipt when the target
	// branch has no server-enforced strict freshness fence. Merge callers leave
	// this false; it is an explicit opt-in for wait-only validation.
	AllowUnfenced bool
	// Workflow restricts the wait to check runs produced by these exact
	// GitHub Actions workflow names (sneat-dev/wb#627), repeatable. Empty
	// means every workflow. Workflow and Check combine with AND.
	Workflow []string
	// Check restricts the wait to check-run names and commit-status contexts
	// matching one of these exact names or simple "*" globs (sneat-dev/wb#627),
	// repeatable. Empty means every check.
	Check []string
	// MergeQueue is set only by a landing that hands the merge to GitHub's
	// merge queue instead of performing it. The queue, not this receipt, then
	// proves freshness: it tests the merge group (the current target plus the
	// candidate) and merges only that. So the candidate need not contain the
	// current target, a target advance does not reject the receipt, and the
	// queue counts as the server-enforced freshness authority. A caller that
	// sets this and then merges directly would land an unfenced head; the only
	// caller is the merge-queue landing, which never does.
	MergeQueue bool
	// WaiveChecks names checks whose failed/cancelled conclusion on the PR head
	// will not fail the wait loop, allowing required checks to finish.
	WaiveChecks       []string
	Slice             time.Duration
	CheckPollInterval time.Duration
	// StableRereadDelay overrides the shortened wait before the confirming
	// reread of a checks-bearing terminal observation. A zero value uses
	// DefaultStableRereadDelay, and the delay never exceeds
	// CheckPollInterval. The no-applicable-checks receipt and any reread
	// after fingerprint churn always wait the full CheckPollInterval.
	StableRereadDelay time.Duration
	// Progress receives completed GitHub observations. It is diagnostic only;
	// callers must use the returned result as the authoritative receipt.
	Progress          func(PullRequestWaitProgress)
	OperationProgress progress.Reporter
}

type ExpectedActionChecks struct {
	WorkflowID        int64
	Event             string
	PullRequestNumber int
	PullRequestBase   string
	Names             []string
}

// PullRequestWaitProgress is one completed observation inside a bounded wait.
type PullRequestWaitProgress struct {
	Observation int
	Result      PullRequestWaitResult
	NextPoll    time.Duration
}

// PullRequestWaitStatus is intentionally small so callers can branch on a
// machine result instead of parsing human GitHub CLI output.
type PullRequestWaitStatus string

const (
	PullRequestWaitPassed  PullRequestWaitStatus = "passed"
	PullRequestWaitPending PullRequestWaitStatus = "pending"
	PullRequestWaitFailed  PullRequestWaitStatus = "failed"
)

// PullRequestWaitResult is one terminating foreground observation slice.
// Pending means resume is required, not that the merger is finished.
type PullRequestWaitResult struct {
	Status                     PullRequestWaitStatus `json:"status" yaml:"status"`
	Repository                 string                `json:"repository" yaml:"repository"`
	PullRequest                string                `json:"pull_request,omitempty" yaml:"pull_request,omitempty"`
	Target                     string                `json:"target" yaml:"target"`
	Head                       string                `json:"head" yaml:"head"`
	ObservedHead               string                `json:"observed_head,omitempty" yaml:"observed_head,omitempty"`
	ObservedTargetHead         string                `json:"observed_target_head,omitempty" yaml:"observed_target_head,omitempty"`
	CandidateContainsTarget    bool                  `json:"candidate_contains_target,omitempty" yaml:"candidate_contains_target,omitempty"`
	TargetContainsHead         bool                  `json:"target_contains_head,omitempty" yaml:"target_contains_head,omitempty"`
	TargetFreshnessAuthority   string                `json:"target_freshness_authority,omitempty" yaml:"target_freshness_authority,omitempty"`
	Checks                     []RemoteCheck         `json:"checks,omitempty" yaml:"checks,omitempty"`
	FailureDetails             []CIFailureDetail     `json:"failure_details,omitempty" yaml:"failure_details,omitempty"`
	RequiredChecks             []RequiredRemoteCheck `json:"required_checks,omitempty" yaml:"required_checks,omitempty"`
	RequiredChecksAuthority    string                `json:"required_checks_authority,omitempty" yaml:"required_checks_authority,omitempty"`
	PolicyAuthorityUnavailable string                `json:"policy_authority_unavailable,omitempty" yaml:"policy_authority_unavailable,omitempty"`
	UnfencedValidation         bool                  `json:"unfenced_validation,omitempty" yaml:"unfenced_validation,omitempty"`
	StableObservations         int                   `json:"stable_observations" yaml:"stable_observations"`
	// Filter is present only when the wait was scoped by --workflow/--check
	// (sneat-dev/wb#627). An unfiltered receipt carries no filter field.
	Filter *CheckWaitFilter `json:"filter,omitempty" yaml:"filter,omitempty"`
	Reason string           `json:"reason,omitempty" yaml:"reason,omitempty"`
	// Evidence carries auxiliary receipt facts that are not part of the wait
	// outcome itself. "github_read_retries" mirrors the same key on
	// PullRequestLandResult: the count and last cause of in-process transient
	// GitHub read recoveries absorbed while producing this result.
	Evidence map[string]string `json:"evidence,omitempty" yaml:"evidence,omitempty"`
}
