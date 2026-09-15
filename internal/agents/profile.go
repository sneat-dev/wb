// Package agents dispatches one bounded coding task to one configured agent
// harness running inside an isolated WB worktree, and reports the execution
// facts of that run to whoever asks later.
//
// It is deliberately the deterministic execution layer only: it resolves an
// agent profile, creates or resolves a worktree through WB's existing worktree
// service, launches the harness as a detached child, and records what
// happened. It never judges whether the work is correct. A PASS/FAIL/ESCALATE
// verdict on a diff is a semantic decision that belongs to the supervisor
// above WB, not to WB.
package agents

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// Harness names a coding-agent environment and tool loop. The MVP supports
// exactly one; the field exists so provider and model are not collapsed into
// the harness name.
const HarnessCodex = "codex"

// Wire protocols the Codex harness can speak. The set is closed because it is
// the harness's own contract, not a user extension point.
const (
	WireAPIResponses = "responses"
	WireAPIChat      = "chat"
)

// Provider is one entry in the provider registry: where inference lives, which
// environment variable carries its credential, and which wire protocol the
// harness must use. It never carries the credential itself.
type Provider struct {
	BaseURL string `yaml:"base_url" json:"base_url"`
	// CredentialEnv is the *name* of the environment variable holding the
	// credential. The value is read from the process environment at launch
	// and never persisted.
	CredentialEnv string `yaml:"credential_env" json:"credential_env"`
	WireAPI       string `yaml:"wire_api" json:"wire_api"`
}

// Profile is a named agent execution configuration. It is intentionally this
// small: harness + provider + model + optional reasoning, and nothing else.
type Profile struct {
	Harness   string `yaml:"harness" json:"harness"`
	Provider  string `yaml:"provider" json:"provider"`
	Model     string `yaml:"model" json:"model"`
	Reasoning string `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
}

// Config is the resolved user agent configuration.
type Config struct {
	Providers map[string]Provider
	Profiles  map[string]Profile
}

// fileConfig is the on-disk shape, nested under the shared wb.yaml "agents"
// mapping so profiles live in WB's existing configuration file rather than a
// second configuration hierarchy.
type fileConfig struct {
	Agents struct {
		Providers map[string]Provider `yaml:"providers"`
		Profiles  map[string]Profile  `yaml:"profiles"`
	} `yaml:"agents"`
}

// builtinProviders are the registry entries WB ships with, so the common case
// needs no provider configuration at all. The model identifiers are the ones
// each provider's own catalogue publishes for DeepSeek V4.1 Flash; WB passes a
// profile's model through verbatim and keeps no catalogue of its own.
func builtinProviders() map[string]Provider {
	return map[string]Provider{
		"deepseek": {
			BaseURL:       "https://api.deepseek.com",
			CredentialEnv: "DEEPSEEK_API_KEY",
			WireAPI:       WireAPIResponses,
		},
		"openrouter": {
			BaseURL:       "https://openrouter.ai/api/v1",
			CredentialEnv: "OPENROUTER_API_KEY",
			WireAPI:       WireAPIResponses,
		},
	}
}

// LoadConfigFile reads the agent configuration from a wb.yaml document. A
// missing file is not an error: it yields the built-in provider registry and
// no profiles, so "no such profile" stays the actionable message rather than
// "no such file".
func LoadConfigFile(path string) (Config, error) {
	config := Config{Providers: builtinProviders(), Profiles: map[string]Profile{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return Config{}, fmt.Errorf("read agent configuration %s: %w", path, err)
	}
	var document fileConfig
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return Config{}, fmt.Errorf("parse agent configuration %s: %w", path, err)
	}
	for name, provider := range document.Agents.Providers {
		// A user entry overrides a built-in one field by field, so a provider
		// only needs to name what differs. This is the whole extension point:
		// no plugin loading, no catalogue, no capability negotiation.
		merged := config.Providers[name]
		if strings.TrimSpace(provider.BaseURL) != "" {
			merged.BaseURL = strings.TrimSpace(provider.BaseURL)
		}
		if strings.TrimSpace(provider.CredentialEnv) != "" {
			merged.CredentialEnv = strings.TrimSpace(provider.CredentialEnv)
		}
		if strings.TrimSpace(provider.WireAPI) != "" {
			merged.WireAPI = strings.TrimSpace(provider.WireAPI)
		}
		if err := validateProvider(name, merged); err != nil {
			return Config{}, fmt.Errorf("agent configuration %s: %w", path, err)
		}
		config.Providers[name] = merged
	}
	for name, profile := range document.Agents.Profiles {
		config.Profiles[name] = profile
	}
	for name := range config.Providers {
		if err := validateProvider(name, config.Providers[name]); err != nil {
			return Config{}, fmt.Errorf("agent configuration %s: %w", path, err)
		}
	}
	return config, nil
}

// Resolved is one profile after resolution: the requested name plus what will
// actually execute, snapshotted so a later edit to the profile cannot rewrite
// what a finished run did.
type Resolved struct {
	Profile   string `json:"profile"`
	Harness   string `json:"harness"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Reasoning string `json:"reasoning,omitempty"`
	// Routing is the provider registry entry the harness will be configured
	// with. It carries no credential, only the name of the variable holding it.
	Routing Provider `json:"routing"`
}

