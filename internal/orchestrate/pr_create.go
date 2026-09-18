package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/prmeta"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// wb pr create opens a pull request for one worktree without running the
// local suite `wb worktree merge`/`wb worktree land` pays for first (#591).
// It exists because that cost — 26+ minutes on a shared 4-core VM — pushed
// agents onto raw `gh pr create`, which loses worktree-pinned pushes (a wrong
// repository has been pushed to by hand before), WB-authored PR text,
// duplicate-PR adoption, and the task -> pull-request binding the daemon needs
// to resolve a pull request back to its owning session. CI is the gate here;
// nothing in this file runs a build, a test, or a lint pass.

// CreateRefusal codes are the machine-readable half of a refusal.
const (
	CreateRefusalCanonicalClone        = "canonical-clone"
	CreateRefusalDirtyWorktree         = "dirty-worktree"
	CreateRefusalNothingToPush         = "nothing-to-push"
	CreateRefusalUnapprovedPatch       = "unapproved-patch-set"
	CreateRefusalNothingStaged         = "nothing-staged"
	CreateRefusalSecretPath            = "secret-like-path"
	CreateRefusalLeftoverBeforeLanding = "leftover-before-landing"
	CreateRefusalInvalidPath           = "invalid-add-path"
)

// CreateOutcome is the envelope outcome, mapped onto WB's exit-code contract
// by ExitCode: success is 0, findings is 1, refused is 2.
type CreateOutcome string

const (
	CreateSuccess  CreateOutcome = "success"
	CreateFindings CreateOutcome = "findings"
	CreateRefused  CreateOutcome = "refused"
)

// PullRequestCreateOptions identifies the worktree to open a pull request for.
type PullRequestCreateOptions struct {
	// Worktree is a linked worktree path or a task name. Empty resolves to
	// the current directory.
	Worktree     string
	ProjectsRoot string
	Title        string
	// Body is literal pull-request body text, as `gh pr create --body` takes
	// it. Mutually exclusive with BodyFile; the caller validates that.
	Body     string
	BodyFile string
	Draft    bool
	// Base overrides the pull request's target branch. Empty uses the
	// worktree's own recorded base.
	Base string

	// AutoMerge arms GitHub auto-merge immediately after the pull request is
	// created or adopted, under the same authority `wb pr land` requires to
	// arm it: a mechanical diff, or a non-mechanical one with ApprovedBy, and
	// a target whose policy AllowUnfenced does not have to widen.
	AutoMerge     bool
	ApprovedBy    string
	AllowUnfenced bool
	MergeMethod   string

	// CommitStaged commits exactly the worktree's index. It refuses when
	// nothing is staged. CommitAll runs `git add -A` first (respecting
	// .gitignore, and refusing any path that looks like a secret), then
	// commits everything. They are mutually exclusive; the caller validates
	// that. Message is required with either one and is used verbatim.
	// Add commits exactly the named paths: `git add -- <paths>`, then
	// `git commit -m Message -- <paths>`, so anything else already staged is
	// not included. It is a third commit mode, mutually exclusive with
	// CommitStaged and CommitAll; the caller validates that.
	Add          []string
	CommitStaged bool
	CommitAll    bool
	Message      string

	// Land lands the pull request in-process through the existing
	// `LandPullRequest`, using LandOptions as a template: Repository and
	// PullRequest are filled in here once both are known. `--land` implies
	// arming, the way `wb pr land` itself does, so AutoMerge is not armed
	// separately when Land is set.
	Land        bool
	LandOptions *PullRequestLandOptions

	// LinkPreflight runs immediately before arming auto-merge or landing,
	// exactly as `wb pr land` runs `refuseLinkedRepositoryWorktrees` before
	// its own GitHub calls. It lives in cmd/wb, not here, so it is injected
	// rather than imported: internal/orchestrate must not depend on cmd/wb.
	LinkPreflight func(repository string) error

	Timeout time.Duration
	Retry   int

	// Events receives one structured record per invocation, whatever the
	// outcome. A nil appender discards.
	Events streams.EventAppender
	Stream string
}

