package deps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// npmLockedVersion is what one lockfile scope actually resolved for one
// package name. Values is every distinct resolved version found in that
// scope: more than one means the scope's own importers disagree, which is
// itself drift and must never be collapsed into a single guessed answer.
type npmLockedVersion struct {
	Values []string
	Source string
}

// Version returns the single locked version, or "" when the scope's importers
// disagree (in which case Conflict describes them).
func (locked npmLockedVersion) Version() string {
	if len(locked.Values) == 1 {
		return locked.Values[0]
	}
	return ""
}

// Conflict renders the disagreement between importers, or "" when there is none.
func (locked npmLockedVersion) Conflict() string {
	if len(locked.Values) < 2 {
		return ""
	}
	return "lockfile importers pin conflicting versions: " + strings.Join(locked.Values, ", ")
}

// npmLockScope is one lockfile-owning directory's resolved dependency index.
type npmLockScope struct {
	// Directory is relative and slash-separated ("" for the repository root).
	Directory string
	// Versions maps package name to the version(s) the lockfile resolved.
	Versions map[string]npmLockedVersion
	// Reason is non-empty when the lockfile exists but could not be indexed;
	// the scope is then reported honestly rather than treated as absent.
	Reason string
}

// readNpmLockScopes indexes every pnpm-lock.yaml and package-lock.json under
// root. A lockfile WB cannot index is recorded with a reason rather than
// dropped: "no lockfile evidence" and "lockfile in a format WB does not index"
// are different findings and an operator must be able to tell them apart.
func readNpmLockScopes(root string) (map[string]npmLockScope, error) {
	lockfilesByDir, err := npmLockfileDirectories(root)
	if err != nil {
		return nil, err
	}
	scopes := make(map[string]npmLockScope, len(lockfilesByDir))
	directories := make([]string, 0, len(lockfilesByDir))
	for directory := range lockfilesByDir {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	for _, directory := range directories {
		kinds := append([]npmLockfileKind(nil), lockfilesByDir[directory]...)
		sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
		scope := npmLockScope{Directory: directory, Versions: map[string]npmLockedVersion{}}
		for _, kind := range kinds {
			relative := directory
			if relative != "" {
				relative += "/"
			}
			relative += string(kind)
			absolute := filepath.Join(root, filepath.FromSlash(relative))
			contents, readErr := os.ReadFile(absolute) // #nosec G304 -- path is derived from a walk of the inspected checkout
			if readErr != nil {
				scope.Reason = appendReason(scope.Reason, fmt.Sprintf("%s: %v", relative, readErr))
				continue
			}
			var versions map[string][]string
			var parseErr error
			switch kind {
			case npmLockfilePnpm:
				versions, parseErr = parsePnpmLockVersions(contents)
			case npmLockfileNpm:
				versions, parseErr = parsePackageLockVersions(contents)
			case npmLockfileYarn:
				scope.Reason = appendReason(scope.Reason, relative+": yarn.lock is not indexed by WB")
				continue
			}
			if parseErr != nil {
				scope.Reason = appendReason(scope.Reason, fmt.Sprintf("%s: %v", relative, parseErr))
				continue
			}
			for name, found := range versions {
				merged := scope.Versions[name]
				if merged.Source == "" {
					merged.Source = relative
				} else if !strings.Contains(merged.Source, relative) {
					merged.Source += ", " + relative
				}
				merged.Values = mergeSortedUnique(merged.Values, found)
				scope.Versions[name] = merged
			}
		}
		scopes[directory] = scope
	}
	return scopes, nil
}

func appendReason(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

func mergeSortedUnique(existing, addition []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(addition))
	merged := make([]string, 0, len(existing)+len(addition))
	for _, value := range append(append([]string(nil), existing...), addition...) {
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		merged = append(merged, value)
	}
	sort.Strings(merged)
	return merged
}

// pnpmLockDocument is the subset of a pnpm-lock.yaml WB indexes. The
// `importers` section has the same shape in lockfile versions 6 and 9, which
// is every version the fleet ships; a lockfile without it (pnpm 5 and older)
// is reported as unindexed rather than guessed at.
type pnpmLockDocument struct {
	LockfileVersion yaml.Node                       `yaml:"lockfileVersion"`
	Importers       map[string]pnpmLockImporterNode `yaml:"importers"`
}

type pnpmLockImporterNode struct {
	Dependencies         map[string]pnpmLockEntry `yaml:"dependencies"`
	DevDependencies      map[string]pnpmLockEntry `yaml:"devDependencies"`
	OptionalDependencies map[string]pnpmLockEntry `yaml:"optionalDependencies"`
	PeerDependencies     map[string]pnpmLockEntry `yaml:"peerDependencies"`
}

type pnpmLockEntry struct {
	Specifier string `yaml:"specifier"`
	Version   string `yaml:"version"`
}

func (importer pnpmLockImporterNode) sections() []map[string]pnpmLockEntry {
	return []map[string]pnpmLockEntry{
		importer.Dependencies, importer.DevDependencies,
		importer.OptionalDependencies, importer.PeerDependencies,
	}
}

func parsePnpmLockVersions(contents []byte) (map[string][]string, error) {
	var document pnpmLockDocument
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return nil, err
	}
	if len(document.Importers) == 0 {
		return nil, fmt.Errorf("no `importers` section; WB indexes pnpm lockfile versions 6 and 9")
	}
	versions := map[string][]string{}
	for _, importer := range document.Importers {
		for _, section := range importer.sections() {
			for name, entry := range section {
				resolved := cleanPnpmLockVersion(entry.Version)
				if resolved == "" {
					continue
				}
				versions[name] = mergeSortedUnique(versions[name], []string{resolved})
			}
		}
	}
	return versions, nil
}

// cleanPnpmLockVersion strips the peer-suffix pnpm appends to a resolved
// version ("0.24.3(@angular/core@20.0.0)") and rejects link:/file: importer
// entries, which resolve to a path rather than a published version.
func cleanPnpmLockVersion(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '('); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	if value == "" || strings.Contains(value, ":") || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "/") {
		return ""
	}
	if !universalSemverValid(value) {
		return ""
	}
	return value
}

type packageLockDocument struct {
	Packages map[string]struct {
		Version string `json:"version"`
	} `json:"packages"`
}

func parsePackageLockVersions(contents []byte) (map[string][]string, error) {
	var document packageLockDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, err
	}
	if len(document.Packages) == 0 {
		return nil, fmt.Errorf("no `packages` section; WB indexes package-lock.json versions 2 and 3")
	}
	versions := map[string][]string{}
	for key, entry := range document.Packages {
		name, ok := packageLockEntryName(key)
		if !ok || entry.Version == "" || !universalSemverValid(entry.Version) {
			continue
		}
		versions[name] = mergeSortedUnique(versions[name], []string{entry.Version})
	}
	return versions, nil
}

// packageLockEntryName maps a package-lock.json `packages` key to the package
// name it installs. Keys are install paths ("node_modules/@scope/name",
// "packages/app/node_modules/name"); the root workspace key "" and workspace
// member paths that are not under node_modules name a local package rather
// than a resolved registry install.
func packageLockEntryName(key string) (string, bool) {
	marker := "node_modules/"
	index := strings.LastIndex(key, marker)
	if index < 0 {
		return "", false
	}
	name := key[index+len(marker):]
	if name == "" || strings.Contains(name, "/node_modules/") {
		return "", false
	}
	return name, true
}