// Resolve turns a requested profile name into an executable configuration,
// failing closed on anything it cannot execute.
func (config Config) Resolve(name string) (Resolved, error) {
	requested := strings.TrimSpace(name)
	if requested == "" {
		return Resolved{}, requestErrorf("--profile is required; configured profiles: %s", config.profileNames())
	}
	profile, ok := config.Profiles[requested]
	if !ok {
		return Resolved{}, requestErrorf("unknown agent profile %q; configured profiles: %s", requested, config.profileNames())
	}
	harness := strings.TrimSpace(profile.Harness)
	if harness != HarnessCodex {
		return Resolved{}, requestErrorf("agent profile %q requests harness %q; this WB build supports %q", requested, profile.Harness, HarnessCodex)
	}
	providerName := strings.TrimSpace(profile.Provider)
	routing, ok := config.Providers[providerName]
	if !ok {
		return Resolved{}, requestErrorf("agent profile %q requests unknown provider %q; configured providers: %s", requested, profile.Provider, config.providerNames())
	}
	model := strings.TrimSpace(profile.Model)
	if err := validateExecutionValue("model", model, true); err != nil {
		return Resolved{}, requestErrorf("agent profile %q: %v", requested, err)
	}
	reasoning := strings.TrimSpace(profile.Reasoning)
	if err := validateExecutionValue("reasoning", reasoning, false); err != nil {
		return Resolved{}, requestErrorf("agent profile %q: %v", requested, err)
	}
	return Resolved{
		Profile: requested, Harness: harness, Provider: providerName,
		Model: model, Reasoning: reasoning, Routing: routing,
	}, nil
}

func (config Config) profileNames() string {
	if len(config.Profiles) == 0 {
		return "(none configured)"
	}
	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (config Config) providerNames() string {
	names := make([]string, 0, len(config.Providers))
	for name := range config.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func validateProvider(name string, provider Provider) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("provider name must not be empty")
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			// The name is interpolated into the harness's dotted configuration
			// key ("model_providers.<name>.base_url"), so it must stay one
			// safe segment rather than quoting its way into a nested key.
			return fmt.Errorf("provider %q must be a lowercase identifier of letters, digits, '-' or '_'", name)
		}
	}
	if !strings.HasPrefix(provider.BaseURL, "https://") {
		return fmt.Errorf("provider %q base_url must be an https URL", name)
	}
	if strings.TrimSpace(provider.CredentialEnv) == "" {
		return fmt.Errorf("provider %q requires credential_env naming the environment variable that holds its credential", name)
	}
	switch provider.WireAPI {
	case WireAPIResponses, WireAPIChat:
	default:
		return fmt.Errorf("provider %q wire_api %q unsupported; use %q or %q", name, provider.WireAPI, WireAPIResponses, WireAPIChat)
	}
	return nil
}

// validateExecutionValue rejects values that cannot safely become one harness
// configuration token, or that are shaped like a credential. The rule itself is
// WB's existing one, reused rather than restated: a value accepted here is also
// accepted by the Work Log claim a created worktree publishes, so an agent
// profile cannot record an execution identity the claim would refuse.
func validateExecutionValue(field, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if !worktrees.ValidExecutionIdentifier(value, false) {
		return fmt.Errorf("%s %q must be a non-secret execution identifier: letters, digits, and . _ : / + - only, starting with a letter or digit", field, value)
	}
	return nil
}
