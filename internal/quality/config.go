package quality

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const repositoryQualityConfigPath = ".wb/quality.yaml"

type repositoryQualityConfig struct {
	Version int `yaml:"version"`
	GoTest  struct {
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
	base.GoTestShards = config.GoTest.Shards
	base.GoShardPackages = append([]string(nil), config.GoTest.Packages...)
	return base, nil
}
