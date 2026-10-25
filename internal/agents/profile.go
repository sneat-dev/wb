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
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// Harness names a coding-agent environment and tool loop. Each name is an
// adapter in this package; the field exists so provider and model are not
// collapsed into the harness name.
const (
	HarnessCodex    = "codex"
	HarnessOpenCode = "opencode"
	HarnessDSH      = "dsh"
)

// supportedHarnesses is the closed set this build can launch. A new harness is
// a new adapter, never a configuration value.
func supportedHarnesses() []string {
	return []string{HarnessCodex, HarnessOpenCode, HarnessDSH}
}

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
	// credential. The value is read from the process environment at launch and
	// never persisted.
	CredentialEnv string `yaml:"credential_env,omitempty" json:"credential_env,omitempty"`
	// CredentialFile is an absolute path to a private file holding the
	// credential, following WB's existing credential-file convention (a path in
	// configuration, the secret in a 0600 file under
	// ~/.config/wb/credentials). It exists because a dispatched worker needs its
	// credential in the harness's environment, and a remote machine reachable
	// only over non-interactive SSH has no reliable place to export one.
	CredentialFile string `yaml:"credential_file,omitempty" json:"credential_file,omitempty"`
	WireAPI        string `yaml:"wire_api" json:"wire_api"`
}

// ProviderCredentialEnv is the environment variable WB synthesises when a
// provider supplies its credential as a file. The harness is told this name —
// never a value — so the same per-process mechanism serves both sources.
const ProviderCredentialEnv = "WB_AGENT_PROVIDER_CREDENTIAL"

// Profile is a named agent execution configuration. It is intentionally this
// small: harness + provider + model + optional effort and timeout, and nothing
// else. Nothing here names a model WB ships: the operator's configuration is
// the only place a model identifier exists.
type Profile struct {
	Harness  string `yaml:"harness" json:"harness"`
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string `yaml:"model" json:"model"`
	// Variant is an OpenCode model variant, appended as "#variant" when the
	// argv is built. It is a separate field because "#" is not a legal
	// execution identifier and because a variant name is model-specific.
	Variant string `yaml:"variant,omitempty" json:"variant,omitempty"`
	// EffortVariants maps a WB effort level to the harness's own variant name.
	// An effort with no mapping is not applied to OpenCode.
	EffortVariants map[string]string `yaml:"effort_variants,omitempty" json:"effort_variants,omitempty"`
	// Effort is the profile's default reasoning effort. Reasoning is the
	// legacy spelling of the same thing; naming both is an error.
	Effort    string `yaml:"effort,omitempty" json:"effort,omitempty"`
	Reasoning string `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
	// Timeout is a Go duration string bounding one run with this profile.
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// Config is the resolved user agent configuration: providers, profiles, task
// types, named validation suites, defaults and the ordered fallback list.
type Config struct {
	Providers map[string]Provider
	Profiles  map[string]Profile
	TaskTypes map[string]TaskType
	// ConfiguredTaskTypes records which task types came from the operator, so
	// discovery can tell an override from a built-in.
	ConfiguredTaskTypes map[string]bool
	Validations         map[string][]ValidationStep
	Defaults            Defaults
	Fallback            []string
}

// Defaults are the values a request inherits when it names neither a task type
// nor a profile. They exist so an operator can make one command the default
// without editing every task type.
type Defaults struct {
	TaskType string `yaml:"task_type,omitempty" json:"task_type,omitempty"`
	Profile  string `yaml:"profile,omitempty" json:"profile,omitempty"`
}

// ValidationStep is one objective check a task type may require. Argv is a
// literal argument list, never a shell string, so a validation cannot smuggle
// quoting or piping into the worker's environment.
type ValidationStep struct {
	Name    string   `yaml:"name" json:"name"`
	Argv    []string `yaml:"argv" json:"argv"`
	Timeout string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// TaskType is a named execution policy. It names no model: it points at
// profiles the operator configured.
type TaskType struct {
	Profile    string            `yaml:"profile,omitempty" json:"profile,omitempty"`
	Complexity map[string]string `yaml:"complexity,omitempty" json:"complexity,omitempty"`
	// Fallback is the ordered list of profiles to try after Profile, so one
	// provider or model being down does not stop the work. Order is the
	// operator's cost decision: WB never reorders it.
	Fallback []string `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	// FallbackOn selects when fallback applies: "preflight" (a capability WB
	// can check before starting) and/or "runtime" (the provider failed the run
	// itself). Preflight is the default; runtime is opt-in because it starts a
	// second execution.
	FallbackOn []string `yaml:"fallback_on,omitempty" json:"fallback_on,omitempty"`
	Effort     string   `yaml:"effort,omitempty" json:"effort,omitempty"`
	Worktree   string   `yaml:"worktree,omitempty" json:"worktree,omitempty"`
	Timeout    string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Validate   []string `yaml:"validate,omitempty" json:"validate,omitempty"`
	Acceptance string   `yaml:"acceptance,omitempty" json:"acceptance,omitempty"`
}

