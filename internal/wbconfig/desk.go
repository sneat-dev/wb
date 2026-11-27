package wbconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/hubaddress"
	"gopkg.in/yaml.v3"
)

// Desk intervals (desk-connect#req:publish-on-change, desk-connect#req:heartbeat).
const (
	DefaultDeskPublishMinInterval = 60 * time.Second
	MinDeskPublishMinInterval     = 30 * time.Second
	DefaultDeskHeartbeatInterval  = 5 * time.Minute
	MinDeskHeartbeatInterval      = time.Minute
)

// DeskConfig is the `desk:` section (desk-connect#req:desk-configuration).
type DeskConfig struct {
	URL                string
	MachineID          string
	SpaceID            string
	SharedSpaceIDs     []string
	CredentialFile     string
	Detail             desk.Level
	Enabled            bool
	PublishMinInterval time.Duration
	HeartbeatInterval  time.Duration
}

// Connected reports whether the section names an installed machine
// credential; only then may the daemon make a Desk request.
func (config DeskConfig) Connected() bool {
	return config.Enabled && config.Installed()
}

// Installed reports whether a machine credential is configured, whether or
// not publishing is paused with enabled: false. Disconnect and status act on
// an installed credential.
func (config DeskConfig) Installed() bool {
	return config.CredentialFile != "" && config.MachineID != ""
}

// DefaultDeskConfig is the configuration of a machine never connected.
func DefaultDeskConfig() DeskConfig {
	return DeskConfig{URL: desk.DefaultURL, Detail: desk.LevelRepositories, Enabled: true,
		PublishMinInterval: DefaultDeskPublishMinInterval, HeartbeatInterval: DefaultDeskHeartbeatInterval}
}

type deskSection struct {
	URL            *string  `yaml:"url"`
	MachineID      *string  `yaml:"machine_id"`
	SpaceID        *string  `yaml:"space_id"`
	SharedSpaceIDs []string `yaml:"shared_space_ids"`
	CredentialFile *string  `yaml:"credential_file"`
	Detail         *string  `yaml:"detail"`
	Enabled        *bool    `yaml:"enabled"`
	Publish        *struct {
		MinInterval *string `yaml:"min_interval"`
	} `yaml:"publish"`
	Heartbeat *struct {
		Interval *string `yaml:"interval"`
	} `yaml:"heartbeat"`
}

