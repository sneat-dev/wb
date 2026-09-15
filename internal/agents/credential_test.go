package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCredentialFile(t *testing.T, mode os.FileMode, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provider.key")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies the umask, so set the mode explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadCredentialFileAcceptsAPrivateFile(t *testing.T) {
	path := writeCredentialFile(t, 0o600, "  sk-from-a-file\n")
	value, err := ReadCredentialFile(path)
	if err != nil {
		t.Fatalf("ReadCredentialFile: %v", err)
	}
	if value != "sk-from-a-file" {
		t.Fatalf("value = %q; surrounding whitespace must be trimmed", value)
	}
}

func TestReadCredentialFileRefusesAnythingNotPrivate(t *testing.T) {
	cases := map[string]struct {
		prepare func(t *testing.T) string
		want    string
	}{
		"missing": {
			prepare: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.key") },
			want:    "does not exist",
		},
		"group readable": {
			prepare: func(t *testing.T) string { return writeCredentialFile(t, 0o640, "sk-x") },
			want:    "readable by group or others",
		},
		"world readable": {
			prepare: func(t *testing.T) string { return writeCredentialFile(t, 0o644, "sk-x") },
			want:    "readable by group or others",
		},
		"empty": {
			prepare: func(t *testing.T) string { return writeCredentialFile(t, 0o600, "   \n") },
			want:    "is empty",
		},
		"a directory": {
			prepare: func(t *testing.T) string {
				directory := filepath.Join(t.TempDir(), "keys")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				return directory
			},
			want: "not a regular file",
		},
		"a symlink": {
			prepare: func(t *testing.T) string {
				target := writeCredentialFile(t, 0o600, "sk-x")
				link := filepath.Join(t.TempDir(), "linked.key")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			want: "is a symlink",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ReadCredentialFile(testCase.prepare(t))
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestResolveCredentialUsesWhicheverSourceTheProviderNames(t *testing.T) {
	// A file-sourced credential is injected under one fixed variable name, so a
	// machine that exports nothing can still dispatch.
	path := writeCredentialFile(t, 0o600, "sk-from-a-file")
	credential, err := ResolveCredential(Provider{CredentialFile: path})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if credential.EnvName != ProviderCredentialEnv || credential.Value != "sk-from-a-file" {
		t.Fatalf("credential = %#v", credential)
	}

	// The environment still wins when that is what the provider names.
	t.Setenv("EXPLICIT_KEY", "sk-from-the-environment")
	credential, err = ResolveCredential(Provider{CredentialEnv: "EXPLICIT_KEY"})
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if credential.EnvName != "EXPLICIT_KEY" || credential.Value != "sk-from-the-environment" {
		t.Fatalf("credential = %#v", credential)
	}

	// A broken file source is reported at resolve time, not at launch.
	if _, err := ResolveCredential(Provider{CredentialFile: filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("a missing credential file must fail closed")
	}
}

func TestProviderCredentialSourceMustBeExactlyOneCleanPath(t *testing.T) {
	// One agents mapping with both sections: YAML forbids a second top-level key.
	withProvider := func(provider string) string {
		return "agents:\n  providers:\n" + provider + "  profiles:\n    p:\n      harness: codex\n      provider: deepseek\n      model: m\n"
	}
	cases := map[string]struct {
		body   string
		expect string
	}{
		"both sources": {
			body:   withProvider("    deepseek:\n      credential_env: A\n      credential_file: /home/ai/.config/wb/credentials/deepseek.key\n"),
			expect: "exactly one credential source",
		},
		"relative path": {
			body:   withProvider("    deepseek:\n      credential_file: credentials/deepseek.key\n"),
			expect: "clean absolute path",
		},
		"unclean path": {
			body:   withProvider("    deepseek:\n      credential_file: /home/ai/../ai/credentials/deepseek.key\n"),
			expect: "clean absolute path",
		},
		// A new provider has no built-in credential source to inherit, so it
		// must name one.
		"no source at all": {
			body:   withProvider("    custom:\n      base_url: https://api.example.com\n"),
			expect: "requires credential_env",
		},
		// Overriding only a built-in field legitimately inherits the built-in
		// credential source.
		"builtin override inherits the credential source": {
			body: withProvider("    deepseek:\n      base_url: https://proxy.example/v1\n"),
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			config, err := LoadConfigFile(writeConfig(t, testCase.body))
			if testCase.expect == "" {
				if err != nil {
					t.Fatalf("%s must be accepted: %v", name, err)
				}
				if got := config.Providers["deepseek"].CredentialEnv; got != "DEEPSEEK_API_KEY" {
					t.Fatalf("inherited credential source = %q", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.expect) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.expect)
			}
		})
	}

	// A file source replaces the built-in environment source rather than leaving
	// the provider with two.
	path := writeCredentialFile(t, 0o600, "sk-x")
	config, err := LoadConfigFile(writeConfig(t, withProvider("    deepseek:\n      credential_file: "+path+"\n")))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	provider := config.Providers["deepseek"]
	if provider.CredentialFile != path || provider.CredentialEnv != "" {
		t.Fatalf("provider = %#v", provider)
	}
	if provider.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("the untouched built-in fields must survive: %#v", provider)
	}
}

func TestDispatchResolvesAFileSourcedCredential(t *testing.T) {
	fixture := newDispatchFixture(t)
	path := writeCredentialFile(t, 0o600, "sk-from-a-file")
	fixture.deps.LoadConfig = func() (Config, error) {
		config, err := LoadConfigFile(filepath.Join(t.TempDir(), "absent.yaml"))
		if err != nil {
			return Config{}, err
		}
		config.Profiles["cheap"] = Profile{Harness: HarnessCodex, Provider: "deepseek", Model: "deepseek-flash"}
		config.Providers["deepseek"] = Provider{
			BaseURL: "https://api.deepseek.com", CredentialFile: path, WireAPI: WireAPIResponses,
		}
		return config, nil
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, err := fixture.dispatch(t, newRequest()); err != nil {
		t.Fatalf("a file-sourced credential must be enough to dispatch: %v", err)
	}

	// And an unusable file fails before anything is created.
	fixture = newDispatchFixture(t)
	fixture.deps.LoadConfig = func() (Config, error) {
		config, err := LoadConfigFile(filepath.Join(t.TempDir(), "absent.yaml"))
		if err != nil {
			return Config{}, err
		}
		config.Profiles["cheap"] = Profile{Harness: HarnessCodex, Provider: "deepseek", Model: "deepseek-flash"}
		config.Providers["deepseek"] = Provider{
			BaseURL: "https://api.deepseek.com", CredentialFile: filepath.Join(t.TempDir(), "absent"), WireAPI: WireAPIResponses,
		}
		return config, nil
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, err := fixture.dispatch(t, newRequest()); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("a missing credential file must be refused: %v", err)
	}
	if fixture.createCalls != 0 {
		t.Fatal("no worktree may be created without a usable credential")
	}
}

func TestRunOwnerUsesAFileSourcedCredential(t *testing.T) {
	_, deps := writeFakeHarness(t)
	path := writeCredentialFile(t, 0o600, "sk-from-a-file")
	store, record, _ := ownedRun(t, "file credential", time.Minute)
	record.Resolved.Routing = Provider{
		BaseURL: "https://api.deepseek.com", CredentialFile: path, WireAPI: WireAPIResponses,
	}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")

	if err := RunOwner(context.Background(), store, record.AgentID, deps); err != nil {
		t.Fatalf("RunOwner: %v", err)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted {
		t.Fatalf("state = %s (failure=%q)", loaded.State, loaded.Failure)
	}
}
