package quality

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const repositoryQualityConfigPath = ".wb/quality.yaml"

type repositoryQualityConfig struct {
	Version int `yaml:"version"`
	GoLint  struct {
		Commands [][]string `yaml:"commands"`
	} `yaml:"go_lint"`
	GoTest *struct {
		Shards   int      `yaml:"shards"`
		Packages []string `yaml:"packages"`
	} `yaml:"go_test"`
}

// RepositoryRunOptions applies an explicit repository-owned quality policy to
// one validation run. Absence is the portable default; malformed or ambiguous
// policy fails closed rather than silently falling back to a slower or weaker
// command.
func RepositoryRunOptions(root string, base RunOptions) (RunOptions, error) {
	path := filepath.Join(root, repositoryQualityConfigPath)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return base, nil
	}
	if err != nil {
		return base, fmt.Errorf("open repository quality policy %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var config repositoryQualityConfig
	if err := decoder.Decode(&config); err != nil {
		return base, fmt.Errorf("decode repository quality policy %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return base, fmt.Errorf("decode trailing repository quality policy %s: %w", path, err)
		}
		return base, fmt.Errorf("repository quality policy %s contains multiple YAML documents", path)
	}
	if config.Version != 1 {
		return base, fmt.Errorf("repository quality policy %s has version %d; want 1", path, config.Version)
	}
	if config.GoTest != nil {
		if config.GoTest.Shards < 2 {
			return base, fmt.Errorf("repository quality policy %s go_test.shards must be at least 2", path)
		}
		if len(config.GoTest.Packages) == 0 {
			return base, fmt.Errorf("repository quality policy %s go_test.packages must name at least one package", path)
		}
		seen := map[string]bool{}
		for _, packagePath := range config.GoTest.Packages {
			if strings.TrimSpace(packagePath) == "" {
				return base, fmt.Errorf("repository quality policy %s contains an empty go_test package", path)
			}
			if seen[packagePath] {
				return base, fmt.Errorf("repository quality policy %s repeats go_test package %q", path, packagePath)
			}
			seen[packagePath] = true
		}
		if !base.ExplicitGoTestSharding {
			base.GoTestShards = config.GoTest.Shards
			base.GoShardPackages = append([]string(nil), config.GoTest.Packages...)
		}
	}
	for commandIndex, command := range config.GoLint.Commands {
		if len(command) == 0 {
			return base, fmt.Errorf("repository quality policy %s go_lint.commands[%d] is empty", path, commandIndex)
		}
		for argumentIndex, argument := range command {
			if strings.TrimSpace(argument) == "" {
				return base, fmt.Errorf("repository quality policy %s go_lint.commands[%d][%d] is empty", path, commandIndex, argumentIndex)
			}
		}
		base.GoLintCommands = append(base.GoLintCommands, append([]string(nil), command...))
	}
	return base, nil
}

// ScopeShardPolicyToPackages narrows a repository-owned shard policy to a run
// that was explicitly limited to GoTestPackages. The sharded runner rejects a
// shard package outside the selected scope, so a scoped run that carried the
// whole policy would fail instead of measuring the packages it was asked for.
// Shard packages the scope covers keep their sharding; when none are left the
// run is unsharded. Without GoTestPackages, or without a policy, it returns
// the options unchanged.
func ScopeShardPolicyToPackages(options RunOptions) RunOptions {
	if len(options.GoTestPackages) == 0 || len(options.GoShardPackages) == 0 {
		return options
	}
	var kept []string
	for _, shardPackage := range options.GoShardPackages {
		for _, pattern := range options.GoTestPackages {
			if packagePatternCovers(pattern, shardPackage) {
				kept = append(kept, shardPackage)
				break
			}
		}
	}
	options.GoShardPackages = kept
	if len(kept) == 0 {
		options.GoTestShards = 1
	}
	return options
}

// packagePatternCovers reports whether one `go test` package pattern selects
// the single package named by target. Both are module-relative; only the
// forms the changed-package scope produces are understood: an exact directory
// and a "/..." subtree.
func packagePatternCovers(pattern, target string) bool {
	pattern = path.Clean(strings.TrimSpace(pattern))
	target = path.Clean(strings.TrimSpace(target))
	if pattern == target {
		return true
	}
	if pattern == "..." {
		return true
	}
	if root, ok := strings.CutSuffix(pattern, "/..."); ok {
		return root == "." || target == root || strings.HasPrefix(target, root+"/")
	}
	return false
}

// existingPackagePatterns keeps the patterns whose directory exists under
// root. A package a change adds has no directory at its merge base, so a
// merge-base measurement scoped to the changed packages must skip it: its
// baseline is then absent, which the ratchet treats as zero uncovered.
func existingPackagePatterns(root string, patterns []string) []string {
	var existing []string
	for _, pattern := range patterns {
		directory := strings.TrimSuffix(strings.TrimSpace(pattern), "/...")
		if directory == "..." {
			directory = "."
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(directory))); err == nil && info.IsDir() {
			existing = append(existing, pattern)
		}
	}
	return existing
}
