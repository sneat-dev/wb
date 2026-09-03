package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// This file holds the GitHub reads the land and merge verbs used to make
// through `gh`'s own convenience commands, re-expressed as REST calls.
//
// The reason is measured rather than stylistic. The `gh` installed on this
// fleet is 2.45, which has neither `gh api --slurp` nor `gh pr checks --json`.
// Both were on the merge verb's critical path, so its checks stage failed on
// every run, operators fell back to raw `gh pr merge`, and the opt-in
// `--cleanup` that should have retired the worktree never ran. That is the
// whole causal chain behind 60 abandoned checkouts, and it started with a verb
// that depended on a newer client than the one installed.
//
// Everything here therefore uses only `gh api` with an endpoint — the one
// surface that has been stable across every `gh` this fleet has seen — and
// follows GitHub's own `link` header for pagination.

// PullRequestView is the pull-request state the land and merge verbs branch on.
// It is deliberately small: every field here is one a verb actually reads.
type PullRequestView struct {
	Number         int        `json:"number"`
	State          string     `json:"state"`
	Draft          bool       `json:"draft"`
	Locked         bool       `json:"locked"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	HTMLURL        string     `json:"html_url"`
	Merged         bool       `json:"merged"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	// Mergeable is nil while GitHub is still computing the merge state. A nil
	// is not a "no": it is "ask again", and a verb must not read it as either
	// mergeable or conflicted.
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

// ReadPullRequest reads one pull request. It replaces `gh pr view --json`,
// which the installed client supports but which would still be a second
// dialect for the same fact.
func ReadPullRequest(ctx context.Context, repository, selector string) (PullRequestView, error) {
	number, err := PullRequestNumber(selector)
	if err != nil {
		return PullRequestView{}, err
	}
	if strings.TrimSpace(repository) == "" {
		return PullRequestView{}, fmt.Errorf("repository is required to read pull request %s", number)
	}
	body, err := githubGet(ctx, "", repository, "", "", "repos/"+repository+"/pulls/"+url.PathEscape(number))
	if err != nil {
		return PullRequestView{}, fmt.Errorf("read pull request %s#%s: %w", repository, number, err)
	}
	var view PullRequestView
	if err := json.Unmarshal(body, &view); err != nil {
		return PullRequestView{}, fmt.Errorf("decode pull request %s#%s: %w", repository, number, err)
	}
	if view.Number == 0 {
		return PullRequestView{}, fmt.Errorf("pull request %s#%s returned no identity", repository, number)
	}
	return view, nil
}

// activeBranchRules reads every active rule for one branch, following GitHub's
// link header. It returns one slice per page so callers keep the page-shaped
// reading `--slurp` used to give them, with none of its version dependency.
func activeBranchRules(ctx context.Context, repository, target string) ([][]githubActiveBranchRule, error) {
	endpoint := "repos/" + repository + "/rules/branches/" + url.PathEscape(target) + "?per_page=100"
	responses, err := githubobserver.GetPages(ctx, githubobserver.GetRequest{
		Repository: repository,
		Target:     target,
		Endpoint:   endpoint,
	}, 0)
	if err != nil {
		return nil, err
	}
	pages := make([][]githubActiveBranchRule, 0, len(responses))
	for _, response := range responses {
		var rules []githubActiveBranchRule
		if err := json.Unmarshal(response.Body, &rules); err != nil {
			return nil, fmt.Errorf("decode active branch rules for %s: %w", target, err)
		}
		pages = append(pages, rules)
	}
	if len(pages) == 0 {
		// An unruled branch answers with an empty array, not with nothing; a
		// caller that cannot tell those apart would treat "no rules" as "the
		// read failed" and refuse a permitted route forever.
		pages = append(pages, nil)
	}
	return pages, nil
}

// PullRequestNumber accepts every spelling a caller already has in hand — a
// bare number, "#12", "owner/repo#12", or the pull request's own URL — and
// returns the number the API is addressed by. Callers hold whichever form their
// own source gave them, and making each one normalize it separately is how a
// URL reaches an endpoint path and produces a 404 that reads like a missing
// pull request.
func PullRequestNumber(selector string) (string, error) {
	value := strings.TrimSpace(selector)
	if value == "" {
		return "", fmt.Errorf("pull request selector is required")
	}
	if index := strings.LastIndex(value, "/pull/"); index >= 0 {
		value = value[index+len("/pull/"):]
		// A browser URL often carries a tab after the number — /files,
		// /commits, /checks — and it still names the same pull request.
		if slash := strings.IndexByte(value, '/'); slash >= 0 {
			value = value[:slash]
		}
	}
	if index := strings.LastIndex(value, "#"); index >= 0 {
		value = value[index+1:]
	}
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return "", fmt.Errorf("pull request selector %q carries no number", selector)
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return "", fmt.Errorf("pull request selector %q carries no number", selector)
		}
	}
	return value, nil
}

// HeadCheck is one observed check on a commit, in the shape a caller outside
// this package needs: a name and a normalized bucket.
type HeadCheck struct {
	Name   string
	Bucket string
}

// PullRequestHeadChecks reads every check GitHub has for a pull request's
// current head, and reports whether they have all passed.
//
// It exists so there is exactly one implementation of "are this pull request's
// checks green?" in WB, reachable from outside this package. The alternative —
// `gh pr checks --json` — is unavailable on the installed client, and even
// where it works it is a second dialect for a fact the API already answers.
func PullRequestHeadChecks(ctx context.Context, repository, selector string) ([]HeadCheck, bool, error) {
	view, err := ReadPullRequest(ctx, repository, selector)
	if err != nil {
		return nil, false, err
	}
	options := PullRequestWaitOptions{Repository: repository, Target: view.Base.Ref, Head: view.Head.SHA}
	runs, runsPending, reason := commitCheckRuns(ctx, options)
	if reason != "" {
		return nil, false, fmt.Errorf("%s", reason)
	}
	statuses, statusesPending, reason := commitStatuses(ctx, options)
	if reason != "" {
		return nil, false, fmt.Errorf("%s", reason)
	}
	observed := append(append([]RemoteCheck{}, runs...), statuses...)
	sortRemoteChecks(observed)
	checks := make([]HeadCheck, 0, len(observed))
	green := !runsPending && !statusesPending
	for _, check := range observed {
		checks = append(checks, HeadCheck{Name: check.Name, Bucket: check.Bucket})
		if check.Bucket != "pass" && check.Bucket != "skipping" {
			green = false
		}
	}
	return checks, green, nil
}

// RepositoryFromPullRequestURL extracts owner/repository from a pull request
// URL, so a caller holding only the URL can still address the API.
func RepositoryFromPullRequestURL(pullRequestURL string) (string, error) {
	value := strings.TrimSpace(pullRequestURL)
	index := strings.Index(value, "/pull/")
	if index < 0 {
		return "", fmt.Errorf("pull request URL %q has no /pull/ segment", pullRequestURL)
	}
	parts := strings.Split(strings.Trim(value[:index], "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("pull request URL %q names no repository", pullRequestURL)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}