// PullRequestCreateResult is the receipt, and the JSON envelope.
type PullRequestCreateResult struct {
	SchemaVersion     int           `json:"v"`
	Verb              string        `json:"verb"`
	Outcome           CreateOutcome `json:"outcome"`
	RefusalCode       string        `json:"refusal_code,omitempty"`
	SanctionedCommand string        `json:"sanctioned_command,omitempty"`
	Reason            string        `json:"reason,omitempty"`

	Repository  string `json:"repository,omitempty"`
	PullRequest int    `json:"pull_request,omitempty"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	HeadRef     string `json:"head_ref,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	BaseRef     string `json:"base_ref,omitempty"`
	// Adopted records that an already-open pull request for this branch was
	// reused rather than a new one created.
	Adopted bool `json:"adopted,omitempty"`

	Mechanical bool   `json:"mechanical"`
	ApprovedBy string `json:"approved_by,omitempty"`

	AutoMergeArmed  bool   `json:"auto_merge_armed,omitempty"`
	AutoMergeReason string `json:"auto_merge_not_armed_reason,omitempty"`

	// NextCommand is the exact follow-up invocation: `wb pr land` unless
	// AutoMerge was requested, in which case it is `wb wait pr`.
	NextCommand string `json:"next_command,omitempty"`

	// Task and ClaimID identify the durable Work Log claim the pull request
	// was bound to, when binding succeeded.
	Task    string `json:"task,omitempty"`
	ClaimID string `json:"claim_id,omitempty"`

	// CommittedPaths lists every path a requested --commit-staged/--commit-all
	// committed, in the order `git diff-tree` reports them.
	CommittedPaths []string `json:"committed_paths,omitempty"`

	// LandResult is populated when Land was requested: the full receipt of the
	// in-process `LandPullRequest` call this invocation's own Outcome mirrors.
	LandResult *PullRequestLandResult `json:"land_result,omitempty"`

	Evidence map[string]string `json:"evidence,omitempty"`
}

// ExitCode maps the outcome onto WB's exit contract.
func (result PullRequestCreateResult) ExitCode() int {
	switch result.Outcome {
	case CreateSuccess:
		return 0
	case CreateRefused:
		return 2
	default:
		return 1
	}
}

// createRefusal is the internal shape a guard fills in before it is merged
// into the result.
type createRefusal struct {
	code    string
	reason  string
	command string
}

func mergeCreateRefusal(result PullRequestCreateResult, refusal createRefusal) PullRequestCreateResult {
	result.Outcome = CreateRefused
	result.RefusalCode = refusal.code
	result.Reason = refusal.reason
	result.SanctionedCommand = refusal.command
	return result
}

// CreatePullRequest resolves one worktree, pushes its branch through the
// normal push path, and opens or adopts its pull request. It performs no
// local build, test, or lint: CI is the gate, and the caller that wants a
// verified merge runs `wb pr land` on the number this prints.
func CreatePullRequest(ctx context.Context, options PullRequestCreateOptions) (result PullRequestCreateResult, err error) {
	started := time.Now()
	defer func() {
		appendCreateEvent(options, result, started, err)
	}()
	return createPullRequest(ctx, options)
}