// Fallback triggers. Preflight is a capability WB can prove before a worktree
// exists; runtime is a failure the harness reported after it started.
const (
	FallbackPreflight = "preflight"
	FallbackRuntime   = "runtime"
)

// fileConfig is the on-disk shape, nested under the shared wb.yaml "agents"
// mapping so agent configuration lives in WB's existing configuration file
// rather than a second configuration hierarchy.
type fileConfig struct {
	Agents struct {
		Providers   map[string]Provider         `yaml:"providers"`
		Profiles    map[string]Profile          `yaml:"profiles"`
		TaskTypes   map[string]TaskType         `yaml:"task_types"`
		Validations map[string][]ValidationStep `yaml:"validations"`
		Defaults    Defaults                    `yaml:"defaults"`
		Fallback    []string                    `yaml:"fallback"`
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
	config := Config{
		Providers:   builtinProviders(),
		Profiles:    map[string]Profile{},
		TaskTypes:   builtinTaskTypes(),
		Validations: map[string][]ValidationStep{},
	}
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
		// Naming two credential sources is ambiguous, and silently picking one
		// would make a mistake in configuration indistinguishable from a
		// deliberate override. Refuse it before the merge can hide it.
		if strings.TrimSpace(provider.CredentialEnv) != "" && strings.TrimSpace(provider.CredentialFile) != "" {
			return Config{}, fmt.Errorf("agent configuration %s: provider %q must name exactly one credential source: credential_env or credential_file, not both", path, name)
		}
		// A user entry overrides a built-in one field by field, so a provider
		// only needs to name what differs. This is the whole extension point:
		// no plugin loading, no catalogue, no capability negotiation.
		merged := config.Providers[name]
		if strings.TrimSpace(provider.BaseURL) != "" {
			merged.BaseURL = strings.TrimSpace(provider.BaseURL)
		}
		if strings.TrimSpace(provider.CredentialEnv) != "" {
			merged.CredentialEnv = strings.TrimSpace(provider.CredentialEnv)
			// An explicit source replaces the built-in one rather than being
			// combined with it, so a provider never has two credentials.
			merged.CredentialFile = ""
		}
		if strings.TrimSpace(provider.CredentialFile) != "" {
			merged.CredentialFile = strings.TrimSpace(provider.CredentialFile)
			merged.CredentialEnv = ""
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
	for name, steps := range document.Agents.Validations {
		config.Validations[name] = steps
	}
	config.ConfiguredTaskTypes = make(map[string]bool, len(document.Agents.TaskTypes))
	for name, taskType := range document.Agents.TaskTypes {
		config.TaskTypes[name] = taskType
		config.ConfiguredTaskTypes[name] = true
	}
	config.Defaults = document.Agents.Defaults
	config.Fallback = document.Agents.Fallback
	// Everything a request can name is validated once, here, so a typo fails
	// before a worktree exists rather than halfway through a comparison.
	if err := config.validate(); err != nil {
		return Config{}, fmt.Errorf("agent configuration %s: %w", path, err)
	}
	return config, nil
}

// validate cross-checks every reference the configuration makes. It runs after
// the whole document is merged, because a task type may name a profile declared
// later in the same file.
func (config Config) validate() error {
	for name := range config.Validations {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("validation suite name must not be empty")
		}
	}
	for suite, steps := range config.Validations {
		if len(steps) == 0 {
			return fmt.Errorf("validation suite %q has no steps", suite)
		}
		for index, step := range steps {
			if strings.TrimSpace(step.Name) == "" {
				return fmt.Errorf("validation suite %q step %d requires a name", suite, index+1)
			}
			if len(step.Argv) == 0 {
				return fmt.Errorf("validation suite %q step %q requires argv", suite, step.Name)
			}
			if _, err := parseStepTimeout(step.Timeout); err != nil {
				return fmt.Errorf("validation suite %q step %q: %w", suite, step.Name, err)
			}
		}
	}
	for name, taskType := range config.TaskTypes {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("task type name must not be empty")
		}
		if err := config.validateTaskType(name, taskType); err != nil {
			return err
		}
	}
	if config.Defaults.TaskType != "" {
		if _, ok := config.TaskTypes[config.Defaults.TaskType]; !ok {
			return fmt.Errorf("agents.defaults.task_type %q is not a configured task type", config.Defaults.TaskType)
		}
	}
	if config.Defaults.Profile != "" {
		if _, ok := config.Profiles[config.Defaults.Profile]; !ok {
			return fmt.Errorf("agents.defaults.profile %q is not a configured profile", config.Defaults.Profile)
		}
	}
	for _, name := range config.Fallback {
		if _, err := config.Resolve(name); err != nil {
			return fmt.Errorf("agents.fallback profile %q is not usable: %w", name, err)
		}
	}
	// Profiles themselves are resolved on demand: `wb agent profiles` must be
	// able to list a configuration it reports as unusable, and `run` fails on the
	// exact profile it was asked for rather than on an unrelated one.
	return nil
}

