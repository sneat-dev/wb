package quality

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AffectedCoverageScope reads both revision graphs before selecting one logical
// comparison scope. Graph failures fail validation, never silently skip tests.
func AffectedCoverageScope(ctx context.Context, repo, mergeBase string, touched map[string]bool, includeE2E bool) (CoverageSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return affectedCoverageScope(ctx, repo, mergeBase, touched, includeE2E, runStdout, os.MkdirTemp)
}

func affectedCoverageScope(ctx context.Context, repo, mergeBase string, touched map[string]bool, includeE2E bool,
	command func(context.Context, string, string, ...string) (string, error), temporary func(string, string) (string, error),
) (selection CoverageSelection, resultErr error) {
	directory, err := temporary("", "wb-coverage-scope-*")
	if err != nil {
		return CoverageSelection{}, err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	base := filepath.Join(directory, "base")
	if _, err := command(ctx, repo, "git", "worktree", "add", "--detach", base, mergeBase); err != nil {
		return CoverageSelection{}, fmt.Errorf("prepare coverage scope at %s: %w", mergeBase, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := command(cleanupCtx, repo, "git", "worktree", "remove", "--force", base); err != nil {
			resultErr = fmt.Errorf("coverage scope cleanup (%s): %w", base, err)
		}
	}()
	headSHA, err := command(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		return CoverageSelection{}, fmt.Errorf("resolve coverage head revision: %w", err)
	}
	var buildEnvironment string
	for _, root := range []string{repo, base} {
		identity, err := command(ctx, root, "go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS")
		if err != nil {
			return CoverageSelection{}, fmt.Errorf("read coverage build identity: %w", err)
		}
		if buildEnvironment != "" && buildEnvironment != identity {
			return CoverageSelection{}, fmt.Errorf("coverage baseline and head use different Go versions or build flags")
		}
		buildEnvironment = identity
	}
	var graphs [][]CoverageScopePackage
	tiers := []string{""}
	if includeE2E {
		tiers = append(tiers, "e2e")
	}
	for _, root := range []string{repo, base} {
		for _, tags := range tiers {
			args := []string{"list", "-test", "-json"}
			if tags != "" {
				args = append(args, "-tags="+tags)
			}
			output, err := command(ctx, root, "go", append(args, "./...")...)
			if err != nil {
				return CoverageSelection{}, fmt.Errorf("read coverage scope graph (tags=%q): %w", tags, err)
			}
			graph, err := parseCoverageScopeGraph(root, output)
			if err != nil {
				return CoverageSelection{}, err
			}
			graphs = append(graphs, graph)
		}
	}
	files := make([]string, 0, len(touched))
	for file := range touched {
		files = append(files, file)
	}
	selection = PlanCoverageScope(files, graphs...)
	if includeE2E && !selection.Full && (coverageTierMismatch(selection.Packages, graphs[0], graphs[1]) || coverageTierMismatch(selection.Packages, graphs[2], graphs[3])) {
		selection.Packages = []string{"./..."}
		selection.Full = true
		selection.Reason = "selected package has different default/native build-tag membership"
	}
	selection.Identity = &CoverageScopeIdentity{HeadSHA: strings.TrimSpace(headSHA), BaseSHA: mergeBase, IncludeE2E: includeE2E, BuildSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(buildEnvironment)))}
	return selection, nil
}

func parseCoverageScopeGraph(root, output string) ([]CoverageScopePackage, error) {
	decoder := json.NewDecoder(strings.NewReader(output))
	var result []CoverageScopePackage
	for {
		var pkg struct {
			Dir             string
			ForTest         string
			Name            string
			GoFiles         []string
			ImportPath      string
			Imports         []string
			TestImports     []string
			XTestImports    []string
			EmbedFiles      []string
			TestEmbedFiles  []string
			XTestEmbedFiles []string
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			return result, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode coverage scope graph: %w", err)
		}
		// -test resolves matched embed files on original package records.
		// Recompiled test variants and the generated test main are not new
		// package owners; original TestImports/XTestImports already supply edges.
		if pkg.ForTest != "" || (pkg.Name == "main" && strings.HasSuffix(pkg.ImportPath, ".test") && len(pkg.GoFiles) == 1 && (pkg.GoFiles[0] == "_testmain.go" || filepath.IsAbs(pkg.GoFiles[0]))) {
			continue
		}
		dir, err := filepath.Rel(root, pkg.Dir)
		if err != nil {
			return nil, err
		}
		if dir == ".." || strings.HasPrefix(dir, ".."+string(filepath.Separator)) || pkg.ImportPath == "" {
			return nil, fmt.Errorf("invalid coverage scope package %q outside module", pkg.ImportPath)
		}
		pattern := "."
		if dir != "." {
			pattern = "./" + filepath.ToSlash(dir)
		}
		result = append(result, CoverageScopePackage{Pattern: pattern, ImportPath: pkg.ImportPath,
			Imports: pkg.Imports, TestImports: pkg.TestImports, XTestImports: pkg.XTestImports, EmbedFiles: pkg.EmbedFiles, TestEmbedFiles: pkg.TestEmbedFiles, XTestEmbedFiles: pkg.XTestEmbedFiles})
	}
}

// ExistingCoveragePackages resolves a logical selection at one revision. A new
// or deleted package contributes no statements at the revision where absent.
func ExistingCoveragePackages(root string, patterns []string) ([]string, error) {
	return existingCoveragePackages(root, patterns, os.ReadDir)
}

func existingCoveragePackages(root string, patterns []string, readDir func(string) ([]os.DirEntry, error)) ([]string, error) {
	var existing []string
	for _, pattern := range patterns {
		if strings.Contains(pattern, "...") {
			existing = append(existing, pattern)
			continue
		}
		entries, err := readDir(filepath.Join(root, pattern))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve coverage package %s: %w", pattern, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_") {
				existing = append(existing, pattern)
				break
			}
		}
	}
	return existing, nil
}

func coverageTierMismatch(selected []string, unit, native []CoverageScopePackage) bool {
	unitSet, nativeSet := make(map[string]bool), make(map[string]bool)
	for _, pkg := range unit {
		unitSet[pkg.Pattern] = true
	}
	for _, pkg := range native {
		nativeSet[pkg.Pattern] = true
	}
	for _, pattern := range selected {
		if unitSet[pattern] != nativeSet[pattern] {
			return true
		}
	}
	return false
}

// SelectedCoverageOptions keeps repository shard policy within a selected set.
// The policy is loaded and validated before this operation.
func SelectedCoverageOptions(options RunOptions, patterns []string) RunOptions {
	options.GoTestPackages = append([]string(nil), patterns...)
	if len(patterns) == 1 && patterns[0] == "./..." {
		return options
	}
	var shards []string
	for _, shard := range options.GoShardPackages {
		for _, pattern := range patterns {
			if pattern == shard {
				shards = append(shards, shard)
				break
			}
		}
	}
	options.GoShardPackages = shards
	if len(shards) == 0 {
		options.GoTestShards = 1
	}
	return options
}
