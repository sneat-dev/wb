package streams

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Ecosystem names one package system a library publishes into.
type Ecosystem string

const (
	// EcosystemGo is a Go module path.
	EcosystemGo Ecosystem = "go"
	// EcosystemNpm is an npm package name.
	EcosystemNpm Ecosystem = "npm"
)

// Identity is one published identity of a library: the name a consumer
// resolves, and the manifest that declares it.
type Identity struct {
	Ecosystem Ecosystem `json:"ecosystem"`
	Name      string    `json:"name"`
	// Manifest is the worktree-relative path that declares the identity.
	Manifest string `json:"manifest"`
	// Directory is the worktree-relative directory of the manifest. For a Go
	// module it is the directory a workspace `use` entry names.
	Directory string `json:"directory"`
}

// DiscoverPublished reads the identities a library worktree publishes.
//
// Discovery is evidence-based and reads the library worktree itself: the Go
// module path from `backend/go.mod`, or from the module root where the
// repository has no `backend/`, and npm package names from
// `libs/**/package.json`. An operator-supplied package name is never accepted
// as a substitute, because the whole value of a local link is that it exposes
// what the library actually publishes.
//
// Implements: dependency-streams#req:local-link-discovers-what-the-library-publishes.
func DiscoverPublished(root string) ([]Identity, error) {
	var identities []Identity
	goManifest := ""
	if _, err := os.Stat(filepath.Join(root, "backend", "go.mod")); err == nil {
		goManifest = filepath.Join("backend", "go.mod")
	} else if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		goManifest = "go.mod"
	}
	if goManifest != "" {
		contents, err := os.ReadFile(filepath.Join(root, goManifest))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", goManifest, err)
		}
		if module, ok := goModulePath(contents); ok {
			identities = append(identities, Identity{
				Ecosystem: EcosystemGo,
				Name:      module,
				Manifest:  filepath.ToSlash(goManifest),
				Directory: filepath.ToSlash(filepath.Dir(goManifest)),
			})
		}
	}
	manifests, err := npmPackageManifests(root)
	if err != nil {
		return nil, err
	}
	for _, manifest := range manifests {
		// The repository root manifest of a workspace is the workspace
		// itself, not a published package; only a named, non-private
		// manifest under libs/ is a published identity.
		if manifest == "package.json" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(root, manifest))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", manifest, err)
		}
		var parsed struct {
			Name    string `json:"name"`
			Private bool   `json:"private"`
		}
		if err := json.Unmarshal(contents, &parsed); err != nil {
			return nil, fmt.Errorf("parse %s: %w", manifest, err)
		}
		if parsed.Private || strings.TrimSpace(parsed.Name) == "" {
			continue
		}
		identities = append(identities, Identity{
			Ecosystem: EcosystemNpm,
			Name:      parsed.Name,
			Manifest:  manifest,
			Directory: filepath.ToSlash(filepath.Dir(manifest)),
		})
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].Ecosystem != identities[j].Ecosystem {
			return identities[i].Ecosystem < identities[j].Ecosystem
		}
		return identities[i].Name < identities[j].Name
	})
	return identities, nil
}

// Declaration is one consumer's declared dependency on a library identity.
type Declaration struct {
	Identity Identity `json:"identity"`
	// Manifest is the consumer-relative manifest that declares it.
	Manifest string `json:"manifest"`
	// Version is exactly as declared, including any range prefix.
	Version string `json:"version"`
	// Section is the canonical dependency section the declaration came from.
	Section string `json:"section,omitempty"`
}

// npmDependencySections is the canonical dependency-field set. It is the same
// set graph discovery and release evidence use, because
// `link-discovery-uses-the-canonical-dependency-sections` forbids a link
// deciding relevance from a different field list than the graph does.
var npmDependencySections = []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"}

