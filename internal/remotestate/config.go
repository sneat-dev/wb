package remotestate

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigSnippet is printed whenever the remote section is absent or
// incomplete, so the fix is copy-paste rather than documentation lookup.
const ConfigSnippet = `remote:
  provider: git
  repo: <owner>/<name>
  machine: <unique-name-for-this-machine>
  publish:
    unpushed: subjects   # or: counts`

// PublishConfig tunes what a snapshot contains.
type PublishConfig struct {
	Unpushed Redaction `yaml:"unpushed"`
}

// Config is the remote section of ~/.config/wb/wb.yaml.
type Config struct {
	Provider string        `yaml:"provider"`
	Repo     string        `yaml:"repo"`
	Machine  string        `yaml:"machine"`
	Publish  PublishConfig `yaml:"publish"`
}

// RepoOwner returns the part of Repo before the slash.
func (c Config) RepoOwner() string { owner, _, _ := strings.Cut(c.Repo, "/"); return owner }

// RepoName returns the part of Repo after the slash.
func (c Config) RepoName() string { _, name, _ := strings.Cut(c.Repo, "/"); return name }

// UnconfiguredError reports a missing or incomplete remote section. Commands
// map it to the usage exit code.
type UnconfiguredError struct {
	Path    string
	Missing []string
}

func (e *UnconfiguredError) Error() string {
	return fmt.Sprintf("wb remote is not configured (missing %s in %s); add:\n\n%s", strings.Join(e.Missing, ", "), e.Path, ConfigSnippet)
}

var machineName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// repoPart matches a single valid GitHub owner or repository name segment.
// Requiring a leading alphanumeric rejects "." and ".." outright (both start
// with a dot), which is what keeps ClonePath — built by joining
// projects-root/owner/name — from ever being coaxed outside the projects
// root by a crafted remote.repo value.
var repoPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type configFile struct {
	Remote *Config `yaml:"remote"`
}

// LoadConfig reads the remote section from path. A missing file or section
// is an UnconfiguredError; a present but invalid value is a plain error.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, &UnconfiguredError{Path: path, Missing: []string{"remote section"}}
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var file configFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if file.Remote == nil {
		return Config{}, &UnconfiguredError{Path: path, Missing: []string{"remote section"}}
	}
	cfg := *file.Remote
	if cfg.Provider == "" {
		cfg.Provider = "git"
	}
	if cfg.Publish.Unpushed == "" {
		cfg.Publish.Unpushed = RedactNone
	}
	var missing []string
	if strings.TrimSpace(cfg.Repo) == "" {
		missing = append(missing, "repo")
	}
	if strings.TrimSpace(cfg.Machine) == "" {
		missing = append(missing, "machine")
	}
	if len(missing) > 0 {
		return Config{}, &UnconfiguredError{Path: path, Missing: missing}
	}
	if cfg.Provider != "git" {
		return Config{}, fmt.Errorf("remote.provider %q is not supported; only \"git\" is available", cfg.Provider)
	}
	if strings.Count(cfg.Repo, "/") != 1 || !repoPart.MatchString(cfg.RepoOwner()) || !repoPart.MatchString(cfg.RepoName()) {
		return Config{}, fmt.Errorf("remote.repo %q must be <owner>/<name>, each matching %s", cfg.Repo, repoPart)
	}
	if !machineName.MatchString(cfg.Machine) {
		return Config{}, fmt.Errorf("remote.machine %q must match %s", cfg.Machine, machineName)
	}
	if cfg.Publish.Unpushed != RedactNone && cfg.Publish.Unpushed != RedactUnpushed {
		return Config{}, fmt.Errorf("remote.publish.unpushed %q must be %q or %q", cfg.Publish.Unpushed, RedactNone, RedactUnpushed)
	}
	return cfg, nil
}
