package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreebranches"
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
	Description  string
	Replacement  []string
	Retention    string
	Annotate     bool
	Manifest     string
	Apply        bool
	ReportDir    string
	Now          func() time.Time
}

type BranchQuarantineManifest struct {
	Entries []BranchQuarantineRequest `json:"entries"`
}
type BranchQuarantineRequest = worktreebranches.BranchQuarantineRequest
type BranchQuarantineResult = worktreebranches.BranchQuarantineResult
type BranchQuarantineOutcome struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Apply       bool                     `json:"apply"`
	Results     []BranchQuarantineResult `json:"results"`
	ReportPath  string                   `json:"report_path,omitempty"`
}

func BranchQuarantine(ctx context.Context, options BranchQuarantineOptions) (BranchQuarantineOutcome, error) {
	return branchQuarantineWithOps(ctx, options, realBranchQuarantineOps(), nil)
}

// branchQuarantineWithOps shares one operation set across planning and the
// immediate apply recheck. Production supplies real operations; a caller-local
// report injector can exercise the same durable checkpoints without global hooks.
func branchQuarantineWithOps(ctx context.Context, options BranchQuarantineOptions, ops branchQuarantinePlanOps, reportInjector *filewrite.Injector) (BranchQuarantineOutcome, error) {
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
		var result BranchQuarantineResult
		if options.Annotate {
			result = planBranchAnnotationWithOps(ctx, options.ProjectsRoot, paths[request.Repository], request, now, ops)
		} else {
			result = planBranchQuarantineWithOps(ctx, options.ProjectsRoot, paths[request.Repository], request, now, ops)
		}
		if result.ReceiptID == "" && result.SHA != "" {
			result.ReceiptID = quarantineReceiptID(request.Repository, request.Ref, result.SHA, now)
		}
		result.ReviewedAt = now
		results = append(results, result)
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
	if err := writeQuarantineReportInjected(outcome.ReportPath, outcome, reportInjector); err != nil {
		return outcome, err
	}
	for i := range outcome.Results {
		if outcome.Results[i].Outcome != "planned" {
			continue
		}
		// This is the pre-CAS checkpoint. A crash after it leaves an exact
		// source/destination/reason record for recovery before any ref moves.
		if err := writeQuarantineReportInjected(outcome.ReportPath, outcome, reportInjector); err != nil {
			return outcome, err
		}
		applyBranchQuarantineWithOps(ctx, options.ProjectsRoot, paths[outcome.Results[i].Repository], &outcome.Results[i], ops, func() error {
			return writeQuarantineReportInjected(outcome.ReportPath, outcome, reportInjector)
		})
		if err := writeQuarantineReportInjected(outcome.ReportPath, outcome, reportInjector); err != nil {
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
	if options.Annotate {
		options.Repository, options.Branch, options.SHA = strings.TrimSpace(options.Repository), strings.TrimSpace(options.Branch), strings.TrimSpace(options.SHA)
		options.Reason, options.Description, options.Retention = strings.TrimSpace(options.Reason), strings.TrimSpace(options.Description), strings.TrimSpace(options.Retention)
		for i := range options.Replacement {
			options.Replacement[i] = strings.TrimSpace(options.Replacement[i])
		}
		if options.Manifest != "" || options.Repository == "" || options.Branch == "" || options.SHA == "" || options.Reason == "" {
			return nil, fmt.Errorf("--annotate requires --repo, --branch, exact --sha, and --reason; it cannot use --manifest")
		}
		if !isRetiredBranch(options.Branch) || !isGitObjectID(options.SHA) {
			return nil, fmt.Errorf("--annotate requires an exact local retired/* ref and valid SHA")
		}
		if parts := strings.Split(options.Repository, "/"); len(parts) != 2 || !validRepositorySegment(parts[0]) || !validRepositorySegment(parts[1]) {
			return nil, fmt.Errorf("invalid quarantine repository %q", options.Repository)
		}
		if err := branchValidationError(context.Background(), "quarantine retired ref", options.Branch); err != nil {
			return nil, err
		}
		if err := validateQuarantineMetadata(options.Repository, options.Branch, options.Description, options.Reason, options.Retention, options.Replacement); err != nil {
			return nil, err
		}
		return []BranchQuarantineRequest{{Repository: strings.TrimSpace(options.Repository), Ref: strings.TrimSpace(options.Branch), SHA: strings.TrimSpace(options.SHA), Reason: strings.TrimSpace(options.Reason), Description: strings.TrimSpace(options.Description), Replacement: options.Replacement, Retention: strings.TrimSpace(options.Retention)}}, nil
	}
	if options.Manifest != "" {
		if options.Repository != "" || options.Branch != "" || options.SHA != "" || options.Reason != "" || options.Description != "" || len(options.Replacement) != 0 || options.Retention != "" {
			return nil, fmt.Errorf("--manifest cannot be combined with per-entry flags")
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
	return validateQuarantineRequests([]BranchQuarantineRequest{{Repository: options.Repository, Ref: options.Branch, SHA: options.SHA, Reason: options.Reason, Description: options.Description, Replacement: options.Replacement, Retention: options.Retention}})
}

func validateQuarantineRequests(requests []BranchQuarantineRequest) ([]BranchQuarantineRequest, error) {
	seen := map[string]bool{}
	for i := range requests {
		r := &requests[i]
		r.Repository, r.Ref, r.SHA, r.Reason = strings.TrimSpace(r.Repository), strings.TrimSpace(r.Ref), strings.TrimSpace(r.SHA), strings.TrimSpace(r.Reason)
		r.Description, r.Retention = strings.TrimSpace(r.Description), strings.TrimSpace(r.Retention)
		for j := range r.Replacement {
			r.Replacement[j] = strings.TrimSpace(r.Replacement[j])
		}
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
		if err := validateQuarantineMetadata(r.Repository, r.Ref, r.Description, r.Reason, r.Retention, r.Replacement); err != nil {
			return nil, err
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

func validateQuarantineMetadata(repository, ref, description, reason, retention string, replacement []string) error {
	for _, field := range append([]string{description, reason, retention}, replacement...) {
		if strings.ContainsAny(field, "\r\n\x00") {
			return fmt.Errorf("quarantine metadata for %s/%s cannot contain line breaks or NUL", repository, ref)
		}
	}
	return nil
}

func quarantineRepositoryPaths(root string, requests []BranchQuarantineRequest) (map[string]string, error) {
	repositories, err := branchInventoryService().DiscoverBranchRepositories(root, "")
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, repository := range repositories {
		paths[repository.Slug] = repository.Path
	}
	for _, request := range requests {
		if paths[request.Repository] == "" {
			return nil, fmt.Errorf("quarantine repository %q was not discovered", request.Repository)
		}
	}
	return paths, nil
}

// branchQuarantinePlanOps keeps the safety decisions testable without starting
// git or gh. The production entry point always supplies the real operations.
type branchQuarantinePlanOps struct {
	git          func(context.Context, string, ...string) (string, error)
	checkedOut   func(context.Context, string) (map[string]bool, string)
	inUse        func(context.Context, string, string) (map[string]string, string)
	pullRequests func(context.Context, string, string, string) ([]githubPullRequest, error)
	openBasePull func(context.Context, string, string, string) (*PullRequest, error)
	retire       func(context.Context, string, string, string, string, string) error
	annotate     func(context.Context, string, string, string, string) error
	commit       func(context.Context, string, string, string) (string, error)
}

func realBranchQuarantineOps() branchQuarantinePlanOps {
	return branchQuarantinePlanOps{
		git: git, checkedOut: checkedOutLocalBranches, inUse: branchInUseIndex,
		pullRequests: githubPullRequests, openBasePull: openPullRequestUsingBranchAsBase,
		retire: atomicLocalBranchRetirement, annotate: atomicLocalRetiredAnnotation, commit: quarantineMetadataCommit,
	}
}

func branchQuarantineService(ops branchQuarantinePlanOps) worktreebranches.InventoryService {
	return worktreebranches.InventoryService{Ports: worktreebranches.InventoryPorts{
		Git: ops.git, CheckedOut: ops.checkedOut, InUse: ops.inUse,
		OpenHeadPull: func(ctx context.Context, path, repository, branch, head string) (*PullRequest, error) {
			pulls, err := ops.pullRequests(ctx, path, repository, head)
			if err != nil {
				return nil, err
			}
			open, _ := matchingPullRequests(pulls, repository, "main", branch, head)
			return open, nil
		},
		OpenBasePull: ops.openBasePull,
	}}
}

func planBranchQuarantineWithOps(ctx context.Context, projectsRoot, path string, request BranchQuarantineRequest, now time.Time, ops branchQuarantinePlanOps) BranchQuarantineResult {
	return branchQuarantineService(ops).PlanBranchQuarantine(ctx, projectsRoot, path, request, now)
}

func applyBranchQuarantineWithOps(ctx context.Context, projectsRoot, path string, result *BranchQuarantineResult, ops branchQuarantinePlanOps, checkpoint func() error) {
	if checkpoint == nil {
		checkpoint = func() error { return nil }
	}
	if ops.commit == nil || ops.retire == nil || ops.annotate == nil {
		result.Outcome, result.Error = "failed", "quarantine mutation operations are unavailable"
		return
	}
	request := result.BranchQuarantineRequest
	if strings.HasPrefix(request.Ref, "retired/") {
		current, refusal := inspectBranchQuarantineCandidate(ctx, projectsRoot, path, request, "", true, ops)
		if refusal != "" {
			result.Outcome, result.Error = "failed", refusal
			return
		}
		message := quarantineCommitMessage(request, current, result.ReceiptID, result.ReviewedAt)
		metadata, err := ops.commit(ctx, path, current, message)
		if err != nil {
			result.Outcome, result.Error = "failed", "create annotation commit: "+err.Error()
			return
		}
		result.OriginalSHA, result.RetiredSHA = current, metadata
		if err := checkpoint(); err != nil {
			result.Outcome, result.Error = "failed", "publish annotation receipt before CAS: "+err.Error()
			return
		}
		if err := ops.annotate(ctx, path, request.Ref, current, metadata); err != nil {
			result.Outcome, result.Error = "failed", "local CAS annotation: "+err.Error()
			return
		}
		result.Outcome = "annotated"
		return
	}
	current, refusal := inspectBranchQuarantineCandidate(ctx, projectsRoot, path, request, result.Destination, true, ops)
	if refusal != "" {
		result.Outcome, result.Error = "failed", refusal
		return
	}
	result.OriginalSHA = current
	message := quarantineCommitMessage(request, current, result.ReceiptID, result.ReviewedAt)
	metadata, err := ops.commit(ctx, path, current, message)
	if err != nil {
		result.Outcome, result.Error = "failed", "create retirement commit: "+err.Error()
		return
	}
	result.RetiredSHA = metadata
	if err := checkpoint(); err != nil {
		result.Outcome, result.Error = "failed", "publish retirement receipt before CAS: "+err.Error()
		return
	}
	if err := ops.retire(ctx, path, result.Ref, result.Destination, current, metadata); err != nil {
		result.Outcome, result.Error = "failed", "local CAS retirement: "+err.Error()
		return
	}
	result.Outcome = "quarantined"
}

const quarantineDocsURL = "https://github.com/sneat-dev/wb/blob/main/ai/skills/wb-branches/SKILL.md#local-quarantine"

func quarantineReceiptID(repository, ref, sha string, reviewedAt time.Time) string {
	digest := sha256.Sum256([]byte(repository + "\x00" + ref + "\x00" + sha + "\x00" + reviewedAt.UTC().Format(time.RFC3339Nano)))
	return fmt.Sprintf("wb-quarantine-%x", digest[:8])
}

func quarantineCommitMessage(request BranchQuarantineRequest, original, receiptID string, reviewedAt time.Time) string {
	retention := request.Retention
	if retention == "" {
		retention = "Not specified; retained until explicitly removed. WB does not automatically expire retired refs."
	}
	description := request.Description
	if description == "" {
		description = "No additional description supplied."
	}
	var b strings.Builder
	title := description
	if title == "No additional description supplied." {
		title = request.Reason
	}
	fmt.Fprintf(&b, "retire: %s\n\n", title)
	fmt.Fprintf(&b, "Preserved repository: %s\n", request.Repository)
	fmt.Fprintf(&b, "Preserved ref: %s\n", request.Ref)
	fmt.Fprintf(&b, "Original commit: %s\n", original)
	fmt.Fprintf(&b, "Description: %s\n", description)
	fmt.Fprintf(&b, "Reason: %s\n", request.Reason)
	fmt.Fprintf(&b, "Reviewed at (UTC): %s\n", reviewedAt.UTC().Format(time.RFC3339))
	if len(request.Replacement) == 0 {
		b.WriteString("Superseded by: none recorded\n")
	} else {
		fmt.Fprintf(&b, "Superseded by: %s\n", strings.Join(request.Replacement, ", "))
	}
	fmt.Fprintf(&b, "Retention: %s\n", retention)
	fmt.Fprintf(&b, "WB receipt: %s\n", receiptID)
	fmt.Fprintf(&b, "Details: %s\n", quarantineDocsURL)
	return b.String()
}

func planBranchAnnotationWithOps(ctx context.Context, projectsRoot, path string, request BranchQuarantineRequest, now time.Time, ops branchQuarantinePlanOps) BranchQuarantineResult {
	result := BranchQuarantineResult{BranchQuarantineRequest: request, Outcome: "refused"}
	current, refusal := inspectBranchQuarantineCandidate(ctx, projectsRoot, path, request, "", false, ops)
	if refusal != "" {
		result.Error = refusal
		return result
	}
	result.SHA = current
	result.ReceiptID = quarantineReceiptID(request.Repository, request.Ref, current, now)
	result.OriginalSHA, result.RetiredSHA = current, current
	result.Outcome = "planned"
	return result
}

func inspectBranchQuarantineCandidate(
	ctx context.Context,
	projectsRoot, path string,
	request BranchQuarantineRequest,
	destination string,
	applying bool,
	ops branchQuarantinePlanOps,
) (string, string) {
	return branchQuarantineService(ops).InspectBranchQuarantineCandidate(ctx, projectsRoot, path, request, destination, applying)
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

func quarantineMetadataCommit(ctx context.Context, path, parent, message string) (string, error) {
	tree, err := git(ctx, path, "rev-parse", "--verify", parent+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("read preserved tree: %w", err)
	}
	output, err := runGitWithInput(ctx, path, []byte(message), "commit-tree", tree, "-p", parent, "-F", "-")
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w: %s", err, output)
	}
	sha := strings.TrimSpace(output)
	if !isGitObjectID(sha) {
		return "", fmt.Errorf("git commit-tree returned invalid object id %q", sha)
	}
	return sha, nil
}

func atomicLocalBranchRetirement(ctx context.Context, path, source, destination, expected, replacement string) error {
	output, err := runGitWithInput(ctx, path, []byte("start\ncreate refs/heads/"+destination+" "+replacement+"\ndelete refs/heads/"+source+" "+expected+"\nprepare\ncommit\n"), "update-ref", "--stdin")
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func atomicLocalRetiredAnnotation(ctx context.Context, path, ref, expected, replacement string) error {
	output, err := runGitWithInput(ctx, path, nil, "update-ref", "refs/heads/"+ref, replacement, expected)
	if err != nil {
		return fmt.Errorf("%v: %s", err, output)
	}
	return nil
}

func runGitWithInput(ctx context.Context, path string, input []byte, args ...string) (string, error) {
	commandRunner, ok := ctx.Value(gitRunnerContextKey{}).(runner.Runner)
	if !ok {
		commandRunner = runner.New()
	}
	result, err := commandRunner.RunWithInput(ctx, path, input, "git", append([]string{"-C", path}, args...)...)
	output := strings.TrimSpace(result.Stdout + result.Stderr)
	if err != nil && output == "" {
		output = err.Error()
	}
	return output, err
}

// writeQuarantineReportInjected publishes every durable checkpoint through the
// same filewrite primitives. Production uses a nil injector; a caller-local
// injector reaches publication failures without replacing the checkpoint logic.
// The fixed temporary name (path+".tmp") is always fully overwritten, so this
// deliberately uses CreateOrTruncatePath rather than an exclusive create.
func writeQuarantineReportInjected(path string, outcome BranchQuarantineOutcome, inj *filewrite.Injector) error {
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + ".tmp"
	file, err := filewrite.CreateOrTruncatePath(temporary, 0o600, inj)
	if err != nil {
		return err
	}
	if err := filewrite.Write(file, data, temporary, inj); err != nil {
		_ = file.Close()
		return err
	}
	if err := filewrite.Sync(file, temporary, inj); err != nil {
		_ = file.Close()
		return err
	}
	if err := filewrite.Close(file, temporary, inj); err != nil {
		return err
	}
	if err := filewrite.Rename(temporary, path, inj); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