func createPullRequest(ctx context.Context, options PullRequestCreateOptions) (PullRequestCreateResult, error) {
	result := PullRequestCreateResult{SchemaVersion: 1, Verb: "pr create", Evidence: map[string]string{}}

	worktreeArg := strings.TrimSpace(options.Worktree)
	if worktreeArg == "" {
		worktreeArg = "."
	}
	worktree, err := resolvePullRequestCreateWorktree(ctx, options.ProjectsRoot, worktreeArg)
	if err != nil {
		return result, err
	}

	guard, err := worktrees.Guard(ctx, worktree, worktrees.GuardOptions{ProjectsRoot: options.ProjectsRoot})
	if err != nil {
		return result, err
	}
	if guard.Kind == "canonical" {
		return mergeCreateRefusal(result, createRefusal{
			code:    CreateRefusalCanonicalClone,
			reason:  fmt.Sprintf("%s is the canonical clone; a pull request is opened from a linked worktree, never from it", guard.CanonicalDir),
			command: "wb worktree create <task> <owner/repository>",
		}), nil
	}

	if options.CommitStaged || options.CommitAll || len(options.Add) > 0 {
		refusal, committed, commitErr := performPullRequestCreateCommit(ctx, worktree, options)
		if refusal != nil {
			return mergeCreateRefusal(result, *refusal), nil
		}
		if commitErr != nil {
			return result, commitErr
		}
		result.CommittedPaths = committed
	}

	dirtyPaths, err := pullRequestCreateDirtyPaths(ctx, worktree)
	if err != nil {
		return result, err
	}
	if len(dirtyPaths) > 0 {
		return mergeCreateRefusal(result, createRefusal{
			code:    CreateRefusalDirtyWorktree,
			reason:  "worktree has uncommitted changes: " + strings.Join(dirtyPaths, ", "),
			command: "commit or discard the listed paths, then retry",
		}), nil
	}

	manifest, manifestErr := worktrees.ReadManifest(worktree)
	repository := ""
	base := strings.TrimSpace(options.Base)
	if manifestErr == nil {
		repository = strings.TrimSpace(manifest.Repository)
		if base == "" {
			base = strings.TrimSpace(manifest.Base)
		}
	}
	if repository == "" {
		address, addressErr := repopath.FromLocalPath(options.ProjectsRoot, guard.CanonicalDir)
		if addressErr != nil {
			return result, fmt.Errorf("resolve repository for %s: %w", worktree, addressErr)
		}
		repository = address.Slug()
	}
	if base == "" {
		base = "main"
	}
	branch := guard.Branch
	result.Repository = repository
	result.BaseRef = base
	result.HeadRef = branch

	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "fetch", "--no-tags", "origin", base); err != nil {
		return result, fmt.Errorf("fetch base branch %s: %w", base, err)
	}
	subjectsRaw, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "log", "--format=%s", "origin/"+base+"..HEAD")
	if err != nil {
		return result, fmt.Errorf("read commits ahead of %s: %w", base, err)
	}
	subjects := splitNonEmptyLines(subjectsRaw)
	if len(subjects) == 0 {
		return mergeCreateRefusal(result, createRefusal{
			code:    CreateRefusalNothingToPush,
			reason:  fmt.Sprintf("branch %s carries no commit ahead of %s", branch, base),
			command: "commit the change, then retry",
		}), nil
	}

	headSHA, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.HeadSHA = strings.TrimSpace(headSHA)

	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return result, fmt.Errorf("push %s: %w", branch, err)
	}

	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = pullRequestCreateTitle(subjects)
	}
	result.Title = title

	body, err := pullRequestCreateBody(options, worktree, subjects)
	if err != nil {
		return result, err
	}

	url, adopted, err := openOrAdoptPullRequest(ctx, worktree, branch, base, title, body, options.Draft,
		Options{Timeout: options.Timeout, Retry: options.Retry})
	if err != nil {
		return result, err
	}
	result.URL = url
	result.Adopted = adopted
	if number, numberErr := PullRequestNumber(url); numberErr == nil {
		if parsed, convErr := strconv.Atoi(number); convErr == nil {
			result.PullRequest = parsed
		}
	}
	numberText := strconv.Itoa(result.PullRequest)

	if task, claimID, bindErr := worktrees.RecordClaimPullRequestBinding(options.ProjectsRoot, worktree, worktrees.ClaimPullRequestBinding{
		Repository: repository, PullRequest: result.PullRequest, URL: url,
	}); bindErr == nil {
		result.Task, result.ClaimID = task, claimID
	} else {
		// A caller that cannot record the binding still has an opened pull
		// request; that is reported, never swallowed, but it does not turn a
		// successful creation into a refusal.
		result.Evidence["claim_binding_error"] = bindErr.Error()
	}

	result.Outcome = CreateSuccess
	result.NextCommand = "wb pr land " + repository + "#" + numberText

	if options.Land {
		return createPullRequestLand(ctx, options, result, repository, numberText)
	}
	if options.AutoMerge {
		return createPullRequestAutoMerge(ctx, options, result, repository, numberText, headSHA)
	}
	return result, nil
}

