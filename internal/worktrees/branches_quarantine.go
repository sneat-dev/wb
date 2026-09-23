package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// BranchQuarantineOptions moves explicitly selected local refs into retired/*.
// It deliberately has no remote scope: a safe remote rename needs independent
// peer evidence and an exact remote lease, which this local operation cannot
// honestly prove.
type BranchQuarantineOptions struct {
	ProjectsRoot string
	Repository   string
	Branch       string
	SHA          string
	Reason       string
	Manifest     string
	Apply        bool
	ReportDir    string
	Now          func() time.Time
}

type BranchQuarantineManifest struct {
	Entries []BranchQuarantineRequest `json:"entries"`
}
type BranchQuarantineRequest struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	SHA        string `json:"sha"`
	Reason     string `json:"reason"`
}
type BranchQuarantineResult struct {
	BranchQuarantineRequest
	Destination string `json:"destination,omitempty"`
	Outcome     string `json:"outcome"`
	Error       string `json:"error,omitempty"`
}
type BranchQuarantineOutcome struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Apply       bool                     `json:"apply"`
	Results     []BranchQuarantineResult `json:"results"`
	ReportPath  string                   `json:"report_path,omitempty"`
}

func BranchQuarantine(ctx context.Context, options BranchQuarantineOptions) (BranchQuarantineOutcome, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	root, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return BranchQuarantineOutcome{}, err
	}
	options.ProjectsRoot = root
	requests, err := quarantineRequests(options)
	if err != nil {
		return BranchQuarantineOutcome{}, err
	}
	paths, err := quarantineRepositoryPaths(options.ProjectsRoot, requests)
	if err != nil {
		return BranchQuarantineOutcome{}, err
	}
	now := options.Now().UTC()
	results := make([]BranchQuarantineResult, 0, len(requests))
	for _, request := range requests {
		results = append(results, planBranchQuarantine(ctx, options.ProjectsRoot, paths[request.Repository], request, now))
	}
	// Flat names can collide (a/b and a-b). Refuse every colliding plan before
	// creating the report or mutating the first row, so a manifest is atomic in
	// its admission even though successful rows are applied independently.
	destinations := map[string][]int{}
	for i := range results {
		if results[i].Outcome == "planned" {
			key := results[i].Repository + "|" + results[i].Destination
			destinations[key] = append(destinations[key], i)
		}
	}
	for _, indexes := range destinations {
		if len(indexes) > 1 {
			for _, index := range indexes {
				results[index].Outcome, results[index].Error = "refused", "manifest destinations collide after flattening source names"
			}
		}
	}
	outcome := BranchQuarantineOutcome{GeneratedAt: now, Apply: options.Apply, Results: results}
	if !options.Apply {
		return outcome, nil
	}
	reportDir := options.ReportDir
	if reportDir == "" {
		home, err := wbhome.Resolve(options.ProjectsRoot)
		if err != nil {
			return outcome, err
		}
		reportDir = filepath.Join(home.Write.Home, "reports", "branch-quarantine", now.Format("20060102T150405.000000000Z"))
	}
	if err := validateBranchCleanupReportDir(ctx, reportDir, paths); err != nil {
		return outcome, err
	}
	if err := reserveQuarantineReportDir(reportDir); err != nil {
		return outcome, err
	}
	outcome.ReportPath = filepath.Join(reportDir, "quarantine.json")
	if err := writeQuarantineReport(outcome.ReportPath, outcome); err != nil {
		return outcome, err
	}
	for i := range outcome.Results {
		if outcome.Results[i].Outcome != "planned" {
			continue
		}
		// This is the pre-CAS checkpoint. A crash after it leaves an exact
		// source/destination/reason record for recovery before any ref moves.
		if err := writeQuarantineReport(outcome.ReportPath, outcome); err != nil {
			return outcome, err
		}
		applyBranchQuarantine(ctx, options.ProjectsRoot, paths[outcome.Results[i].Repository], &outcome.Results[i])
		if err := writeQuarantineReport(outcome.ReportPath, outcome); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// reserveQuarantineReportDir makes the known-safe parent hierarchy durable,
// then claims one run directory exclusively. This supports a fresh WB home
// while preserving the no-shared-report guarantee between concurrent runs.
func reserveQuarantineReportDir(reportDir string) error {
	parent := filepath.Dir(reportDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create quarantine report parent %s: %w", parent, err)
	}
	if err := syncDirectoryAndAncestors(parent); err != nil {
		return err
	}
	if err := os.Mkdir(reportDir, 0o700); err != nil {
		return fmt.Errorf("reserve exclusive quarantine report directory %s: %w", reportDir, err)
	}
	return syncDirectory(parent)
}

func quarantineRequests(options BranchQuarantineOptions) ([]BranchQuarantineRequest, error) {
	if options.Manifest != "" {
		if options.Repository != "" || options.Branch != "" || options.SHA != "" || options.Reason != "" {
			return nil, fmt.Errorf("--manifest cannot be combined with --repo, --branch, --sha, or --reason")
		}
		data, err := os.ReadFile(options.Manifest)
		if err != nil {
			return nil, fmt.Errorf("read quarantine manifest: %w", err)
		}
		var manifest BranchQuarantineManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("decode quarantine manifest: %w", err)
		}
		if len(manifest.Entries) == 0 {
			return nil, fmt.Errorf("quarantine manifest has no entries")
		}
		requests, err := validateQuarantineRequests(manifest.Entries)
		if err != nil {
			return nil, err
		}
		for _, request := range requests {
			if request.SHA == "" {
				return nil, fmt.Errorf("quarantine manifest requires exact sha for %s/%s", request.Repository, request.Ref)
			}
		}
		return requests, nil
	}
	if options.Repository == "" || options.Branch == "" || options.Reason == "" {
		return nil, fmt.Errorf("single quarantine requires --repo, --branch, and --reason")
	}
	return validateQuarantineRequests([]BranchQuarantineRequest{{Repository: options.Repository, Ref: options.Branch, SHA: options.SHA, Reason: options.Reason}})
}

