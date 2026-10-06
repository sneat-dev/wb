package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"

	"github.com/sneat-dev/wb/internal/prmeta"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// pinPullRequestViewPollDelay is the real poll interval pinPullRequestViewToHead
// waits between re-reads.
const pinPullRequestViewPollDelay = 200 * time.Millisecond

// pinPullRequestViewToHead re-reads a pull request until its own reported
// head SHA matches pushedHead, bounded rather than immediate: GitHub's own
// read-after-write for a pull request this call just adopted or created can
// briefly still report the head observed before the push that produced
// pushedHead. A view already at pushedHead is returned unchanged with no
// extra call.
//
// sleep is the retry-backoff seam: createPullRequestAutoMerge always passes
// time.Sleep; a test passes a recorder. It is a function parameter, not a
// package-level mutable var, so a test cannot leave shared package state
// mutated for another test running in parallel.
func pinPullRequestViewToHead(ctx context.Context, repository, number, pushedHead string, view githubchecks.PullRequestView, sleep func(time.Duration)) (githubchecks.PullRequestView, error) {
	if pushedHead == "" || view.Head.SHA == pushedHead {
		return view, nil
	}
	const attempts = 5
	for attempt := 1; attempt < attempts; attempt++ {
		sleep(pinPullRequestViewPollDelay)
		refreshed, err := githubchecks.ReadPullRequest(ctx, repository, number)
		if err != nil {
			return view, fmt.Errorf("re-read pull request %s#%s to confirm its pushed head: %w", repository, number, err)
		}
		view = refreshed
		if view.Head.SHA == pushedHead {
			return view, nil
		}
	}
	return view, fmt.Errorf("pull request %s#%s still reports head %s, not the pushed head %s",
		repository, number, view.Head.SHA, pushedHead)
}

// pullRequestBaseMismatchError reports that an already-open pull request was
// found for the branch, but against a different base than this invocation
// asked for. Adoption looks for the branch's open pull request against ANY
// base — GitHub allows exactly one open pull request per (repository, head
// branch) regardless of base, so a `--base`-scoped list can miss the one
// that already exists — but adopting a pull request onto the WRONG base
// would silently retarget it, so a mismatch refuses instead.
type pullRequestBaseMismatchError struct {
	url, wantBase, gotBase string
}

func (mismatch *pullRequestBaseMismatchError) Error() string {
	return fmt.Sprintf("pull request %s is already open against %s, not %s", mismatch.url, mismatch.gotBase, mismatch.wantBase)
}

// openOrAdoptPullRequest opens or adopts one branch's pull request, pinned to
// repository with `--repo` on every `gh pr list`/`gh pr create` call so the
// worktree's own cwd-inferred repository is never silently substituted.
// openOrAdoptPullRequest adopts a matching-base pull request or creates one.
// Draft creation records available worktree provenance in its body.
func openOrAdoptPullRequest(ctx context.Context, worktree, repository, branch, base, title, body string, draft bool, options Options, closesIssues []int) (url string, adopted bool, err error) {
	// Round 3, minor 6: the body field is read here too, only so an
	// adopted (already-open) pull request's --closes lines can be applied
	// to it below — the "body" this function otherwise takes as a
	// parameter is used solely by the create calls further down, and was
	// never previously applied to a pull request that already existed.
	existing, listErr := githubRead(ctx, worktree, "pr", "list", "--repo", repository, "--head", branch,
		"--state", "open", "--json", "url,baseRefName,body", "--jq", ".[0] | (.url + \"\\t\" + .baseRefName + \"\\t\" + (.body // \"\"))")
	if listErr == nil {
		if trimmed := strings.TrimSpace(existing); trimmed != "" {
			parts := strings.SplitN(trimmed, "\t", 3)
			if len(parts) >= 2 {
				url, gotBase := parts[0], parts[1]
				currentBody := ""
				if len(parts) == 3 {
					currentBody = parts[2]
				}
				if gotBase != base {
					return "", false, &pullRequestBaseMismatchError{url: url, wantBase: base, gotBase: gotBase}
				}
				if len(closesIssues) > 0 {
					if editErr := applyClosesToAdoptedPullRequest(ctx, worktree, repository, url, currentBody, closesIssues, options); editErr != nil {
						return "", false, editErr
					}
				}
				return url, true, nil
			}
		}
	}
	createBody := body
	if draft {
		if manifest, manifestErr := worktrees.ReadManifest(worktree); manifestErr == nil {
			createBody = prmeta.Append(createBody, prmeta.Provenance{Effort: manifest.EffortID})
		}
	}
	arguments := []string{"pr", "create", "--repo", repository, "--base", base, "--head", branch, "--title", title, "--body", createBody}
	if draft {
		arguments = append(arguments, "--draft")
	}
	created, _, createErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "gh", arguments...)
	if createErr != nil {
		return "", false, createErr
	}
	if createdURL := lastNonEmptyLine(created); createdURL != "" {
		return createdURL, false, nil
	}
	if draft {
		return "", false, fmt.Errorf("gh pr create --draft returned no pull request URL")
	}
	return "", false, fmt.Errorf("gh pr create returned no pull request URL")
}