// createPullRequestLand lands the just-created or -adopted pull request
// in-process, through the existing, unmodified `LandPullRequest`. `--land`
// implies arming the way `wb pr land` itself does, so this never also runs
// `createPullRequestAutoMerge`: arming twice would be redundant at best and a
// race against itself at worst.
func createPullRequestLand(ctx context.Context, options PullRequestCreateOptions, result PullRequestCreateResult, repository, number string) (PullRequestCreateResult, error) {
	if options.LinkPreflight != nil {
		if err := options.LinkPreflight(repository); err != nil {
			return result, err
		}
	}
	landOptions := PullRequestLandOptions{}
	if options.LandOptions != nil {
		landOptions = *options.LandOptions
	}
	landOptions.Repository, landOptions.PullRequest = repository, number
	landResult, landErr := LandPullRequest(ctx, landOptions)
	result.LandResult = &landResult
	if landErr != nil {
		return result, landErr
	}
	switch landResult.Outcome {
	case LandSuccess:
		result.Outcome = CreateSuccess
	case LandRefused:
		result.Outcome = CreateRefused
		result.RefusalCode = landResult.RefusalCode
	default:
		result.Outcome = CreateFindings
	}
	result.Reason = landResult.Reason
	result.Mechanical = landResult.Mechanical
	result.ApprovedBy = landResult.ApprovedBy
	result.NextCommand = ""
	return result, nil
}

// createPullRequestAutoMerge arms GitHub auto-merge for a just-created or
// -adopted pull request, under the same authority `wb pr land` requires: a
// mechanical diff needs no approval, a non-mechanical one is refused without
// --approved-by, and arming is skipped (with the pull request left open) on
// whatever guard `autoMergeBypassesAGuard` names.
func createPullRequestAutoMerge(ctx context.Context, options PullRequestCreateOptions, result PullRequestCreateResult, repository, number, head string) (PullRequestCreateResult, error) {
	if files, filesErr := pullRequestChangedFiles(ctx, repository, number); filesErr == nil {
		verdict := ClassifyMechanical(files)
		result.Mechanical = verdict.Mechanical
		if !verdict.Mechanical {
			result.Evidence["not_mechanical_because"] = verdict.Summary()
		}
	} else {
		result.Evidence["mechanical_classification_error"] = filesErr.Error()
	}
	result.ApprovedBy = strings.TrimSpace(options.ApprovedBy)
	if !result.Mechanical && result.ApprovedBy == "" {
		return mergeCreateRefusal(result, createRefusal{
			code:    CreateRefusalUnapprovedPatch,
			reason:  fmt.Sprintf("pull request %s#%s exists but is not a mechanical change, so --auto-merge needs a recorded review", repository, number),
			command: "wb pr create --auto-merge --approved-by <review-file-or-comment-url>",
		}), nil
	}
	if options.LinkPreflight != nil {
		if err := options.LinkPreflight(repository); err != nil {
			return result, err
		}
	}
	landOptions := PullRequestLandOptions{Repository: repository, AllowUnfenced: options.AllowUnfenced}
	if reason := autoMergeBypassesAGuard(ctx, landOptions, result.BaseRef); reason != "" {
		result.AutoMergeReason = reason
		result.Outcome = CreateFindings
		result.Reason = "not armed: " + reason
		return result, nil
	}
	view, viewErr := ReadPullRequest(ctx, repository, number)
	if viewErr != nil {
		return result, fmt.Errorf("read pull request %s#%s: %w", repository, number, viewErr)
	}
	commits, commitsErr := pullRequestCommits(ctx, repository, number)
	if commitsErr != nil {
		return result, fmt.Errorf("read commits of %s#%s: %w", repository, number, commitsErr)
	}
	subject := fmt.Sprintf("%s (#%s)", view.Title, number)
	body := aggregatedCommitMessage(view, commits, result.ApprovedBy, "")
	method := strings.TrimSpace(options.MergeMethod)
	if method == "" {
		method = "merge"
	}
	if reason := enablePullRequestAutoMerge(ctx, repository, number, method, head, subject, body); reason != "" {
		return result, fmt.Errorf("arm auto-merge for %s#%s: %s", repository, number, reason)
	}
	result.AutoMergeArmed = true
	result.NextCommand = "wb wait pr " + repository + "#" + number + " --until closed"
	return result, nil
}

