package wbconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultCockpitHostedURL is the one place the hosted Cockpit address is
// spelled; every consumer reads it through CockpitConfig.
const DefaultCockpitHostedURL = "https://sneat.dev/wb/cockpit/"

// DefaultCockpitCodeBrowserURL is the code browser Cockpit links to unless
// cockpit.code_browser_url says otherwise.
const DefaultCockpitCodeBrowserURL = "https://codegrapher.dev/"

// CockpitConfig is wb.yaml's cockpit: section, after defaults.
type CockpitConfig struct {
	// HostedURL is the hosted Cockpit origin and path.
	HostedURL string
	// CodeBrowserURL is where Cockpit's code links point.
	CodeBrowserURL string
	// AnonymousMetadata allows a loopback request with no session to read
	// fleet metadata.
	AnonymousMetadata bool
	// RefreshInterval is how often the daemon refreshes the fleet snapshot.
	// Zero means unset: the consumer chooses its default.
	RefreshInterval time.Duration
	// PullRequestLimit is the most pull requests the daemon observes on GitHub
	// in one pass (cockpit-views#req:pull-request-fields). Zero means unset:
	// the consumer chooses its default.
	PullRequestLimit int
	// PullRequestHourlyBudget is the most pull request observations in a
	// rolling hour. Zero means unset: the consumer chooses its default.
	PullRequestHourlyBudget int
	// CodeIndexProvider names the code-index provider whose statistics the
	// panels show; empty means none is configured, which is a normal state.
	CodeIndexProvider string
	// CodeIndexIndexer is the hooks executor whose receipts the provider
	// follows; empty means the provider's own default.
	CodeIndexIndexer string
}

// MaxCockpitPullRequestLimit bounds cockpit.pull_request_limit, so one pass
// cannot be configured into an unbounded number of GitHub reads.
const MaxCockpitPullRequestLimit = 200

// The bounds of cockpit.pull_request_hourly_budget.
const (
	MinCockpitPullRequestHourlyBudget = 10
	MaxCockpitPullRequestHourlyBudget = 2000
)

// CodeIndexProviderCodeGrapher is the one code-index provider there is.
const CodeIndexProviderCodeGrapher = "codegrapher"

// codeIndexIndexerName is what the lifecycle-hook worker allows an executor to
// be called.
var codeIndexIndexerName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// cockpitSection is the keys cockpit: may hold; an unknown key is an error.
type cockpitSection struct {
	HostedURL         *string `yaml:"hosted_url"`
	CodeBrowserURL    *string `yaml:"code_browser_url"`
	AnonymousMetadata *bool   `yaml:"anonymous_metadata"`
	RefreshInterval   *string `yaml:"refresh_interval"`
	PullRequestLimit  *int    `yaml:"pull_request_limit"`
	PullRequestBudget *int    `yaml:"pull_request_hourly_budget"`
	CodeIndexProvider *string `yaml:"code_index_provider"`
	CodeIndexIndexer  *string `yaml:"code_index_indexer"`
}

// DefaultCockpitConfig is the section's value when wb.yaml sets nothing.
func DefaultCockpitConfig() CockpitConfig {
	return CockpitConfig{
		HostedURL:         DefaultCockpitHostedURL,
		CodeBrowserURL:    DefaultCockpitCodeBrowserURL,
		AnonymousMetadata: true,
	}
}

// validateCockpitURL applies the rule validatePeersUpstreamURL applies to a
// hub address, minus its no-path requirement: https, or http only for a
// loopback host, with no userinfo, query or fragment.
func validateCockpitURL(key, raw string) error {
	parsed, err := url.Parse(raw)
	valid := err == nil && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
	valid = valid && (parsed.Scheme == "https" || (parsed.Scheme == "http" && isLoopbackPeersHost(parsed.Hostname())))
	if !valid {
		return fmt.Errorf("cockpit.%s must be an https URL, or an http URL to localhost/127.0.0.1/::1, with no userinfo, query or fragment, got %q", key, raw)
	}
	return nil
}

