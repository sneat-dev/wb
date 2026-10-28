package agents

import (
	"os"
	"strings"
	"testing"
)

const dshMandatoryProviderEnv = "WB_TEST_DEEPSEEK_OFFICIAL_KEY"

func dshMandatoryBody(providers, profiles string) string {
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1)
	if providers != "" {
		body += providers
	}
	return body + profiles
}

func TestDshUnderMandatoryPolicyRequiresAnExplicitOfficialProvider(t *testing.T) {
	config, err := LoadConfigFile(writeConfig(t, dshMandatoryBody("", `  profiles:
    ds:
      harness: dsh
      model: deepseek-v4-flash
`)))
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if _, ok := config.Providers[ProviderDshDefault]; ok {
		t.Fatal("the official provider must never be added implicitly")
	}
	// Resolving must fail on the missing explicit entry without reading a key:
	// this test deliberately clears the named variable.
	t.Setenv(dshMandatoryProviderEnv, "")
	_, err = config.Resolve("ds")
	if err == nil || !strings.Contains(err.Error(), "explicit agents.providers.deepseek-official") {
		t.Fatalf("error = %v, want the missing explicit provider entry", err)
	}
}

func TestDshUnderMandatoryPolicyResolvesTheExplicitOfficialRouting(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, dshMandatoryBody(`  providers:
    deepseek-official:
      base_url: https://api.deepseek.com/v1
      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY
      wire_api: chat
`, `  profiles:
    ds:
      harness: dsh
      model: deepseek-v4-flash
`)))
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	resolved, err := config.Resolve("ds")
	if err != nil {
		t.Fatalf("resolve dsh profile: %v", err)
	}
	if resolved.Provider != ProviderDshDefault || resolved.Model != "deepseek-v4-flash" {
		t.Fatalf("provider/model = %q/%q; both must stay verbatim", resolved.Provider, resolved.Model)
	}
	if resolved.Routing.BaseURL != "https://api.deepseek.com/v1" ||
		resolved.Routing.CredentialEnv != dshMandatoryProviderEnv ||
		resolved.Routing.WireAPI != WireAPIChat {
		t.Fatalf("routing = %#v", resolved.Routing)
	}
}

func TestDshUnderMandatoryPolicyAcceptsAFileCredentialSource(t *testing.T) {
	t.Parallel()
	path := writeDshCredentialFile(t, "not-a-real-key")
	config, err := LoadConfigFile(writeConfig(t, dshMandatoryBody(`  providers:
    deepseek-official:
      base_url: https://api.deepseek.com
      credential_file: `+path+`
      wire_api: chat
`, `  profiles:
    ds:
      harness: dsh
      model: deepseek-v4-flash
`)))
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	resolved, err := config.Resolve("ds")
	if err != nil {
		t.Fatalf("resolve dsh profile: %v", err)
	}
	if resolved.Routing.CredentialFile != path || resolved.Routing.CredentialEnv != "" {
		t.Fatalf("routing = %#v", resolved.Routing)
	}
}

func writeDshCredentialFile(t *testing.T, value string) string {
	t.Helper()
	path := t.TempDir() + "/deepseek.key"
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDshUnderMandatoryPolicyRefusesUnsafeOrUntruthfulRouting(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		provider string
		want     string
	}{
		"not the official host": {
			provider: "      base_url: https://deepseek.example.com\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: chat\n",
			want:     "not the official HTTPS DeepSeek endpoint",
		},
		"official host suffix": {
			provider: "      base_url: https://api.deepseek.com.evil.example\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: chat\n",
			want:     "not the official HTTPS DeepSeek endpoint",
		},
		"plain http": {
			provider: "      base_url: http://api.deepseek.com\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: chat\n",
			want:     "must be an https URL",
		},
		"userinfo": {
			provider: "      base_url: https://user:secret@api.deepseek.com\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: chat\n",
			want:     "not the official HTTPS DeepSeek endpoint",
		},
		"non-default port": {
			provider: "      base_url: https://api.deepseek.com:8443\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: chat\n",
			want:     "not the official HTTPS DeepSeek endpoint",
		},
		"query string": {
			provider: "      base_url: https://api.deepseek.com?proxy=1\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: chat\n",
			want:     "not the official HTTPS DeepSeek endpoint",
		},
		"unexpected path": {
			provider: "      base_url: https://api.deepseek.com/other\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: chat\n",
			want:     "not the official HTTPS DeepSeek endpoint",
		},
		"responses wire is not dsh native chat": {
			provider: "      base_url: https://api.deepseek.com\n      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY\n      wire_api: responses\n",
			want:     "not truthful",
		},
		"no credential source": {
			provider: "      base_url: https://api.deepseek.com\n      wire_api: chat\n",
			want:     "requires credential_env",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := dshMandatoryBody("  providers:\n    deepseek-official:\n"+testCase.provider, `  profiles:
    ds:
      harness: dsh
      model: deepseek-v4-flash
`)
			config, err := LoadConfigFile(writeConfig(t, body))
			if err != nil {
				if strings.Contains(err.Error(), testCase.want) {
					return
				}
				t.Fatalf("load configuration: %v, want %q", err, testCase.want)
			}
			_, err = config.Resolve("ds")
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestDshWithoutMandatoryPolicyKeepsTheLegacyHarnessStore(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, `agents:
  profiles:
    ds:
      harness: dsh
      model: deepseek-v4-flash
`))
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	resolved, err := config.Resolve("ds")
	if err != nil {
		t.Fatalf("a host dsh profile must keep resolving without an explicit provider entry: %v", err)
	}
	if resolved.Provider != ProviderDshDefault {
		t.Fatalf("provider = %q", resolved.Provider)
	}
	if resolved.Routing != (Provider{}) {
		t.Fatalf("absent-policy dsh must keep its legacy credential store, routing = %#v", resolved.Routing)
	}
}

func TestOfficialDeepSeekEndpointAcceptsOnlyThePublicHTTPSBase(t *testing.T) {
	t.Parallel()
	for _, accepted := range []string{
		"https://api.deepseek.com",
		"https://api.deepseek.com/",
		"https://api.deepseek.com/v1",
		"https://api.deepseek.com/v1/",
	} {
		if !officialDeepSeekEndpoint(accepted) {
			t.Fatalf("%q must be accepted as the official endpoint", accepted)
		}
	}
	for _, refused := range []string{
		"", " ", "http://api.deepseek.com", "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com//v1", "https://api.deepseek.com#fragment",
		"https://api.deepseek.com@evil.example", "https://evil.example/api.deepseek.com",
		"https://api.deepseek.com/v1?x=1",
	} {
		if officialDeepSeekEndpoint(refused) {
			t.Fatalf("%q must be refused as the official endpoint", refused)
		}
	}
}