// openOrAdoptPullRequest opens or adopts one branch's pull request. It is a
// thin extension of the shared, idempotent `openPullRequest` (engine.go):
// that primitive is reused verbatim for the non-draft path, which is the
// common case. Draft creation is a separate branch because `openPullRequest`
// deliberately never passes `--draft` — its only caller before this one is
// `wb worktree merge`'s non-draft candidate publication — and changing its
// signature would also have to change that call site, which belongs to a
// change in flight elsewhere. The adoption check therefore runs once here so
// both branches agree on it, and `openPullRequest` repeats its own equivalent
// check before ever creating anything, so no path can create twice.
func openOrAdoptPullRequest(ctx context.Context, worktree, branch, base, title, body string, draft bool, options Options) (url string, adopted bool, err error) {
	existing, listErr := githubRead(ctx, worktree, "pr", "list", "--head", branch, "--base", base,
		"--state", "open", "--json", "url", "--jq", ".[0].url")
	if listErr == nil {
		if trimmed := strings.TrimSpace(existing); trimmed != "" {
			return trimmed, true, nil
		}
	}
	if !draft {
		createdURL, createErr := openPullRequest(ctx, worktree, branch, base, title, body, options)
		return createdURL, false, createErr
	}
	draftBody := body
	if manifest, manifestErr := worktrees.ReadManifest(worktree); manifestErr == nil {
		draftBody = prmeta.Append(draftBody, prmeta.Provenance{Effort: manifest.EffortID})
	}
	created, _, createErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "gh", "pr", "create",
		"--base", base, "--head", branch, "--title", title, "--body", draftBody, "--draft")
	if createErr != nil {
		return "", false, createErr
	}
	if createdURL := lastNonEmptyLine(created); createdURL != "" {
		return createdURL, false, nil
	}
	return "", false, fmt.Errorf("gh pr create --draft returned no pull request URL")
}

// resolvePullRequestCreateWorktree resolves the CLI's `<worktree|task>`
// argument. An existing directory is used as-is; anything else is looked up
// as a task name against the fleet's worktree inventory. Resolution never
// runs a network fetch: the guard and the base-branch fetch that follow are
// where a caller pays that cost, once, for the worktree it actually meant.
func resolvePullRequestCreateWorktree(ctx context.Context, projectsRoot, argument string) (string, error) {
	if info, statErr := os.Stat(argument); statErr == nil && info.IsDir() {
		return argument, nil
	}
	entries, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot, Task: argument})
	if err != nil {
		return "", fmt.Errorf("resolve task %q: %w", argument, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no worktree or task %q found", argument)
	}
	if len(entries) > 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Repository+" "+entry.WorktreeDir)
		}
		return "", fmt.Errorf("task %q has more than one worktree (%s); pass a path instead", argument, strings.Join(names, "; "))
	}
	return entries[0].WorktreeDir, nil
}

// pullRequestCreateDirtyPaths lists every uncommitted path in worktree, or
// nil for a clean one.
func pullRequestCreateDirtyPaths(ctx context.Context, worktree string) ([]string, error) {
	output, _, err := runCommand(ctx, 0, 0, worktree, "git", "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("read worktree status: %w", err)
	}
	return splitNonEmptyLines(output), nil
}

func splitNonEmptyLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// wipSubjectPattern recognizes a work-in-progress commit subject in any of
// its ordinary spellings: "wip", "wip:", "wip(scope):". GitHub would
// otherwise happily title a pull request with exactly this, and a caller
// cannot fix that afterward without rewriting a protected branch's history.
var wipSubjectPattern = regexp.MustCompile(`(?i)^wip\b`)

func isWipSubject(subject string) bool {
	return wipSubjectPattern.MatchString(strings.TrimSpace(subject))
}

