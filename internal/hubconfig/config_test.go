package hubconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadReportsAnAbsentSectionRatherThanAnError is the promise that keeps
// every operator who does not self-host on exactly the path they were on.
func TestLoadReportsAnAbsentSectionRatherThanAnError(t *testing.T) {
	for name, body := range map[string]string{
		"empty file":          "",
		"other sections only": "remote:\n  provider: git\n  repo: sneat-dev/wb\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, found, err := Load(writeConfig(t, body))
			if err != nil || found || cfg != (Config{}) {
				t.Fatalf("Load = %+v, %t, %v", cfg, found, err)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		cfg, found, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
		if err != nil || found || cfg != (Config{}) {
			t.Fatalf("Load = %+v, %t, %v", cfg, found, err)
		}
	})
}

// TestLoadAppliesTheDocumentedDefaults pins the founder's 2026-09-11
// resolution: inGitDB under ~/.wb/hub, 60s polling.
func TestLoadAppliesTheDocumentedDefaults(t *testing.T) {
	cfg, found, err := Load(writeConfig(t, "hub:\n  github:\n    token_file: /tmp/github.token\n"))
	if err != nil || !found {
		t.Fatalf("Load = %+v, %t, %v", cfg, found, err)
	}
	if cfg.Store.Engine != EngineInGitDB {
		t.Fatalf("engine = %q, want %q", cfg.Store.Engine, EngineInGitDB)
	}
	if cfg.Store.Path != DefaultStorePath() || cfg.Store.Path == "" {
		t.Fatalf("path = %q, want %q", cfg.Store.Path, DefaultStorePath())
	}
	if cfg.GitHub.PollInterval != DefaultPollInterval {
		t.Fatalf("poll interval = %s, want %s", cfg.GitHub.PollInterval, DefaultPollInterval)
	}
	if cfg.GitHub.App != nil {
		t.Fatalf("app = %+v, want nil", cfg.GitHub.App)
	}
	if cfg.Location() != cfg.Store.Path {
		t.Fatalf("location = %q", cfg.Location())
	}
}

// TestLoadParsesEveryFieldAndExpandsHome walks the section the feature
// documents, including the optional App block.
func TestLoadParsesEveryFieldAndExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	cfg, found, err := Load(writeConfig(t, `hub:
  store:
    engine: OpenVaultDB
    url: http://127.0.0.1:6832
    database_id: bench
  github:
    token_file: ~/.config/wb/credentials/github.token
    poll_interval: 90s
    app:
      app_id: 123
      private_key_file: /etc/wb/app.pem
      webhook_secret_file: /etc/wb/webhook.secret
      public_url: https://tunnel.example
`))
	if err != nil || !found {
		t.Fatalf("Load = %+v, %t, %v", cfg, found, err)
	}
	if cfg.Store.Engine != EngineOpenVaultDB || cfg.Store.URL != "http://127.0.0.1:6832" || cfg.Store.DatabaseID != "bench" {
		t.Fatalf("store = %+v", cfg.Store)
	}
	// openvaultdb keeps no local directory, so no default path is invented.
	if cfg.Store.Path != "" {
		t.Fatalf("path = %q, want empty for openvaultdb", cfg.Store.Path)
	}
	if cfg.Location() != cfg.Store.URL {
		t.Fatalf("location = %q", cfg.Location())
	}
	wantToken := filepath.Join(home, ".config", "wb", "credentials", "github.token")
	if cfg.GitHub.TokenFile != wantToken {
		t.Fatalf("token file = %q, want %q", cfg.GitHub.TokenFile, wantToken)
	}
	if cfg.GitHub.PollInterval != 90*time.Second {
		t.Fatalf("poll interval = %s", cfg.GitHub.PollInterval)
	}
	app := cfg.GitHub.App
	if app == nil || app.AppID != 123 || app.PrivateKeyFile != "/etc/wb/app.pem" ||
		app.WebhookSecretFile != "/etc/wb/webhook.secret" || app.PublicURL != "https://tunnel.example" {
		t.Fatalf("app = %+v", app)
	}
}

