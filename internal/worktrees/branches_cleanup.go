package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// BranchCleanupOptions plans or applies retirement of branches provably
// contained in the freshly fetched exact origin target. Dry run is the
// default; --apply is required for every deletion in every scope.
type BranchCleanupOptions struct {
	ProjectsRoot string
	Base         string
	Scope        string
	Apply        bool
	OlderThan    time.Duration
	ReportDir    string
	Filter       string
	Repository   string
	Branch       string
	Progress     io.Writer
	// Receipts enables landing-receipt classification, making receipted
	// branches eligible alongside contained ones. Off by default because it
	// costs a GitHub query per non-contained candidate. See
	// #req:receipted-is-opt-in-and-fails-closed.
	Receipts bool
	// AbsorbedBy is the optional operator-supplied landing pointer (a merged
	// pull request number or an exact landing commit) verified with the same
	// attested-absorption proof `wb worktree cleanup --absorbed-by` performs.
	// A branch that proves out is recorded as receipted, with the landing
	// commit carried in the plan exactly like a discovered receipt, so a
	// content-proven squash-absorbed branch whose worktree is already gone
	// can still be retired with an audited receipt. See
	// #req:attested-absorption-requires-exact-entry-point. Empty by default:
	// it never runs unless explicitly passed, and a pointer that fails to
	// verify for a given candidate refuses only that candidate.
	AbsorbedBy string
	// SupersededBy is a trusted-reviewer receipt for a deliberately split
	// branch. It requires one exact repository/ref selector.
	SupersededBy string
	PeerEvidence []string
	RequireHosts []string
	// Now is injectable so age eligibility is deterministic under test.
	Now func() time.Time
}

// BranchCleanupResult is one candidate's plan and, under --apply, its
// outcome. Only contained — and, under --receipts, receipted — can ever have
// Applied == true; absorbed is permanently report-only. See
// #req:absorbed-is-report-only and #req:receipted-requires-a-proved-landing.
type BranchCleanupResult struct {
	BranchEntry
	Eligible       bool   `json:"eligible"`
	SkipReason     string `json:"skip_reason,omitempty"`
	Applied        bool   `json:"applied"`
	Outcome        string `json:"outcome"` // planned, deleted, skipped, or failed
	Error          string `json:"error,omitempty"`
	RecoveryBundle string `json:"recovery_bundle,omitempty"`
}

// BranchCleanupOutcome is the full result of one plan or apply run.
type BranchCleanupOutcome struct {
	Base        string                `json:"base"`
	Scope       string                `json:"scope"`
	Apply       bool                  `json:"apply"`
	Results     []BranchCleanupResult `json:"results"`
	Diagnostics []string              `json:"diagnostics,omitempty"`
	Totals      map[string]int        `json:"totals"`
	ReportPath  string                `json:"report_path,omitempty"`
	ElapsedMS   int64                 `json:"elapsed_ms"`
}