func (config Config) validateTaskType(name string, taskType TaskType) error {
	if strings.TrimSpace(taskType.Profile) != "" {
		if _, ok := config.Profiles[taskType.Profile]; !ok {
			return fmt.Errorf("task type %q names unknown profile %q; configured profiles: %s", name, taskType.Profile, config.profileNames())
		}
	}
	for _, profile := range taskType.Fallback {
		if _, ok := config.Profiles[profile]; !ok {
			return fmt.Errorf("task type %q fallback names unknown profile %q; configured profiles: %s", name, profile, config.profileNames())
		}
		if profile == taskType.Profile {
			return fmt.Errorf("task type %q fallback repeats its own profile %q", name, profile)
		}
	}
	for _, trigger := range taskType.FallbackOn {
		if trigger != FallbackPreflight && trigger != FallbackRuntime {
			return fmt.Errorf("task type %q fallback_on %q must be %q or %q", name, trigger, FallbackPreflight, FallbackRuntime)
		}
	}
	for level, profile := range taskType.Complexity {
		if !validComplexity(level) {
			return fmt.Errorf("task type %q complexity %q is not one of %s", name, level, complexityLevels())
		}
		if _, ok := config.Profiles[profile]; !ok {
			return fmt.Errorf("task type %q complexity %q names unknown profile %q; configured profiles: %s", name, level, profile, config.profileNames())
		}
	}
	if strings.TrimSpace(taskType.Effort) != "" && !validEffort(taskType.Effort) {
		return fmt.Errorf("task type %q effort %q is not one of %s", name, taskType.Effort, effortLevels())
	}
	if mode := strings.TrimSpace(taskType.Worktree); mode != "" && mode != ModeAuto && mode != ModeCurrent {
		return fmt.Errorf("task type %q worktree %q must be %q or %q", name, mode, ModeAuto, ModeCurrent)
	}
	if _, err := parseStepTimeout(taskType.Timeout); err != nil {
		return fmt.Errorf("task type %q: %w", name, err)
	}
	for _, suite := range taskType.Validate {
		if _, ok := config.Validations[suite]; !ok {
			return fmt.Errorf("task type %q names unknown validation suite %q", name, suite)
		}
	}
	if acceptance := strings.TrimSpace(taskType.Acceptance); acceptance != "" && acceptance != AcceptanceRequired && acceptance != AcceptanceOptional {
		return fmt.Errorf("task type %q acceptance %q must be %q or %q", name, acceptance, AcceptanceRequired, AcceptanceOptional)
	}
	return nil
}