// parseCockpit decodes the cockpit: section from raw YAML bytes, applying
// defaults to every key that is absent and rejecting an unknown key or a value
// that is present but invalid. URL values are stored trimmed.
func parseCockpit(raw []byte) (CockpitConfig, error) {
	var file struct {
		Cockpit yaml.Node `yaml:"cockpit"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return CockpitConfig{}, fmt.Errorf("parse config: %w", err)
	}
	config := DefaultCockpitConfig()
	if file.Cockpit.Kind == 0 {
		return config, nil
	}
	// Re-encoding the subtree lets a strict decoder reject unknown keys in
	// it alone, without constraining the file's other sections.
	subtree, _ := yaml.Marshal(&file.Cockpit)
	decoder := yaml.NewDecoder(bytes.NewReader(subtree))
	decoder.KnownFields(true)
	var section cockpitSection
	// An empty "cockpit:" re-encodes to nothing; that is the defaults.
	if err := decoder.Decode(&section); err != nil && !errors.Is(err, io.EOF) {
		return CockpitConfig{}, fmt.Errorf("parse cockpit section: %w", err)
	}
	if section.HostedURL != nil {
		config.HostedURL = strings.TrimSpace(*section.HostedURL)
	}
	if section.CodeBrowserURL != nil {
		config.CodeBrowserURL = strings.TrimSpace(*section.CodeBrowserURL)
	}
	if section.AnonymousMetadata != nil {
		config.AnonymousMetadata = *section.AnonymousMetadata
	}
	if err := validateCockpitURL("hosted_url", config.HostedURL); err != nil {
		return CockpitConfig{}, err
	}
	if err := validateCockpitURL("code_browser_url", config.CodeBrowserURL); err != nil {
		return CockpitConfig{}, err
	}
	if section.RefreshInterval != nil {
		interval, err := time.ParseDuration(*section.RefreshInterval)
		if err != nil || interval <= 0 {
			return CockpitConfig{}, fmt.Errorf("cockpit.refresh_interval must be a positive duration such as 30s, got %q", *section.RefreshInterval)
		}
		config.RefreshInterval = interval
	}
	if section.PullRequestLimit != nil {
		if *section.PullRequestLimit < 1 || *section.PullRequestLimit > MaxCockpitPullRequestLimit {
			return CockpitConfig{}, fmt.Errorf("cockpit.pull_request_limit must be between 1 and %d, got %d", MaxCockpitPullRequestLimit, *section.PullRequestLimit)
		}
		config.PullRequestLimit = *section.PullRequestLimit
	}
	if section.PullRequestBudget != nil {
		if *section.PullRequestBudget < MinCockpitPullRequestHourlyBudget || *section.PullRequestBudget > MaxCockpitPullRequestHourlyBudget {
			return CockpitConfig{}, fmt.Errorf("cockpit.pull_request_hourly_budget must be between %d and %d, got %d", MinCockpitPullRequestHourlyBudget, MaxCockpitPullRequestHourlyBudget, *section.PullRequestBudget)
		}
		config.PullRequestHourlyBudget = *section.PullRequestBudget
	}
	if err := parseCockpitCodeIndex(section, &config); err != nil {
		return CockpitConfig{}, err
	}
	return config, nil
}

// parseCockpitCodeIndex applies the code-index keys: a provider that is known,
// and an indexer name only beside a provider.
func parseCockpitCodeIndex(section cockpitSection, config *CockpitConfig) error {
	if section.CodeIndexProvider != nil {
		config.CodeIndexProvider = strings.TrimSpace(*section.CodeIndexProvider)
		if config.CodeIndexProvider != CodeIndexProviderCodeGrapher {
			return fmt.Errorf("cockpit.code_index_provider must be %q, got %q", CodeIndexProviderCodeGrapher, config.CodeIndexProvider)
		}
	}
	if section.CodeIndexIndexer != nil {
		config.CodeIndexIndexer = strings.TrimSpace(*section.CodeIndexIndexer)
		if config.CodeIndexProvider == "" {
			return errors.New("cockpit.code_index_indexer needs cockpit.code_index_provider")
		}
		if !codeIndexIndexerName.MatchString(config.CodeIndexIndexer) {
			return fmt.Errorf("cockpit.code_index_indexer must be a hooks executor name (lower-case letters, digits and hyphens), got %q", config.CodeIndexIndexer)
		}
	}
	return nil
}

// LoadCockpit reads the cockpit: section from path. An absent file or section
// is not an error: the defaults apply.
func LoadCockpit(path string) (CockpitConfig, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultCockpitConfig(), nil
	}
	if err != nil {
		return CockpitConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	config, err := parseCockpit(raw)
	if err != nil {
		return CockpitConfig{}, fmt.Errorf("config %s: %w", path, err)
	}
	return config, nil
}