func validateQuarantineRequests(requests []BranchQuarantineRequest) ([]BranchQuarantineRequest, error) {
	seen := map[string]bool{}
	for i := range requests {
		r := &requests[i]
		r.Repository, r.Ref, r.SHA, r.Reason = strings.TrimSpace(r.Repository), strings.TrimSpace(r.Ref), strings.TrimSpace(r.SHA), strings.TrimSpace(r.Reason)
		if parts := strings.Split(r.Repository, "/"); len(parts) != 2 || !validRepositorySegment(parts[0]) || !validRepositorySegment(parts[1]) {
			return nil, fmt.Errorf("invalid quarantine repository %q", r.Repository)
		}
		if err := branchValidationError(context.Background(), "quarantine source ref", r.Ref); err != nil {
			return nil, err
		}
		if isRetiredBranch(r.Ref) {
			return nil, fmt.Errorf("quarantine source %q is already retired", r.Ref)
		}
		if r.SHA != "" && !isGitObjectID(r.SHA) {
			return nil, fmt.Errorf("invalid quarantine SHA for %s/%s", r.Repository, r.Ref)
		}
		if r.Reason == "" {
			return nil, fmt.Errorf("quarantine reason is required for %s/%s", r.Repository, r.Ref)
		}
		key := r.Repository + "|" + r.Ref
		if seen[key] {
			return nil, fmt.Errorf("duplicate quarantine manifest entry %s", key)
		}
		seen[key] = true
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].Repository != requests[j].Repository {
			return requests[i].Repository < requests[j].Repository
		}
		return requests[i].Ref < requests[j].Ref
	})
	return requests, nil
}

func quarantineRepositoryPaths(root string, requests []BranchQuarantineRequest) (map[string]string, error) {
	repositories, err := discoverBranchRepositories(root, "")
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, repository := range repositories {
		paths[repository.Slug()] = repository.Path
	}
	for _, request := range requests {
		if paths[request.Repository] == "" {
			return nil, fmt.Errorf("quarantine repository %q was not discovered", request.Repository)
		}
	}
	return paths, nil
}

