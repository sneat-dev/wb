// Package discover finds the repositories to operate on by reconciling the
// local ~/projects/{org}/{repo} tree with the non-archived repositories GitHub
// reports for the relevant orgs.
package discover

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	cmd := exec.Command("gh", "repo", "list", owner,
		"--limit", "1000",
		"--json", "name,isArchived,isFork,sshUrl")
	out, err := cmd.Output()
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

// AuthUser returns the authenticated GitHub login via gh.
func AuthUser() (string, error) {
	out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// MemberOrgs returns the GitHub orgs the authenticated user belongs to. This is
// the authoritative source of "owners I control" — local directory names are
// not, since they include third-party clones.
func MemberOrgs() ([]string, error) {
	out, err := exec.Command("gh", "api", "user/orgs", "--jq", ".[].login").Output()
	if err != nil {
		return nil, err
	}
	var orgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			orgs = append(orgs, line)
		}
	}
	return orgs, nil
}
