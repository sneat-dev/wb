// Package discover finds the repositories to operate on by reconciling the
// local ~/projects/{org}/{repo} tree with the non-archived repositories GitHub
// reports for the relevant orgs.
package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/repopath"
)

// ghObserver is a package-private test seam over the githubobserver.Observer
// this package reads gh through. Once Get/Read started routing gh through
// the task-24 guarded runner seam, this package's tests could no longer
// answer them with a fake `gh` on PATH (that real subprocess start is
// refused under go test). Tests reassign it to one built with
// githubobserver.NewTestObserver and restore it with t.Cleanup; production
// callers never touch it. None of the tests that reassign it run in
// parallel with each other, so the shared var carries no race.
var ghObserver = githubobserver.Default()

// Repo identifies a single repository and where it lives.
type Repo struct {
	Org      string
	Name     string
	Host     string // literal forge hostname of the placement, empty for a legacy flat clone
	Path     string // local working-tree path; empty if not cloned locally
	CloneURL string // transport URL from GitHub; empty if only known locally
	Archived bool
	IsFork   bool
	Local    bool
	Remote   bool
	// TransferFrom is set when this remotely listed repository is the
	// canonical identity GitHub returns for a local clone still stored under
	// an older owner/name path.
	TransferFrom  string
	DefaultBranch string
	TransferError string
}

// Slug returns the "org/repo" identifier.
func (r Repo) Slug() string { return r.Org + "/" + r.Name }

// Identity is the repository's host-qualified address when its placement knows
// the forge, and the bare owner/repository slug otherwise. Two clones of the
// same owner/repository on different forges are different repositories: they
// must never collapse into one inventory entry, which is much of why the host
// level exists.
func (r Repo) Identity() string {
	if r.Host == "" {
		return r.Slug()
	}
	return r.Host + "/" + r.Slug()
}

// Reconcile merges locally-cloned repos with remotely-listed ones.
//
// Entries are keyed by their host-qualified Identity, so two clones of the same
// owner/repository on different forges stay two entries instead of silently
// collapsing into one — the multi-forge case the host level exists to express.
// Remote metadata (archived flag, clone URL) wins where a remote listing
// describes a repository that is already cloned locally. The result is sorted
// by slug, then host and path, for deterministic output.
func Reconcile(local, remote []Repo) []Repo {
	out := make([]*Repo, 0, len(local)+len(remote))
	byKey := map[string]*Repo{}
	byIdentity := map[string]*Repo{}
	appendRepo := func(candidate *Repo) *Repo {
		key := candidate.Identity()
		if existing, taken := byKey[key]; taken && existing.Path != candidate.Path {
			// The same identity twice on disk. Keep both rather than dropping
			// one silently: a duplicate clone is a finding for the operator,
			// not data an inventory may discard.
			key += "\x00" + candidate.Path
		}
		byKey[key] = candidate
		if _, known := byIdentity[candidate.Identity()]; !known {
			byIdentity[candidate.Identity()] = candidate
		}
		out = append(out, candidate)
		return candidate
	}
	// A legacy flat clone carries no host in its path, but in this fleet a flat
	// clone of a GitHub repository IS the GitHub clone. Index it under its bare
	// slug as well, so a remote GitHub listing still matches it and `wb sync`
	// updates it in place instead of cloning a second copy beside it.
	legacyBySlug := map[string]*Repo{}
	for _, r := range local {
		c := r
		c.Local = true
		appended := appendRepo(&c)
		if c.Host == "" {
			if _, known := legacyBySlug[c.Slug()]; !known {
				legacyBySlug[c.Slug()] = appended
			}
		}
	}
	for _, r := range remote {
		existing := byIdentity[r.Identity()]
		if existing == nil {
			// A remote listing describes the clone of the same slug. A legacy
			// flat clone cannot carry a host in its path, so it is the match
			// for its own slug; when the listing names no forge at all, the
			// GitHub clone is the only addressable candidate.
			existing = legacyBySlug[r.Slug()]
		}
		if existing == nil && r.Host == "" {
			existing = byIdentity["github.com/"+r.Slug()]
		}
		if existing != nil {
			existing.Remote = true
			existing.Archived = r.Archived
			existing.IsFork = r.IsFork
			if r.CloneURL != "" {
				existing.CloneURL = r.CloneURL
			}
			continue
		}
		c := r
		c.Remote = true
		appendRepo(&c)
	}
	result := make([]Repo, 0, len(out))
	for _, repo := range out {
		result = append(result, *repo)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Slug() != result[j].Slug() {
			return result[i].Slug() < result[j].Slug()
		}
		if result[i].Identity() != result[j].Identity() {
			return result[i].Identity() < result[j].Identity()
		}
		return result[i].Path < result[j].Path
	})
	return result
}

