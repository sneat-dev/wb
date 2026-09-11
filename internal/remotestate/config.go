package remotestate

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

// HubConfigSnippet is the explicit hosted alternative. It has no anonymous or
// insecure default and defaults to privacy-safe unpushed counts.
const HubConfigSnippet = `remote:
  provider: hub
  url: https://wb-github-app.sneat.dev
  token_file: <absolute-private-token-file>
  machine: <unique-name-for-this-machine>
  publish:
    unpushed: counts`

// PublishConfig tunes what a snapshot contains.
type PublishConfig struct {
	Unpushed Redaction `yaml:"unpushed"`
}

// Config is the remote section of ~/.config/wb/wb.yaml.
type Config struct {
	Provider  string        `yaml:"provider"`
	Repo      string        `yaml:"repo"`
	URL       string        `yaml:"url"`
	TokenFile string        `yaml:"token_file"`
	Machine   string        `yaml:"machine"`
	Publish   PublishConfig `yaml:"publish"`
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
	Snippet string
}

func (e *UnconfiguredError) Error() string {
	snippet := e.Snippet
	if snippet == "" {
		snippet = ConfigSnippet
	}
	return fmt.Sprintf("wb remote is not configured (missing %s in %s); add:\n\n%s", strings.Join(e.Missing, ", "), e.Path, snippet)
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
		if cfg.Provider == "hub" {
			cfg.Publish.Unpushed = RedactUnpushed
		} else {
			cfg.Publish.Unpushed = RedactNone
		}
	}
	var missing []string
	if cfg.Provider == "git" && strings.TrimSpace(cfg.Repo) == "" {
		missing = append(missing, "repo")
	}
	if cfg.Provider == "hub" && strings.TrimSpace(cfg.URL) == "" {
		missing = append(missing, "url")
	}
	if cfg.Provider == "hub" && strings.TrimSpace(cfg.TokenFile) == "" {
		missing = append(missing, "token_file")
	}
	if strings.TrimSpace(cfg.Machine) == "" {
		missing = append(missing, "machine")
	}
	if len(missing) > 0 {
		snippet := ConfigSnippet
		if cfg.Provider == "hub" {
			snippet = HubConfigSnippet
		}
		return Config{}, &UnconfiguredError{Path: path, Missing: missing, Snippet: snippet}
	}
	if cfg.Provider != "git" && cfg.Provider != "hub" {
		return Config{}, fmt.Errorf("remote.provider %q is not supported; use \"git\" or \"hub\"", cfg.Provider)
	}
	if cfg.Provider == "git" && (strings.Count(cfg.Repo, "/") != 1 || !repoPart.MatchString(cfg.RepoOwner()) || !repoPart.MatchString(cfg.RepoName())) {
		return Config{}, fmt.Errorf("remote.repo %q must be <owner>/<name>, each matching %s", cfg.Repo, repoPart)
	}
	if cfg.Provider == "hub" {
		if err := ValidateHubURL(cfg.URL); err != nil {
			return Config{}, err
		}
		if !filepath.IsAbs(cfg.TokenFile) {
			return Config{}, errors.New("remote.token_file must be an absolute path")
		}
	}
	if !machineName.MatchString(cfg.Machine) {
		return Config{}, fmt.Errorf("remote.machine %q must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes", cfg.Machine)
	}
	if cfg.Publish.Unpushed != RedactNone && cfg.Publish.Unpushed != RedactUnpushed {
		return Config{}, fmt.Errorf("remote.publish.unpushed %q must be %q or %q", cfg.Publish.Unpushed, RedactNone, RedactUnpushed)
	}
	return cfg, nil
}

// ErrHubURL is the single explanation both the configuration loader and the
// HTTP provider give for a hub endpoint they will not talk to.
var ErrHubURL = errors.New("remote.url must be an HTTPS origin without credentials, query, or fragment, or an http:// origin on a loopback host")

// ValidateHubURL accepts an HTTPS origin, and an http:// origin only when its
// host is a loopback address.
//
// Plain http is otherwise refused because a machine credential travels in the
// Authorization header on every request. A loopback origin is the one case
// where there is no network to intercept: the self-hosted bench in
// spec/features/self-hosted-bench runs the hub inside the same daemon the
// provider talks to, reachable only from this machine, and demanding a
// certificate for 127.0.0.1 would mean "deploy a service" rather than
// "run wb".
func ValidateHubURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return ErrHubURL
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
	}
	return ErrHubURL
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
