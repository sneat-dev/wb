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
)

// Repo identifies a single repository and where it lives.
type Repo struct {
	Org      string
	Name     string
	Path     string // local working-tree path; empty if not cloned locally
	CloneURL string // ssh URL from GitHub; empty if only known locally
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

// Reconcile merges locally-cloned repos with remotely-listed ones, keyed by
// org/name. Remote metadata (archived flag, clone URL) wins where both exist.
// The result is sorted by slug for deterministic output.
func Reconcile(local, remote []Repo) []Repo {
	m := map[string]*Repo{}
	for _, r := range local {
		c := r
		c.Local = true
		m[c.Slug()] = &c
	}
	for _, r := range remote {
		if ex, ok := m[r.Slug()]; ok {
			ex.Remote = true
			ex.Archived = r.Archived
			ex.IsFork = r.IsFork
			if r.CloneURL != "" {
				ex.CloneURL = r.CloneURL
			}
			continue
		}
		c := r
		c.Remote = true
		m[c.Slug()] = &c
	}
	out := make([]Repo, 0, len(m))
	for _, r := range m {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug() < out[j].Slug() })
	return out
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
	response, err := githubobserver.Get(ctx, githubobserver.GetRequest{Repository: repo.Slug(), Endpoint: "repos/" + repo.Slug()})
	if err != nil {
		return CanonicalRepository{}, err
	}
	var payload struct {
		FullName      string `json:"full_name"`
		SSHURL        string `json:"ssh_url"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return CanonicalRepository{}, err
	}
	remote, err := gitremote.Parse(payload.SSHURL)
	if err != nil || remote.Identity.Host() != "github.com" || remote.Identity.Repository != payload.FullName || payload.DefaultBranch == "" {
		return CanonicalRepository{}, fmt.Errorf("GitHub returned an invalid canonical repository identity")
	}
	return CanonicalRepository{Slug: payload.FullName, CloneURL: payload.SSHURL, DefaultBranch: payload.DefaultBranch}, nil
}

// ReconcileTransfers folds an old-path local-only repository and its new
// remote-only identity into one repository. This must happen before sync's
// worker pool so no worker clones the destination while another moves source.
func ReconcileTransfers(ctx context.Context, repos []Repo, resolve func(context.Context, Repo) (CanonicalRepository, error)) []Repo {
	remoteOnly := map[string]Repo{}
	for _, repo := range repos {
		if repo.Remote && !repo.Local && repo.Path == "" {
			remoteOnly[repo.Slug()] = repo
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
		if _, exists := remoteOnly[canonical.Slug]; exists {
			byTarget[canonical.Slug] = append(byTarget[canonical.Slug], candidate{source: repo, canonical: canonical})
		}
	}
	consumed := map[string]bool{}
	var out []Repo
	for target, candidates := range byTarget {
		remote := remoteOnly[target]
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
			consumed[candidate.source.Slug()] = true
		}
	}
	for _, repo := range repos {
		if !consumed[repo.Slug()] {
			out = append(out, repo)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug() < out[j].Slug() })
	return out
}

// ScanLocal walks projectsRoot two levels deep ({org}/{repo}) and returns every
// canonical git repository. Linked worktrees use a .git file and are excluded:
// they are alternate checkouts of a canonical repository, not fleet members.
func ScanLocal(projectsRoot string) ([]Repo, error) {
	orgs, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil, err
	}
	var repos []Repo
	for _, org := range orgs {
		if !org.IsDir() || strings.HasPrefix(org.Name(), ".") {
			continue
		}
		orgPath := filepath.Join(projectsRoot, org.Name())
		entries, err := os.ReadDir(orgPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			repoPath := filepath.Join(orgPath, e.Name())
			gitDirectory, err := os.Stat(filepath.Join(repoPath, ".git"))
			if err != nil || !gitDirectory.IsDir() {
				continue
			}
			repos = append(repos, Repo{Org: org.Name(), Name: e.Name(), Path: repoPath})
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
	out, err := githubobserver.Read(context.Background(), "", "repo", "list", owner,
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
	out, err := githubobserver.Read(context.Background(), "", "repo", "view", slug, "--json", "isArchived", "--jq", ".isArchived")
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
	response, err := githubobserver.Get(context.Background(), githubobserver.GetRequest{Endpoint: "user"})
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
	response, err := githubobserver.Get(context.Background(), githubobserver.GetRequest{Endpoint: "user/orgs"})
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