func normalizeBranchCleanupOptions(options BranchCleanupOptions) (BranchCleanupOptions, error) {
	base, err := normalizeBranchListOptions(BranchListOptions{
		ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: options.Scope,
		OlderThan: options.OlderThan, Filter: options.Filter, Repository: options.Repository, Branch: options.Branch,
	})
	if err != nil {
		return BranchCleanupOptions{}, err
	}
	options.ProjectsRoot, options.Base, options.Scope = base.ProjectsRoot, base.Base, base.Scope
	options.OlderThan, options.Filter = base.OlderThan, base.Filter
	options.Repository, options.Branch = base.Repository, base.Branch
	options.AbsorbedBy = strings.TrimSpace(options.AbsorbedBy)
	options.SupersededBy = strings.TrimSpace(options.SupersededBy)
	options.PeerEvidence = compactStrings(options.PeerEvidence)
	options.RequireHosts = compactStrings(options.RequireHosts)
	if options.SupersededBy != "" && (options.Repository == "" || options.Branch == "") {
		return BranchCleanupOptions{}, errors.New("--superseded-by requires exact --repo owner/name and --branch ref selectors")
	}
	if options.SupersededBy != "" && options.Scope == BranchScopeRemote && (len(options.PeerEvidence) == 0 || len(options.RequireHosts) == 0) {
		return BranchCleanupOptions{}, errors.New("reviewed remote retirement requires --peer-evidence and --require-host")
	}
	if len(options.PeerEvidence) > 0 || len(options.RequireHosts) > 0 {
		if options.Scope != BranchScopeRemote || options.Repository == "" || options.Branch == "" {
			return BranchCleanupOptions{}, errors.New("--peer-evidence and --require-host require exact --scope remote --repo owner/name --branch ref")
		}
		if len(options.PeerEvidence) == 0 || len(options.RequireHosts) == 0 {
			return BranchCleanupOptions{}, errors.New("--peer-evidence and --require-host must be supplied together")
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReportDir != "" {
		absolute, err := filepath.Abs(options.ReportDir)
		if err != nil {
			return BranchCleanupOptions{}, fmt.Errorf("resolve branch cleanup report directory: %w", err)
		}
		options.ReportDir = filepath.Clean(absolute)
	}
	return options, nil
}

// DefaultBranchCleanupReportDir mirrors DefaultCleanupReportDir's naming
// convention for the branch-hygiene report family.
func DefaultBranchCleanupReportDir(home string, now time.Time) string {
	return filepath.Join(home, "reports", "branch-cleanup", now.UTC().Format("20060102T150405.000000000Z"))
}

// BranchCleanup plans, and under --apply performs, retirement of branches
// whose content is provably contained in the freshly fetched exact origin
// target. It never touches a working tree, worktree registration, or the
// absorbed disposition, and remote deletion fails closed without pull-request
// evidence. See spec/features/branch-hygiene/README.md.
func BranchCleanup(ctx context.Context, options BranchCleanupOptions) (BranchCleanupOutcome, error) {
	started := time.Now()
	normalized, err := normalizeBranchCleanupOptions(options)
	if err != nil {
		return BranchCleanupOutcome{}, err
	}
	now := normalized.Now()
	sweep := branchSweepOptions{
		ProjectsRoot: normalized.ProjectsRoot, Base: normalized.Base, Scope: normalized.Scope,
		OlderThan: normalized.OlderThan, Filter: normalized.Filter, Progress: normalized.Progress, Now: now,
		Receipts: normalized.Receipts, AbsorbedBy: normalized.AbsorbedBy,
		Repository: normalized.Repository, Branch: normalized.Branch,
		SupersededBy: normalized.SupersededBy,
	}
	entries, diagnostics, paths, err := classifyFleetBranchesWithPaths(ctx, sweep)
	if err != nil {
		return BranchCleanupOutcome{}, err
	}
	sortBranchEntries(entries)

	results := planBranchCleanup(entries, sweep)

	if !normalized.Apply {
		return BranchCleanupOutcome{
			Base: normalized.Base, Scope: normalized.Scope, Apply: false,
			Results: results, Diagnostics: diagnostics, Totals: tallyCleanupOutcomes(results),
			ElapsedMS: time.Since(started).Milliseconds(),
		}, nil
	}

	reportDir := normalized.ReportDir
	if reportDir == "" {
		resolution, err := wbhome.Resolve(normalized.ProjectsRoot)
		if err != nil {
			return BranchCleanupOutcome{}, fmt.Errorf("resolve WB home for branch cleanup report: %w", err)
		}
		reportDir = DefaultBranchCleanupReportDir(resolution.Write.Home, now)
	}
	// durable-audit: the plan is written before the first destructive Git
	// operation, then rewritten as each candidate's outcome is known.
	reportPath, err := writeBranchCleanupReport(reportDir, normalized, now, results)
	if err != nil {
		return BranchCleanupOutcome{}, err
	}

	if err := validatePeerEvidence(normalized, results, now); err != nil {
		for index := range results {
			if results[index].Eligible && results[index].Scope == BranchScopeRemote {
				results[index].Eligible = false
				results[index].Outcome = "failed"
				results[index].Error = err.Error()
			}
		}
		if _, writeErr := writeBranchCleanupReport(reportDir, normalized, now, results); writeErr != nil {
			return BranchCleanupOutcome{}, writeErr
		}
		return BranchCleanupOutcome{Base: normalized.Base, Scope: normalized.Scope, Apply: true, Results: results, Diagnostics: diagnostics, Totals: tallyCleanupOutcomes(results), ReportPath: reportPath, ElapsedMS: time.Since(started).Milliseconds()}, nil
	}
	normalized.ReportDir = reportDir
	applyBranchCleanup(ctx, results, paths, normalized, now)

	if _, err := writeBranchCleanupReport(reportDir, normalized, now, results); err != nil {
		return BranchCleanupOutcome{}, err
	}

	return BranchCleanupOutcome{
		Base: normalized.Base, Scope: normalized.Scope, Apply: true,
		Results: results, Diagnostics: diagnostics, Totals: tallyCleanupOutcomes(results),
		ReportPath: reportPath, ElapsedMS: time.Since(started).Milliseconds(),
	}, nil
}

// planBranchCleanup decides, for every classified branch, whether it is
// eligible for deletion. Contained branches always qualify; receipted ones
// qualify only when the run enabled --receipts (they cannot arise otherwise).
// absorbed, unique, protected, in-use, and unreadable are always reported,
// never eligible. A remote candidate additionally requires pull-request
// evidence:
// an open PR refuses it outright, and evidence WB could not obtain refuses
// every remote candidate in the run, never only the ones it touched.
func planBranchCleanup(entries []BranchEntry, sweep branchSweepOptions) []BranchCleanupResult {
	remoteEvidenceUnavailable := remotePullRequestEvidenceUnavailable(entries, sweep)
	results := make([]BranchCleanupResult, 0, len(entries))
	for _, entry := range entries {
		result := BranchCleanupResult{BranchEntry: entry, Outcome: "skipped"}
		switch {
		case !entry.SupersededAtOrigin && entry.Disposition != BranchContained && entry.Disposition != BranchReceipted:
			result.SkipReason = skipReasonForDisposition(entry)
		case sweep.OlderThan > 0 && !entry.CommitterDate.IsZero() && sweep.Now.Sub(entry.CommitterDate) < sweep.OlderThan:
			result.SkipReason = fmt.Sprintf("branch is younger than --older-than %s", sweep.OlderThan)
		case entry.Scope == BranchScopeRemote && remoteEvidenceUnavailable:
			result.SkipReason = "remote pull-request evidence unavailable; refusing every remote deletion in this run"
		case entry.Scope == BranchScopeRemote && entry.OpenPullRequest != nil:
			result.SkipReason = fmt.Sprintf("branch is the head of open pull request %s", entry.OpenPullRequest.URL)
		default:
			result.Eligible = true
			result.Outcome = "planned"
		}
		results = append(results, result)
	}
	return results
}

// skipReasonForDisposition prefers the entry's own tailored Reason, then its
// classification Evidence, before falling back to a generic disposition
// message. contained/absorbed/in-use dispositions already carry a tailored
// Reason. unreadable, unique, and protected never do — they carry only the
// Evidence gathered while classifying them (for example the exact `git
// fetch` failure that made a repository's whole branch set unreadable). That
// Evidence is exactly what `wb branch list` already prints for the same
// entry, so dropping it here silently discarded the one actionable detail an
// operator needs to act on a skip row.
func skipReasonForDisposition(entry BranchEntry) string {
	if entry.Reason != "" {
		return entry.Reason
	}
	if entry.Evidence != "" {
		return entry.Evidence
	}
	return fmt.Sprintf("disposition %s is never eligible for --apply", entry.Disposition)
}

// remotePullRequestEvidenceUnavailable reports whether WB could not query
// pull-request evidence for at least one remote contained candidate. Any
// failure fails the whole remote scope closed for this run, never only the
// branch that happened to be queried first.
func remotePullRequestEvidenceUnavailable(entries []BranchEntry, sweep branchSweepOptions) bool {
	if sweep.Scope == BranchScopeLocal {
		return false
	}
	for _, entry := range entries {
		if entry.Scope == BranchScopeRemote &&
			(entry.Disposition == BranchContained || entry.Disposition == BranchReceipted) &&
			entry.PullRequestQueryFailed {
			return true
		}
	}
	return false
}

func tallyCleanupOutcomes(results []BranchCleanupResult) map[string]int {
	totals := map[string]int{}
	for _, result := range results {
		totals[result.Outcome]++
	}
	return totals
}

// applyBranchCleanup deletes every eligible candidate after repeating its
// evidence check against freshly fetched state. A branch that moved between
// plan and apply refuses only itself, with the moved SHA reported, and never
// aborts the run.
func applyBranchCleanup(ctx context.Context, results []BranchCleanupResult, paths map[string]string, options BranchCleanupOptions, now time.Time) {
	for index := range results {
		result := &results[index]
		if !result.Eligible {
			continue
		}
		path, ok := paths[result.Repository]
		if !ok {
			result.Outcome, result.Error = "failed", "repository path was not retained from the plan"
			continue
		}
		if result.SupersededAtOrigin {
			bundle, err := archiveReviewedBranch(ctx, options.ReportDir, path, *result)
			if err != nil {
				result.Outcome, result.Error = "failed", fmt.Sprintf("create verified recovery bundle: %v", err)
				continue
			}
			result.RecoveryBundle = bundle
		}
		if result.Scope == BranchScopeLocal {
			applyLocalBranchDeletion(ctx, path, result, options)
			continue
		}
		applyRemoteBranchDeletion(ctx, path, result, options)
	}
}

type reviewedBranchRecoveryManifest struct {
	Repository    string `json:"repository"`
	Branch        string `json:"branch"`
	Head          string `json:"head"`
	Target        string `json:"target"`
	Receipt       string `json:"receipt"`
	ReceiptSHA256 string `json:"receipt_sha256"`
	Bundle        string `json:"bundle"`
	BundleSHA256  string `json:"bundle_sha256"`
}

// archiveReviewedBranch preserves the exact source independently of the
// source clone, then proves the bundle restores that exact head into a new
// empty repository before deletion is permitted.
func archiveReviewedBranch(ctx context.Context, reportDir, repositoryPath string, result BranchCleanupResult) (string, error) {
	key := strings.NewReplacer("/", "_", "\\", "_").Replace(result.Repository + "--" + result.Branch + "-")
	recoveryRoot := filepath.Join(reportDir, "recovery")
	if err := os.MkdirAll(recoveryRoot, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(recoveryRoot, key)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	bundle := filepath.Join(dir, "source.bundle")
	sourceRef := "refs/heads/" + result.Branch
	if result.Scope == BranchScopeRemote {
		sourceRef = "refs/remotes/origin/" + result.Branch
	}
	if _, err := git(ctx, repositoryPath, "bundle", "create", bundle, sourceRef); err != nil {
		return "", err
	}
	if err := os.Chmod(bundle, 0o600); err != nil {
		return "", err
	}
	receipt, err := os.ReadFile(result.SupersessionReceipt)
	if err != nil {
		return "", err
	}
	if result.SupersessionSHA256 == "" || fmt.Sprintf("%x", sha256.Sum256(receipt)) != result.SupersessionSHA256 {
		return "", errors.New("supersession receipt bytes changed after planning")
	}
	copyPath := filepath.Join(dir, "supersession.json")
	if err := os.WriteFile(copyPath, receipt, 0o600); err != nil {
		return "", err
	}
	bundleBytes, err := os.ReadFile(bundle)
	if err != nil {
		return "", err
	}
	manifest := reviewedBranchRecoveryManifest{Repository: result.Repository, Branch: result.Branch, Head: result.SHA, Target: result.TargetSHA, Receipt: "supersession.json", ReceiptSHA256: fmt.Sprintf("%x", sha256.Sum256(receipt)), Bundle: "source.bundle", BundleSHA256: fmt.Sprintf("%x", sha256.Sum256(bundleBytes))}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	verify, err := os.MkdirTemp("", "wb-reviewed-branch-verify-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(verify)
	if _, err := git(ctx, verify, "init", "--bare"); err != nil {
		return "", err
	}
	if _, err := git(ctx, verify, "fetch", bundle, sourceRef+":refs/heads/recovery"); err != nil {
		return "", err
	}
	restored, err := git(ctx, verify, "rev-parse", "refs/heads/recovery")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(restored) != result.SHA {
		return "", fmt.Errorf("restored head %s does not match %s", strings.TrimSpace(restored), result.SHA)
	}
	return dir, nil
}

func applyLocalBranchDeletion(ctx context.Context, repositoryPath string, result *BranchCleanupResult, options BranchCleanupOptions) {
	freshTarget, err := fetchRemoteTargetHead(ctx, repositoryPath, result.Base)
	if err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("refetch exact origin/%s target: %v", result.Base, err)
		return
	}
	currentSHA, err := git(ctx, repositoryPath, "rev-parse", "--verify", "refs/heads/"+result.Branch)
	if err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("branch no longer exists: %v", err)
		return
	}
	currentSHA = strings.TrimSpace(currentSHA)
	if currentSHA != result.SHA {
		result.Outcome, result.Error = "failed", fmt.Sprintf("branch moved from %s to %s between plan and apply; refusing", shortSHA(result.SHA), shortSHA(currentSHA))
		return
	}
	if !recheckBranchRetirementGuards(ctx, options.ProjectsRoot, repositoryPath, result) {
		return
	}
	if !recheckDeletionEvidence(ctx, repositoryPath, currentSHA, freshTarget, result) {
		return
	}
	canonical, err := openCanonicalRepository(repositoryPath)
	if err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("open canonical repository: %v", err)
		return
	}
	defer canonical.close()
	if _, err := gitCanonical(ctx, canonical, "update-ref", "-d", "refs/heads/"+result.Branch, currentSHA); err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("compare-and-delete refs/heads/%s: %v", result.Branch, err)
		return
	}
	result.Applied, result.Outcome = true, "deleted"
}

