package orchestrate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Landing an agent pull request squashes by default, and the squash message
// aggregates every source commit. Keeping specific commits separate is an
// explicit, reasoned exception rather than a shape the tool falls into.
//
// The reason is that history on the default branch is read by people who were
// not in the room. One opaque squash hides what changed; a thicket of "fix
// typo" commits hides it just as well. The aggregate keeps the branch's own
// messages inside one commit, and `--keep-commits` promotes the few that are
// worth their own place in the log — each of which must build on its own,
// because a commit that does not build is not a place anyone can bisect to.

// LandRefusalKeepReasonMissing and friends are the refusals specific to a
// partially kept landing.
const (
	LandRefusalKeepReasonMissing = "keep-commits-without-reason"
	LandRefusalKeepUnknownCommit = "keep-commit-not-on-branch"
	LandRefusalKeepDoesNotBuild  = "kept-commit-does-not-build"
	LandRefusalKeepNeedsCheckout = "keep-commits-needs-local-checkout"
)

// LandedCommit pairs a source commit with the commit that carried it onto the
// base. GitHub's rebase merge always rewrites the commits, so after landing
// these pairs are the only way back to the originals.
type LandedCommit struct {
	SourceSHA string `json:"source_sha"`
	LandedSHA string `json:"landed_sha,omitempty"`
	Subject   string `json:"subject"`
	Kept      bool   `json:"kept"`
}

// keepPlan is the rewritten branch: kept commits in their original relative
// order, and one aggregated commit holding everything else, placed where the
// first aggregated commit was.
type keepPlan struct {
	// steps are the commits to build, in order. An aggregate step carries the
	// several source commits it absorbs.
	steps []keepStep
	kept  []string
}

type keepStep struct {
	aggregate bool
	sources   []SourceCommit
}

// planKeptCommits arranges the source commits into the sequence that will land.
func planKeptCommits(commits []SourceCommit, keep []string) (keepPlan, *landRefusal) {
	wanted := map[string]bool{}
	for _, raw := range keep {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		matched := ""
		for _, commit := range commits {
			if strings.HasPrefix(commit.SHA, value) {
				if matched != "" && matched != commit.SHA {
					return keepPlan{}, &landRefusal{
						code:   LandRefusalKeepUnknownCommit,
						reason: "commit prefix " + value + " matches more than one commit on this branch; give more characters",
					}
				}
				matched = commit.SHA
			}
		}
		if matched == "" {
			return keepPlan{}, &landRefusal{
				code: LandRefusalKeepUnknownCommit,
				reason: "commit " + value + " is not on this pull request's branch; " +
					"--keep-commits names commits of the branch being landed",
			}
		}
		wanted[matched] = true
	}
	// Walk the branch in order. Kept commits become their own steps as they
	// appear; everything else accumulates into one aggregate, whose position is
	// decided at the end.
	plan := keepPlan{}
	aggregated := make([]SourceCommit, 0, len(commits))
	keptBeforeLastAggregated := 0
	for _, commit := range commits {
		if wanted[commit.SHA] {
			plan.steps = append(plan.steps, keepStep{sources: []SourceCommit{commit}})
			plan.kept = append(plan.kept, commit.SHA)
			continue
		}
		aggregated = append(aggregated, commit)
		// Everything the aggregate absorbs has to be in place before a kept
		// commit that came after it can replay, so the aggregate belongs after
		// every kept commit that precedes its LAST member — never hoisted to
		// the front, which would reorder the branch under the reader's feet.
		keptBeforeLastAggregated = len(plan.steps)
	}
	if len(aggregated) > 0 {
		aggregate := keepStep{aggregate: true, sources: aggregated}
		steps := make([]keepStep, 0, len(plan.steps)+1)
		steps = append(steps, plan.steps[:keptBeforeLastAggregated]...)
		steps = append(steps, aggregate)
		steps = append(steps, plan.steps[keptBeforeLastAggregated:]...)
		plan.steps = steps
	}
	sort.Strings(plan.kept)
	return plan, nil
}

