// Package layout audits and safely cleans local clone placement under a
// projects root. Canonical clones live at {root}/{host}/{owner}/{repository},
// where {host} is the literal forge hostname. The legacy {owner}/{repository}
// placement this fleet still uses is read in place and reported as a finding,
// never silently accepted as a forge.
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
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/repopath"
)

// Kind classifies one layout finding.
type Kind string

const (
	KindOK         Kind = "ok"
	KindTopLevel   Kind = "top_level"
	KindMisowned   Kind = "misowned"
	KindNoOrigin   Kind = "no_origin"
	KindUnreadable Kind = "unreadable"
	// KindBadHost reports a first-level entry under the projects root that is
	// not a literal forge hostname while carrying canonical clones of hosted
	// repositories. The legacy {owner}/{repository} placement is read in
	// place — no fleet is ever forced to move to be audited — but it is never
	// presented as if its owner level were a forge.
	KindBadHost Kind = "bad_host"
)

// Finding is one inspected checkout relative to the projects root.
type Finding struct {
	Path         string `json:"path" yaml:"path"`
	Kind         Kind   `json:"kind" yaml:"kind"`
	PathSlug     string `json:"path_slug,omitempty" yaml:"path_slug,omitempty"`
	OriginSlug   string `json:"origin_slug,omitempty" yaml:"origin_slug,omitempty"`
	ExpectedPath string `json:"expected_path,omitempty" yaml:"expected_path,omitempty"`
	// RemoteURL is the remote URL the clone's canonical path corresponds to. A
	// clone that already sits at <root>/{host}/{owner}/{repository} inverts to
	// it by pure path arithmetic — no configuration and no repository remote
	// is read — and a legacy two-level clone reports the host its origin
	// already names, which is the host level it must move under.
	RemoteURL       string `json:"remote_url,omitempty" yaml:"remote_url,omitempty"`
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
	BadHost    int `json:"bad_host" yaml:"bad_host"`
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
//
// The first level under the root is the literal forge hostname: canonical
// clones live at {root}/{host}/{owner}/{repository}. A first-level entry that
// is not a valid hostname is reported as a layout finding. Its clones are
// still inspected at the legacy {owner}/{repository} placement, so a fleet
// that has not moved yet stays fully auditable in place.
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
		if repopath.IsForgeHost(entry.Name()) {
			report.Findings = append(report.Findings, inspectHost(ctx, root, path, entry.Name())...)
			continue
		}
		report.Findings = append(report.Findings, inspectLegacyOwner(ctx, root, path, entry.Name())...)
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
	return report.Summary.TopLevel > 0 || report.Summary.Misowned > 0 || report.Summary.NoOrigin > 0 ||
		report.Summary.Unreadable > 0 || report.Summary.BadHost > 0
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
		case KindBadHost:
			summary.BadHost++
		}
	}
	return summary
}

// inspectHost walks one literal forge host directory: {host}/{org}/{repository}.
func inspectHost(ctx context.Context, root, hostPath, host string) []Finding {
	organizations, readErr := os.ReadDir(hostPath)
	if readErr != nil {
		return []Finding{{
			Path: hostPath, Kind: KindUnreadable, PathSlug: host,
			Reason: fmt.Sprintf("cannot read host directory: %v", readErr),
		}}
	}
	var findings []Finding
	for _, organization := range organizations {
		if !organization.IsDir() || strings.HasPrefix(organization.Name(), ".") {
			continue
		}
		organizationPath := filepath.Join(hostPath, organization.Name())
		pathSlug := host + "/" + organization.Name()
		if isCanonicalGitDir(organizationPath) {
			// A checkout directly below a host level is missing its
			// organization: {host}/{repository} is not a canonical address.
			findings = append(findings, Finding{
				Path: organizationPath, Kind: KindMisowned, PathSlug: pathSlug,
				Reason: "host level " + host + " must be followed by {org}/{repository}",
			})
			continue
		}
		repositories, repositoryErr := os.ReadDir(organizationPath)
		if repositoryErr != nil {
			findings = append(findings, Finding{
				Path: organizationPath, Kind: KindUnreadable, PathSlug: pathSlug,
				Reason: fmt.Sprintf("cannot read organization directory: %v", repositoryErr),
			})
			continue
		}
		for _, repository := range repositories {
			if !repository.IsDir() || strings.HasPrefix(repository.Name(), ".") {
				continue
			}
			repositoryPath := filepath.Join(organizationPath, repository.Name())
			if !isCanonicalGitDir(repositoryPath) {
				continue
			}
			finding, _ := inspectCanonical(ctx, root, repositoryPath, host, organization.Name(), repository.Name())
			findings = append(findings, finding)
		}
	}
	return findings
}