// recheckDeletionEvidence repeats, against the freshly fetched target, the
// evidence that made this candidate eligible. A contained branch must still be
// an ancestor. A receipted branch fails ancestry by construction, so it must
// instead re-prove its receipt: the recorded landing commit still contained in
// the fresh target, and the three-way proof still holding against it — which
// is what refuses work reverted between plan and apply. On failure the result
// is marked and false is returned. See
// #req:receipted-requires-a-proved-landing.
func recheckDeletionEvidence(ctx context.Context, repositoryPath, currentSHA, freshTarget string, result *BranchCleanupResult) bool {
	if result.SupersededAtOrigin {
		digest, digestErr := supersessionFileSHA256(result.SupersessionReceipt)
		if digestErr != nil || digest != result.SupersessionSHA256 {
			result.Outcome = "failed"
			result.Error = "supersession receipt bytes changed after planning; refusing"
			return false
		}
		_, rejection, err := branchSupersessionReceipt(ctx, result.SupersessionReceipt, ListResult{
			Repository: result.Repository, Branch: result.Branch, HeadSHA: currentSHA,
			Base: result.Base, RemoteTargetSHA: freshTarget, CanonicalDir: repositoryPath,
		})
		if err != nil || rejection != "" {
			result.Outcome = "failed"
			if err != nil {
				result.Error = fmt.Sprintf("recheck supersession receipt: %v", err)
			} else {
				result.Error = rejection
			}
			return false
		}
		return true
	}
	if result.Disposition == BranchReceipted {
		if !isGitObjectID(result.LandingSHA) {
			result.Outcome, result.Error = "failed", "receipted plan carries no landing commit; refusing"
			return false
		}
		landed, err := isAncestor(ctx, repositoryPath, result.LandingSHA, freshTarget)
		if err != nil || !landed {
			result.Outcome = "failed"
			if err != nil {
				result.Error = fmt.Sprintf("recheck landing containment: %v", err)
			} else {
				result.Error = fmt.Sprintf("landing commit %s is no longer contained in the freshly fetched target", shortSHA(result.LandingSHA))
			}
			return false
		}
		proved, err := contentAbsorbed(ctx, repositoryPath, currentSHA, result.LandingSHA, freshTarget)
		if err != nil || !proved {
			result.Outcome = "failed"
			if err != nil {
				result.Error = fmt.Sprintf("recheck receipt proof: %v", err)
			} else {
				result.Error = "receipt no longer holds against the freshly fetched target; the work may have been reverted"
			}
			return false
		}
		return true
	}
	contained, err := isAncestor(ctx, repositoryPath, currentSHA, freshTarget)
	if err != nil || !contained {
		result.Outcome = "failed"
		if err != nil {
			result.Error = fmt.Sprintf("recheck containment: %v", err)
		} else {
			result.Error = "branch is no longer an ancestor of the freshly fetched target"
		}
		return false
	}
	return true
}

