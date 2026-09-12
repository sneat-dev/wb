// Package lifecyclehooks runs trusted user-configured commands after WB
// changes a repository checkout. It is intentionally separate from package
// hooks, which manages Git's own hook shims and repository policy.
package lifecyclehooks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ConfigVersion        = 1
	EventCheckoutUpdated = "checkout-updated"
)

var executorName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Config struct {
	Version   int                 `yaml:"version"`
	Executors map[string]Executor `yaml:"executors"`
	Bindings  []Binding           `yaml:"bindings"`
}

type Executor struct {
	Run     string   `yaml:"run"`
	Args    []string `yaml:"args"`
	CWD     string   `yaml:"cwd"`
	Mode    string   `yaml:"mode"`
	Timeout string   `yaml:"timeout"`
	Failure string   `yaml:"failure"`
}

type Binding struct {
	On      []string `yaml:"on"`
	Match   Match    `yaml:"match"`
	Execute []string `yaml:"execute"`
}

type Match struct {
	Repositories RepositoryMatch `yaml:"repositories"`
}

type RepositoryMatch struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Load reads only the hooks section from the standard wb.yaml. Other WB
// sections are deliberately ignored while fields inside hooks are strict.
func Load(configPath string) (Config, bool, error) {
	file, err := os.Open(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read lifecycle hooks config %s: %w", configPath, err)
	}
	defer func() { _ = file.Close() }()

	decoder := yaml.NewDecoder(file)
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return Config{}, false, fmt.Errorf("parse lifecycle hooks config %s: %w", configPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, false, fmt.Errorf("parse lifecycle hooks config %s: %w", configPath, err)
	} else if err == nil {
		return Config{}, false, fmt.Errorf("parse lifecycle hooks config %s: multiple YAML documents are not supported", configPath)
	}

	hooksNode, found, err := mappingValue(document, "hooks")
	if err != nil {
		return Config{}, false, fmt.Errorf("parse lifecycle hooks config %s: %w", configPath, err)
	}
	if !found || hooksNode.Kind == 0 || hooksNode.Tag == "!!null" {
		return Config{}, false, nil
	}
	raw, err := yaml.Marshal(hooksNode)
	if err != nil {
		return Config{}, false, fmt.Errorf("parse lifecycle hooks config %s: %w", configPath, err)
	}
	strict := yaml.NewDecoder(bytes.NewReader(raw))
	strict.KnownFields(true)
	var cfg Config
	if err := strict.Decode(&cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse lifecycle hooks config %s: %w", configPath, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, false, fmt.Errorf("lifecycle hooks config %s: %w", configPath, err)
	}
	return cfg, true, nil
}

func mappingValue(document yaml.Node, key string) (yaml.Node, bool, error) {
	if len(document.Content) == 0 {
		return yaml.Node{}, false, nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return yaml.Node{}, false, errors.New("top level must be a mapping")
	}
	var found yaml.Node
	seen := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		if seen {
			return yaml.Node{}, false, fmt.Errorf("duplicate top-level %q section", key)
		}
		seen = true
		found = *root.Content[i+1]
	}
	return found, seen, nil
}

func (cfg Config) validate() error {
	if cfg.Version != ConfigVersion {
		return fmt.Errorf("hooks.version is %d; supported version is %d", cfg.Version, ConfigVersion)
	}
	if len(cfg.Executors) == 0 {
		return errors.New("hooks.executors must not be empty")
	}
	for name, executor := range cfg.Executors {
		if !executorName.MatchString(name) {
			return fmt.Errorf("invalid executor name %q", name)
		}
		if strings.TrimSpace(executor.Run) == "" {
			return fmt.Errorf("executor %q requires run", name)
		}
		if executor.CWD != "repository" {
			return fmt.Errorf("executor %q cwd must be repository", name)
		}
		if executor.Mode != "coalesced" {
			return fmt.Errorf("executor %q mode must be coalesced", name)
		}
		if executor.Failure != "warn" {
			return fmt.Errorf("executor %q failure must be warn", name)
		}
		if _, err := executor.timeout(); err != nil {
			return fmt.Errorf("executor %q: %w", name, err)
		}
	}
	if len(cfg.Bindings) == 0 {
		return errors.New("hooks.bindings must not be empty")
	}
	for i, binding := range cfg.Bindings {
		if len(binding.On) == 0 {
			return fmt.Errorf("binding %d requires on", i+1)
		}
		for _, event := range binding.On {
			if event != EventCheckoutUpdated {
				return fmt.Errorf("binding %d has unsupported event %q", i+1, event)
			}
		}
		if len(binding.Match.Repositories.Include) == 0 {
			return fmt.Errorf("binding %d requires match.repositories.include", i+1)
		}
		for _, pattern := range append(append([]string(nil), binding.Match.Repositories.Include...), binding.Match.Repositories.Exclude...) {
			if _, err := path.Match(pattern, "github.com/owner/repository"); err != nil {
				return fmt.Errorf("binding %d has invalid repository glob %q: %w", i+1, pattern, err)
			}
		}
		if len(binding.Execute) == 0 {
			return fmt.Errorf("binding %d requires execute", i+1)
		}
		for _, name := range binding.Execute {
			if _, ok := cfg.Executors[name]; !ok {
				return fmt.Errorf("binding %d references unknown executor %q", i+1, name)
			}
		}
	}
	return nil
}

func (binding Binding) matches(event Event) bool {
	if !contains(binding.On, event.Name) {
		return false
	}
	repository := strings.ToLower(event.Repository)
	included := false
	for _, pattern := range binding.Match.Repositories.Include {
		matched, _ := path.Match(strings.ToLower(pattern), repository)
		if matched {
			included = true
			break
		}
	}
	if !included {
		return false
	}
	for _, pattern := range binding.Match.Repositories.Exclude {
		matched, _ := path.Match(strings.ToLower(pattern), repository)
		if matched {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
