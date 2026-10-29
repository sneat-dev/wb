package agents

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExecutionPolicyUnmarshalReportsANonMappingDocument(t *testing.T) {
	t.Parallel()
	var holder struct {
		Execution ExecutionPolicy `yaml:"execution"`
	}
	err := yaml.Unmarshal([]byte("execution:\n  - mode: linux-container\n"), &holder)
	if err == nil {
		t.Fatal("a sequence in place of the policy mapping must be refused by the decoder")
	}
	if strings.Contains(err.Error(), "agents.execution: field") {
		t.Fatalf("error = %v, want the underlying decode failure, not a field-name refusal", err)
	}
}

func TestRequireRunnableExecutionRefusesAMatchingPolicyItCannotEnforce(t *testing.T) {
	t.Parallel()
	invalid := executionTestPolicy()
	invalid.MaxParallel = 2
	configured := invalid
	err := RequireRunnableExecution(&configured, &invalid, "run record", HarnessDSH)
	if err == nil || !strings.Contains(err.Error(), "cannot be enforced exactly as written") {
		t.Fatalf("error = %v, want the enforceability refusal", err)
	}
	if !strings.Contains(err.Error(), "max_parallel") {
		t.Fatalf("error = %v, want the offending field named", err)
	}
}

func TestMandatoryDshRoutingRejectsAnInvalidOfficialProviderEntry(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	config := Config{
		Execution: &policy,
		Providers: map[string]Provider{
			ProviderDshDefault: {
				BaseURL:        "https://api.deepseek.com",
				CredentialEnv:  "WB_TEST_DEEPSEEK_OFFICIAL_KEY",
				CredentialFile: "/fleet/private/provider.key",
				WireAPI:        WireAPIChat,
			},
		},
	}
	_, err := config.mandatoryDshRouting()
	if err == nil || !strings.Contains(err.Error(), "agents.providers."+ProviderDshDefault) {
		t.Fatalf("error = %v, want the invalid provider entry named", err)
	}
	if !strings.Contains(err.Error(), "exactly one credential source") {
		t.Fatalf("error = %v, want the two-credential-source refusal preserved", err)
	}
}