func applyRemoteBranchDeletion(ctx context.Context, repositoryPath string, result *BranchCleanupResult, options BranchCleanupOptions) {
	if _, err := git(ctx, repositoryPath, "fetch", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("refetch --prune origin: %v", err)
		return
	}
	freshTarget, err := fetchRemoteTargetHead(ctx, repositoryPath, result.Base)
	if err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("refetch exact origin/%s target: %v", result.Base, err)
		return
	}
	observedSHA, err := remoteBranchHead(ctx, repositoryPath, result.Branch)
	if err != nil || observedSHA == "" {
		result.Outcome = "failed"
		if err != nil {
			result.Error = fmt.Sprintf("re-resolve remote branch: %v", err)
		} else {
			result.Error = "remote branch no longer exists"
		}
		return
	}
	if observedSHA != result.SHA {
		result.Outcome, result.Error = "failed", fmt.Sprintf("remote branch moved from %s to %s between plan and apply; refusing", shortSHA(result.SHA), shortSHA(observedSHA))
		return
	}
	if !recheckBranchRetirementGuards(ctx, options.ProjectsRoot, repositoryPath, result) {
		return
	}
	if !recheckDeletionEvidence(ctx, repositoryPath, observedSHA, freshTarget, result) {
		return
	}
	pullRequests, err := githubPullRequests(ctx, repositoryPath, result.Repository, observedSHA)
	if err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("recheck pull-request evidence: %v", err)
		return
	}
	if open, _ := matchingPullRequests(pullRequests, result.Repository, result.Base, result.Branch, observedSHA); open != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("branch became the head of open pull request %s", open.URL)
		return
	}
	canonical, err := openCanonicalRepository(repositoryPath)
	if err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("open canonical repository: %v", err)
		return
	}
	defer canonical.close()
	pushSpec := "--force-with-lease=refs/heads/" + result.Branch + ":" + observedSHA
	if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "push", pushSpec, "origin", ":refs/heads/"+result.Branch); err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("force-with-lease delete refs/heads/%s: %v", result.Branch, err)
		return
	}
	result.Applied, result.Outcome = true, "deleted"
}