// rewriteBranchForKeptCommits rebuilds the pull request's branch as the planned
// sequence and pushes it under a lease. It works in a throwaway linked worktree
// so nothing it does can touch the canonical clone or the lane's own checkout.
func rewriteBranchForKeptCommits(
	ctx context.Context,
	canonical, repository, headRef, baseSHA string,
	plan keepPlan,
	view PullRequestView,
	commits []SourceCommit,
	approvedBy, reason string,
	buildCommand []string,
) ([]LandedCommit, string, *landRefusal, error) {
	scratch, err := os.MkdirTemp("", "wb-pr-land-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("create landing scratch directory: %w", err)
	}
	worktree := filepath.Join(scratch, "rewrite")
	defer func() {
		// A cancelled context would make this cleanup a no-op and leave a
		// registered worktree behind — the exact debris this whole feature
		// exists to stop producing — so the removal gets its own bounded
		// context that the cancellation cannot reach.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = runGit(cleanupCtx, canonical, "worktree", "remove", "--force", worktree)
		_ = os.RemoveAll(scratch)
	}()
	if _, err := runGit(ctx, canonical, "worktree", "add", "--detach", worktree, baseSHA); err != nil {
		return nil, "", nil, fmt.Errorf("prepare landing scratch worktree: %w", err)
	}

	landed := make([]LandedCommit, 0, len(commits))
	for _, step := range plan.steps {
		if step.aggregate {
			args := []string{"cherry-pick", "--no-commit"}
			for _, source := range step.sources {
				args = append(args, source.SHA)
			}
			if _, err := runGit(ctx, worktree, args...); err != nil {
				return nil, "", &landRefusal{
					code:   LandRefusalMergeRejected,
					reason: "the aggregated commits do not replay cleanly onto the base: " + err.Error(),
				}, nil
			}
			message := view.Title + "\n\n" + aggregatedCommitMessage(view, step.sources, approvedBy, reason)
			if _, err := runGit(ctx, worktree, "commit", "--no-verify", "-m", message); err != nil {
				return nil, "", nil, fmt.Errorf("write the aggregated commit: %w", err)
			}
			for _, source := range step.sources {
				landed = append(landed, LandedCommit{SourceSHA: source.SHA, Subject: source.Subject})
			}
			continue
		}
		source := step.sources[0]
		if _, err := runGit(ctx, worktree, "cherry-pick", source.SHA); err != nil {
			return nil, "", &landRefusal{
				code:   LandRefusalMergeRejected,
				reason: "kept commit " + shortMergeRevision(source.SHA) + " does not replay cleanly onto the base: " + err.Error(),
			}, nil
		}
		// A commit that does not build is not a place anyone can bisect to, so
		// promoting it to its own place in the log is worse than aggregating it.
		if refusal := buildAt(ctx, worktree, source, buildCommand); refusal != nil {
			return nil, "", refusal, nil
		}
		// The SHA this scratch worktree produced is not the SHA that will land:
		// GitHub's rebase merge replays every commit with new committer
		// metadata. Recording it here would put a commit id in the ledger that
		// exists nowhere afterwards, so the pairing is read back from the base
		// after the merge — see MapLandedCommits.
		landed = append(landed, LandedCommit{SourceSHA: source.SHA, Subject: source.Subject, Kept: true})
	}

	head, err := runGit(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", nil, err
	}
	if _, err := runGit(ctx, worktree, "push",
		"--force-with-lease=refs/heads/"+headRef+":"+view.Head.SHA,
		"origin", "HEAD:refs/heads/"+headRef); err != nil {
		return nil, "", &landRefusal{
			code: LandRefusalHeadMoved,
			reason: "the rewritten branch could not be published under a lease on " +
				shortMergeRevision(view.Head.SHA) + ": " + err.Error(),
			command: "wb pr land " + repository + "#" + fmt.Sprint(view.Number),
		}, nil
	}
	return landed, head, nil, nil
}

// buildAt runs the repository's own build at the current checkout.
func buildAt(ctx context.Context, worktree string, source SourceCommit, buildCommand []string) *landRefusal {
	command := buildCommand
	if len(command) == 0 {
		command = defaultBuildCommand(worktree)
	}
	if len(command) == 0 {
		// The guard exists because a commit that does not build is not a state
		// anyone can bisect to. Skipping it where WB cannot infer the build
		// would keep the promise only for Go repositories while appearing to
		// keep it everywhere, which is worse than not making it.
		return &landRefusal{
			code: LandRefusalKeepDoesNotBuild,
			reason: "WB cannot infer this repository's build, so it cannot prove kept commit " +
				shortMergeRevision(source.SHA) + " builds on its own",
			command: "wb pr land … --keep-commits … --reason \"…\" --build-command \"<the repository's build>\"",
		}
	}
	run := exec.CommandContext(ctx, command[0], command[1:]...)
	run.Dir = worktree
	run.Env = console.Env()
	if output, err := run.CombinedOutput(); err != nil {
		return &landRefusal{
			code: LandRefusalKeepDoesNotBuild,
			reason: "kept commit " + shortMergeRevision(source.SHA) + " (" + source.Subject + ") does not build: " +
				strings.TrimSpace(lastLines(string(output), 5)),
			command: "wb pr land --keep-commits <a smaller set that excludes " + shortMergeRevision(source.SHA) + "> --reason \"…\"",
		}
	}
	return nil
}

// defaultBuildCommand picks the repository's own build target. Only Go is
// recognised automatically; anything else must be named explicitly rather than
// guessed, because a guessed build that silently does nothing would turn the
// guard into decoration.
func defaultBuildCommand(worktree string) []string {
	if _, err := os.Stat(filepath.Join(worktree, "go.mod")); err == nil {
		return []string{"go", "build", "./..."}
	}
	return nil
}