// inspectLegacyOwner reads the legacy {owner}/{repository} placement. Clones
// are inspected exactly as before so an unmigrated fleet stays operable, but
// when those clones publish a hosted origin the missing host level is reported
// as a finding rather than silently accepted.
func inspectLegacyOwner(ctx context.Context, root, ownerPath, owner string) []Finding {
	children, readErr := os.ReadDir(ownerPath)
	if readErr != nil {
		return []Finding{{
			Path: ownerPath, Kind: KindUnreadable, PathSlug: owner,
			Reason: fmt.Sprintf("cannot read owner directory: %v", readErr),
		}}
	}
	var findings []Finding
	hosts := map[string]bool{}
	for _, child := range children {
		if !child.IsDir() || strings.HasPrefix(child.Name(), ".") {
			continue
		}
		repositoryPath := filepath.Join(ownerPath, child.Name())
		if !isCanonicalGitDir(repositoryPath) {
			continue
		}
		finding, host := inspectCanonical(ctx, root, repositoryPath, "", owner, child.Name())
		if host != "" {
			hosts[host] = true
		}
		findings = append(findings, finding)
	}
	if len(hosts) == 0 {
		return findings
	}
	return append([]Finding{badHostFinding(root, ownerPath, owner, hosts)}, findings...)
}

// badHostFinding reports one non-hostname first level that carries clones of
// hosted repositories at the legacy placement.
func badHostFinding(root, ownerPath, owner string, hosts map[string]bool) Finding {
	finding := Finding{
		Path: ownerPath, Kind: KindBadHost, PathSlug: owner,
		Reason: fmt.Sprintf("first-level entry %q is not a literal forge hostname; its clones sit at the legacy {owner}/{repository} placement", owner),
	}
	if len(hosts) == 1 {
		for host := range hosts {
			expected := filepath.Join(root, host, owner)
			finding.ExpectedPath = expected
			finding.Reason = fmt.Sprintf("first-level entry %q is not a literal forge hostname; expected clones under %s",
				owner, filepath.ToSlash(filepath.Join(host, owner)))
		}
	}
	return finding
}

func inspectTopLevel(ctx context.Context, root, path, name string) []Finding {
	finding := Finding{Path: path, Kind: KindTopLevel, PathSlug: name}
	address, err := OriginAddress(ctx, path)
	if err != nil {
		finding.Kind = KindNoOrigin
		finding.Reason = "top-level checkout under projects root without a usable origin remote"
		return []Finding{finding}
	}
	finding.OriginSlug = address.Slug()
	if !repopath.SafeSegment(address.Org, false) || !repopath.SafeSegment(address.Repo, true) {
		finding.Kind = KindUnreadable
		finding.Reason = "origin remote does not identify owner/repository: " + address.Slug()
		return []Finding{finding}
	}
	expected := filepath.Join(root, address.Host, address.Org, address.Repo)
	finding.ExpectedPath = expected
	finding.RemoteURL = expectedRemoteURL(root, expected, address)
	finding.CanonicalExists = canonicalCloneExists(root, address)
	finding.Reason = "clone sits directly under projects root; expected " + filepath.ToSlash(expected[len(root)+1:])
	return []Finding{finding}
}

