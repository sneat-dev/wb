// Package prinventory owns the remote pull-request inventory. It is separate
// from worktree discovery: the latter describes local WB-managed checkouts,
// while this package describes every open PR visible to GitHub for an owner.
package prinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// Runner is the narrow GitHub command boundary. Tests can inject a deterministic
// runner while production uses gh, which keeps authentication and API details
// in the same boundary WB already uses elsewhere.
type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "gh", args...)
	command.Env = console.Env()
	return command.Output()
}

// Owner identifies one GitHub search qualifier.
type Owner struct {
	Login     string `json:"login"`
	Qualifier string `json:"qualifier"` // org or user
}

func (o Owner) String() string { return o.Qualifier + ":" + o.Login }

// Options controls one immutable snapshot.
type Options struct {
	Owners          []Owner
	ExcludeArchived bool
	CreatedBefore   string
	Runner          Runner
	Now             func() time.Time
	Parallel        int
}

// Filters is recorded in every report so an operator can see the scope that
// was actually queried, rather than inferring it from the rows returned.
type Filters struct {
	State           string `json:"state"`
	IncludeArchived bool   `json:"include_archived"`
	CreatedBefore   string `json:"created_before,omitempty"`
}

type Counts struct {
	OwnersRequested int `json:"owners_requested"`
	OwnersCompleted int `json:"owners_completed"`
	OwnersFailed    int `json:"owners_failed"`
	PullRequests    int `json:"pull_requests"`
}

type Diagnostic struct {
	Owner    string `json:"owner,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

type PullRequest struct {
	ID               string    `json:"id,omitempty"`
	Repository       string    `json:"repository"`
	Number           int       `json:"number"`
	Title            string    `json:"title"`
	URL              string    `json:"url"`
	Author           string    `json:"author,omitempty"`
	Draft            bool      `json:"draft"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Mergeable        string    `json:"mergeable,omitempty"`
	MergeStateStatus string    `json:"merge_state_status,omitempty"`
	Conflict         bool      `json:"conflict"`
	Checks           []Check   `json:"checks,omitempty"`
}

type OwnerResult struct {
	Owner         Owner        `json:"owner"`
	Complete      bool         `json:"complete"`
	TotalReported int          `json:"total_reported"`
	Retrieved     int          `json:"retrieved"`
	PullRequests  int          `json:"pull_requests"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

type Report struct {
	SchemaVersion    int           `json:"schema_version"`
	SnapshotAt       time.Time     `json:"snapshot_at"`
	Complete         bool          `json:"complete"`
	Owners           []Owner       `json:"owners"`
	EffectiveFilters Filters       `json:"effective_filters"`
	Counts           Counts        `json:"counts"`
	PullRequests     []PullRequest `json:"pull_requests"`
	OwnerResults     []OwnerResult `json:"owner_results"`
	Diagnostics      []Diagnostic  `json:"diagnostics,omitempty"`
}

type searchPage struct {
	TotalCount int          `json:"total_count"`
	Items      []searchItem `json:"items"`
}

type searchItem struct {
	ID            json.RawMessage `json:"id"`
	Number        int             `json:"number"`
	Title         string          `json:"title"`
	URL           string          `json:"html_url"`
	RepositoryURL string          `json:"repository_url"`
	User          struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Draft       bool      `json:"draft"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request,omitempty"`
}