func lastLines(output string, count int) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) <= count {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-count:], "\n")
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = console.Env()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// locateBranchCheckout finds the canonical clone and the WB task for a branch.
func locateBranchCheckout(ctx context.Context, projectsRoot, repository, headRef, base string) (canonical, task string, found bool, err error) {
	listed, listErr := worktrees.ListWithDiagnostics(ctx, worktrees.ListOptions{
		ProjectsRoot: projectsRoot, Base: base,
	})
	if listErr != nil {
		return "", "", false, listErr
	}
	for _, entry := range listed.Results {
		if entry.Repository == repository && entry.Branch == headRef {
			return entry.CanonicalDir, entry.Task, true, nil
		}
	}
	return "", "", false, nil
}

// landKeepingCommits rewrites the branch so the named commits land as their
// own and everything else lands as one aggregated commit.
func landKeepingCommits(
	ctx context.Context,
	options PullRequestLandOptions,
	view PullRequestView,
	commits []SourceCommit,
	number, approvedBy string,
) ([]LandedCommit, string, *landRefusal, error) {
	plan, refusal := planKeptCommits(commits, options.KeepCommits)
	if refusal != nil {
		if refusal.command == "" {
			refusal.command = "wb pr land " + options.Repository + "#" + number +
				" --keep-commits <a commit on this branch> --reason \"…\""
		}
		return nil, "", refusal, nil
	}
	canonical, _, found, err := locateBranchCheckout(ctx, options.ProjectsRoot, options.Repository, view.Head.Ref, view.Base.Ref)
	if err != nil {
		return nil, "", nil, err
	}
	if !found {
		// Rewriting a branch is local Git work, and WB will not do it against a
		// clone it cannot identify.
		return nil, "", &landRefusal{
			code: LandRefusalKeepNeedsCheckout,
			reason: "keeping commits separate rewrites the branch, which needs the repository's canonical clone; " +
				"WB found no worktree for " + options.Repository + " on " + view.Head.Ref,
			command: "wb pr land " + options.Repository + "#" + number,
		}, nil
	}
	if _, err := runGit(ctx, canonical, "fetch", "origin",
		view.Base.Ref, view.Head.Ref); err != nil {
		return nil, "", nil, fmt.Errorf("fetch the branch before rewriting it: %w", err)
	}
	baseSHA, err := runGit(ctx, canonical, "rev-parse", "refs/remotes/origin/"+view.Base.Ref)
	if err != nil {
		return nil, "", nil, err
	}
	return rewriteBranchForKeptCommits(ctx, canonical, options.Repository, view.Head.Ref, baseSHA,
		plan, view, commits, approvedBy, options.Reason, options.BuildCommand)
}

// MapLandedCommits pairs each source commit with the commit that carried it
// onto the base, by patch identity.
//
// GitHub's rebase merge replays every commit with new committer metadata and
// new SHAs, so the only durable link back to a source commit is the content it
// carried. `git patch-id --stable` is that link: it hashes the diff, ignoring
// whitespace-insensitive noise and every piece of metadata a replay rewrites.
//
// The aggregated sources all map to the one commit that absorbed them, which is
// found by elimination: it is the landed commit no kept source claims.
func MapLandedCommits(ctx context.Context, canonical, base, mergeBase string, landed []LandedCommit) ([]LandedCommit, error) {
	newCommits, err := commitsBetween(ctx, canonical, mergeBase, base)
	if err != nil {
		return landed, err
	}
	byPatch := map[string]string{}
	for _, commit := range newCommits {
		identity, identityErr := patchIdentity(ctx, canonical, commit)
		if identityErr != nil || identity == "" {
			continue
		}
		byPatch[identity] = commit
	}
	claimed := map[string]bool{}
	for index := range landed {
		if !landed[index].Kept {
			continue
		}
		identity, identityErr := patchIdentity(ctx, canonical, landed[index].SourceSHA)
		if identityErr != nil || identity == "" {
			continue
		}
		if match, found := byPatch[identity]; found {
			landed[index].LandedSHA = match
			claimed[match] = true
		}
	}
	// Whatever is left carried the aggregate.
	aggregate := ""
	for _, commit := range newCommits {
		if !claimed[commit] {
			aggregate = commit
			break
		}
	}
	if aggregate == "" {
		return landed, nil
	}
	for index := range landed {
		if !landed[index].Kept {
			landed[index].LandedSHA = aggregate
		}
	}
	return landed, nil
}

func commitsBetween(ctx context.Context, canonical, from, to string) ([]string, error) {
	output, err := runGit(ctx, canonical, "rev-list", "--reverse", from+".."+to)
	if err != nil {
		return nil, err
	}
	commits := make([]string, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			commits = append(commits, trimmed)
		}
	}
	return commits, nil
}

// patchIdentity is the content fingerprint of one commit.
func patchIdentity(ctx context.Context, canonical, commit string) (string, error) {
	command := exec.CommandContext(ctx, "sh", "-c",
		"git -C "+shellQuote(canonical)+" diff-tree -p --no-color "+shellQuote(commit)+" | git patch-id --stable")
	command.Env = console.Env()
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("patch identity of %s: %w", shortMergeRevision(commit), err)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
