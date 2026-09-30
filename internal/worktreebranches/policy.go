// Package worktreebranches owns dependency-free branch selection and cleanup policy.
package worktreebranches

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	BranchContained  = "contained"  // ancestor of the fetched exact target; always eligible for deletion
	BranchAbsorbed   = "absorbed"   // patch-id/tree equal to the target, but not an ancestor; report-only, forever
	BranchReceipted  = "receipted"  // a proved landing receipt shows the work is in the target; eligible only under --receipts
	BranchSuperseded = "superseded" // a trusted reviewer receipt replaces the exact branch; eligible only under --superseded-by
	BranchRetired    = "retired"    // user-selected quarantine; never an active-backlog candidate
	BranchUnique     = "unique"     // has content git cherry proves is not upstream
	BranchProtected  = "protected"  // base, canonical HEAD, or a protected name
	BranchInUse      = "in-use"     // checked out in a linked worktree, or named by a WB Work Log claim
	BranchUnreadable = "unreadable" // required evidence could not be obtained
)

const (
	BranchScopeLocal  = "local"
	BranchScopeRemote = "remote"
	BranchScopeAll    = "all"
)

type BranchEntry struct {
	Repository            string              `json:"repository"`
	Branch                string              `json:"branch"`
	RefKind               string              `json:"ref_kind,omitempty"` // branch or tag
	Scope                 string              `json:"scope"`              // local or remote
	SHA                   string              `json:"sha"`
	ShortSHA              string              `json:"short_sha"`
	CommitterDate         time.Time           `json:"committer_date,omitempty"`
	Author                string              `json:"author,omitempty"`
	Title                 string              `json:"title,omitempty"`
	Base                  string              `json:"base"`
	TargetSHA             string              `json:"target_sha,omitempty"`
	Disposition           string              `json:"disposition"`
	Evidence              string              `json:"evidence"`
	Reason                string              `json:"reason,omitempty"`
	Task                  string              `json:"task,omitempty"`
	OpenPullRequest       *PullRequest        `json:"open_pull_request,omitempty"`
	OpenBasePullRequest   *PullRequest        `json:"open_base_pull_request,omitempty"`
	PullRequests          []BranchPullRequest `json:"pull_requests,omitempty"`
	PullRequestQueried    bool                `json:"pull_request_queried,omitempty"`
	PullRequestQueryError string              `json:"pull_request_query_error,omitempty"`
	// LandingSHA and ReceiptPullRequest carry a receipted branch's proved
	// landing so apply can re-verify the receipt — not ancestry, which a
	// receipted branch fails by construction — against the freshly fetched
	// target. See #req:receipted-requires-a-proved-landing.
	LandingSHA         string       `json:"landing_sha,omitempty"`
	ReceiptPullRequest *PullRequest `json:"receipt_pull_request,omitempty"`
	// PullRequestQueryFailed distinguishes "no matching pull request" from
	// "WB could not ask GitHub." Cleanup's remote apply fails the whole scope
	// closed on the latter; list surfaces the specific error.
	PullRequestQueryFailed bool `json:"pull_request_query_failed,omitempty"`
	// AbsorbedByRejection explains why an operator-supplied --absorbed-by
	// pointer did not verify for this branch. It is set only when
	// --absorbed-by was passed and its attested-absorption proof (see
	// attestedAbsorbedReceipt, shared with `wb worktree cleanup
	// --absorbed-by`) failed for this candidate specifically; the branch keeps
	// whatever disposition its patch evidence (or --receipts) produces. A
	// wrong or dishonest pointer can therefore only fail closed, never widen
	// eligibility, and the rejection is reported rather than silently
	// swallowed.
	AbsorbedByRejection   string `json:"absorbed_by_rejection,omitempty"`
	SupersessionReceipt   string `json:"supersession_receipt,omitempty"`
	SupersessionReviewer  string `json:"supersession_reviewer,omitempty"`
	SupersessionReceiptID string `json:"supersession_receipt_id,omitempty"`
	SupersessionSHA256    string `json:"supersession_sha256,omitempty"`
	SupersessionRejection string `json:"supersession_rejection,omitempty"`
	SupersededAtOrigin    bool   `json:"superseded_at_origin,omitempty"`
}

type BranchRef struct {
	Name          string
	SHA           string
	CommitterDate time.Time
	UnknownDate   bool
	Author        string
	Title         string
}

type BranchCleanupResult struct {
	BranchEntry
	Eligible       bool   `json:"eligible"`
	SkipReason     string `json:"skip_reason,omitempty"`
	Applied        bool   `json:"applied"`
	Outcome        string `json:"outcome"` // planned, deleted, skipped, or failed
	Error          string `json:"error,omitempty"`
	RecoveryBundle string `json:"recovery_bundle,omitempty"`
}