// pullRequestCreateTitle derives a pull request title from the branch's own
// commit subjects: a single commit contributes its subject verbatim, and
// several commits contribute the most recent one that reads like a real
// change. A "wip"-shaped subject never wins either way, because it is a
// placeholder, not a description.
func pullRequestCreateTitle(subjects []string) string {
	nonEmpty := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if trimmed := strings.TrimSpace(subject); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) == 0 {
		return "Open pull request"
	}
	if len(nonEmpty) == 1 {
		if !isWipSubject(nonEmpty[0]) {
			return nonEmpty[0]
		}
		return "apply 1 commit"
	}
	usable := make([]string, 0, len(nonEmpty))
	for _, subject := range nonEmpty {
		if !isWipSubject(subject) {
			usable = append(usable, subject)
		}
	}
	if len(usable) == 0 {
		return fmt.Sprintf("apply %d commits", len(nonEmpty))
	}
	if len(usable) == 1 {
		return usable[0]
	}
	// git log lists newest first; the oldest usable subject normally states
	// the branch's own purpose, and later commits are review/CI repairs.
	base := usable[len(usable)-1]
	related := len(usable) - 1
	word := "changes"
	if related == 1 {
		word = "change"
	}
	return fmt.Sprintf("%s and %d related %s", base, related, word)
}

// pullRequestCreateBody derives a pull request body from options and the
// branch's own commits, in the order the contract fixes: literal --body wins,
// then --body-file, then the commit-derived default. --title alone never
// substitutes for a body: overriding what the pull request is called says
// nothing about what it contains.
func pullRequestCreateBody(options PullRequestCreateOptions, worktree string, subjects []string) (string, error) {
	if body := strings.TrimSpace(options.Body); body != "" {
		return options.Body, nil
	}
	if path := strings.TrimSpace(options.BodyFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --body-file %s: %w", path, err)
		}
		return string(contents), nil
	}
	if len(subjects) == 1 {
		commitBody, _, err := runCommand(context.Background(), options.Timeout, options.Retry, worktree, "git", "log", "-1", "--format=%b")
		if err == nil {
			if trimmed := strings.TrimSpace(commitBody); trimmed != "" {
				return trimmed, nil
			}
		}
		return "", nil
	}
	var body strings.Builder
	body.WriteString("Mechanically prepared by `wb pr create` from the branch's own commits.\n\nCommits:\n\n")
	for index := len(subjects) - 1; index >= 0; index-- {
		fmt.Fprintf(&body, "- %s\n", subjects[index])
	}
	return body.String(), nil
}

