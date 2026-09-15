package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigFileShipsBuiltinsAndAppliesUserOverrides(t *testing.T) {
	config, err := LoadConfigFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing configuration file must not be an error: %v", err)
	}
	if got := config.Providers["deepseek"].BaseURL; got != "https://api.deepseek.com" {
		t.Fatalf("built-in deepseek base_url = %q", got)
	}
	if got := config.Providers["deepseek"].CredentialEnv; got != "DEEPSEEK_API_KEY" {
		t.Fatalf("built-in deepseek credential_env = %q", got)
	}
	if got := config.Providers["deepseek"].WireAPI; got != WireAPIResponses {
		t.Fatalf("built-in deepseek wire_api = %q", got)
	}
	if got := config.Providers["openrouter"].BaseURL; got != "https://openrouter.ai/api/v1" {
		t.Fatalf("built-in openrouter base_url = %q", got)
	}

	path := writeConfig(t, `
agents:
  providers:
    deepseek:
      base_url: https://proxy.internal/v1
    local:
      base_url: https://localhost.example/v1
      credential_env: LOCAL_KEY
      wire_api: chat
  profiles:
    cheap:
      harness: codex
      provider: deepseek
      model: deepseek-flash
      reasoning: high
    local-model:
      harness: codex
      provider: local
      model: local-1
`)
	config, err = LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	merged := config.Providers["deepseek"]
	if merged.BaseURL != "https://proxy.internal/v1" {
		t.Fatalf("override did not apply: %q", merged.BaseURL)
	}
	// An override names only what differs, so the untouched fields survive.
	if merged.CredentialEnv != "DEEPSEEK_API_KEY" || merged.WireAPI != WireAPIResponses {
		t.Fatalf("partial override dropped built-in fields: %#v", merged)
	}
	if got := config.Providers["local"].WireAPI; got != WireAPIChat {
		t.Fatalf("new provider wire_api = %q", got)
	}
	if len(config.Profiles) != 2 {
		t.Fatalf("profiles = %#v", config.Profiles)
	}
}

func TestLoadConfigFileRejectsUnusableProvidersAndYAML(t *testing.T) {
	cases := map[string]string{
		"provider name is not one safe segment":  "agents:\n  providers:\n    \"bad name\":\n      base_url: https://x.example\n      credential_env: K\n      wire_api: responses\n",
		"provider name with a dot nests the key": "agents:\n  providers:\n    \"bad.name\":\n      base_url: https://x.example\n      credential_env: K\n      wire_api: responses\n",
		"base_url is not https":                  "agents:\n  providers:\n    p:\n      base_url: http://x.example\n      credential_env: K\n      wire_api: responses\n",
		"credential_env missing":                 "agents:\n  providers:\n    p:\n      base_url: https://x.example\n      wire_api: responses\n",
		"wire_api unsupported":                   "agents:\n  providers:\n    p:\n      base_url: https://x.example\n      credential_env: K\n      wire_api: grpc\n",
		"malformed yaml":                         "agents: [this is not a map\n",
		"empty provider name":                    "agents:\n  providers:\n    \"\":\n      base_url: https://x.example\n      credential_env: K\n      wire_api: responses\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfigFile(writeConfig(t, body)); err == nil {
				t.Fatalf("LoadConfigFile accepted %s", name)
			}
		})
	}
}

func TestLoadConfigFileReportsUnreadablePath(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadConfigFile(directory); err == nil {
		t.Fatal("a directory is not a readable configuration file")
	}
}

