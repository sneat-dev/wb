package streams

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/hooks"
)

// PreflightStatus is one member check's outcome.
type PreflightStatus string

const (
	// PreflightPass means the check ran and found nothing.
	PreflightPass PreflightStatus = "pass"
	// PreflightFail means the check ran and found a problem. A failing check
	// refuses the start unless the caller explicitly reported past it.
	PreflightFail PreflightStatus = "fail"
	// PreflightUnknown means the check could not be established. It is
	// reported, never silently treated as a pass: an unverified assurance is
	// worse than no gate.
	PreflightUnknown PreflightStatus = "unknown"
)

// PreflightFinding is one check's result for one member.
type PreflightFinding struct {
	Repository string          `json:"repository"`
	Check      string          `json:"check"`
	Status     PreflightStatus `json:"status"`
	Detail     string          `json:"detail,omitempty"`
}

// Preflight is the fleet-readiness report `wb stream start` produces before it
// creates anything.
//
// Implements: dependency-streams#req:stream-start-proves-the-fleet-is-ready,
// dependency-streams#req:push-hook-defers-to-ci-on-stream-branches (its
// concurrency clause).
type Preflight struct {
	Findings []PreflightFinding `json:"findings"`
}

// Failed reports the findings that must be resolved or explicitly accepted.
func (preflight Preflight) Failed() []PreflightFinding {
	var failed []PreflightFinding
	for _, finding := range preflight.Findings {
		if finding.Status == PreflightFail {
			failed = append(failed, finding)
		}
	}
	return failed
}

// Check names every preflight check WB runs, in the order it runs them.
// `verbs-state-and-deduplicate-their-work` requires a verb to say what it will
// run before running it, so the list is data rather than control flow.
const (
	CheckHooks               = "hooks-check"
	CheckNpmProviderIdentity = "npm-provider-identity"
	CheckRedMain             = "red-main"
	CheckStreamConcurrency   = "stream-pr-concurrency"
)

// PreflightChecks is the declared plan, in run order.
func PreflightChecks() []string {
	return []string{CheckHooks, CheckNpmProviderIdentity, CheckRedMain, CheckStreamConcurrency}
}

// PreflightInput is one member's coordinates for the readiness checks.
type PreflightInput struct {
	Repository string
	// Path is the checkout the checks read. It is the canonical clone at
	// start time: the stream worktrees do not exist yet, and refusing to
	// create them is the whole point of running these first.
	Path string
	// DefaultBranch is the branch the red-`main` check reads.
	DefaultBranch string
}

// HooksChecker answers "are this checkout's WB-managed hooks healthy" for the
// readiness preflight. It is a port so the refusal it drives is provable
// without installing real hooks into a temporary repository.
type HooksChecker func(path string) (findings []string, err error)

// InstalledHooksChecker is the production checker: the existing
// `wb hooks check`, called in process.
func InstalledHooksChecker(wbExecutable, projectsRoot string) HooksChecker {
	return func(path string) ([]string, error) {
		report, err := hooks.Check(path, "", wbExecutable, projectsRoot)
		if err != nil {
			return nil, err
		}
		messages := make([]string, 0, len(report.Findings))
		for _, finding := range report.Findings {
			messages = append(messages, finding.Message)
		}
		return messages, nil
	}
}

// RunPreflight runs every readiness check over the proposed members.
//
// Every check reports per repository. A check that cannot run reports
// PreflightUnknown with its reason rather than passing: this is the same rule
// `batch-verification-runs-what-ci-runs` applies to skipped mechanisms, and
// for the same reason — a false assurance is worse than no gate.
func RunPreflight(ctx context.Context, hub GitHub, checkHooksIn HooksChecker, inputs []PreflightInput) Preflight {
	preflight := Preflight{}
	declaredPackages := map[string][]string{}
	for _, input := range inputs {
		preflight.Findings = append(preflight.Findings, checkHooks(input, checkHooksIn))
		names, finding := collectNpmPackageNames(input)
		preflight.Findings = append(preflight.Findings, finding)
		for _, name := range names {
			declaredPackages[name] = append(declaredPackages[name], input.Repository)
		}
		preflight.Findings = append(preflight.Findings, checkRedMain(ctx, hub, input))
		preflight.Findings = append(preflight.Findings, checkStreamConcurrency(input))
	}
	preflight.Findings = append(preflight.Findings, ambiguousProviderFindings(declaredPackages)...)
	return preflight
}