func planBranchQuarantine(ctx context.Context, projectsRoot, path string, request BranchQuarantineRequest, now time.Time) BranchQuarantineResult {
	result := BranchQuarantineResult{BranchQuarantineRequest: request, Outcome: "refused"}
	source, err := git(ctx, path, "rev-parse", "--verify", "refs/heads/"+request.Ref+"^{commit}")
	if err != nil {
		result.Error = "source ref unavailable: " + err.Error()
		return result
	}
	source = strings.TrimSpace(source)
	if request.SHA != "" && request.SHA != source {
		result.Error = fmt.Sprintf("source moved from manifest SHA %s to %s", shortSHA(request.SHA), shortSHA(source))
		return result
	}
	result.SHA = source
	head, _ := git(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if isProtectedBranch(request.Ref, "main", strings.TrimSpace(head)) {
		result.Error = "source is protected or the canonical current branch"
		return result
	}
	checked, diagnostic := checkedOutLocalBranches(ctx, path)
	if diagnostic != "" {
		result.Error = diagnostic
		return result
	}
	if checked[request.Ref] {
		result.Error = "source is checked out in a linked worktree"
		return result
	}
	if inUse, diagnostic := branchInUseIndex(ctx, projectsRoot, ""); diagnostic != "" {
		result.Error = diagnostic
		return result
	} else if _, claimed := inUse[branchInUseKey(request.Repository, request.Ref)]; claimed {
		result.Error = "source is claimed by a live WB work log"
		return result
	}
	pulls, err := githubPullRequests(ctx, path, request.Repository, source)
	if err != nil {
		result.Error = "cannot prove pull-request safety: " + err.Error()
		return result
	}
	if open, _ := matchingPullRequests(pulls, request.Repository, "main", request.Ref, source); open != nil {
		result.Error = "source is head of open pull request " + open.URL
		return result
	}
	if open, err := openPullRequestUsingBranchAsBase(ctx, path, request.Repository, request.Ref); err != nil {
		result.Error = "cannot prove pull-request base safety: " + err.Error()
		return result
	} else if open != nil {
		result.Error = "source is base of open pull request " + open.URL
		return result
	}
	result.Destination = retiredBranchDestination(now, request.Ref, source)
	if _, err := git(ctx, path, "rev-parse", "--verify", "refs/heads/"+result.Destination); err == nil {
		result.Error = "destination already exists"
		return result
	}
	result.Outcome = "planned"
	return result
}

var quarantineSegmentUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func retiredBranchDestination(now time.Time, source, sha string) string {
	flat := strings.ReplaceAll(source, "/", "-")
	flat = quarantineSegmentUnsafe.ReplaceAllString(flat, "-")
	flat = strings.Trim(flat, "-.")
	if flat == "" {
		flat = "branch"
	}
	return "retired/" + now.UTC().Format("20060102") + "-" + flat + "-" + shortSHA(sha)
}

func applyBranchQuarantine(ctx context.Context, projectsRoot, path string, result *BranchQuarantineResult) {
	current, err := git(ctx, path, "rev-parse", "--verify", "refs/heads/"+result.Ref+"^{commit}")
	if err != nil {
		result.Outcome, result.Error = "failed", "source disappeared before apply: "+err.Error()
		return
	}
	current = strings.TrimSpace(current)
	if current != result.SHA {
		result.Outcome, result.Error = "failed", fmt.Sprintf("source moved from %s to %s before apply", shortSHA(result.SHA), shortSHA(current))
		return
	}
	head, _ := git(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if isProtectedBranch(result.Ref, "main", strings.TrimSpace(head)) {
		result.Outcome, result.Error = "failed", "source became protected or the canonical current branch"
		return
	}
	checked, diagnostic := checkedOutLocalBranches(ctx, path)
	if diagnostic != "" {
		result.Outcome, result.Error = "failed", diagnostic
		return
	}
	if checked[result.Ref] {
		result.Outcome, result.Error = "failed", "source became checked out in a linked worktree"
		return
	}
	if _, err := git(ctx, path, "rev-parse", "--verify", "refs/heads/"+result.Destination); err == nil {
		result.Outcome, result.Error = "failed", "destination appeared before apply"
		return
	}
	pulls, err := githubPullRequests(ctx, path, result.Repository, current)
	if err != nil {
		result.Outcome, result.Error = "failed", "cannot re-prove pull-request safety: "+err.Error()
		return
	}
	if open, _ := matchingPullRequests(pulls, result.Repository, "main", result.Ref, current); open != nil {
		result.Outcome, result.Error = "failed", "source became head of open pull request "+open.URL
		return
	}
	if open, err := openPullRequestUsingBranchAsBase(ctx, path, result.Repository, result.Ref); err != nil {
		result.Outcome, result.Error = "failed", "cannot re-prove pull-request base safety: "+err.Error()
		return
	} else if open != nil {
		result.Outcome, result.Error = "failed", "source became base of open pull request "+open.URL
		return
	}
	if inUse, diagnostic := branchInUseIndex(ctx, projectsRoot, ""); diagnostic != "" {
		result.Outcome, result.Error = "failed", diagnostic
		return
	} else if _, claimed := inUse[branchInUseKey(result.Repository, result.Ref)]; claimed {
		result.Outcome, result.Error = "failed", "source became claimed by a live WB work log"
		return
	}
	if err := atomicLocalBranchRename(ctx, path, result.Ref, result.Destination, current); err != nil {
		result.Outcome, result.Error = "failed", "local CAS rename: "+err.Error()
		return
	}
	result.Outcome = "quarantined"
}

func openPullRequestUsingBranchAsBase(ctx context.Context, worktree, repository, branch string) (*PullRequest, error) {
	query := url.Values{"base": []string{branch}, "state": []string{"open"}}.Encode()
	response := githubobserver.Execute(ctx, worktree, "api", "--paginate", "repos/"+repository+"/pulls?"+query)
	if response.Err != nil {
		return nil, fmt.Errorf("query open pull requests with base %s: %w: %s", branch, response.Err, strings.TrimSpace(string(response.Stderr)+string(response.Stdout)))
	}
	var pulls []githubPullRequest
	if err := json.Unmarshal(response.Stdout, &pulls); err != nil {
		return nil, fmt.Errorf("decode open pull requests with base %s: %w", branch, err)
	}
	for _, pull := range pulls {
		if strings.EqualFold(pull.State, "open") && pull.Base.Ref == branch {
			return &PullRequest{Number: pull.Number, URL: pull.URL, Repository: repository, State: pull.State, Base: pull.Base.Ref, HeadSHA: pull.Head.SHA}, nil
		}
	}
	return nil, nil
}

func atomicLocalBranchRename(ctx context.Context, path, source, destination, sha string) error {
	command := exec.CommandContext(ctx, "git", "-C", path, "update-ref", "--stdin")
	command.Stdin = strings.NewReader("start\ncreate refs/heads/" + destination + " " + sha + "\ndelete refs/heads/" + source + " " + sha + "\nprepare\ncommit\n")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func writeQuarantineReport(path string, outcome BranchQuarantineOutcome) error {
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func QuarantineManifestDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]), nil
}
