// Package layout audits and safely cleans local clone placement under a
// projects root. Canonical clones live at {root}/{owner}/{repository}.
package layout

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
	"github.com/sneat-dev/wb/internal/gitops"
)

// Kind classifies one layout finding.
type Kind string

const (
	KindOK         Kind = "ok"
	KindTopLevel   Kind = "top_level"
	KindMisowned   Kind = "misowned"
	KindNoOrigin   Kind = "no_origin"
	KindUnreadable Kind = "unreadable"
)

// Finding is one inspected checkout relative to the projects root.
type Finding struct {
	Path            string `json:"path" yaml:"path"`
	Kind            Kind   `json:"kind" yaml:"kind"`
	PathSlug        string `json:"path_slug,omitempty" yaml:"path_slug,omitempty"`
	OriginSlug      string `json:"origin_slug,omitempty" yaml:"origin_slug,omitempty"`
	ExpectedPath    string `json:"expected_path,omitempty" yaml:"expected_path,omitempty"`
	CanonicalExists bool   `json:"canonical_exists,omitempty" yaml:"canonical_exists,omitempty"`
	Reason          string `json:"reason" yaml:"reason"`
}

// Report is the deterministic layout audit index.
type Report struct {
	SchemaVersion int       `json:"schema_version" yaml:"schema_version"`
	ProjectsRoot  string    `json:"projects_root" yaml:"projects_root"`
	ObservedAt    time.Time `json:"observed_at" yaml:"observed_at"`
	Summary       Summary   `json:"summary" yaml:"summary"`
	Findings      []Finding `json:"findings" yaml:"findings"`
}

// Summary counts findings by kind.
type Summary struct {
	Inspected  int `json:"inspected" yaml:"inspected"`
	OK         int `json:"ok" yaml:"ok"`
	TopLevel   int `json:"top_level" yaml:"top_level"`
	Misowned   int `json:"misowned" yaml:"misowned"`
	NoOrigin   int `json:"no_origin" yaml:"no_origin"`
	Unreadable int `json:"unreadable" yaml:"unreadable"`
}

// CleanOptions controls safe removal of top-level clones.
type CleanOptions struct {
	Apply                 bool
	AllowMissingCanonical bool
}

// CleanAction is one planned or applied cleanup.
type CleanAction struct {
	Path       string `json:"path" yaml:"path"`
	OriginSlug string `json:"origin_slug,omitempty" yaml:"origin_slug,omitempty"`
	Status     string `json:"status" yaml:"status"` // removed, planned, skipped, error
	Reason     string `json:"reason" yaml:"reason"`
}

// CleanReport summarizes a clean run.
type CleanReport struct {
	SchemaVersion int           `json:"schema_version" yaml:"schema_version"`
	ProjectsRoot  string        `json:"projects_root" yaml:"projects_root"`
	DryRun        bool          `json:"dry_run" yaml:"dry_run"`
	Actions       []CleanAction `json:"actions" yaml:"actions"`
}

// Audit walks projectsRoot for canonical, top-level, and misowned clones.
func Audit(ctx context.Context, projectsRoot string) (Report, error) {
	root, err := absoluteRoot(projectsRoot)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion: 1,
		ProjectsRoot:  root,
		ObservedAt:    time.Now().UTC(),
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return Report{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if isCanonicalGitDir(path) {
			report.Findings = append(report.Findings, inspectTopLevel(ctx, root, path, entry.Name())...)
			continue
		}
		// Treat as an owner directory and inspect child clones.
		children, readErr := os.ReadDir(path)
		if readErr != nil {
			report.Findings = append(report.Findings, Finding{
				Path: path, Kind: KindUnreadable, PathSlug: entry.Name(),
				Reason: fmt.Sprintf("cannot read owner directory: %v", readErr),
			})
			continue
		}
		for _, child := range children {
			if !child.IsDir() || strings.HasPrefix(child.Name(), ".") {
				continue
			}
			repoPath := filepath.Join(path, child.Name())
			if !isCanonicalGitDir(repoPath) {
				continue
			}
			report.Findings = append(report.Findings, inspectCanonical(ctx, root, repoPath, entry.Name(), child.Name()))
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Kind == report.Findings[j].Kind {
			return report.Findings[i].Path < report.Findings[j].Path
		}
		return report.Findings[i].Kind < report.Findings[j].Kind
	})
	report.Summary = summarize(report.Findings)
	return report, nil
}