type rawDetails struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	IsDraft           bool      `json:"isDraft"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Mergeable         string    `json:"mergeable"`
	MergeStateStatus  string    `json:"mergeStateStatus"`
	StatusCheckRollup []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

// Inventory queries each owner independently, allowing GitHub's owner-filter
// limit to be bypassed without changing the declared scope. Owner results are
// reconciled before deduplication so a partial owner can never look complete.
func Inventory(ctx context.Context, options Options) Report {
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	if options.Runner == nil {
		options.Runner = execRunner{}
	}
	owners := normalizeOwners(options.Owners)
	report := Report{SchemaVersion: 1, SnapshotAt: now().UTC(), Complete: true,
		Owners: owners, EffectiveFilters: Filters{State: "open", IncludeArchived: !options.ExcludeArchived, CreatedBefore: options.CreatedBefore},
		Counts: Counts{OwnersRequested: len(owners)}, OwnerResults: make([]OwnerResult, len(owners))}
	if options.Parallel <= 0 {
		options.Parallel = 8
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for worker := 0; worker < options.Parallel && worker < len(owners); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				result, prs, diagnostics := inventoryOwner(ctx, options, owners[i])
				mu.Lock()
				report.OwnerResults[i], report.OwnerResults[i].Diagnostics = result, diagnostics
				report.PullRequests = append(report.PullRequests, prs...)
				mu.Unlock()
			}
		}()
	}
	for i := range owners {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	seen := map[string]bool{}
	deduped := make([]PullRequest, 0, len(report.PullRequests))
	for _, pr := range report.PullRequests {
		key := fmt.Sprintf("%s#%d", pr.Repository, pr.Number)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, pr)
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Repository != deduped[j].Repository {
			return deduped[i].Repository < deduped[j].Repository
		}
		return deduped[i].Number < deduped[j].Number
	})
	report.PullRequests = deduped
	for _, result := range report.OwnerResults {
		if result.Complete {
			report.Counts.OwnersCompleted++
		} else {
			report.Counts.OwnersFailed++
			report.Complete = false
		}
		report.Diagnostics = append(report.Diagnostics, result.Diagnostics...)
	}
	report.Counts.PullRequests = len(report.PullRequests)
	sort.Slice(report.Diagnostics, func(i, j int) bool {
		if report.Diagnostics[i].Owner != report.Diagnostics[j].Owner {
			return report.Diagnostics[i].Owner < report.Diagnostics[j].Owner
		}
		return report.Diagnostics[i].Message < report.Diagnostics[j].Message
	})
	return report
}

func normalizeOwners(owners []Owner) []Owner {
	seen := map[string]bool{}
	result := make([]Owner, 0, len(owners))
	for _, owner := range owners {
		owner.Login = strings.TrimSpace(owner.Login)
		owner.Qualifier = strings.TrimSpace(owner.Qualifier)
		if owner.Qualifier == "" {
			owner.Qualifier = "org"
		}
		if owner.Login == "" || (owner.Qualifier != "org" && owner.Qualifier != "user") {
			continue
		}
		key := owner.Qualifier + ":" + strings.ToLower(owner.Login)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, owner)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func inventoryOwner(ctx context.Context, options Options, owner Owner) (OwnerResult, []PullRequest, []Diagnostic) {
	result := OwnerResult{Owner: owner}
	query := "is:pr is:open " + owner.Qualifier + ":" + owner.Login
	if !options.ExcludeArchived { /* no archive qualifier: archived repositories remain in scope */
	} else {
		query += " archived:false"
	}
	if options.CreatedBefore != "" {
		cutoff, err := time.Parse(time.RFC3339, options.CreatedBefore)
		if err != nil {
			d := Diagnostic{Owner: owner.Login, Severity: "error", Message: "invalid created-before cutoff: " + err.Error()}
			return result, nil, []Diagnostic{d}
		}
		// GitHub's search date qualifier is day-granular. Include the entire
		// cutoff day in the provider query, then apply the immutable timestamp
		// client-side; using created:<DATE would incorrectly omit earlier PRs
		// on a non-midnight cutoff day.
		query += " created:<=" + cutoff.UTC().Format("2006-01-02")
	}
	args := []string{"api", "--method", "GET", "--paginate", "--slurp", "/search/issues", "-f", "q=" + query, "-f", "per_page=100"}
	out, err := options.Runner.Run(ctx, args...)
	if err != nil {
		d := Diagnostic{Owner: owner.Login, Severity: "error", Message: "GitHub owner query failed: " + err.Error()}
		return result, nil, []Diagnostic{d}
	}
	pages, err := parsePages(out)
	if err != nil {
		d := Diagnostic{Owner: owner.Login, Severity: "error", Message: "GitHub owner response invalid: " + err.Error()}
		return result, nil, []Diagnostic{d}
	}
	var items []searchItem
	for _, page := range pages {
		result.TotalReported += page.TotalCount
		items = append(items, page.Items...)
	}
	// --slurp returns one page object per page; total_count repeats on every
	// page, so reconcile against the first authoritative count.
	if len(pages) > 0 {
		result.TotalReported = pages[0].TotalCount
	}
	result.Retrieved = len(items)
	if result.Retrieved < result.TotalReported {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Owner: owner.Login, Severity: "error", Message: fmt.Sprintf("incomplete GitHub pagination: retrieved %d of %d results", result.Retrieved, result.TotalReported)})
	}
	cutoff, _ := time.Parse(time.RFC3339, options.CreatedBefore)
	prs := make([]PullRequest, 0, len(items))
	ownerSeen := map[string]bool{}
	for _, item := range items {
		if !cutoff.IsZero() && !item.CreatedAt.Before(cutoff) {
			continue
		}
		repository := repositorySlug(item.RepositoryURL)
		if repository == "" {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Owner: owner.Login, Severity: "error", Message: fmt.Sprintf("PR #%d has no repository identity", item.Number)})
			continue
		}
		key := fmt.Sprintf("%s#%d", repository, item.Number)
		if ownerSeen[key] {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Owner: owner.Login, Severity: "error", Message: "duplicate PR identity across provider pages: " + key})
			continue
		}
		ownerSeen[key] = true
		pr := PullRequest{ID: idString(item.ID), Repository: repository, Number: item.Number, Title: item.Title, URL: item.URL, Author: item.User.Login, Draft: item.Draft, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
		if item.PullRequest != nil && item.PullRequest.URL != "" {
			detail, detailErr := details(ctx, options.Runner, repository, item.Number)
			if detailErr != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Owner: owner.Login, Severity: "error", Message: fmt.Sprintf("PR %s#%d details failed: %v", repository, item.Number, detailErr)})
			} else {
				applyDetails(&pr, detail)
			}
		}
		prs = append(prs, pr)
	}
	result.PullRequests = len(prs)
	result.Complete = len(result.Diagnostics) == 0 && result.Retrieved >= result.TotalReported
	return result, prs, result.Diagnostics
}

func parsePages(raw []byte) ([]searchPage, error) {
	var pages []searchPage
	if err := json.Unmarshal(raw, &pages); err == nil {
		return pages, nil
	}
	var page searchPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, err
	}
	return []searchPage{page}, nil
}

func repositorySlug(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		return parts[1] + "/" + parts[2]
	}
	return ""
}

func details(ctx context.Context, runner Runner, repository string, number int) (rawDetails, error) {
	args := []string{"pr", "view", fmt.Sprint(number), "--repo", repository, "--json", "number,title,url,author,isDraft,createdAt,updatedAt,mergeable,mergeStateStatus,statusCheckRollup"}
	out, err := runner.Run(ctx, args...)
	if err != nil {
		return rawDetails{}, err
	}
	var result rawDetails
	if err := json.Unmarshal(out, &result); err != nil {
		return rawDetails{}, err
	}
	return result, nil
}

func applyDetails(pr *PullRequest, detail rawDetails) {
	if detail.Number != 0 {
		pr.Number = detail.Number
	}
	if detail.Title != "" {
		pr.Title = detail.Title
	}
	if detail.URL != "" {
		pr.URL = detail.URL
	}
	if detail.Author.Login != "" {
		pr.Author = detail.Author.Login
	}
	pr.Draft = detail.IsDraft
	if !detail.CreatedAt.IsZero() {
		pr.CreatedAt = detail.CreatedAt
	}
	if !detail.UpdatedAt.IsZero() {
		pr.UpdatedAt = detail.UpdatedAt
	}
	pr.Mergeable, pr.MergeStateStatus = detail.Mergeable, detail.MergeStateStatus
	pr.Conflict = strings.EqualFold(pr.Mergeable, "CONFLICTING") || strings.EqualFold(pr.MergeStateStatus, "dirty")
	pr.Checks = make([]Check, 0, len(detail.StatusCheckRollup))
	for _, check := range detail.StatusCheckRollup {
		pr.Checks = append(pr.Checks, Check{Name: check.Name, Status: check.Status, Conclusion: check.Conclusion})
	}
	sort.Slice(pr.Checks, func(i, j int) bool { return pr.Checks[i].Name < pr.Checks[j].Name })
}

func idString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

// RenderMarkdown renders a stable, reviewable snapshot. Rows are already
// sorted by Inventory; sorting again makes hand-built reports deterministic.
func RenderMarkdown(report Report) string {
	var b strings.Builder
	f := report.EffectiveFilters
	fArchived := "included"
	if !f.IncludeArchived {
		fArchived = "excluded"
	}
	fmt.Fprintf(&b, "# Open pull-request inventory\n\nSnapshot: `%s`  \nComplete: `%t`  \nOwners: `%s`  \nState: `%s`  \nArchived repositories: `%s`  \n", report.SnapshotAt.UTC().Format(time.RFC3339), report.Complete, ownerList(report.Owners), f.State, fArchived)
	if f.CreatedBefore != "" {
		fmt.Fprintf(&b, "Created before: `%s`  \n", f.CreatedBefore)
	}
	fmt.Fprintf(&b, "\nCounts: %d PRs across %d/%d owners\n\n| Repository | PR | Title | Author | Draft | Mergeable | Merge state | Checks |\n|---|---:|---|---|:---:|---|---|---|\n", report.Counts.PullRequests, report.Counts.OwnersCompleted, report.Counts.OwnersRequested)
	for _, pr := range report.PullRequests {
		fmt.Fprintf(&b, "| [%s](%s) | #%d | %s | %s | %t | %s | %s | %s |\n", pr.Repository, pr.URL, pr.Number, markdownCell(pr.Title), pr.Author, pr.Draft, pr.Mergeable, pr.MergeStateStatus, checkSummary(pr.Checks))
	}
	if len(report.Diagnostics) > 0 {
		b.WriteString("\n## Diagnostics\n\n")
		for _, d := range report.Diagnostics {
			fmt.Fprintf(&b, "- `%s` %s: %s\n", d.Severity, d.Owner, d.Message)
		}
	}
	return b.String()
}

func ownerList(owners []Owner) string {
	values := make([]string, len(owners))
	for i, owner := range owners {
		values[i] = owner.Login
	}
	return strings.Join(values, ", ")
}
func markdownCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}
func checkSummary(checks []Check) string {
	values := make([]string, len(checks))
	for i, check := range checks {
		values[i] = check.Name + ":" + check.Conclusion
	}
	return strings.Join(values, ", ")
}