// CanonicalRepository is GitHub's current identity for a possibly redirected
// owner/name URL.
type CanonicalRepository struct {
	Slug          string
	CloneURL      string
	DefaultBranch string
}

// ResolveCanonicalRepository follows GitHub's repository redirect and returns
// the current owner/name and transport URL for one local clone.
func ResolveCanonicalRepository(ctx context.Context, repo Repo) (CanonicalRepository, error) {
	origin, err := gitops.OriginURL(repo.Path)
	if err != nil {
		return CanonicalRepository{}, err
	}
	parsed, err := gitremote.Parse(origin)
	if err != nil || parsed.Identity.Host() != "github.com" || parsed.Identity.Repository != repo.Slug() {
		return CanonicalRepository{}, fmt.Errorf("origin does not identify github.com/%s", repo.Slug())
	}
	response, err := ghObserver.Get(ctx, githubobserver.GetRequest{Repository: repo.Slug(), Endpoint: "repos/" + repo.Slug()})
	if err != nil {
		return CanonicalRepository{}, err
	}
	var payload struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return CanonicalRepository{}, err
	}
	canonicalURL := "https://github.com/" + payload.FullName
	remote, err := gitremote.Parse(canonicalURL)
	if err != nil || remote.Identity.Host() != "github.com" || remote.Identity.Repository != payload.FullName || payload.DefaultBranch == "" {
		return CanonicalRepository{}, fmt.Errorf("GitHub returned an invalid canonical repository identity")
	}
	return CanonicalRepository{Slug: payload.FullName, CloneURL: canonicalURL, DefaultBranch: payload.DefaultBranch}, nil
}

// ReconcileTransfers folds an old-path local-only repository and its new
// remote-only identity into one repository. This must happen before sync's
// worker pool so no worker clones the destination while another moves source.
func ReconcileTransfers(ctx context.Context, repos []Repo, resolve func(context.Context, Repo) (CanonicalRepository, error)) []Repo {
	remoteTargets := map[string]Repo{}
	remoteIdentityBySlug := map[string]string{}
	for _, repo := range repos {
		if repo.Remote {
			remoteTargets[repo.Identity()] = repo
			if _, known := remoteIdentityBySlug[repo.Slug()]; !known {
				remoteIdentityBySlug[repo.Slug()] = repo.Identity()
			}
		}
	}
	type candidate struct {
		source    Repo
		canonical CanonicalRepository
	}
	byTarget := map[string][]candidate{}
	for _, repo := range repos {
		if !repo.Local || repo.Remote || repo.Path == "" {
			continue
		}
		canonical, err := resolve(ctx, repo)
		if err != nil || canonical.Slug == repo.Slug() {
			continue
		}
		if identity, exists := remoteIdentityBySlug[canonical.Slug]; exists {
			byTarget[identity] = append(byTarget[identity], candidate{source: repo, canonical: canonical})
		}
	}
	consumed := map[string]bool{}
	var out []Repo
	for target, candidates := range byTarget {
		remote := remoteTargets[target]
		consumed[target] = true
		for _, candidate := range candidates {
			combined := remote
			combined.Local = true
			combined.Path = candidate.source.Path
			combined.TransferFrom = candidate.source.Slug()
			combined.CloneURL = candidate.canonical.CloneURL
			combined.DefaultBranch = candidate.canonical.DefaultBranch
			if len(candidates) != 1 {
				combined.TransferError = fmt.Sprintf("ambiguous transfer: %d local repositories resolve to %s", len(candidates), target)
			}
			out = append(out, combined)
			consumed[candidate.source.Identity()] = true
		}
	}
	for _, repo := range repos {
		if !consumed[repo.Identity()] {
			out = append(out, repo)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug() < out[j].Slug() })
	return out
}