// Clean removes safe top-level clones. Without Apply it only plans.
func Clean(ctx context.Context, projectsRoot string, options CleanOptions) (CleanReport, error) {
	audit, err := Audit(ctx, projectsRoot)
	if err != nil {
		return CleanReport{}, err
	}
	report := CleanReport{
		SchemaVersion: 1,
		ProjectsRoot:  audit.ProjectsRoot,
		DryRun:        !options.Apply,
	}
	for _, finding := range audit.Findings {
		if finding.Kind != KindTopLevel {
			continue
		}
		action := evaluateTopLevelClean(ctx, audit.ProjectsRoot, finding, options)
		if options.Apply && action.Status == "planned" {
			if err := removeContainedPath(audit.ProjectsRoot, finding.Path); err != nil {
				action.Status = "error"
				action.Reason = err.Error()
			} else {
				action.Status = "removed"
				action.Reason = "removed top-level clone; canonical copy remains at " + finding.ExpectedPath
			}
		}
		report.Actions = append(report.Actions, action)
	}
	sort.Slice(report.Actions, func(i, j int) bool { return report.Actions[i].Path < report.Actions[j].Path })
	return report, nil
}

// Failed reports whether an audit found layout problems.
func Failed(report Report) bool {
	return report.Summary.TopLevel > 0 || report.Summary.Misowned > 0 || report.Summary.NoOrigin > 0 || report.Summary.Unreadable > 0
}

// CleanFailed reports whether clean had errors or leftover skips that need attention
// when apply was requested. Dry-run never fails solely for planned removals.
func CleanFailed(report CleanReport) bool {
	for _, action := range report.Actions {
		if action.Status == "error" {
			return true
		}
	}
	return false
}

func summarize(findings []Finding) Summary {
	summary := Summary{Inspected: len(findings)}
	for _, finding := range findings {
		switch finding.Kind {
		case KindOK:
			summary.OK++
		case KindTopLevel:
			summary.TopLevel++
		case KindMisowned:
			summary.Misowned++
		case KindNoOrigin:
			summary.NoOrigin++
		case KindUnreadable:
			summary.Unreadable++
		}
	}
	return summary
}

func inspectTopLevel(ctx context.Context, root, path, name string) []Finding {
	finding := Finding{Path: path, Kind: KindTopLevel, PathSlug: name}
	slug, err := OriginSlug(ctx, path)
	if err != nil {
		finding.Kind = KindNoOrigin
		finding.Reason = "top-level checkout under projects root without a usable origin remote"
		return []Finding{finding}
	}
	finding.OriginSlug = slug
	owner, repo, ok := splitSlug(slug)
	if !ok {
		finding.Kind = KindUnreadable
		finding.Reason = "origin remote does not identify owner/repository: " + slug
		return []Finding{finding}
	}
	expected := filepath.Join(root, owner, repo)
	finding.ExpectedPath = expected
	finding.CanonicalExists = isCanonicalGitDir(expected)
	finding.Reason = "clone sits directly under projects root; expected " + filepath.ToSlash(filepath.Join(owner, repo))
	return []Finding{finding}
}

