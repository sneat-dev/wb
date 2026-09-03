package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// A review checkout is the largest source of permanent worktree debt on this
// fleet, and the cause is mechanical rather than cultural. A reviewer needs the
// pull request's head, so `gh pr checkout` or `git worktree add --detach` is the
// natural move — and from that moment WB has no manifest, no claim, no owner
// and no Work Log for the checkout, which means no WB verb can ever retire it.
// One night's sweep found ten of them, four to seventeen hours old, every pull
// request merged, ~1.2 GB, unreachable by anything.
//
// `wb worktree review` closes that at the source: it creates the checkout the
// same way every other WB worktree is created, so it arrives tracked, claimed,
// owned and TTL'd, and `wb worktree gc` retires it on the pull request's own
// state without anyone having to remember anything.

// DefaultReviewTTL is how long a review checkout is expected to be useful. It
// is reporting only: nothing removes a checkout because a clock elapsed.
const DefaultReviewTTL = 24 * time.Hour

// ReviewRefusal codes.
const (
	ReviewRefusalNotOpen    = "pull-request-not-open"
	ReviewRefusalNoHead     = "pull-request-has-no-head"
	ReviewRefusalForeignSHA = "revision-not-on-the-pull-request"
)

// WorktreeReviewOptions identifies the pull request to review.
type WorktreeReviewOptions struct {
	Repository   string
	PullRequest  string
	ProjectsRoot string
	// Task overrides the derived task name.
	Task string
	// SHA reviews an exact commit instead of the pull request's current head.
	// It is also the only way to review a pull request that is no longer open,
	// because then the head is a historical fact rather than a live one.
	SHA string
	// TTL overrides DefaultReviewTTL.
	TTL time.Duration
	// Identity and prompt are threaded to the Work Log exactly as
	// `wb worktree create` threads them.
	WorkLog worktrees.WorkLogOptions
	// SessionRequired mirrors CreateOptions.SessionRequired.
	SessionRequired bool
}

// WorktreeReviewResult is the created checkout.
type WorktreeReviewResult struct {
	SchemaVersion     int    `json:"v"`
	Verb              string `json:"verb"`
	Outcome           string `json:"outcome"`
	RefusalCode       string `json:"refusal_code,omitempty"`
	SanctionedCommand string `json:"sanctioned_command,omitempty"`
	Reason            string `json:"reason,omitempty"`

	Repository  string `json:"repository"`
	PullRequest int    `json:"pull_request"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Task        string `json:"task"`
	Branch      string `json:"branch,omitempty"`
	HeadSHA     string `json:"head_sha"`
	BaseRef     string `json:"base_ref"`
	WorktreeDir string `json:"worktree_dir,omitempty"`
	TTLSeconds  int64  `json:"ttl_seconds"`
}

// ReviewTaskName derives the task a review checkout lives under. It is
// deterministic so a second reviewer of the same pull request collides with the
// first — which is the correct outcome, and a far better one than two
// independent checkouts nobody can tell apart.
func ReviewTaskName(repository string, number int) string {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return fmt.Sprintf("review-%d", number)
	}
	return fmt.Sprintf("review-%s-%s-%d", sanitizeTaskSegment(owner), sanitizeTaskSegment(name), number)
}

func sanitizeTaskSegment(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == '-' || character == '_':
			builder.WriteByte('-')
		default:
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

// ReviewBranchName is the local branch a review checkout sits on. It is
// deliberately a branch rather than a detached HEAD: a detached checkout is
// exactly the shape WB cannot retire, and the whole point of this verb is to
// stop producing that shape.
func ReviewBranchName(repository string, number int) string {
	return "review/" + strings.TrimPrefix(ReviewTaskName(repository, number), "review-")
}

// CreateReviewWorktree fetches the pull request's head and creates a tracked,
// claimed, TTL'd checkout at it.
func CreateReviewWorktree(ctx context.Context, options WorktreeReviewOptions) (WorktreeReviewResult, error) {
	number, err := PullRequestNumber(options.PullRequest)
	if err != nil {
		return WorktreeReviewResult{}, err
	}
	if strings.TrimSpace(options.Repository) == "" {
		return WorktreeReviewResult{}, fmt.Errorf("repository is required (owner/repository#number)")
	}
	view, err := ReadPullRequest(ctx, options.Repository, number)
	if err != nil {
		return WorktreeReviewResult{}, err
	}
	result := WorktreeReviewResult{
		SchemaVersion: 1, Verb: "worktree review",
		Repository: options.Repository, PullRequest: view.Number,
		URL: view.HTMLURL, Title: view.Title, BaseRef: view.Base.Ref,
	}
	ttl := options.TTL
	if ttl <= 0 {
		ttl = DefaultReviewTTL
	}
	result.TTLSeconds = int64(ttl / time.Second)

	revision := strings.TrimSpace(options.SHA)
	if revision == "" {
		if !strings.EqualFold(view.State, "open") {
			// A closed pull request's head is a historical fact, and reviewing
			// one is a deliberate act rather than the default.
			result.Outcome = "refused"
			result.RefusalCode = ReviewRefusalNotOpen
			result.Reason = "pull request state is " + view.State + "; reviewing a head it no longer has is deliberate"
			result.SanctionedCommand = "wb worktree review " + options.Repository + "#" + number + " --sha " + shortMergeRevision(view.Head.SHA)
			return result, nil
		}
		revision = view.Head.SHA
	}
	if revision == "" {
		result.Outcome = "refused"
		result.RefusalCode = ReviewRefusalNoHead
		result.Reason = "GitHub reports no head commit for this pull request"
		result.SanctionedCommand = "gh pr view " + number + " --repo " + options.Repository + " --web"
		return result, nil
	}
	result.HeadSHA = revision

	task := strings.TrimSpace(options.Task)
	if task == "" {
		task = ReviewTaskName(options.Repository, view.Number)
	}
	result.Task = task

	workLog := options.WorkLog
	workLog.Purpose = worktrees.PurposeReview
	workLog.ReviewOf = options.Repository + "#" + number
	workLog.TTL = ttl
	if strings.TrimSpace(workLog.OriginalPrompt) == "" {
		// A review checkout's originating instruction is the pull request it
		// reviews, and WB already knows it. Requiring the operator to restate
		// it would be ceremony that makes the tracked path harder than the
		// untracked one, which is the whole failure this verb exists to fix.
		brief := "Review " + options.Repository + "#" + number + " (" + view.Title + ") at " + revision + "\n" +
			view.HTMLURL + "\n"
		prepared, promptErr := workLog.WithOriginalPromptFromStdin([]byte(brief))
		if promptErr != nil {
			return result, promptErr
		}
		workLog = prepared
	}

	created, err := worktrees.Create(ctx, []string{options.Repository}, worktrees.CreateOptions{
		ProjectsRoot:    options.ProjectsRoot,
		Operation:       task,
		SessionRequired: options.SessionRequired,
		Branch:          ReviewBranchName(options.Repository, view.Number),
		BranchChosen:    true,
		Base:            view.Base.Ref,
		StartRevision:   revision,
		WorkLog:         workLog,
	})
	if err != nil {
		return result, err
	}
	if len(created) != 1 {
		return result, fmt.Errorf("review checkout creation returned %d results", len(created))
	}
	result.Outcome = "success"
	result.Branch = created[0].Branch
	result.WorktreeDir = created[0].WorktreeDir
	return result, nil
}