// ScanLocal walks projectsRoot and returns every canonical git repository.
//
// Canonical clones live at <root>/{host}/{org}/{repo} with the literal forge
// hostname at the first level; the legacy two-level <root>/{org}/{repo}
// placement is read as well, so a fleet that has not adopted the host level
// stays fully visible and a migrated one is never read as empty. Linked
// worktrees use a .git file and are excluded: they are alternate checkouts of a
// canonical repository, not fleet members.
func ScanLocal(projectsRoot string) ([]Repo, error) {
	if _, err := os.ReadDir(projectsRoot); err != nil {
		return nil, err
	}
	owners, _ := repopath.Owners(projectsRoot)
	var repos []Repo
	for _, owner := range owners {
		entries, err := os.ReadDir(owner.Path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			repoPath := filepath.Join(owner.Path, e.Name())
			gitDirectory, err := os.Stat(filepath.Join(repoPath, ".git"))
			if err != nil || !gitDirectory.IsDir() {
				continue
			}
			repos = append(repos, Repo{Org: owner.Name, Name: e.Name(), Host: owner.Host, Path: repoPath})
		}
	}
	return repos, nil
}

// ghRepo mirrors the JSON fields requested from `gh repo list`.
type ghRepo struct {
	Name       string `json:"name"`
	IsArchived bool   `json:"isArchived"`
	IsFork     bool   `json:"isFork"`
	SSHURL     string `json:"sshUrl"`
}

// ListRemote returns all repos for owner via gh. The archived flag is
// preserved so callers can report and skip archived repos explicitly.
func ListRemote(owner string) ([]Repo, error) {
	out, err := ghObserver.Read(context.Background(), "", "repo", "list", owner,
		"--limit", "1000",
		"--json", "name,isArchived,isFork,sshUrl")
	if err != nil {
		return nil, err
	}
	var raw []ghRepo
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	repos := make([]Repo, 0, len(raw))
	for _, r := range raw {
		repos = append(repos, Repo{
			Org:      owner,
			Name:     r.Name,
			Host:     repopath.FromCloneURL(r.SSHURL, owner, r.Name).Host,
			CloneURL: r.SSHURL,
			Archived: r.IsArchived,
			IsFork:   r.IsFork,
		})
	}
	return repos, nil
}

// IsArchived confirms, live against GitHub, whether the repository named by
// slug ("owner/repository") is archived right now. It is deliberately a
// single per-repository query rather than a reuse of a fleet-wide listing
// (ListRemote, or a caller's own cached Repo.Archived): a bulk listing can be
// stale by the time a destructive decision is made from it, can silently omit
// a repository the caller lacks org-listing access to, and answers "was this
// archived when the list was built" rather than "is this archived now". A
// caller about to delete a local clone based on archived status must ask this
// exact question about this exact repository immediately before acting, and
// must treat any error (network, auth, rate limit, unknown repository) as
// "could not confirm" rather than guessing either way.
func IsArchived(slug string) (bool, error) {
	out, err := ghObserver.Read(context.Background(), "", "repo", "view", slug, "--json", "isArchived", "--jq", ".isArchived")
	if err != nil {
		return false, fmt.Errorf("confirm archived status of %s: %w", slug, err)
	}
	value := strings.TrimSpace(string(out))
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("confirm archived status of %s: unexpected gh output %q", slug, value)
	}
}

// AuthUser returns the authenticated GitHub login via gh.
func AuthUser() (string, error) {
	response, err := ghObserver.Get(context.Background(), githubobserver.GetRequest{Endpoint: "user"})
	if err != nil {
		return "", err
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return "", err
	}
	out := []byte(payload.Login)
	return strings.TrimSpace(string(out)), nil
}

// MemberOrgs returns the GitHub orgs the authenticated user belongs to. This is
// the authoritative source of "owners I control" — local directory names are
// not, since they include third-party clones.
func MemberOrgs() ([]string, error) {
	response, err := ghObserver.Get(context.Background(), githubobserver.GetRequest{Endpoint: "user/orgs"})
	if err != nil {
		return nil, err
	}
	var values []struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(response.Body, &values); err != nil {
		return nil, err
	}
	var output strings.Builder
	for _, value := range values {
		output.WriteString(value.Login)
		output.WriteByte('\n')
	}
	out := []byte(output.String())
	var orgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			orgs = append(orgs, line)
		}
	}
	return orgs, nil
}