// recheckBranchRetirementGuards repeats the non-content guards after the
// durable plan/archive work. A branch can become checked out or claimed while
// the receipt is being archived, so plan-time classification is insufficient.
func recheckBranchRetirementGuards(ctx context.Context, projectsRoot, repositoryPath string, result *BranchCleanupResult) bool {
	head, err := git(ctx, repositoryPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		result.Outcome, result.Error = "failed", fmt.Sprintf("recheck canonical HEAD: %v", err)
		return false
	}
	if isProtectedBranch(result.Branch, result.Base, strings.TrimSpace(head)) {
		result.Outcome, result.Error = "failed", "branch is now protected; refusing deletion"
		return false
	}
	checkedOut, diagnostic := checkedOutLocalBranches(ctx, repositoryPath)
	if diagnostic != "" {
		result.Outcome, result.Error = "failed", diagnostic
		return false
	}
	if projectsRoot != "" {
		claims, diagnostic := branchInUseIndex(ctx, projectsRoot, "")
		if diagnostic != "" {
			result.Outcome, result.Error = "failed", diagnostic
			return false
		}
		if _, claimed := claims[branchInUseKey(result.Repository, result.Branch)]; claimed {
			result.Outcome, result.Error = "failed", "branch became claimed by a live WB work log"
			return false
		}
	}
	if checkedOut[result.Branch] {
		result.Outcome, result.Error = "failed", "branch became checked out in a linked worktree"
		return false
	}
	return true
}

type branchCleanupReport struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Base        string                `json:"base"`
	Scope       string                `json:"scope"`
	Apply       bool                  `json:"apply"`
	OlderThan   string                `json:"older_than"`
	Results     []BranchCleanupResult `json:"results"`
}

func writeBranchCleanupReport(reportDir string, options BranchCleanupOptions, now time.Time, results []BranchCleanupResult) (string, error) {
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return "", fmt.Errorf("create branch cleanup report directory: %w", err)
	}
	report := branchCleanupReport{
		GeneratedAt: now, Base: options.Base, Scope: options.Scope, Apply: options.Apply,
		OlderThan: options.OlderThan.String(), Results: results,
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode branch cleanup report: %w", err)
	}
	content = append(content, '\n')
	path := filepath.Join(reportDir, "cleanup.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return "", fmt.Errorf("write branch cleanup report: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", fmt.Errorf("activate branch cleanup report: %w", err)
	}
	return path, nil
}