// Resolved is one profile after resolution: the requested name plus what will
// actually execute, snapshotted so a later edit to the profile cannot rewrite
// what a finished run did.
type Resolved struct {
	Profile  string `json:"profile"`
	Harness  string `json:"harness"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	// Variant is the OpenCode model variant exactly as configured; it is never
	// folded into Model, so Model stays a valid execution identifier.
	Variant string `json:"variant,omitempty"`
	// Effort is the profile's own default. The effort a run actually requested
	// and whether it was applied are recorded separately from this snapshot.
	Effort string `json:"effort,omitempty"`
	// EffortVariants snapshots the profile's effort→variant map, so a plan can
	// still resolve an effort after the configuration file changes.
	EffortVariants map[string]string `json:"effort_variants,omitempty"`
	// Routing is the provider registry entry a Codex run will be configured
	// with. It carries no credential, only the name of the variable holding it,
	// and is absent for harnesses that use their own auth store.
	Routing Provider `json:"routing,omitempty"`
	// TimeoutMS is the profile's own bound, when configured.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
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
	return config.resolveProfile(requested, profile)
}

func (config Config) resolveProfile(requested string, profile Profile) (Resolved, error) {
	harness := strings.TrimSpace(profile.Harness)
	if !supportedHarness(harness) {
		return Resolved{}, requestErrorf("agent profile %q requests harness %q; this WB build supports %s", requested, profile.Harness, strings.Join(supportedHarnesses(), ", "))
	}
	model := strings.TrimSpace(profile.Model)
	if err := validateExecutionValue("model", model, true); err != nil {
		return Resolved{}, requestErrorf("agent profile %q: %v", requested, err)
	}
	effort := strings.TrimSpace(profile.Effort)
	reasoning := strings.TrimSpace(profile.Reasoning)
	if effort != "" && reasoning != "" {
		return Resolved{}, requestErrorf("agent profile %q sets both effort and the legacy reasoning; keep one", requested)
	}
	if effort == "" {
		effort = reasoning
	}
	if effort != "" && !validEffort(effort) {
		return Resolved{}, requestErrorf("agent profile %q effort %q is not one of %s", requested, effort, effortLevels())
	}
	variant := strings.TrimSpace(profile.Variant)
	if variant != "" {
		if harness != HarnessOpenCode {
			return Resolved{}, requestErrorf("agent profile %q sets variant, which only the %q harness supports", requested, HarnessOpenCode)
		}
		if err := validateExecutionValue("variant", variant, true); err != nil {
			return Resolved{}, requestErrorf("agent profile %q: %v", requested, err)
		}
	}
	for level, mapped := range profile.EffortVariants {
		if !validEffort(level) {
			return Resolved{}, requestErrorf("agent profile %q effort_variants key %q is not one of %s", requested, level, effortLevels())
		}
		if strings.TrimSpace(mapped) == "" {
			return Resolved{}, requestErrorf("agent profile %q effort_variants %q is empty", requested, level)
		}
		if err := validateExecutionValue("variant", mapped, true); err != nil {
			return Resolved{}, requestErrorf("agent profile %q effort_variants %q: %v", requested, level, err)
		}
		if harness != HarnessOpenCode {
			return Resolved{}, requestErrorf("agent profile %q sets effort_variants, which only the %q harness supports", requested, HarnessOpenCode)
		}
	}
	timeout, err := parseStepTimeout(profile.Timeout)
	if err != nil {
		return Resolved{}, requestErrorf("agent profile %q: %v", requested, err)
	}
	resolved := Resolved{Profile: requested, Harness: harness, Model: model, Variant: variant, Effort: effort}
	if len(profile.EffortVariants) > 0 {
		resolved.EffortVariants = make(map[string]string, len(profile.EffortVariants))
		for level, mapped := range profile.EffortVariants {
			resolved.EffortVariants[level] = mapped
		}
	}
	if timeout > 0 {
		resolved.TimeoutMS = timeout.Milliseconds()
	}
	switch harness {
	case HarnessCodex:
		providerName := strings.TrimSpace(profile.Provider)
		routing, ok := config.Providers[providerName]
		if !ok {
			return Resolved{}, requestErrorf("agent profile %q requests unknown provider %q; configured providers: %s", requested, profile.Provider, config.providerNames())
		}
		resolved.Provider = providerName
		resolved.Routing = routing
	case HarnessOpenCode:
		// OpenCode resolves its own provider from "provider/model", so the
		// provider is metadata derived from the model. Naming a different one
		// would put two incompatible answers in the same record.
		prefix, _, found := strings.Cut(model, "/")
		if !found || strings.TrimSpace(prefix) == "" {
			return Resolved{}, requestErrorf("agent profile %q model %q must be an OpenCode \"provider/model\" identifier", requested, model)
		}
		if named := strings.TrimSpace(profile.Provider); named != "" && named != prefix {
			return Resolved{}, requestErrorf("agent profile %q names provider %q but its model %q belongs to provider %q", requested, named, model, prefix)
		}
		resolved.Provider = prefix
	case HarnessDSH:
		// dsh reads its own credentials and its default inference provider; the
		// only provider WB can prove is the one its compose tree names.
		if named := strings.TrimSpace(profile.Provider); named != "" && named != ProviderDshDefault {
			return Resolved{}, requestErrorf("agent profile %q names provider %q; the %q harness supports only %q in this build", requested, named, HarnessDSH, ProviderDshDefault)
		}
		resolved.Provider = ProviderDshDefault
	}
	return resolved, nil
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
	envNamed := strings.TrimSpace(provider.CredentialEnv) != ""
	fileNamed := strings.TrimSpace(provider.CredentialFile) != ""
	switch {
	case envNamed && fileNamed:
		return fmt.Errorf("provider %q must name exactly one credential source: credential_env or credential_file, not both", name)
	case !envNamed && !fileNamed:
		return fmt.Errorf("provider %q requires credential_env (the environment variable holding its credential) or credential_file (a private file holding it)", name)
	case fileNamed:
		credentialFile := strings.TrimSpace(provider.CredentialFile)
		if !path.IsAbs(credentialFile) || path.Clean(credentialFile) != credentialFile {
			return fmt.Errorf("provider %q credential_file %q must be a clean absolute path", name, credentialFile)
		}
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