// DiscoverDeclarations finds every declaration a consumer worktree makes of
// the given library identities. A consumer that declares none of them is not
// linkable, and reporting that is the point: it must be skipped rather than
// linked to something it does not use.
func DiscoverDeclarations(root string, identities []Identity) ([]Declaration, error) {
	byName := map[string]Identity{}
	for _, identity := range identities {
		byName[identity.Ecosystem.key(identity.Name)] = identity
	}
	var declarations []Declaration
	goManifests, err := GoModules(root)
	if err != nil {
		return nil, err
	}
	for _, module := range goManifests {
		contents, err := os.ReadFile(filepath.Join(root, module.Manifest))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", module.Manifest, err)
		}
		for name, version := range goRequirements(contents) {
			identity, ok := byName[EcosystemGo.key(name)]
			if !ok {
				continue
			}
			declarations = append(declarations, Declaration{
				Identity: identity, Manifest: module.Manifest, Version: version, Section: "require",
			})
		}
	}
	npmManifests, err := npmPackageManifests(root)
	if err != nil {
		return nil, err
	}
	for _, manifest := range npmManifests {
		contents, err := os.ReadFile(filepath.Join(root, manifest))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", manifest, err)
		}
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(contents, &parsed); err != nil {
			return nil, fmt.Errorf("parse %s: %w", manifest, err)
		}
		for _, section := range npmDependencySections {
			raw, ok := parsed[section]
			if !ok {
				continue
			}
			var entries map[string]string
			if err := json.Unmarshal(raw, &entries); err != nil {
				continue
			}
			for name, version := range entries {
				identity, ok := byName[EcosystemNpm.key(name)]
				if !ok {
					continue
				}
				declarations = append(declarations, Declaration{
					Identity: identity, Manifest: manifest, Version: version, Section: section,
				})
			}
		}
	}
	sort.Slice(declarations, func(i, j int) bool {
		if declarations[i].Manifest != declarations[j].Manifest {
			return declarations[i].Manifest < declarations[j].Manifest
		}
		return declarations[i].Identity.Name < declarations[j].Identity.Name
	})
	return declarations, nil
}

func (ecosystem Ecosystem) key(name string) string { return string(ecosystem) + " " + name }

// GoModule is one Go module inside a worktree.
type GoModule struct {
	// Path is the module path declared by the manifest.
	Path string `json:"path"`
	// Manifest is the worktree-relative go.mod path.
	Manifest string `json:"manifest"`
	// Directory is the worktree-relative module directory, which is what a
	// workspace `use` entry names. It is "." for a module at the root.
	Directory string `json:"directory"`
}

// GoModules lists every Go module in a worktree: `backend/`, the module root
// where there is no `backend/`, and any nested tooling module.
//
// A workspace containing only the library would leave the consumer's own
// module outside the workspace it now sits under, and `go build ./...` in
// `backend/` would not resolve at all — which is why this enumerates the whole
// worktree rather than a conventional pair of paths.
//
// Implements: dependency-streams#req:go-consumers-link-through-an-untracked-go-work.
func GoModules(root string) ([]GoModule, error) {
	var modules []GoModule
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			if skippedModuleDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		module, ok := goModulePath(contents)
		if !ok {
			return nil
		}
		modules = append(modules, GoModule{
			Path:      module,
			Manifest:  filepath.ToSlash(relative),
			Directory: filepath.ToSlash(filepath.Dir(relative)),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan Go modules in %s: %w", root, err)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Directory < modules[j].Directory })
	return modules, nil
}

func skippedModuleDirectory(name string) bool {
	switch name {
	case "node_modules", "vendor", "dist", "testdata", ".git":
		return true
	}
	return strings.HasPrefix(name, ".")
}

var goModuleDirective = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

func goModulePath(contents []byte) (string, bool) {
	match := goModuleDirective.FindSubmatch(contents)
	if match == nil {
		return "", false
	}
	return strings.Trim(string(match[1]), `"`), true
}

var (
	goSingleRequire = regexp.MustCompile(`(?m)^\s*require\s+([^\s()]+)\s+(\S+)`)
	goBlockRequire  = regexp.MustCompile(`(?ms)^require\s*\(\s*(.*?)^\)`)
	// A module path contains slashes, so the name group must not exclude
	// them; requiring the version to start with "v" is what keeps this from
	// matching the `go` and `toolchain` directives.
	goBlockEntry = regexp.MustCompile(`(?m)^\s*(\S+)\s+(v\S+)`)
)

// goRequirements reads a go.mod's require directives, both spellings.
func goRequirements(contents []byte) map[string]string {
	requirements := map[string]string{}
	for _, match := range goSingleRequire.FindAllSubmatch(contents, -1) {
		requirements[string(match[1])] = string(match[2])
	}
	for _, block := range goBlockRequire.FindAllSubmatch(contents, -1) {
		for _, entry := range goBlockEntry.FindAllSubmatch(block[1], -1) {
			requirements[string(entry[1])] = string(entry[2])
		}
	}
	return requirements
}