// inspectCanonical classifies one canonical-shaped checkout. host is the
// literal hostname of its placement, or empty for the legacy {owner}/
// {repository} placement. It also returns the host the origin remote names, so
// the caller can report a missing host level.
//
// A legacy placement carries no host to compare, so only its owner/repository
// identity is checked here; the missing host level is reported once for the
// whole first-level directory instead (see badHostFinding).
func inspectCanonical(ctx context.Context, root, path, host, owner, name string) (Finding, string) {
	pathAddress := repopath.Address{Host: host, Org: owner, Repo: name}
	finding := Finding{Path: path, PathSlug: pathAddress.Slug(), ExpectedPath: path}
	address, err := OriginAddress(ctx, path)
	if err != nil {
		finding.Kind = KindNoOrigin
		finding.Reason = "canonical-path checkout has no usable origin remote"
		return finding, ""
	}
	finding.OriginSlug = address.Slug()
	finding.RemoteURL = expectedRemoteURL(root, path, address)
	matches := strings.EqualFold(address.Slug(), pathAddress.Slug()) &&
		(host == "" || strings.EqualFold(address.Host, host))
	if !matches {
		finding.Kind = KindMisowned
		finding.ExpectedPath = address.Path(root)
		finding.CanonicalExists = canonicalCloneExists(root, address)
		finding.Reason = fmt.Sprintf("path is %s but origin is %s", pathAddress.Relative(), address.Relative())
		return finding, address.Host
	}
	finding.Kind = KindOK
	finding.CanonicalExists = true
	if host == "" {
		finding.Reason = "canonical owner/repository path matches origin"
	} else {
		finding.Reason = "canonical {host}/{owner}/{repository} path matches origin"
	}
	return finding, address.Host
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

// expectedRemoteURL reports the remote URL a canonical clone path corresponds
// to.
//
// For a path that already carries the literal host level this is the pure path
// inversion: no configuration and no repository remote is read, which is what
// makes the answer available before a clone exists and immune to a rewritten
// origin. A legacy two-level path has no host level to invert, so it reports
// the host its origin already names — the host level the clone must move under
// — and a local-only origin reports nothing rather than an invented forge.
func expectedRemoteURL(root, path string, address repopath.Address) string {
	if remote, err := repopath.RemoteURLForLocalPath(root, path); err == nil {
		return remote
	}
	if address.Host == "" {
		return ""
	}
	return address.RemoteURL()
}

// canonicalCloneExists reports whether a canonical clone of address exists
// under root. Both placements count: the literal-host
// <root>/{host}/{owner}/{repository} and the legacy
// <root>/{owner}/{repository} this fleet still uses. A repository is cloned or
// it is not; which of the two valid placements holds it must not change a
// safety decision such as "is it safe to remove this duplicate".
func canonicalCloneExists(root string, address repopath.Address) bool {
	if isCanonicalGitDir(address.Path(root)) {
		return true
	}
	return address.Host != "" && isCanonicalGitDir(filepath.Join(root, address.Org, address.Repo))
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

// OriginAddress returns the canonical clone address the origin remote of the
// repository at path identifies: the literal forge hostname plus
// owner/repository. The host is empty for a local-only remote, which names no
// forge and therefore no first path level.
func OriginAddress(ctx context.Context, path string) (repopath.Address, error) {
	command := exec.CommandContext(ctx, "git", "-C", path, "remote", "get-url", "origin")
	command.Env = console.Env()
	output, err := command.Output()
	if err != nil {
		return repopath.Address{}, err
	}
	parsed, err := gitremote.Parse(strings.TrimSpace(string(output)))
	if err != nil {
		return repopath.Address{}, fmt.Errorf("origin remote does not identify a repository: %w", err)
	}
	owner, name, found := strings.Cut(parsed.Identity.Repository, "/")
	if !found || owner == "" || name == "" {
		return repopath.Address{}, fmt.Errorf("origin remote does not identify owner/repository")
	}
	host := parsed.Identity.Host()
	if host != "" && !repopath.IsForgeHost(host) {
		return repopath.Address{}, fmt.Errorf("origin remote host %q is not a literal forge hostname", host)
	}
	return repopath.Address{Host: host, Org: owner, Repo: name}, nil
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