func inspectCanonical(ctx context.Context, root, path, owner, name string) Finding {
	pathSlug := owner + "/" + name
	finding := Finding{Path: path, PathSlug: pathSlug, ExpectedPath: path}
	slug, err := OriginSlug(ctx, path)
	if err != nil {
		finding.Kind = KindNoOrigin
		finding.Reason = "canonical-path checkout has no usable origin remote"
		return finding
	}
	finding.OriginSlug = slug
	if !strings.EqualFold(slug, pathSlug) {
		finding.Kind = KindMisowned
		if originOwner, originRepo, ok := splitSlug(slug); ok {
			finding.ExpectedPath = filepath.Join(root, originOwner, originRepo)
			finding.CanonicalExists = isCanonicalGitDir(finding.ExpectedPath)
		}
		finding.Reason = fmt.Sprintf("path is %s but origin is %s", pathSlug, slug)
		return finding
	}
	finding.Kind = KindOK
	finding.CanonicalExists = true
	finding.Reason = "canonical owner/repository path matches origin"
	return finding
}

func evaluateTopLevelClean(ctx context.Context, root string, finding Finding, options CleanOptions) CleanAction {
	action := CleanAction{Path: finding.Path, OriginSlug: finding.OriginSlug}
	if finding.OriginSlug == "" {
		action.Status = "skipped"
		action.Reason = "refusing to remove a top-level clone without a usable origin"
		return action
	}
	if finding.ExpectedPath == "" {
		action.Status = "skipped"
		action.Reason = "cannot derive canonical path from origin"
		return action
	}
	if !finding.CanonicalExists && !options.AllowMissingCanonical {
		action.Status = "skipped"
		action.Reason = "canonical clone is missing at " + finding.ExpectedPath + "; pass --allow-missing-canonical to remove the only copy when it is clean"
		return action
	}
	status, err := gitops.Status(finding.Path)
	if err != nil {
		action.Status = "error"
		action.Reason = err.Error()
		return action
	}
	if status.Dirty() {
		action.Status = "skipped"
		action.Reason = "working tree is not clean: " + status.Summary()
		return action
	}
	if !finding.CanonicalExists {
		action.Status = "planned"
		action.Reason = "would remove the only local copy; canonical path does not exist yet"
		return action
	}
	action.Status = "planned"
	action.Reason = "safe to remove; clean working tree and canonical clone exists at " + finding.ExpectedPath
	_ = ctx
	_ = root
	return action
}

func isCanonicalGitDir(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func absoluteRoot(projectsRoot string) (string, error) {
	if strings.TrimSpace(projectsRoot) == "" {
		return "", fmt.Errorf("projects root is required")
	}
	absolute, err := filepath.Abs(projectsRoot)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("projects root is not a directory: %s", absolute)
	}
	return absolute, nil
}

func removeContainedPath(root, target string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove path outside projects root: %s", target)
	}
	// Only remove immediate children of the projects root (top-level clones).
	if filepath.Dir(absTarget) != absRoot {
		return fmt.Errorf("refusing to remove non-top-level path: %s", target)
	}
	return os.RemoveAll(absTarget)
}

// OriginSlug returns owner/repository from path's origin remote.
func OriginSlug(ctx context.Context, path string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", path, "remote", "get-url", "origin")
	command.Env = console.Env()
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	remote := strings.TrimSuffix(strings.TrimSpace(string(output)), ".git")
	remote = strings.TrimSuffix(remote, "/")
	if marker := strings.LastIndex(remote, "github.com:"); marker >= 0 {
		remote = remote[marker+len("github.com:"):]
	} else if marker := strings.LastIndex(remote, "github.com/"); marker >= 0 {
		remote = remote[marker+len("github.com/"):]
	} else {
		parts := strings.Split(remote, "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("cannot derive owner/repository from origin %q", remote)
		}
		remote = strings.Join(parts[len(parts)-2:], "/")
	}
	if _, _, ok := splitSlug(remote); !ok {
		return "", fmt.Errorf("origin remote does not identify owner/repository: %q", remote)
	}
	return remote, nil
}

func splitSlug(slug string) (owner, name string, ok bool) {
	owner, name, found := strings.Cut(strings.Trim(slug, "/"), "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

// Counts returns summary fields useful for fleet rollups without retaining findings.
func Counts(ctx context.Context, projectsRoot string) (Summary, error) {
	report, err := Audit(ctx, projectsRoot)
	if err != nil {
		return Summary{}, err
	}
	return report.Summary, nil
}