// TestMemoryEngineSaysSoInItsLocation is what makes `wb daemon status` honest
// about a throwaway run.
func TestMemoryEngineSaysSoInItsLocation(t *testing.T) {
	cfg, found, err := Load(writeConfig(t, "hub:\n  store:\n    engine: memory\n"))
	if err != nil || !found {
		t.Fatalf("Load = %+v, %t, %v", cfg, found, err)
	}
	if !strings.Contains(cfg.Location(), "in-memory") {
		t.Fatalf("location = %q, want it to name the volatility", cfg.Location())
	}
}

func TestLoadRejectsEveryInvalidSection(t *testing.T) {
	for name, testCase := range map[string]struct{ body, want string }{
		"unreadable yaml": {
			body: "hub:\n  store:\n   - engine: memory\n", want: "parse config",
		},
		"unknown engine": {
			body: "hub:\n  store:\n    engine: postgres\n", want: "hub.store.engine",
		},
		"interval below the floor": {
			body: "hub:\n  github:\n    poll_interval: 10s\n", want: "below the 30s floor",
		},
		"relative store path": {
			body: "hub:\n  store:\n    path: ./hub\n", want: "hub.store.path",
		},
		"relative token file": {
			body: "hub:\n  github:\n    token_file: github.token\n", want: "hub.github.token_file",
		},
		"openvaultdb without url": {
			body: "hub:\n  store:\n    engine: openvaultdb\n", want: "hub.store.url is required",
		},
		"app missing everything but the id": {
			body: "hub:\n  github:\n    app:\n      app_id: 5\n", want: "hub.github.app is incomplete",
		},
		"app without an id": {
			body: "hub:\n  github:\n    app:\n      private_key_file: /etc/wb/app.pem\n      webhook_secret_file: /etc/wb/webhook.secret\n      public_url: https://tunnel.example\n",
			want: "missing app_id",
		},
		"app private key is relative": {
			body: "hub:\n  github:\n    app:\n      app_id: 5\n      private_key_file: app.pem\n",
			want: "hub.github.app.private_key_file",
		},
		"app webhook secret is relative": {
			body: "hub:\n  github:\n    app:\n      app_id: 5\n      private_key_file: /etc/wb/app.pem\n      webhook_secret_file: webhook.secret\n",
			want: "hub.github.app.webhook_secret_file",
		},
		"app public url is not https": {
			body: "hub:\n  github:\n    app:\n      app_id: 5\n      private_key_file: /etc/wb/app.pem\n      webhook_secret_file: /etc/wb/webhook.secret\n      public_url: http://tunnel.example\n",
			want: "must be an HTTPS origin",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, found, err := Load(writeConfig(t, testCase.body))
			if err == nil {
				t.Fatalf("invalid section was accepted (found=%t)", found)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestLoadSurfacesAnUnreadableFile(t *testing.T) {
	// A directory in place of the file is the portable way to make ReadFile
	// fail with something that is not ErrNotExist.
	if _, _, err := Load(t.TempDir()); err == nil {
		t.Fatal("reading a directory as configuration succeeded")
	}
}

// TestAbsolutePathResolvesABareTilde covers the "~" spelling the ~/ prefix
// check would otherwise miss.
func TestAbsolutePathResolvesABareTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	got, err := absolutePath("hub.store.path", " ~ ")
	if err != nil || got != filepath.Clean(home) {
		t.Fatalf("absolutePath = %q, %v; want %q", got, err, home)
	}
}

// TestDefaultStorePathIsEmptyWithoutAHome pins the one case where the default
// cannot be invented, which Load turns into an explicit error.
func TestDefaultStorePathIsEmptyWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if runtimeHomeIsEnvironmentIndependent() {
		t.Skip("this platform does not resolve the home directory from the environment")
	}
	if got := DefaultStorePath(); got != "" {
		t.Fatalf("DefaultStorePath = %q, want empty", got)
	}
	if _, _, err := Load(writeConfig(t, "hub:\n  store:\n    engine: ingitdb\n")); err == nil {
		t.Fatal("ingitdb without a resolvable home was accepted")
	}
	if _, err := absolutePath("hub.store.path", "~/x"); err == nil {
		t.Fatal("~ expansion without a resolvable home was accepted")
	}
}

// runtimeHomeIsEnvironmentIndependent reports whether os.UserHomeDir still
// answers after HOME is cleared, which is true on Windows and on some
// container images.
func runtimeHomeIsEnvironmentIndependent() bool {
	home, err := os.UserHomeDir()
	return err == nil && strings.TrimSpace(home) != ""
}
