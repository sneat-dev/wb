package discover

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const LocalIndexSchemaVersion = 1

// LocalIndexOptions configures the persisted read-only fleet index. A blank
// CachePath deliberately falls back to a fresh scan. Callers that can mutate a
// repository continue to use ScanLocal so cached discovery never grants write
// authority.
type LocalIndexOptions struct {
	CachePath string
	MaxAge    time.Duration
	Now       func() time.Time
}

// LocalIndexResult describes whether discovery reused a persisted observation.
// Diagnostics are cache-only failures: fresh discovery still succeeds when a
// cache is corrupt or temporarily unwritable.
type LocalIndexResult struct {
	Repositories      []Repo
	SourceFingerprint string
	ObservedAt        time.Time
	CacheHit          bool
	Diagnostics       []string
}

type persistedLocalIndex struct {
	SchemaVersion     int       `json:"schema_version"`
	ProjectsRoot      string    `json:"projects_root"`
	SourceFingerprint string    `json:"source_fingerprint"`
	ObservedAt        time.Time `json:"observed_at"`
	Repositories      []Repo    `json:"repositories"`
}

type localSourceSnapshot struct {
	root          string
	organizations []os.DirEntry
	fingerprint   string
}

// ScanLocalIndexed reuses a persisted canonical-clone inventory only for a
// bounded time and only while the projects root and organization-directory
// metadata fingerprint is unchanged. Nested changes that do not update an
// organization directory are bounded by MaxAge. The index is discovery data,
// never mutation evidence.
func ScanLocalIndexed(projectsRoot string, options LocalIndexOptions) (LocalIndexResult, error) {
	if strings.TrimSpace(options.CachePath) == "" || options.MaxAge <= 0 {
		repositories, err := ScanLocal(projectsRoot)
		return LocalIndexResult{Repositories: repositories}, err
	}
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	snapshot, err := snapshotLocalSource(projectsRoot)
	if err != nil {
		return LocalIndexResult{}, err
	}
	result := LocalIndexResult{SourceFingerprint: snapshot.fingerprint}
	if cached, readErr := readLocalIndex(options.CachePath); readErr == nil {
		age := now.Sub(cached.ObservedAt)
		if cached.SchemaVersion == LocalIndexSchemaVersion &&
			filepath.Clean(cached.ProjectsRoot) == snapshot.root &&
			cached.SourceFingerprint == snapshot.fingerprint &&
			age >= 0 && age <= options.MaxAge &&
			validCachedRepos(snapshot.root, cached.Repositories) {
			result.Repositories = cloneRepos(cached.Repositories)
			result.ObservedAt = cached.ObservedAt
			result.CacheHit = true
			return result, nil
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		result.Diagnostics = append(result.Diagnostics, "read local fleet index: "+readErr.Error())
	}
	repositories, err := scanLocalOrganizations(snapshot.root, snapshot.organizations)
	if err != nil {
		return LocalIndexResult{}, err
	}
	result.Repositories = repositories
	result.ObservedAt = now
	cache := persistedLocalIndex{
		SchemaVersion: LocalIndexSchemaVersion, ProjectsRoot: snapshot.root,
		SourceFingerprint: snapshot.fingerprint, ObservedAt: now,
		Repositories: cloneRepos(repositories),
	}
	if writeErr := writeLocalIndex(options.CachePath, cache); writeErr != nil {
		result.Diagnostics = append(result.Diagnostics, "write local fleet index: "+writeErr.Error())
	}
	return result, nil
}

func snapshotLocalSource(projectsRoot string) (localSourceSnapshot, error) {
	root, err := filepath.Abs(projectsRoot)
	if err != nil {
		return localSourceSnapshot{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return localSourceSnapshot{}, err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return localSourceSnapshot{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return localSourceSnapshot{}, err
	}
	organizations := make([]os.DirEntry, 0, len(entries))
	parts := []string{root, fileInfoFingerprint(rootInfo)}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		organizations = append(organizations, entry)
		parts = append(parts, entry.Name()+"\x00"+fileInfoFingerprint(info))
	}
	sort.Strings(parts[2:])
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return localSourceSnapshot{root: filepath.Clean(root), organizations: organizations, fingerprint: hex.EncodeToString(digest[:])}, nil
}

func fileInfoFingerprint(info os.FileInfo) string {
	return strconv.FormatInt(info.Size(), 10) + ":" +
		strconv.FormatInt(info.ModTime().UTC().UnixNano(), 10) + ":" +
		strconv.FormatUint(uint64(info.Mode()), 10)
}

func scanLocalOrganizations(root string, organizations []os.DirEntry) ([]Repo, error) {
	var repositories []Repo
	for _, organization := range organizations {
		organizationPath := filepath.Join(root, organization.Name())
		entries, err := os.ReadDir(organizationPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			repositoryPath := filepath.Join(organizationPath, entry.Name())
			gitDirectory, statErr := os.Stat(filepath.Join(repositoryPath, ".git"))
			if statErr != nil || !gitDirectory.IsDir() {
				continue
			}
			repositories = append(repositories, Repo{Org: organization.Name(), Name: entry.Name(), Path: repositoryPath})
		}
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].Slug() < repositories[j].Slug() })
	return repositories, nil
}

func readLocalIndex(path string) (persistedLocalIndex, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return persistedLocalIndex{}, err
	}
	var index persistedLocalIndex
	if err := json.Unmarshal(contents, &index); err != nil {
		return persistedLocalIndex{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return index, nil
}

func writeLocalIndex(path string, index persistedLocalIndex) error {
	contents, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".fleet-inventory-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func cloneRepos(repositories []Repo) []Repo {
	return append([]Repo(nil), repositories...)
}

func validCachedRepos(root string, repositories []Repo) bool {
	seen := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		if repository.Org == "" || repository.Name == "" || repository.Org == "." || repository.Org == ".." || repository.Name == "." || repository.Name == ".." ||
			filepath.Base(repository.Org) != repository.Org || filepath.Base(repository.Name) != repository.Name ||
			filepath.Clean(repository.Path) != filepath.Join(root, repository.Org, repository.Name) ||
			repository.CloneURL != "" || repository.Archived || repository.IsFork || repository.Local || repository.Remote {
			return false
		}
		if _, duplicate := seen[repository.Slug()]; duplicate {
			return false
		}
		seen[repository.Slug()] = struct{}{}
	}
	return true
}
