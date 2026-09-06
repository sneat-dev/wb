package githubapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPDoer and GitHubTokenSource keep credentials and transport in the host.
// The reader never imports a GitHub SDK or accepts identity from a webhook
// payload beyond its repository/install identity.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type GitHubTokenSource interface {
	Token(context.Context, WebhookDelivery) (string, error)
}

// GitHubRESTProjectionReader builds one delivery snapshot from GitHub's REST
// API. APIBase is injectable for a host proxy and tests; the default is the
// public GitHub API.
type GitHubRESTProjectionReader struct {
	HTTP    HTTPDoer
	Tokens  GitHubTokenSource
	APIBase string
	Now     func() time.Time
}

type githubRepository struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	OpenIssues    int    `json:"open_issues_count"`
	HTMLURL       string `json:"html_url"`
}
type githubOrganization struct {
	Login       string `json:"login"`
	PublicRepos int    `json:"public_repos"`
	HTMLURL     string `json:"html_url"`
}
type githubSearch struct {
	Total int `json:"total_count"`
}
type githubCommit struct {
	SHA string `json:"sha"`
}
type githubContent struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	HTMLURL  string `json:"html_url"`
}
type githubRelease struct {
	HTMLURL string `json:"html_url"`
}
type githubPull struct {
	Number         int        `json:"number"`
	MergedAt       *time.Time `json:"merged_at"`
	HTMLURL        string     `json:"html_url"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
}

func (reader GitHubRESTProjectionReader) Refresh(ctx context.Context, delivery WebhookDelivery) error {
	_, err := reader.RefreshAuthoritativeProjection(ctx, delivery)
	return err
}

func (reader GitHubRESTProjectionReader) RefreshProjection(ctx context.Context, delivery WebhookDelivery) (ProjectionSnapshot, error) {
	return reader.RefreshAuthoritativeProjection(ctx, delivery)
}

func (reader GitHubRESTProjectionReader) RefreshAuthoritativeProjection(ctx context.Context, delivery WebhookDelivery) (ProjectionSnapshot, error) {
	owner, repo, err := deliveryRepository(delivery)
	if err != nil {
		return ProjectionSnapshot{}, err
	}
	if reader.HTTP == nil || reader.Tokens == nil {
		return ProjectionSnapshot{}, errors.New("GitHub REST reader requires HTTP and token sources")
	}
	token, err := reader.Tokens.Token(ctx, delivery)
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("resolve GitHub installation token: %w", err)
	}
	var repository githubRepository
	err = reader.get(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), &repository)
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("read GitHub repository: %w", err)
	}
	canonical := "github.com/" + owner + "/" + repo
	if repository.FullName != "" && !strings.EqualFold(repository.FullName, owner+"/"+repo) && !strings.EqualFold(repository.FullName, canonical) {
		return ProjectionSnapshot{}, fmt.Errorf("GitHub repository identity %q does not match delivery %q", repository.FullName, canonical)
	}
	verifiedAt := reader.now()
	eligibility, eligibilityErr := reader.readEligibility(ctx, token, owner, repo, verifiedAt)
	public := eligibilityErr == nil
	if eligibilityErr != nil && !errors.Is(eligibilityErr, errNoPublicOptIn) {
		return ProjectionSnapshot{}, eligibilityErr
	}
	openPRs, err := reader.searchCount(ctx, token, "repo:"+owner+"/"+repo+" is:pr is:open")
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("read open pull requests: %w", err)
	}
	mergedPRs, err := reader.searchCount(ctx, token, "repo:"+owner+"/"+repo+" is:pr is:merged")
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("read merged pull requests: %w", err)
	}
	releases, err := reader.listReleases(ctx, token, owner, repo)
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("read releases: %w", err)
	}
	summary := Summary{Repositories: 1, OpenPulls: openPRs, MergedPulls: mergedPRs, OpenIssues: repository.OpenIssues - openPRs, Releases: releases}
	updated := verifiedAt
	if repository.DefaultBranch == "" {
		return ProjectionSnapshot{}, errors.New("GitHub repository has no default branch")
	}
	document := ProjectionDocument{Scope: ScopeRepository, ID: canonical, DisplayName: repository.Name, Summary: summary, UpdatedAt: updated, PublicOptIn: public}
	if public {
		document.PublicEligibility = &eligibility
	}
	var org githubOrganization
	err = reader.get(ctx, token, "/orgs/"+url.PathEscape(owner), &org)
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("read GitHub organization: %w", err)
	}
	orgID := "github.com/" + org.Login
	if org.Login == "" {
		orgID = "github.com/" + owner
	}
	orgDoc := ProjectionDocument{Scope: ScopeOrganization, ID: orgID, DisplayName: owner, Summary: Summary{Repositories: org.PublicRepos}, UpdatedAt: updated}
	merges, err := reader.latestMerges(ctx, token, owner, repo)
	if err != nil {
		return ProjectionSnapshot{}, fmt.Errorf("read latest merges: %w", err)
	}
	return ProjectionSnapshot{Repositories: []ProjectionDocument{document}, Organizations: []ProjectionDocument{orgDoc}, LatestMerges: merges}, nil
}

var errNoPublicOptIn = errors.New("repository has no public Workbench opt-in")

func (reader GitHubRESTProjectionReader) readEligibility(ctx context.Context, token, owner, repo string, verifiedAt time.Time) (PublicEligibility, error) {
	var commits []githubCommit
	if err := reader.get(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/commits?path=README.md&per_page=1", &commits); err != nil {
		return PublicEligibility{}, fmt.Errorf("read root README commit: %w", err)
	}
	if len(commits) != 1 || len(commits[0].SHA) != 40 {
		return PublicEligibility{}, errors.New("GitHub root README has no exact commit")
	}
	sha := commits[0].SHA
	var content githubContent
	if err := reader.get(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/contents/README.md?ref="+url.QueryEscape(sha), &content); err != nil {
		return PublicEligibility{}, fmt.Errorf("read root README at %s: %w", sha, err)
	}
	if content.Encoding != "base64" {
		return PublicEligibility{}, errors.New("GitHub root README is not base64 encoded")
	}
	markdown, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return PublicEligibility{}, fmt.Errorf("decode root README: %w", err)
	}
	readmeURL := "https://github.com/" + owner + "/" + repo + "/blob/" + sha + "/README.md"
	evidence, err := VerifyPublicEligibility("github.com/"+owner+"/"+repo, readmeURL, string(markdown), verifiedAt)
	if err != nil {
		return PublicEligibility{}, errNoPublicOptIn
	}
	return evidence, nil
}

func (reader GitHubRESTProjectionReader) searchCount(ctx context.Context, token, query string) (int, error) {
	var result githubSearch
	if err := reader.get(ctx, token, "/search/issues?q="+url.QueryEscape(query)+"&per_page=1", &result); err != nil {
		return 0, err
	}
	return result.Total, nil
}

func (reader GitHubRESTProjectionReader) listReleases(ctx context.Context, token, owner, repo string) (int, error) {
	var releases []githubRelease
	if err := reader.get(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/releases?per_page=100", &releases); err != nil {
		return 0, err
	}
	return len(releases), nil
}

func (reader GitHubRESTProjectionReader) latestMerges(ctx context.Context, token, owner, repo string) ([]LatestMerge, error) {
	var pulls []githubPull
	if err := reader.get(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls?state=closed&sort=updated&direction=desc&per_page=10", &pulls); err != nil {
		return nil, err
	}
	merges := make([]LatestMerge, 0, len(pulls))
	for _, pull := range pulls {
		if pull.MergedAt == nil {
			continue
		}
		merges = append(merges, LatestMerge{Repository: "github.com/" + owner + "/" + repo, PullRequest: pull.Number, MergedAt: pull.MergedAt.UTC(), PullRequestURL: pull.HTMLURL, MergeCommitSHA: pull.MergeCommitSHA})
	}
	return merges, nil
}

func (reader GitHubRESTProjectionReader) get(ctx context.Context, token, path string, value any) error {
	base := strings.TrimRight(reader.APIBase, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := reader.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("GitHub API %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(response.Body).Decode(value)
}

func deliveryRepository(delivery WebhookDelivery) (string, string, error) {
	canonical := strings.TrimSpace(delivery.Repository)
	if canonical == "" {
		var envelope struct {
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(delivery.Payload, &envelope); err != nil {
			return "", "", errors.New("webhook does not identify a repository")
		}
		canonical = envelope.Repository.FullName
	}
	if !strings.HasPrefix(canonical, "github.com/") {
		canonical = "github.com/" + canonical
	}
	owner, repo, err := canonicalGitHubRepository(canonical)
	if err != nil {
		return "", "", err
	}
	return owner, repo, nil
}

func (reader GitHubRESTProjectionReader) now() time.Time {
	if reader.Now != nil {
		return reader.Now().UTC()
	}
	return time.Now().UTC()
}

var _ ProjectionReader = GitHubRESTProjectionReader{}
var _ AuthoritativeProjectionReader = GitHubRESTProjectionReader{}
var _ AuthoritativeReader = GitHubRESTProjectionReader{}