var (
	deskMachineID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	deskSpaceID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// deskInterval parses a duration, raising one below floor to floor as
// remote.publish.interval does.
func deskInterval(key string, raw *string, fallback, floor time.Duration) (time.Duration, error) {
	if raw == nil {
		return fallback, nil
	}
	interval, err := time.ParseDuration(strings.TrimSpace(*raw))
	if err != nil || interval <= 0 {
		return 0, fmt.Errorf("desk.%s must be a positive duration such as 5m, got %q", key, *raw)
	}
	return max(interval, floor), nil
}

func parseDesk(raw []byte) (DeskConfig, error) {
	var file struct {
		Desk yaml.Node `yaml:"desk"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return DeskConfig{}, fmt.Errorf("parse config: %w", err)
	}
	config := DefaultDeskConfig()
	if file.Desk.Kind == 0 {
		return config, nil
	}
	subtree, _ := yaml.Marshal(&file.Desk)
	decoder := yaml.NewDecoder(bytes.NewReader(subtree))
	decoder.KnownFields(true)
	var section deskSection
	if err := decoder.Decode(&section); err != nil && !errors.Is(err, io.EOF) {
		return DeskConfig{}, fmt.Errorf("parse desk section: %w", err)
	}
	if section.URL != nil {
		config.URL = strings.TrimSpace(*section.URL)
		if !hubaddress.Valid(config.URL) {
			return DeskConfig{}, fmt.Errorf("desk.url must be https, or http on a loopback host, with no path, got %q", config.URL)
		}
		config.URL = hubaddress.Origin(config.URL)
	}
	if section.MachineID != nil {
		config.MachineID = strings.TrimSpace(*section.MachineID)
		if !deskMachineID.MatchString(config.MachineID) {
			return DeskConfig{}, errors.New("desk.machine_id must be 32 lower-case hex characters")
		}
	}
	for _, id := range append([]string{deref(section.SpaceID)}, section.SharedSpaceIDs...) {
		if id != "" && !deskSpaceID.MatchString(id) {
			return DeskConfig{}, fmt.Errorf("desk space id %q is not a valid space id", id)
		}
	}
	config.SpaceID, config.SharedSpaceIDs = deref(section.SpaceID), section.SharedSpaceIDs
	if section.CredentialFile != nil {
		config.CredentialFile = strings.TrimSpace(*section.CredentialFile)
		if !filepath.IsAbs(config.CredentialFile) {
			return DeskConfig{}, errors.New("desk.credential_file must be an absolute path")
		}
	}
	if section.Detail != nil {
		config.Detail = desk.Level(strings.TrimSpace(*section.Detail))
		if !config.Detail.Valid() {
			return DeskConfig{}, fmt.Errorf("desk.detail must be %q or %q, got %q", desk.LevelRepositories, desk.LevelFull, *section.Detail)
		}
	}
	if section.Enabled != nil {
		config.Enabled = *section.Enabled
	}
	var err error
	if section.Publish != nil {
		if config.PublishMinInterval, err = deskInterval("publish.min_interval", section.Publish.MinInterval,
			DefaultDeskPublishMinInterval, MinDeskPublishMinInterval); err != nil {
			return DeskConfig{}, err
		}
	}
	if section.Heartbeat != nil {
		if config.HeartbeatInterval, err = deskInterval("heartbeat.interval", section.Heartbeat.Interval,
			DefaultDeskHeartbeatInterval, MinDeskHeartbeatInterval); err != nil {
			return DeskConfig{}, err
		}
	}
	return config, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// LoadDesk reads the `desk:` section of the configuration at path.
func LoadDesk(path string) (DeskConfig, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultDeskConfig(), nil
	}
	if err != nil {
		return DeskConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	config, err := parseDesk(raw)
	if err != nil {
		return DeskConfig{}, fmt.Errorf("config %s: %w", path, err)
	}
	return config, nil
}

// DeskConnection is what `wb desk connect` owns in the `desk:` section.
type DeskConnection struct {
	URL            string
	MachineID      string
	SpaceID        string
	SharedSpaceIDs []string
	CredentialFile string
	Detail         desk.Level
}

// deskOwnedKeys are the keys SetDesk writes and ClearDesk removes; enabled
// and the intervals are the operator's.
var deskOwnedKeys = []string{"url", "machine_id", "space_id", "shared_space_ids", "credential_file", "detail"}

// SetDesk atomically writes only the `desk:` keys connect owns, keeping
// every other setting (desk-connect#req:credential-installed-privately).
func SetDesk(path string, connection DeskConnection) error {
	document, root, err := loadDocument(path)
	if err != nil {
		return err
	}
	deskNode, err := section(path, root, "desk")
	if err != nil {
		return err
	}
	setMappingScalar(deskNode, "url", connection.URL)
	setMappingScalar(deskNode, "machine_id", connection.MachineID)
	setMappingScalar(deskNode, "space_id", connection.SpaceID)
	removeMappingKey(deskNode, "shared_space_ids")
	if len(connection.SharedSpaceIDs) > 0 {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, id := range connection.SharedSpaceIDs {
			list.Content = append(list.Content, scalar(id))
		}
		deskNode.Content = append(deskNode.Content, scalar("shared_space_ids"), list)
	}
	setMappingScalar(deskNode, "credential_file", connection.CredentialFile)
	setMappingScalar(deskNode, "detail", string(connection.Detail))
	return writeDocument(path, document, nil)
}

// SetDeskDetail changes only `desk.detail`.
func SetDeskDetail(path string, detail desk.Level) error {
	document, root, err := loadDocument(path)
	if err != nil {
		return err
	}
	deskNode, err := section(path, root, "desk")
	if err != nil {
		return err
	}
	setMappingScalar(deskNode, "detail", string(detail))
	return writeDocument(path, document, nil)
}

// ClearDesk removes the keys connect owns, and the section when nothing else
// is left in it (desk-connect#req:disconnect-revokes). A missing file or
// section is already clear.
func ClearDesk(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	document, root, err := loadDocument(path)
	if err != nil {
		return err
	}
	deskNode := mappingValue(root, "desk")
	if deskNode == nil {
		return nil
	}
	if deskNode.Kind == yaml.MappingNode {
		for _, key := range deskOwnedKeys {
			removeMappingKey(deskNode, key)
		}
	}
	if deskNode.Kind != yaml.MappingNode || len(deskNode.Content) == 0 {
		removeMappingKey(root, "desk")
	}
	return writeDocument(path, document, nil)
}

func removeMappingKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}