func checkHooks(input PreflightInput, check HooksChecker) PreflightFinding {
	if check == nil {
		return PreflightFinding{Repository: input.Repository, Check: CheckHooks, Status: PreflightUnknown, Detail: "no hooks checker available"}
	}
	messages, err := check(input.Path)
	if err != nil {
		return PreflightFinding{Repository: input.Repository, Check: CheckHooks, Status: PreflightUnknown, Detail: err.Error()}
	}
	if len(messages) == 0 {
		return PreflightFinding{Repository: input.Repository, Check: CheckHooks, Status: PreflightPass}
	}
	return PreflightFinding{
		Repository: input.Repository,
		Check:      CheckHooks,
		Status:     PreflightFail,
		Detail:     strings.Join(messages, "; ") + " — run `wb hooks repair " + input.Path + "`",
	}
}

// collectNpmPackageNames reads the package names this repository publishes.
//
// The provider-identity question a stream must answer before it starts is
// "does exactly one member claim to publish each package name": two members
// declaring the same name make every later link and bump ambiguous, and the
// ambiguity is invisible until a consumer resolves the wrong one.
func collectNpmPackageNames(input PreflightInput) ([]string, PreflightFinding) {
	manifests, err := npmPackageManifests(input.Path)
	if err != nil {
		return nil, PreflightFinding{Repository: input.Repository, Check: CheckNpmProviderIdentity, Status: PreflightUnknown, Detail: err.Error()}
	}
	var names []string
	var unnamed []string
	for _, manifest := range manifests {
		// Preserve the existing repository-root provider identity used by
		// stream preflight. A nested workspace root describes that workspace,
		// not a package published by the repository.
		if manifest.Root && manifest.Workspace != "." {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(input.Path, filepath.FromSlash(manifest.Path)))
		if err != nil {
			return nil, PreflightFinding{Repository: input.Repository, Check: CheckNpmProviderIdentity, Status: PreflightUnknown, Detail: err.Error()}
		}
		var parsed struct {
			Name    string `json:"name"`
			Private bool   `json:"private"`
		}
		if err := json.Unmarshal(contents, &parsed); err != nil {
			return nil, PreflightFinding{Repository: input.Repository, Check: CheckNpmProviderIdentity, Status: PreflightUnknown, Detail: fmt.Sprintf("parse %s: %v", manifest.Path, err)}
		}
		if parsed.Private {
			continue
		}
		if strings.TrimSpace(parsed.Name) == "" {
			unnamed = append(unnamed, manifest.Path)
			continue
		}
		names = append(names, parsed.Name)
	}
	sort.Strings(names)
	if len(unnamed) > 0 {
		return names, PreflightFinding{
			Repository: input.Repository,
			Check:      CheckNpmProviderIdentity,
			Status:     PreflightFail,
			Detail:     "publishable manifests declare no package name: " + strings.Join(unnamed, ", "),
		}
	}
	return names, PreflightFinding{Repository: input.Repository, Check: CheckNpmProviderIdentity, Status: PreflightPass}
}

func ambiguousProviderFindings(declared map[string][]string) []PreflightFinding {
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	var findings []PreflightFinding
	for _, name := range names {
		owners := declared[name]
		if len(owners) < 2 {
			continue
		}
		sort.Strings(owners)
		for _, owner := range owners {
			findings = append(findings, PreflightFinding{
				Repository: owner,
				Check:      CheckNpmProviderIdentity,
				Status:     PreflightFail,
				Detail:     fmt.Sprintf("package %s is declared by more than one stream member: %s", name, strings.Join(owners, ", ")),
			})
		}
	}
	return findings
}

func checkRedMain(ctx context.Context, hub GitHub, input PreflightInput) PreflightFinding {
	if hub == nil {
		return PreflightFinding{Repository: input.Repository, Check: CheckRedMain, Status: PreflightUnknown, Detail: "no GitHub reader available"}
	}
	conclusion, err := hub.DefaultBranchStatus(ctx, input.Path, input.DefaultBranch)
	if err != nil {
		return PreflightFinding{Repository: input.Repository, Check: CheckRedMain, Status: PreflightUnknown, Detail: err.Error()}
	}
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "":
		return PreflightFinding{Repository: input.Repository, Check: CheckRedMain, Status: PreflightUnknown, Detail: "no completed run on " + input.DefaultBranch}
	case "success", "skipped", "neutral":
		return PreflightFinding{Repository: input.Repository, Check: CheckRedMain, Status: PreflightPass}
	default:
		return PreflightFinding{
			Repository: input.Repository,
			Check:      CheckRedMain,
			Status:     PreflightFail,
			Detail:     fmt.Sprintf("the last completed run on %s concluded %s; a stream started on a red base cannot tell its own breakage from the base's", input.DefaultBranch, conclusion),
		}
	}
}

