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

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/repopath"
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
	root string
	// owners are the {owner} directories the root holds, already read through
	// a literal forge host level where one is present.
	owners      []repopath.Owner
	fingerprint string
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
	repositories, err := scanLocalOrganizations(snapshot.owners)
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
	if _, err := os.ReadDir(root); err != nil {
		return localSourceSnapshot{}, err
	}
	owners, _ := repopath.Owners(root)
	parts := []string{root, fileInfoFingerprint(rootInfo)}
	seenHosts := make(map[string]bool, len(owners))
	for _, owner := range owners {
		info, infoErr := os.Stat(owner.Path)
		if infoErr != nil {
			continue
		}
		parts = append(parts, filepath.ToSlash(owner.Relative())+"\x00"+fileInfoFingerprint(info))
		// A literal forge level is one directory holding every organization on
		// that forge, so fingerprint the host directory itself too: adding or
		// removing a host must invalidate the cache even when no organization
		// directory changed.
		if owner.Host == "" || seenHosts[owner.Host] {
			continue
		}
		seenHosts[owner.Host] = true
		if hostInfo, hostErr := os.Stat(filepath.Join(root, owner.Host)); hostErr == nil {
			parts = append(parts, owner.Host+"\x00"+fileInfoFingerprint(hostInfo))
		}
	}
	sort.Strings(parts[2:])
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return localSourceSnapshot{root: filepath.Clean(root), owners: owners, fingerprint: hex.EncodeToString(digest[:])}, nil
}

func fileInfoFingerprint(info os.FileInfo) string {
	return strconv.FormatInt(info.Size(), 10) + ":" +
		strconv.FormatInt(info.ModTime().UTC().UnixNano(), 10) + ":" +
		strconv.FormatUint(uint64(info.Mode()), 10)
}

// scanLocalOrganizations reads one repository per canonical clone below each
// owner directory the snapshot found — at <root>/{host}/{owner}/{repo} or at
// the legacy <root>/{owner}/{repo}. It deliberately walks the owner PATH rather
// than re-deriving it from the projects root, so both shapes are read exactly
// where they were found.
func scanLocalOrganizations(owners []repopath.Owner) ([]Repo, error) {
	var repositories []Repo
	for _, owner := range owners {
		entries, err := os.ReadDir(owner.Path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			repositoryPath := filepath.Join(owner.Path, entry.Name())
			gitDirectory, statErr := os.Stat(filepath.Join(repositoryPath, ".git"))
			if statErr != nil || !gitDirectory.IsDir() {
				continue
			}
			repositories = append(repositories, Repo{Org: owner.Name, Name: entry.Name(), Host: owner.Host, Path: repositoryPath})
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
	return writeLocalIndexInjected(path, index, nil)
}

// writeLocalIndexInjected is writeLocalIndex's test seam (task-9 PR-8):
// every production call site reaches it only through writeLocalIndex,
// which always passes a nil *filewrite.Injector, so production behaviour
// is unchanged. A test passes its own Injector to reach the create/chmod/
// write/close/rename failure branches deterministically.
func writeLocalIndexInjected(path string, index persistedLocalIndex, inj *filewrite.Injector) error {
	contents, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := filewrite.CreateTemp(directory, ".fleet-inventory-*.tmp", inj)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryPath, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryPath, inj)
		return err
	}
	if err := filewrite.Write(temporary, append(contents, '\n'), temporaryPath, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryPath, inj)
		return err
	}
	if err := filewrite.Close(temporary, temporaryPath, inj); err != nil {
		return err
	}
	return filewrite.Rename(temporaryPath, path, inj)
}

func cloneRepos(repositories []Repo) []Repo {
	return append([]Repo(nil), repositories...)
}

// validCachedRepositoryPlacement reports whether a cached repository's Path is
// a canonical clone placement for its own coordinate under root: the
// literal-host <root>/{host}/{org}/{repo} or the legacy
// <root>/{org}/{repo}. Both must be accepted, or every read of an index
// written against a host-level fleet would be rejected and re-scanned forever.
func validCachedRepositoryPlacement(root string, repository Repo) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(repository.Path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	switch len(parts) {
	case 2:
		// The legacy flat placement carries no host, so a cached entry that
		// claims one describes a different layout and must be re-scanned.
		return repository.Host == "" && parts[0] == repository.Org && parts[1] == repository.Name
	case 3:
		return repopath.IsForgeHost(parts[0]) && parts[0] == repository.Host &&
			parts[1] == repository.Org && parts[2] == repository.Name
	default:
		return false
	}
}

func validCachedRepos(root string, repositories []Repo) bool {
	seen := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		if repository.Org == "" || repository.Name == "" || repository.Org == "." || repository.Org == ".." || repository.Name == "." || repository.Name == ".." ||
			filepath.Base(repository.Org) != repository.Org || filepath.Base(repository.Name) != repository.Name ||
			!validCachedRepositoryPlacement(root, repository) ||
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