func TestResolveFailsClosedAndIsARequestError(t *testing.T) {
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    good:
      harness: codex
      provider: deepseek
      model: deepseek-flash
      reasoning: high
`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"":             "required",
		"   ":          "required",
		"missing":      "unknown agent profile",
		"sk-secretkey": "unknown agent profile",
	}
	for requested, fragment := range cases {
		t.Run("requested="+requested, func(t *testing.T) {
			_, err := config.Resolve(requested)
			if err == nil {
				t.Fatalf("Resolve(%q) succeeded", requested)
			}
			if !IsRequestError(err) {
				t.Fatalf("Resolve(%q) must refuse the invocation, got %T", requested, err)
			}
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("Resolve(%q) error %q does not mention %q", requested, err, fragment)
			}
		})
	}

	resolved, err := config.Resolve("good")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Harness != HarnessCodex || resolved.Provider != "deepseek" || resolved.Model != "deepseek-flash" || resolved.Reasoning != "high" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if resolved.Routing.CredentialEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("routing was not carried through: %#v", resolved.Routing)
	}
	if resolved.Profile != "good" {
		t.Fatalf("requested profile name was not retained: %q", resolved.Profile)
	}
}

func TestResolveRejectsUnsupportedHarnessProviderAndUnsafeValues(t *testing.T) {
	cases := map[string]struct {
		body   string
		expect string
	}{
		"unsupported harness": {
			body:   "agents:\n  profiles:\n    p:\n      harness: claude-code\n      provider: deepseek\n      model: m\n",
			expect: "supports \"codex\"",
		},
		"unknown provider": {
			body:   "agents:\n  profiles:\n    p:\n      harness: codex\n      provider: nope\n      model: m\n",
			expect: "unknown provider",
		},
		"missing model": {
			body:   "agents:\n  profiles:\n    p:\n      harness: codex\n      provider: deepseek\n",
			expect: "model is required",
		},
		"credential-shaped model": {
			body:   "agents:\n  profiles:\n    p:\n      harness: codex\n      provider: deepseek\n      model: sk-abcdef\n",
			expect: "looks like a credential",
		},
		"credential-shaped reasoning": {
			body:   "agents:\n  profiles:\n    p:\n      harness: codex\n      provider: deepseek\n      model: m\n      reasoning: my-token\n",
			expect: "looks like a credential",
		},
		"whitespace in model": {
			body:   "agents:\n  profiles:\n    p:\n      harness: codex\n      provider: deepseek\n      model: \"two words\"\n",
			expect: "non-secret execution identifier",
		},
		"overlong model": {
			body:   "agents:\n  profiles:\n    p:\n      harness: codex\n      provider: deepseek\n      model: " + strings.Repeat("a", 200) + "\n",
			expect: "too long",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			config, err := LoadConfigFile(writeConfig(t, testCase.body))
			if err != nil {
				t.Fatal(err)
			}
			_, err = config.Resolve("p")
			if err == nil {
				t.Fatalf("Resolve accepted %s", name)
			}
			if !IsRequestError(err) {
				t.Fatalf("Resolve refusal must be a request error, got %T", err)
			}
			if !strings.Contains(err.Error(), testCase.expect) {
				t.Fatalf("error %q does not mention %q", err, testCase.expect)
			}
		})
	}
}

func TestResolveNamesConfiguredProfilesAndProviders(t *testing.T) {
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    zebra:
      harness: codex
      provider: deepseek
      model: m
    alpha:
      harness: codex
      provider: deepseek
      model: m
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Resolve("nope"); !strings.Contains(err.Error(), "alpha, zebra") {
		t.Fatalf("profile list must be sorted and complete: %v", err)
	}
	empty, err := LoadConfigFile(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.Resolve("nope"); !strings.Contains(err.Error(), "(none configured)") {
		t.Fatalf("an empty configuration must say so: %v", err)
	}
	if _, err := empty.Resolve(""); !strings.Contains(err.Error(), "configured profiles: (none configured)") {
		t.Fatalf("missing-profile message must name the file's state: %v", err)
	}
	providers := empty.providerNames()
	if !strings.Contains(providers, "deepseek") || !strings.Contains(providers, "openrouter") {
		t.Fatalf("provider list = %q", providers)
	}
}

func TestValidateExecutionValueAcceptsRealModelIdentifiers(t *testing.T) {
	// The identifiers each provider actually publishes must survive validation
	// unchanged: WB passes the model through verbatim.
	for _, value := range []string{
		"deepseek-flash",
		"deepseek-v4-pro",
		"deepseek/deepseek-v4.1-flash",
		"gpt-6-astra",
		"claude-opus-5",
		"model@2026-09-10",
		"a.b_c-d/e:f+g",
	} {
		if err := validateExecutionValue("model", value, true); err != nil {
			t.Errorf("validateExecutionValue(%q) = %v, want it accepted verbatim", value, err)
		}
	}
	if err := validateExecutionValue("reasoning", "", false); err != nil {
		t.Fatalf("optional empty value must be accepted: %v", err)
	}
}
