package sessionmove

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/hubaddress"
)

// Courier names the configured delivery adapter. Machine identity remains the
// target map key; courier addresses are deliberately nested beneath it.
type Courier string

const (
	CourierSSH        Courier = "ssh"
	CourierLoopback   Courier = "loopback"
	CourierSynchestra Courier = "synchestra"
)

type SSHConfig struct {
	Host string `yaml:"host" json:"host"`
	// User is an optional target account. It is passed to OpenSSH as a fixed
	// `-l` argv pair, never joined with Host or remote command text.
	User   string `yaml:"user,omitempty" json:"user,omitempty"`
	WBPath string `yaml:"wb_path,omitempty" json:"wb_path,omitempty"`
}

var safeRemotePathSegment = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
var safeSSHUser = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Validate rejects values that OpenSSH could reinterpret when it constructs
// the remote shell command. Host is deliberately limited to a configured SSH
// alias, and a custom WBPath may contain only shell-inert ASCII path segments.
// An empty WBPath selects the fixed remote command name "wb".
func (c SSHConfig) Validate() error {
	if !safeID.MatchString(c.Host) {
		return fmt.Errorf("ssh.host %q must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes", c.Host)
	}
	if c.WBPath == "" {
		if c.User == "" || safeSSHUser.MatchString(c.User) {
			return nil
		}
		return fmt.Errorf("ssh.user %q must start with a letter or underscore and contain only letters, digits, underscores, or dashes", c.User)
	}
	if c.User != "" && !safeSSHUser.MatchString(c.User) {
		return fmt.Errorf("ssh.user %q must start with a letter or underscore and contain only letters, digits, underscores, or dashes", c.User)
	}
	if !path.IsAbs(c.WBPath) || path.Clean(c.WBPath) != c.WBPath {
		return fmt.Errorf("ssh.wb_path %q must be a clean absolute target path", c.WBPath)
	}
	segments := strings.Split(strings.TrimPrefix(c.WBPath, "/"), "/")
	for _, segment := range segments {
		if !safeRemotePathSegment.MatchString(segment) {
			return fmt.Errorf("ssh.wb_path %q must contain only shell-inert ASCII path segments using letters, digits, dots, underscores, pluses, or dashes", c.WBPath)
		}
	}
	return nil
}

type SynchestraConfig struct {
	Runner string `yaml:"runner" json:"runner"`
}

// Validate keeps the configured runner safe to pass as one fixed argv value.
// The runner is routing data only; it never contributes shell or command text.
func (c SynchestraConfig) Validate() error {
	return validateFixedArgument("synchestra.runner", c.Runner)
}

// HTTPConfig is the optional address of a machine's daemon-hosted hub, where
// another machine's daemon reads its Cockpit export envelope
// (cockpit-views#req:remote-http-fetch). It is not a Courier: no session is
// delivered through it, and default_courier never names it.
//
// TokenFile is the private file holding the machine bearer credential for that
// hub. It may be omitted only for the machine whose URL is the hub this
// machine is already enrolled with (remote.url), whose remote.token_file is
// then used; the reader decides that, not this package.
type HTTPConfig struct {
	URL       string `yaml:"url" json:"url"`
	TokenFile string `yaml:"token_file,omitempty" json:"token_file,omitempty"`
}

// Validate holds the URL to the one rule for an address a machine credential
// is sent to (hubaddress.Valid, which remotestate.ValidateHubURL also applies):
// an https origin, or http only on a loopback host, with no user information,
// query, fragment or path. A token file, when named, is an absolute path.
func (c HTTPConfig) Validate() error {
	if !hubaddress.Valid(c.URL) {
		// The value is not echoed: an address with user information holds a password.
		return errors.New("http.url must be an https origin, or an http origin on a loopback host (localhost, 127.0.0.1 or ::1), with no user information, path, query or fragment")
	}
	if c.TokenFile != "" && !filepath.IsAbs(c.TokenFile) {
		return fmt.Errorf("http.token_file %q must be an absolute path", c.TokenFile)
	}
	return nil
}

// TargetConfig is one WB machine and its separate courier addresses. Machine
// is populated from the targets map key and is never decoded from an address.
// HTTP is not a courier address: it is where the machine's export is read.
type TargetConfig struct {
	Machine        string            `yaml:"-" json:"machine"`
	DefaultCourier Courier           `yaml:"default_courier" json:"default_courier"`
	SSH            *SSHConfig        `yaml:"ssh,omitempty" json:"ssh,omitempty"`
	Synchestra     *SynchestraConfig `yaml:"synchestra,omitempty" json:"synchestra,omitempty"`
	HTTP           *HTTPConfig       `yaml:"http,omitempty" json:"http,omitempty"`
}

// Config is the session_move section of ~/.config/wb/wb.yaml.
type Config struct {
	Targets map[string]TargetConfig `yaml:"targets" json:"targets"`
}

type configFile struct {
	SessionMove *Config `yaml:"session_move"`
}

// UnconfiguredError distinguishes an absent session_move section from invalid
// configured values, so a future command can map the former to usage help.
type UnconfiguredError struct{ Path string }

func (e *UnconfiguredError) Error() string {
	return fmt.Sprintf("session move is not configured in %s; add a session_move.targets section", e.Path)
}

// LoadConfig reads and validates only session_move while tolerating unrelated
// top-level WB configuration sections.
func LoadConfig(configPath string) (Config, error) {
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, &UnconfiguredError{Path: configPath}
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", configPath, err)
	}
	var file configFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	if file.SessionMove == nil {
		return Config{}, &UnconfiguredError{Path: configPath}
	}
	config := *file.SessionMove
	if len(config.Targets) == 0 {
		return Config{}, fmt.Errorf("session_move.targets must configure at least one target")
	}
	for machine, target := range config.Targets {
		if err := validateID("session_move target machine", machine); err != nil {
			return Config{}, err
		}
		target.Machine = machine
		if err := validateTarget(target); err != nil {
			return Config{}, fmt.Errorf("session_move.targets.%s: %w", machine, err)
		}
		config.Targets[machine] = target
	}
	return config, nil
}

// Target resolves a canonical WB machine name without consulting courier
// aliases or addresses.
func (c Config) Target(machine string) (TargetConfig, bool) {
	target, ok := c.Targets[machine]
	return target, ok
}

func validateTarget(target TargetConfig) error {
	switch target.DefaultCourier {
	case CourierSSH:
		if target.SSH == nil {
			return fmt.Errorf("default_courier %q requires an ssh section", target.DefaultCourier)
		}
	case CourierSynchestra:
		if target.Synchestra == nil {
			return fmt.Errorf("default_courier %q requires a synchestra section", target.DefaultCourier)
		}
	default:
		return fmt.Errorf("default_courier %q must be %q or %q", target.DefaultCourier, CourierSSH, CourierSynchestra)
	}
	if target.SSH != nil {
		if err := target.SSH.Validate(); err != nil {
			return err
		}
	}
	if target.Synchestra != nil {
		if err := target.Synchestra.Validate(); err != nil {
			return err
		}
	}
	if target.HTTP != nil {
		if err := target.HTTP.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateFixedArgument(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s %q must not be option-like", field, value)
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%s %q must not contain whitespace", field, value)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must not contain NUL", field)
	}
	return nil
}
