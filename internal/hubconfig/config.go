// Package hubconfig reads the optional `hub:` section of ~/.config/wb/wb.yaml,
// which turns `wb daemon serve` into the self-hosting unit for bench.
//
// The section is absent for every operator who does not self-host, so Load
// reports "found" separately from an error: a missing file and a missing
// section are both (Config{}, false, nil), and only a present-but-invalid
// section is an error. That keeps the daemon's behaviour byte-identical
// without `hub:`, which is what spec/features/self-hosted-bench promises.
//
// Secrets are always file paths. A token, private key, or webhook secret
// never appears in the configuration value itself, matching remote.token_file.
package hubconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigSnippet is printed by callers that need the minimal self-hosting
// section, so the fix is copy-paste rather than documentation lookup.
const ConfigSnippet = `hub:
  store:
    engine: ingitdb          # memory | ingitdb | openvaultdb
    path: ~/.wb/hub
  github:
    token_file: <absolute-private-token-file>
    poll_interval: 60s`

// Engine names the DALgo engine the hub's document store runs on.
const (
	EngineMemory      = "memory"
	EngineInGitDB     = "ingitdb"
	EngineOpenVaultDB = "openvaultdb"
)

// DefaultPollInterval and MinimumPollInterval bound the GitHub poller. The
// default is deliberately slow: polling costs two GitHub API calls per
// repository per tick against a per-user hourly budget of 5000, and a fleet
// of a few hundred repositories on a one-minute interval drains that budget
// in minutes for every tool the operator runs (founder ruling 2026-09-11,
// after exactly that happened). Push latency comes from webhook mode, not
// from a short interval.
const (
	DefaultPollInterval = 20 * time.Minute
	MinimumPollInterval = 30 * time.Second
)

// Store selects and locates the hub's document store.
type Store struct {
	Engine string `yaml:"engine"`
	// Path is the inGitDB project directory. Ignored by the other engines.
	Path string `yaml:"path"`
	// URL is the OpenVaultDB server origin. Ignored by the other engines.
	URL string `yaml:"url"`
	// DatabaseID names the OpenVaultDB database. Optional; defaults to "wb".
	DatabaseID string `yaml:"database_id"`
}

// App is the optional GitHub App that switches covered repositories from
// polling to webhooks. Every field is required once the block is present:
// a half-configured App would accept deliveries it cannot verify.
type App struct {
	AppID             int64  `yaml:"app_id"`
	PrivateKeyFile    string `yaml:"private_key_file"`
	WebhookSecretFile string `yaml:"webhook_secret_file"`
	PublicURL         string `yaml:"public_url"`
}

// GitHub configures the ingester. TokenFile alone is enough to poll.
type GitHub struct {
	TokenFile    string        `yaml:"token_file"`
	PollInterval time.Duration `yaml:"poll_interval"`
	App          *App          `yaml:"app"`
}

// Config is the hub section of ~/.config/wb/wb.yaml.
type Config struct {
	Store  Store  `yaml:"store"`
	GitHub GitHub `yaml:"github"`
}

// DefaultStorePath returns ~/.wb/hub, the inGitDB project directory a
// self-hoster gets without saying anything.
func DefaultStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".wb", "hub")
}

// Location describes where the hub keeps its state, for status output and the
// one line the daemon prints on start.
func (c Config) Location() string {
	switch c.Store.Engine {
	case EngineOpenVaultDB:
		return c.Store.URL
	case EngineMemory:
		return "(in-memory; discarded on exit)"
	default:
		return c.Store.Path
	}
}

type configFile struct {
	Hub *Config `yaml:"hub"`
}

// Load reads the hub section from path. A missing file or a missing section
// is (Config{}, false, nil); a present but invalid section is an error.
func Load(path string) (Config, bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-owned configuration path.
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	var file configFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Config{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	if file.Hub == nil {
		return Config{}, false, nil
	}
	cfg, err := normalize(*file.Hub)
	if err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

func normalize(cfg Config) (Config, error) {
	cfg.Store.Engine = strings.ToLower(strings.TrimSpace(cfg.Store.Engine))
	if cfg.Store.Engine == "" {
		cfg.Store.Engine = EngineInGitDB
	}
	switch cfg.Store.Engine {
	case EngineMemory, EngineInGitDB, EngineOpenVaultDB:
	default:
		return Config{}, fmt.Errorf("hub.store.engine %q must be %q, %q, or %q", cfg.Store.Engine, EngineMemory, EngineInGitDB, EngineOpenVaultDB)
	}
	if cfg.GitHub.PollInterval == 0 {
		cfg.GitHub.PollInterval = DefaultPollInterval
	}
	if cfg.GitHub.PollInterval < MinimumPollInterval {
		return Config{}, fmt.Errorf("hub.github.poll_interval %s is below the %s floor GitHub's rate limits require", cfg.GitHub.PollInterval, MinimumPollInterval)
	}

	path, err := absolutePath("hub.store.path", cfg.Store.Path)
	if err != nil {
		return Config{}, err
	}
	if path == "" && cfg.Store.Engine == EngineInGitDB {
		if path = DefaultStorePath(); path == "" {
			return Config{}, errors.New("hub.store.path is required because the home directory could not be resolved")
		}
	}
	cfg.Store.Path = path

	cfg.Store.URL = strings.TrimSpace(cfg.Store.URL)
	cfg.Store.DatabaseID = strings.TrimSpace(cfg.Store.DatabaseID)
	if cfg.Store.Engine == EngineOpenVaultDB && cfg.Store.URL == "" {
		return Config{}, errors.New("hub.store.url is required when hub.store.engine is openvaultdb")
	}

	if cfg.GitHub.TokenFile, err = absolutePath("hub.github.token_file", cfg.GitHub.TokenFile); err != nil {
		return Config{}, err
	}
	if cfg.GitHub.App != nil {
		app, err := normalizeApp(*cfg.GitHub.App)
		if err != nil {
			return Config{}, err
		}
		cfg.GitHub.App = &app
	}
	return cfg, nil
}

func normalizeApp(app App) (App, error) {
	var err error
	if app.PrivateKeyFile, err = absolutePath("hub.github.app.private_key_file", app.PrivateKeyFile); err != nil {
		return App{}, err
	}
	if app.WebhookSecretFile, err = absolutePath("hub.github.app.webhook_secret_file", app.WebhookSecretFile); err != nil {
		return App{}, err
	}
	app.PublicURL = strings.TrimSpace(app.PublicURL)
	// All-or-nothing: a partially configured App would accept deliveries it
	// cannot verify, or advertise a public URL GitHub cannot reach.
	var missing []string
	if app.AppID <= 0 {
		missing = append(missing, "app_id")
	}
	if app.PrivateKeyFile == "" {
		missing = append(missing, "private_key_file")
	}
	if app.WebhookSecretFile == "" {
		missing = append(missing, "webhook_secret_file")
	}
	if app.PublicURL == "" {
		missing = append(missing, "public_url")
	}
	if len(missing) > 0 {
		return App{}, fmt.Errorf("hub.github.app is incomplete (missing %s); configure every field or remove the block", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(app.PublicURL, "https://") {
		return App{}, errors.New("hub.github.app.public_url must be an HTTPS origin GitHub can reach")
	}
	return app, nil
}

// absolutePath trims, expands a leading ~/, and insists on an absolute path.
// An empty value stays empty so the caller can decide whether it is required.
func absolutePath(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("%s uses ~ but the home directory could not be resolved", field)
		}
		value = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(value, "~"), "/"))
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s %q must be an absolute path", field, value)
	}
	return filepath.Clean(value), nil
}