// performPullRequestCreateCommit commits the worktree's own change before
// the usual dirty-worktree refusal ever sees it, so a single command can
// carry an agent from an edited working tree to an open pull request. It
// never bypasses hooks: the commit it issues is a plain `git commit`, so a
// failing hook stops it before anything is pushed.
func performPullRequestCreateCommit(ctx context.Context, worktree string, options PullRequestCreateOptions) (refusal *createRefusal, committed []string, err error) {
	if strings.TrimSpace(options.Message) == "" {
		return nil, nil, fmt.Errorf("--commit-staged/--commit-all/--add require -m/--message")
	}
	if len(options.Add) > 0 {
		paths, pathErr := resolveAddPaths(ctx, worktree, options.Add)
		if pathErr != nil {
			return &createRefusal{
				code:    CreateRefusalInvalidPath,
				reason:  pathErr.Error(),
				command: "pass an --add path inside the worktree that names an existing path, or a tracked path's deletion",
			}, nil, nil
		}
		var secrets []string
		for _, path := range paths {
			if looksLikeSecretPath(path) || path == ".worktree.md" {
				secrets = append(secrets, path)
			}
		}
		if len(secrets) > 0 {
			return &createRefusal{
				code:    CreateRefusalSecretPath,
				reason:  "refusing to stage what looks like a secret: " + strings.Join(secrets, ", "),
				command: "remove the listed paths from --add",
			}, nil, nil
		}
		if options.Land {
			status, statusErr := pullRequestCreateDirtyPaths(ctx, worktree)
			if statusErr != nil {
				return nil, nil, statusErr
			}
			if leftover := leftoverBeyondAddedPaths(status, paths); len(leftover) > 0 {
				return &createRefusal{
					code: CreateRefusalLeftoverBeforeLanding,
					reason: "changes outside --add would remain, and --land retires the worktree: " +
						strings.Join(leftover, ", "),
					command: "name every remaining path in --add, or drop --land and pass --keep",
				}, nil, nil
			}
		}
		addArgs := append([]string{"add", "--"}, paths...)
		if _, _, addErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", addArgs...); addErr != nil {
			return nil, nil, fmt.Errorf("git add -- %s: %w", strings.Join(paths, " "), addErr)
		}
		commitArgs := append([]string{"commit", "-m", options.Message, "--"}, paths...)
		if _, _, commitErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", commitArgs...); commitErr != nil {
			return nil, nil, fmt.Errorf("git commit: %w", commitErr)
		}
		committedRaw, _, treeErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
		if treeErr != nil {
			return nil, nil, fmt.Errorf("read committed paths: %w", treeErr)
		}
		return nil, splitNonEmptyLines(committedRaw), nil
	}
	if options.CommitStaged {
		staged, _, stagedErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "diff", "--cached", "--name-only")
		if stagedErr != nil {
			return nil, nil, fmt.Errorf("read staged changes: %w", stagedErr)
		}
		if len(splitNonEmptyLines(staged)) == 0 {
			return &createRefusal{
				code:    CreateRefusalNothingStaged,
				reason:  "nothing is staged; --commit-staged commits exactly the index",
				command: "git add <paths>, then retry, or pass --commit-all",
			}, nil, nil
		}
		if options.Land {
			status, statusErr := pullRequestCreateDirtyPaths(ctx, worktree)
			if statusErr != nil {
				return nil, nil, statusErr
			}
			if leftover := leftoverAfterStagedCommit(status); len(leftover) > 0 {
				return &createRefusal{
					code: CreateRefusalLeftoverBeforeLanding,
					reason: "unstaged or untracked changes would remain after --commit-staged, and --land retires the worktree: " +
						strings.Join(leftover, ", "),
					command: "pass --commit-all instead, or drop --land and pass --keep",
				}, nil, nil
			}
		}
	}
	if options.CommitAll {
		status, statusErr := pullRequestCreateDirtyPaths(ctx, worktree)
		if statusErr != nil {
			return nil, nil, statusErr
		}
		var secrets []string
		for _, line := range status {
			if path := porcelainPath(line); looksLikeSecretPath(path) {
				secrets = append(secrets, path)
			}
		}
		if len(secrets) > 0 {
			return &createRefusal{
				code:    CreateRefusalSecretPath,
				reason:  "refusing to stage what looks like a secret: " + strings.Join(secrets, ", "),
				command: "remove the listed paths from the change, or stage the safe paths explicitly and use --commit-staged",
			}, nil, nil
		}
		if _, _, addErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "add", "-A"); addErr != nil {
			return nil, nil, fmt.Errorf("git add -A: %w", addErr)
		}
		// .worktree.md is untracked and git-ignored on purpose (see CLAUDE.md),
		// so `git add -A` never stages it in the first place. This unstages it
		// defensively anyway, and is a no-op — never an error — when it was
		// never staged to begin with.
		_, _, _ = runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "reset", "-q", "--", ".worktree.md")
	}
	if _, _, commitErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "commit", "-m", options.Message); commitErr != nil {
		return nil, nil, fmt.Errorf("git commit: %w", commitErr)
	}
	committedRaw, _, treeErr := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if treeErr != nil {
		return nil, nil, fmt.Errorf("read committed paths: %w", treeErr)
	}
	return nil, splitNonEmptyLines(committedRaw), nil
}

// porcelainPath extracts the path from one `git status --porcelain` line,
// following a rename's " -> " to the new path.
func porcelainPath(line string) string {
	trimmed := strings.TrimRight(line, "\r")
	path := trimmed
	if len(trimmed) > 3 {
		path = strings.TrimSpace(trimmed[3:])
	}
	if arrow := strings.Index(path, " -> "); arrow >= 0 {
		path = path[arrow+len(" -> "):]
	}
	return path
}

