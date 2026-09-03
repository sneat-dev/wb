package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	ReviewRefusalUnknownRef = "ref-does-not-resolve"
)

// ReviewSubject is what a review checkout was created to look at: a pull
// request, or — under the local model, where agent work never opens one — a
// local branch or commit.
type ReviewSubject struct {
	// Repository is always known: a review happens inside one.
	Repository string
	// PullRequest is empty for a local review.
	PullRequest string
	// LocalRef is the branch or commit under review, empty for a pull request.
	LocalRef string
	// Revision is the exact commit the checkout will sit at.
	Revision string
	// Title is what the review is about, for the brief and the report.
	Title string
	// URL is the pull request's, when there is one.
	URL string
	// BaseRef is the branch this work targets.
	BaseRef string
}

// Name is the stable identity of what is being reviewed. It is what the
// manifest records and what `wb worktree gc` prints.
func (subject ReviewSubject) Name() string {
	if subject.PullRequest != "" {
		return subject.Repository + "#" + subject.PullRequest
	}
	return subject.Repository + " " + subject.LocalRef
}

// WorktreeReviewOptions identifies what to review.
type WorktreeReviewOptions struct {
	Repository   string
	PullRequest  string
	ProjectsRoot string
	// From is a local branch or commit to review. Under the local model an
	// agent's work never opens a pull request, so a review that can only be
	// addressed by one cannot review most of the work there is. It is resolved
	// in the repository's canonical clone.
	From string
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
	// Base is the branch a locally reviewed ref targets. It is only consulted
	// for --from; a pull request carries its own.
	Base string
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
	LocalRef    string `json:"local_ref,omitempty"`
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
func ReviewTaskName(subject ReviewSubject) string {
	owner, name, err := splitRepository(subject.Repository)
	slug := sanitizeTaskSegment(subject.Repository)
	if err == nil {
		slug = sanitizeTaskSegment(owner) + "-" + sanitizeTaskSegment(name)
	}
	if subject.PullRequest != "" {
		return "review-" + slug + "-" + subject.PullRequest
	}
	// A local review is named for the ref, so two reviewers of the same branch
	// collide — which is the outcome to want — while reviews of different
	// branches do not.
	return "review-" + slug + "-" + sanitizeTaskSegment(subject.LocalRef)
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
func ReviewBranchName(subject ReviewSubject) string {
	return "review/" + strings.TrimPrefix(ReviewTaskName(subject), "review-")
}

// CreateReviewWorktree resolves what is under review and creates a tracked,
// claimed, TTL'd checkout at it.
func CreateReviewWorktree(ctx context.Context, options WorktreeReviewOptions) (WorktreeReviewResult, error) {
	subject, refusal, err := resolveReviewSubject(ctx, options)
	if err != nil {
		return WorktreeReviewResult{}, err
	}
	result := WorktreeReviewResult{
		SchemaVersion: 1, Verb: "worktree review",
		Repository: subject.Repository, URL: subject.URL,
		Title: subject.Title, BaseRef: subject.BaseRef, HeadSHA: subject.Revision,
		LocalRef: subject.LocalRef,
	}
	if number, convErr := strconv.Atoi(subject.PullRequest); convErr == nil {
		result.PullRequest = number
	}
	ttl := options.TTL
	if ttl <= 0 {
		ttl = DefaultReviewTTL
	}
	result.TTLSeconds = int64(ttl / time.Second)
	if refusal != nil {
		result.Outcome = "refused"
		result.RefusalCode = refusal.code
		result.Reason = refusal.reason
		result.SanctionedCommand = refusal.command
		return result, nil
	}

	task := strings.TrimSpace(options.Task)
	if task == "" {
		task = ReviewTaskName(subject)
	}
	result.Task = task

	workLog := options.WorkLog
	workLog.Purpose = worktrees.PurposeReview
	workLog.ReviewOf = subject.Name()
	workLog.TTL = ttl
	if strings.TrimSpace(workLog.OriginalPrompt) == "" {
		// A review checkout's originating instruction is the thing it reviews,
		// and WB already knows it. Requiring the operator to restate it would
		// make the tracked path harder than the untracked one, which is the
		// whole failure this verb exists to fix.
		brief := "Review " + subject.Name() + " (" + subject.Title + ") at " + subject.Revision + "\n"
		if subject.URL != "" {
			brief += subject.URL + "\n"
		}
		prepared, promptErr := workLog.WithOriginalPromptFromStdin([]byte(brief))
		if promptErr != nil {
			return result, promptErr
		}
		workLog = prepared
	}

	created, err := worktrees.Create(ctx, []string{subject.Repository}, worktrees.CreateOptions{
		ProjectsRoot:    options.ProjectsRoot,
		Operation:       task,
		SessionRequired: options.SessionRequired,
		Branch:          ReviewBranchName(subject),
		BranchChosen:    true,
		Base:            subject.BaseRef,
		StartRevision:   subject.Revision,
		WorkLog:         workLog,
	})
	if err != nil {
		return result, err
	}
	if len(created) != 1 {
		return result, fmt.Errorf("review checkout creation returned %d results", len(created))
	}
	result.Branch = created[0].Branch
	result.WorktreeDir = created[0].WorktreeDir

	result.Outcome = "success"
	return result, nil
}

// resolveReviewSubject works out what is under review, and refuses when the
// answer is not a thing anyone can review.
func resolveReviewSubject(ctx context.Context, options WorktreeReviewOptions) (ReviewSubject, *landRefusal, error) {
	repository := strings.TrimSpace(options.Repository)
	if repository == "" {
		return ReviewSubject{}, nil, fmt.Errorf("repository is required")
	}
	local := strings.TrimSpace(options.From)
	pullRequest := strings.TrimSpace(options.PullRequest)
	if local == "" && pullRequest == "" {
		return ReviewSubject{}, nil, fmt.Errorf("give either a pull request (owner/repository#number) or --from <branch-or-commit>")
	}
	if local != "" {
		return resolveLocalReviewSubject(ctx, options, repository, local)
	}
	return resolvePullRequestReviewSubject(ctx, options, repository, pullRequest)
}

// resolveLocalReviewSubject reads a branch or commit out of the canonical
// clone. Under the local model most reviewable work never leaves the machine,
// so this is the ordinary path rather than the exception.
func resolveLocalReviewSubject(ctx context.Context, options WorktreeReviewOptions, repository, ref string) (ReviewSubject, *landRefusal, error) {
	canonical := filepath.Join(options.ProjectsRoot, repository)
	if _, err := os.Stat(canonical); err != nil {
		return ReviewSubject{}, nil, fmt.Errorf("canonical clone for %s is not at %s: %w", repository, canonical, err)
	}
	revision, err := runGit(ctx, canonical, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || len(strings.TrimSpace(revision)) != 40 {
		return ReviewSubject{Repository: repository, LocalRef: ref}, &landRefusal{
			code: ReviewRefusalUnknownRef,
			reason: ref + " does not resolve to a commit in " + repository +
				"; a review checkout has to sit at something that exists",
			command: "git -C " + canonical + " fetch origin, then rerun",
		}, nil
	}
	revision = strings.TrimSpace(revision)
	subject := ReviewSubject{
		Repository: repository, LocalRef: ref, Revision: revision,
		BaseRef: strings.TrimSpace(options.Base),
	}
	if subject.BaseRef == "" {
		subject.BaseRef = "main"
	}
	if title, titleErr := runGit(ctx, canonical, "log", "-1", "--format=%s", revision); titleErr == nil {
		subject.Title = strings.TrimSpace(title)
	}
	return subject, nil, nil
}

// resolvePullRequestReviewSubject reads the pull request GitHub holds.
func resolvePullRequestReviewSubject(ctx context.Context, options WorktreeReviewOptions, repository, selector string) (ReviewSubject, *landRefusal, error) {
	number, err := PullRequestNumber(selector)
	if err != nil {
		return ReviewSubject{}, nil, err
	}
	view, err := ReadPullRequest(ctx, repository, number)
	if err != nil {
		return ReviewSubject{}, nil, err
	}
	subject := ReviewSubject{
		Repository: repository, PullRequest: number, Title: view.Title,
		URL: view.HTMLURL, BaseRef: view.Base.Ref,
	}
	revision := strings.TrimSpace(options.SHA)
	if revision == "" {
		if !strings.EqualFold(view.State, "open") {
			return subject, &landRefusal{
				code:    ReviewRefusalNotOpen,
				reason:  "pull request state is " + view.State + "; reviewing a head it no longer has is deliberate",
				command: "wb worktree review " + repository + "#" + number + " --sha " + shortMergeRevision(view.Head.SHA),
			}, nil
		}
		revision = view.Head.SHA
	}
	if revision == "" {
		return subject, &landRefusal{
			code:    ReviewRefusalNoHead,
			reason:  "GitHub reports no head commit for this pull request",
			command: "gh pr view " + number + " --repo " + repository + " --web",
		}, nil
	}
	subject.Revision = revision
	return subject, nil, nil
}
