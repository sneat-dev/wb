package agents

import (
	"strings"
	"testing"
)

func environmentMap(environment []string) map[string]string {
	values := map[string]string{}
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

func TestWorkerEnvironmentIsAnAllowlistNotACopy(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/example")
	t.Setenv("LANG", "en_GB.UTF-8")
	t.Setenv("SSL_CERT_FILE", "/etc/certs.pem")
	// A machine credential the worker has no business holding.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "not-for-the-worker")
	t.Setenv("GITHUB_TOKEN", "not-for-the-worker")
	// Ambient agent identity must never be inherited: a dispatched worker is
	// not the parent session it must not disturb.
	t.Setenv("WB_AGENT_PID", "4242")
	t.Setenv("WB_AGENT_RUNTIME", "codex")
	t.Setenv("SOMETHING_RANDOM", "value")

	environment := environmentMap(WorkerEnvironment(Credential{}))
	for _, required := range []string{"PATH", "HOME", "LANG", "SSL_CERT_FILE"} {
		if _, present := environment[required]; !present {
			t.Errorf("allowlisted %s was dropped", required)
		}
	}
	for _, forbidden := range []string{
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "WB_AGENT_PID", "WB_AGENT_RUNTIME", "SOMETHING_RANDOM",
	} {
		if _, present := environment[forbidden]; present {
			t.Errorf("worker environment leaked %s: an allowlist must exclude a variable it has never heard of", forbidden)
		}
	}
}

func TestWorkerEnvironmentAddsOnlyTheResolvedCredential(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("DEEPSEEK_API_KEY", "provider-credential")
	t.Setenv("OPENROUTER_API_KEY", "a-different-credential")

	environment := environmentMap(WorkerEnvironment(Credential{EnvName: "DEEPSEEK_API_KEY", Value: "provider-credential"}))
	if environment["DEEPSEEK_API_KEY"] != "provider-credential" {
		t.Fatalf("the resolved provider credential must be added: %#v", environment["DEEPSEEK_API_KEY"])
	}
	if _, present := environment["OPENROUTER_API_KEY"]; present {
		t.Fatal("only the credential the resolved provider names may be added")
	}
	// A file-sourced credential reaches the child under the synthesised name,
	// without depending on anything the machine happens to export.
	environment = environmentMap(WorkerEnvironment(Credential{EnvName: ProviderCredentialEnv, Value: "from-a-file"}))
	if environment[ProviderCredentialEnv] != "from-a-file" {
		t.Fatalf("a file-sourced credential must be injected: %#v", environment)
	}
}

func TestWorkerEnvironmentFillsInAMissingPath(t *testing.T) {
	t.Setenv("PATH", "")
	environment := environmentMap(WorkerEnvironment(Credential{}))
	if environment["PATH"] != defaultPath {
		t.Fatalf("PATH = %q, want the platform default so the harness can still be found", environment["PATH"])
	}
}

func TestResolveCredentialNamesTheExactSource(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "present")
	credential, err := ResolveCredential(Provider{CredentialEnv: "DEEPSEEK_API_KEY"})
	if err != nil {
		t.Fatalf("a present credential must resolve: %v", err)
	}
	if credential.EnvName != "DEEPSEEK_API_KEY" || credential.Value != "present" {
		t.Fatalf("credential = %#v", credential)
	}

	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, err := ResolveCredential(Provider{CredentialEnv: "DEEPSEEK_API_KEY"}); err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("a blank credential must be reported by name: %v", err)
	}
	if _, err := ResolveCredential(Provider{CredentialEnv: "NEVER_SET_KEY"}); err == nil || !strings.Contains(err.Error(), "NEVER_SET_KEY") {
		t.Fatalf("an unset credential must be reported: %v", err)
	}
	if _, err := ResolveCredential(Provider{}); err == nil {
		t.Fatal("a provider with no credential source must fail closed")
	}
}

func TestPathAndHomeHelpersFallBackPredictably(t *testing.T) {
	t.Setenv("PATH", "")
	if got := pathValue(); got != defaultPath {
		t.Fatalf("pathValue = %q", got)
	}
	t.Setenv("PATH", " /custom/bin ")
	if got := pathValue(); got != "/custom/bin" {
		t.Fatalf("pathValue = %q", got)
	}
	t.Setenv("HOME", "")
	// With no HOME there is nothing truthful to pass, so Git is run without
	// one rather than with an empty HOME=.
	if environmentMap(gitEnvironment())["HOME"] != "" {
		t.Fatal("an unresolved HOME must be omitted, not passed as an empty value")
	}
	t.Setenv("HOME", "/home/example")
	if got := homeDir(); got != "/home/example" {
		t.Fatalf("homeDir = %q", got)
	}
	// With no HOME and no resolvable user home there is nothing truthful to
	// report, and the caller must get an empty string rather than a guess.
	t.Setenv("HOME", "")
	if got := homeDir(); got != "" {
		t.Fatalf("homeDir with no home = %q, want empty", got)
	}
}

func TestGitEnvironmentBlocksAmbientGitConfiguration(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/example")
	environment := environmentMap(gitEnvironment())
	if environment["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Fatal("a worker's Git state must not depend on system configuration")
	}
	if environment["GIT_CONFIG_GLOBAL"] != "/dev/null" {
		t.Fatal("a worker's Git state must not depend on the user's global configuration")
	}
	if environment["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatal("a dispatched Git helper must never block on a prompt")
	}
	if environment["LC_ALL"] != "C" {
		t.Fatal("Git output parsed by WB must be locale-stable")
	}
}
