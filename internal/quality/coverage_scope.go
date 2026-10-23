package quality

import (
	"path"
	"sort"
	"strings"
)

// CoverageScopePackage describes one module-local package from go list.
// Pattern is module-relative ("." or "./internal/example"). Supply both
// revisions and both build-tag tiers: test imports are part of the graph.
type CoverageScopePackage struct {
	Pattern         string
	ImportPath      string
	Imports         []string
	TestImports     []string
	XTestImports    []string
	EmbedFiles      []string
	TestEmbedFiles  []string
	XTestEmbedFiles []string
}

// CoverageSelection is a logical scope shared by baseline and head. Packages
// absent from one revision have no statements there; callers resolve existence
// separately without replacing an empty selection with ./....
type CoverageSelection struct {
	Packages        []string               `json:"packages"`
	ChangedPackages []string               `json:"changed_packages"`
	Full            bool                   `json:"full"`
	Reason          string                 `json:"reason"`
	Identity        *CoverageScopeIdentity `json:"identity,omitempty"`
}

// CoverageScopeIdentity records revision and effective build provenance. The
// environment is hashed so build flags containing private values are not logged.
type CoverageScopeIdentity struct {
	HeadSHA     string `json:"head_sha"`
	BaseSHA     string `json:"base_sha"`
	IncludeE2E  bool   `json:"include_e2e"`
	BuildSHA256 string `json:"build_sha256"`
}

// PlanCoverageScope selects changed packages and their transitive dependents.
// Tests and instrumentation use this same set, preserving cross-package hits.
// Package-owned assets, fixtures and generated files use their nearest package
// ancestor. Shared build inputs and unowned inputs conservatively select all.
func PlanCoverageScope(touched []string, graphs ...[]CoverageScopePackage) CoverageSelection {
	touched = append([]string(nil), touched...)
	sort.Strings(touched)
	packages := make(map[string]string)
	dependents := make(map[string]map[string]bool)
	embedConsumers := make(map[string]map[string]bool)
	for _, graph := range graphs {
		for _, pkg := range graph {
			packages[pkg.ImportPath] = pkg.Pattern
			for _, files := range [][]string{pkg.EmbedFiles, pkg.TestEmbedFiles, pkg.XTestEmbedFiles} {
				for _, file := range files {
					file = path.Join(pkg.Pattern, file)
					if embedConsumers[file] == nil {
						embedConsumers[file] = make(map[string]bool)
					}
					embedConsumers[file][pkg.ImportPath] = true
				}
			}
			for _, imports := range [][]string{pkg.Imports, pkg.TestImports, pkg.XTestImports} {
				for _, imported := range imports {
					if dependents[imported] == nil {
						dependents[imported] = make(map[string]bool)
					}
					dependents[imported][pkg.ImportPath] = true
				}
			}
		}
	}
	selected := make(map[string]bool)
	owners := make(map[string]bool)
	for _, file := range touched {
		file = path.Clean(file)
		if sharedCoverageInput(file) {
			return fullCoverageSelection(packages, "shared build input: "+file)
		}
		for imported := range embedConsumers[file] {
			selected[imported] = true
			owners[packages[imported]] = true
		}
		owner := ""
		ownerLength := -1
		for imported, pattern := range packages {
			dir := strings.TrimPrefix(pattern, "./")
			if (dir == "." || strings.HasPrefix(file, dir+"/")) && len(dir) > ownerLength {
				owner, ownerLength = imported, len(dir)
			}
		}
		if owner != "" {
			selected[owner] = true
			owners[packages[owner]] = true
		} else if !documentationCoverageInput(file) {
			return fullCoverageSelection(packages, "unowned input: "+file)
		}
	}
	queue := make([]string, 0, len(selected))
	for imported := range selected {
		queue = append(queue, imported)
	}
	for i := 0; i < len(queue); i++ {
		for dependent := range dependents[queue[i]] {
			if !selected[dependent] {
				selected[dependent] = true
				queue = append(queue, dependent)
			}
		}
	}
	patterns := make(map[string]bool)
	for imported := range selected {
		patterns[packages[imported]] = true
	}
	result := CoverageSelection{Packages: make([]string, 0, len(patterns)), Reason: "changed packages and affected dependents"}
	for pattern := range patterns {
		result.Packages = append(result.Packages, pattern)
	}
	sort.Strings(result.Packages)
	for owner := range owners {
		result.ChangedPackages = append(result.ChangedPackages, owner)
	}
	sort.Strings(result.ChangedPackages)
	return result
}

func fullCoverageSelection(packages map[string]string, reason string) CoverageSelection {
	patterns := make(map[string]bool)
	for _, pattern := range packages {
		patterns[pattern] = true
	}
	selection := CoverageSelection{Packages: []string{"./..."}, Full: true, Reason: reason}
	for pattern := range patterns {
		selection.ChangedPackages = append(selection.ChangedPackages, pattern)
	}
	sort.Strings(selection.ChangedPackages)
	return selection
}

func sharedCoverageInput(file string) bool {
	switch path.Base(file) {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "Makefile", "Dockerfile":
		return true
	}
	for _, prefix := range []string{".github/workflows/", ".github/scripts/", ".wb/", "vendor/"} {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return strings.HasPrefix(file, ".goreleaser.") || strings.HasPrefix(file, ".golangci.")
}

func documentationCoverageInput(file string) bool {
	return strings.HasSuffix(file, ".md") || strings.HasPrefix(file, "spec/") || strings.HasPrefix(file, "docs/")
}