func checkStreamConcurrency(input PreflightInput) PreflightFinding {
	workflows, err := ciaudit.StreamConcurrency(input.Path)
	if err != nil {
		return PreflightFinding{Repository: input.Repository, Check: CheckStreamConcurrency, Status: PreflightUnknown, Detail: err.Error()}
	}
	var pullRequestWorkflows []ciaudit.Concurrency
	for _, workflow := range workflows {
		if workflow.PullRequest {
			pullRequestWorkflows = append(pullRequestWorkflows, workflow)
		}
	}
	if len(pullRequestWorkflows) == 0 {
		return PreflightFinding{
			Repository: input.Repository,
			Check:      CheckStreamConcurrency,
			Status:     PreflightUnknown,
			Detail:     "no pull_request workflow found; the stream pull request would run no CI",
		}
	}
	var uncancelled []string
	for _, workflow := range pullRequestWorkflows {
		if workflow.Cancels() {
			continue
		}
		reason := "declares no concurrency group"
		switch {
		case workflow.Declared && !workflow.RefKeyed:
			reason = fmt.Sprintf("group %q is not keyed to the ref or pull request", workflow.Group)
		case workflow.Declared && !workflow.CancelInProgress:
			reason = "does not set cancel-in-progress: true"
		}
		uncancelled = append(uncancelled, workflow.Workflow+" "+reason)
	}
	if len(uncancelled) == 0 {
		return PreflightFinding{Repository: input.Repository, Check: CheckStreamConcurrency, Status: PreflightPass}
	}
	return PreflightFinding{
		Repository: input.Repository,
		Check:      CheckStreamConcurrency,
		Status:     PreflightFail,
		Detail:     "a superseded stream push will race its predecessor instead of cancelling it: " + strings.Join(uncancelled, "; "),
	}
}

// npmPackageManifest identifies a package manifest and the independent npm
// workspace that owns it. Paths are repository-relative and slash-separated.
type npmPackageManifest struct {
	Path      string
	Workspace string
	Root      bool
}

// npmPackageManifests lists workspace-root manifests and publishable
// `libs/**/package.json` manifests across every independent workspace in a
// repository. This follows the canonical dependency discovery's full-tree
// model while retaining local-link's deliberate `libs/**` publication scope.
func npmPackageManifests(root string) ([]npmPackageManifest, error) {
	workspaceRoots := map[string]bool{".": true}
	var packagePaths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root {
				if skippedNpmSourceDirectory(entry.Name()) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != "package.json" && entry.Name() != "pnpm-workspace.yaml" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Name() != "package.json" {
			workspaceRoots[filepath.ToSlash(filepath.Dir(relative))] = true
		} else {
			packagePaths = append(packagePaths, relative)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan npm manifests in %s: %w", root, err)
	}
	for _, path := range packagePaths {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var parsed struct {
			Workspaces json.RawMessage `json:"workspaces"`
		}
		if err := json.Unmarshal(contents, &parsed); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if len(parsed.Workspaces) > 0 && string(parsed.Workspaces) != "null" {
			workspaceRoots[filepath.ToSlash(filepath.Dir(path))] = true
		}
	}
	var manifests []npmPackageManifest
	for _, path := range packagePaths {
		workspace := owningNpmWorkspace(path, workspaceRoots)
		relative, err := filepath.Rel(filepath.FromSlash(workspace), filepath.FromSlash(path))
		if err != nil {
			return nil, fmt.Errorf("resolve npm workspace for %s: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		rootManifest := relative == "package.json"
		if !rootManifest && !strings.HasPrefix(relative, "libs/") {
			continue
		}
		manifests = append(manifests, npmPackageManifest{Path: path, Workspace: workspace, Root: rootManifest})
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].Path < manifests[j].Path })
	return manifests, nil
}

// skippedNpmSourceDirectory matches the canonical dependency graph's checkout
// exclusions and also omits generated frontend caches. Hidden directories are
// never source package roots; this covers WB state and Angular/Nx caches
// without allowing a stale managed checkout to become a provider identity.
func skippedNpmSourceDirectory(name string) bool {
	switch name {
	case "node_modules", "vendor", "testdata", "dist", "coverage":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func owningNpmWorkspace(manifest string, roots map[string]bool) string {
	workspace := "."
	manifestDir := filepath.ToSlash(filepath.Dir(manifest))
	for candidate := range roots {
		if candidate == "." {
			continue
		}
		if manifestDir == candidate || strings.HasPrefix(manifestDir, candidate+"/") {
			if workspace == "." || len(candidate) > len(workspace) {
				workspace = candidate
			}
		}
	}
	return workspace
}