type BranchPullRequest struct {
	Number   int        `json:"number"`
	URL      string     `json:"url"`
	Role     string     `json:"role"`  // head or base
	State    string     `json:"state"` // open, merged, or closed
	Head     string     `json:"head"`
	Base     string     `json:"base"`
	HeadSHA  string     `json:"head_sha"`
	MergeSHA string     `json:"merge_sha,omitempty"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
}

type PullRequest = worktreeproof.PullRequest

// PolicyOptions contains only the selection and planning inputs consumed by pure policy.
type PolicyOptions struct {
	Base, Scope, Only, Branch, Name string
	OlderThan                       time.Duration
	Now                             time.Time
}

// protectedBranchNames remain fixed pending a configurable branch policy.
var protectedBranchNames = map[string]bool{"main": true, "master": true}
var quarantineSegmentUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func RetiredNamespaceSelected(sweep PolicyOptions) bool {
	if sweep.Only == BranchRetired {
		return true
	}
	return strings.HasPrefix(sweep.Branch, "retired/") || strings.HasPrefix(sweep.Name, "retired/")
}

func ApplyListDisplayFilters(entries []BranchEntry, sweep PolicyOptions) []BranchEntry {
	filtered := make([]BranchEntry, 0, len(entries))
	for _, entry := range entries {
		if sweep.Only != "" && entry.Disposition != sweep.Only {
			continue
		}
		if sweep.OlderThan > 0 && !entry.CommitterDate.IsZero() && sweep.Now.Sub(entry.CommitterDate) < sweep.OlderThan {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func SortBranchEntries(entries []BranchEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Repository != entries[j].Repository {
			return entries[i].Repository < entries[j].Repository
		}
		if entries[i].Branch != entries[j].Branch {
			return entries[i].Branch < entries[j].Branch
		}
		return entries[i].Scope < entries[j].Scope
	})
}

func TallyDispositions(entries []BranchEntry) map[string]int {
	totals := map[string]int{}
	for _, entry := range entries {
		totals[entry.Disposition]++
	}
	return totals
}

func AccumulateRetiredCounts(sweep PolicyOptions, repositorySlug string, refs []BranchRef, scope string, counts map[string]int, names map[string]bool) {
	for _, ref := range refs {
		if !RetiredRefSelected(sweep, ref) {
			continue
		}
		counts[scope]++
		names[repositorySlug+"|"+ref.Name] = true
	}
}

func RetiredRefSelected(sweep PolicyOptions, ref BranchRef) bool {
	if !IsRetiredBranch(ref.Name) {
		return false
	}
	if !BranchNameSelected(sweep, ref.Name) {
		return false
	}
	if sweep.OlderThan == 0 {
		return true
	}
	return !ref.UnknownDate && !ref.CommitterDate.IsZero() && sweep.Now.Sub(ref.CommitterDate) >= sweep.OlderThan
}

func BranchNameSelected(sweep PolicyOptions, name string) bool {
	if sweep.Branch != "" && sweep.Branch != name {
		return false
	}
	if sweep.Name == "" {
		return true
	}
	matched, err := path.Match(sweep.Name, name)
	return err == nil && matched
}

func RetiredBranchEntry(repositorySlug string, sweep PolicyOptions, ref BranchRef, scope, targetSHA string) BranchEntry {
	return BranchEntry{Repository: repositorySlug, Branch: ref.Name, RefKind: "branch", Scope: scope, SHA: ref.SHA,
		ShortSHA: ShortSHA(ref.SHA), CommitterDate: ref.CommitterDate, Author: ref.Author, Title: ref.Title, Base: sweep.Base, TargetSHA: targetSHA,
		Disposition: BranchRetired, Evidence: "user-selected retired quarantine; excluded from active backlog and never cleanup-eligible"}
}

func IsRetiredBranch(branch string) bool { return strings.HasPrefix(branch, "retired/") }

func ReceiptedBranch(entry BranchEntry, landingSHA string, pullRequest *PullRequest, absorbedBy string) BranchEntry {
	entry.Disposition = BranchReceipted
	entry.LandingSHA = landingSHA
	entry.ReceiptPullRequest = pullRequest
	if absorbedBy != "" {
		entry.Evidence = fmt.Sprintf(
			"--absorbed-by %s resolved to %s; branch content is fully contained there and in the fetched target, and %s is exactly where it entered",
			absorbedBy, ShortSHA(landingSHA), ShortSHA(landingSHA))
		entry.Reason = fmt.Sprintf("content-proven absorbed via --absorbed-by %s; eligible for deletion", absorbedBy)
		return entry
	}
	entry.Evidence = fmt.Sprintf(
		"merged pull request #%d into %s; landing %s is in the fetched target and the three-way proof holds",
		pullRequest.Number, entry.Base, ShortSHA(landingSHA))
	entry.Reason = fmt.Sprintf("landed via merged pull request #%d; eligible for deletion under --receipts", pullRequest.Number)
	return entry
}

func ShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func IsProtectedBranch(branch, base, canonicalHEAD string) bool {
	if branch == base || branch == canonicalHEAD {
		return true
	}
	return protectedBranchNames[branch]
}

func ProtectedEvidence(branch, base, canonicalHEAD string) string {
	switch branch {
	case base:
		return fmt.Sprintf("is the base branch %q", base)
	case canonicalHEAD:
		return "is the canonical clone's current HEAD"
	default:
		return "matches a configured protected branch name"
	}
}

func ScopeIncludesRemote(scope string) bool {
	return scope == BranchScopeRemote || scope == BranchScopeAll
}

func PlanBranchCleanup(entries []BranchEntry, sweep PolicyOptions) []BranchCleanupResult {
	remoteEvidenceUnavailable := RemotePullRequestEvidenceUnavailable(entries, sweep)
	results := make([]BranchCleanupResult, 0, len(entries))
	for _, entry := range entries {
		result := BranchCleanupResult{BranchEntry: entry, Outcome: "skipped"}
		switch {
		case !EligibleBranchCleanupDisposition(entry):
			result.SkipReason = SkipReasonForDisposition(entry)
		case sweep.OlderThan > 0 && !entry.CommitterDate.IsZero() && sweep.Now.Sub(entry.CommitterDate) < sweep.OlderThan:
			result.SkipReason = fmt.Sprintf("branch is younger than --older-than %s", sweep.OlderThan)
		case entry.Scope == BranchScopeRemote && remoteEvidenceUnavailable:
			result.SkipReason = "remote pull-request evidence unavailable; refusing every remote deletion in this run"
		case entry.Scope == BranchScopeRemote && entry.OpenPullRequest != nil:
			result.SkipReason = fmt.Sprintf("branch is the head of open pull request %s", entry.OpenPullRequest.URL)
		case entry.Scope == BranchScopeRemote && entry.OpenBasePullRequest != nil:
			result.SkipReason = fmt.Sprintf("branch is the base of open pull request %s", entry.OpenBasePullRequest.URL)
		default:
			result.Eligible = true
			result.Outcome = "planned"
		}
		results = append(results, result)
	}
	return results
}

func EligibleBranchCleanupDisposition(entry BranchEntry) bool {
	switch entry.Disposition {
	case BranchContained, BranchReceipted:
		return true
	case BranchSuperseded:
		return entry.SupersededAtOrigin
	default:
		return false
	}
}

func SkipReasonForDisposition(entry BranchEntry) string {
	if entry.Reason != "" {
		return entry.Reason
	}
	if entry.Evidence != "" {
		return entry.Evidence
	}
	return fmt.Sprintf("disposition %s is never eligible for --apply", entry.Disposition)
}

func RemotePullRequestEvidenceUnavailable(entries []BranchEntry, sweep PolicyOptions) bool {
	if sweep.Scope == BranchScopeLocal {
		return false
	}
	for _, entry := range entries {
		if entry.Scope == BranchScopeRemote &&
			(entry.Disposition == BranchContained || entry.Disposition == BranchReceipted || entry.Disposition == BranchSuperseded) &&
			entry.PullRequestQueryFailed {
			return true
		}
	}
	return false
}

func TallyCleanupOutcomes(results []BranchCleanupResult) map[string]int {
	totals := map[string]int{}
	for _, result := range results {
		totals[result.Outcome]++
	}
	return totals
}

func PeerEvidenceSafeDisposition(disposition string) bool {
	switch disposition {
	case BranchContained, BranchReceipted, BranchAbsorbed, BranchUnique:
		return true
	default:
		return false
	}
}

func RetiredBranchDestination(now time.Time, source, sha string) string {
	flat := strings.ReplaceAll(source, "/", "-")
	flat = quarantineSegmentUnsafe.ReplaceAllString(flat, "-")
	flat = strings.Trim(flat, "-.")
	if flat == "" {
		flat = "branch"
	}
	return "retired/" + now.UTC().Format("20060102") + "-" + flat + "-" + ShortSHA(sha)
}