// leftoverAfterStagedCommit names every porcelain line that a plain commit of
// the index would still leave dirty: an unstaged modification (worktree
// status column is not blank) or an untracked file ("??").
func leftoverAfterStagedCommit(lines []string) []string {
	var leftover []string
	for _, line := range lines {
		if len(line) >= 2 && line[1] != ' ' {
			leftover = append(leftover, porcelainPath(line))
		}
	}
	return leftover
}

// resolveAddPaths validates --add's own paths against worktree: each must
// resolve inside it (no ".." escape, and no absolute path outside it), and
// must match something real — an existing path, or a tracked path this
// worktree's own `git status` still reports, which is how a deleted tracked
// file is named without existing on disk any more. It returns each path
// relative to worktree, using forward slashes, in the order named.
func resolveAddPaths(ctx context.Context, worktree string, raw []string) ([]string, error) {
	absWorktree, err := filepath.Abs(worktree)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree %s: %w", worktree, err)
	}
	var invalid []string
	var missing []string
	resolved := make([]string, 0, len(raw))
	for _, path := range raw {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		var absPath string
		if filepath.IsAbs(trimmed) {
			absPath = filepath.Clean(trimmed)
		} else {
			absPath = filepath.Clean(filepath.Join(absWorktree, trimmed))
		}
		rel, relErr := filepath.Rel(absWorktree, absPath)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			invalid = append(invalid, path)
			continue
		}
		rel = filepath.ToSlash(rel)
		if _, statErr := os.Stat(absPath); statErr != nil {
			statusOutput, _, gitErr := runCommand(ctx, 0, 0, worktree, "git", "status", "--porcelain", "--", rel)
			if gitErr != nil || strings.TrimSpace(statusOutput) == "" {
				missing = append(missing, path)
				continue
			}
		}
		resolved = append(resolved, rel)
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("--add paths must resolve inside the worktree: %s", strings.Join(invalid, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("--add names paths that match nothing: %s", strings.Join(missing, ", "))
	}
	return resolved, nil
}

// leftoverBeyondAddedPaths names every porcelain-status path that is not one
// of --add's own paths: with --land, any such change — staged, unstaged, or
// untracked — would be left behind when the worktree is retired.
func leftoverBeyondAddedPaths(statusLines []string, paths []string) []string {
	added := make(map[string]bool, len(paths))
	for _, path := range paths {
		added[path] = true
	}
	var leftover []string
	for _, line := range statusLines {
		if path := porcelainPath(line); !added[path] {
			leftover = append(leftover, path)
		}
	}
	return leftover
}

// looksLikeSecretPath is a defensive filename heuristic, not a content scan:
// it exists so `--commit-all`'s blanket `git add -A` cannot silently stage a
// credential a reviewer would have caught by eye.
func looksLikeSecretPath(path string) bool {
	base := filepath.Base(path)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") {
		return true
	}
	return strings.HasPrefix(base, "id_rsa")
}

func appendCreateEvent(options PullRequestCreateOptions, result PullRequestCreateResult, started time.Time, createErr error) {
	if options.Events == nil {
		return
	}
	outcome := string(result.Outcome)
	if createErr != nil || outcome == "" {
		outcome = string(CreateFindings)
	}
	evidence := map[string]string{
		"mechanical": strconv.FormatBool(result.Mechanical),
		"adopted":    strconv.FormatBool(result.Adopted),
		"auto_merge": strconv.FormatBool(result.AutoMergeArmed),
	}
	if result.Task != "" {
		evidence["task"] = result.Task
	}
	if result.ApprovedBy != "" {
		evidence["approved_by"] = result.ApprovedBy
	}
	detail := result.Reason
	if createErr != nil {
		detail = createErr.Error()
	}
	_ = options.Events.Append(streams.Event{
		Stream:      options.Stream,
		Verb:        "pr create",
		Repository:  result.Repository,
		Outcome:     outcome,
		RefusalCode: result.RefusalCode,
		Detail:      detail,
		DurationMS:  time.Since(started).Milliseconds(),
		Evidence:    evidence,
	})
}
