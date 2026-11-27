package wbconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultDiskMinimumAvailable is the share of a filesystem below which wb disk
// raises a finding and wb disk prune aims by default. A tenth of the volume is
// enough for a large link step and a test run.
const DefaultDiskMinimumAvailable = 0.10

// DefaultDiskCheckInterval is how often the daemon looks at free space when
// disk.auto_prune is on.
const DefaultDiskCheckInterval = 10 * time.Minute

// DiskConfig is wb.yaml's disk: section, after defaults
// (spec/features/disk-prune).
type DiskConfig struct {
	// MinimumAvailable is the free-space floor, a share of each filesystem.
	MinimumAvailable float64
	// AutoPrune lets the daemon run wb disk prune --auto below the floor. It is
	// off unless disk.auto_prune is true: an unattended deleter is opt-in.
	AutoPrune bool
	// CheckInterval is how often the daemon checks free space.
	CheckInterval time.Duration
	// IgnoreSumFiles are checkout-relative paths of sum files the module
	// reference scan skips, such as deliberately broken test fixtures.
	IgnoreSumFiles []string
}

type diskSection struct {
	MinimumAvailable *float64 `yaml:"minimum_available"`
	AutoPrune        *bool    `yaml:"auto_prune"`
	CheckInterval    *string  `yaml:"check_interval"`
	Prune            *struct {
		IgnoreSumFiles []string `yaml:"ignore_sum_files"`
	} `yaml:"prune"`
}

// DefaultDiskConfig is the section's value when wb.yaml sets nothing.
func DefaultDiskConfig() DiskConfig {
	return DiskConfig{MinimumAvailable: DefaultDiskMinimumAvailable, CheckInterval: DefaultDiskCheckInterval}
}

// parseDisk decodes the disk: section, rejecting unknown keys and invalid
// values rather than silently falling back to a default.
func parseDisk(raw []byte) (DiskConfig, error) {
	var file struct {
		Disk yaml.Node `yaml:"disk"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return DiskConfig{}, fmt.Errorf("parse config: %w", err)
	}
	config := DefaultDiskConfig()
	if file.Disk.Kind == 0 {
		return config, nil
	}
	subtree, _ := yaml.Marshal(&file.Disk)
	decoder := yaml.NewDecoder(bytes.NewReader(subtree))
	decoder.KnownFields(true)
	var section diskSection
	if err := decoder.Decode(&section); err != nil && !errors.Is(err, io.EOF) {
		return DiskConfig{}, fmt.Errorf("parse disk section: %w", err)
	}
	if section.MinimumAvailable != nil {
		if *section.MinimumAvailable <= 0 || *section.MinimumAvailable >= 1 {
			return DiskConfig{}, fmt.Errorf("disk.minimum_available must be between 0 and 1 (exclusive), got %v", *section.MinimumAvailable)
		}
		config.MinimumAvailable = *section.MinimumAvailable
	}
	if section.AutoPrune != nil {
		config.AutoPrune = *section.AutoPrune
	}
	if section.CheckInterval != nil {
		interval, err := time.ParseDuration(*section.CheckInterval)
		if err != nil || interval < time.Second {
			return DiskConfig{}, fmt.Errorf("disk.check_interval must be a duration of at least 1s such as 10m, got %q", *section.CheckInterval)
		}
		config.CheckInterval = interval
	}
	if section.Prune != nil {
		for _, path := range section.Prune.IgnoreSumFiles {
			clean := filepath.Clean(strings.TrimSpace(path))
			if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
				return DiskConfig{}, fmt.Errorf("disk.prune.ignore_sum_files entries must be paths relative to a checkout root, got %q", path)
			}
			config.IgnoreSumFiles = append(config.IgnoreSumFiles, clean)
		}
	}
	return config, nil
}

// LoadDisk reads the disk: section from path. An absent file or section is
// not an error: the defaults apply.
func LoadDisk(path string) (DiskConfig, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultDiskConfig(), nil
	}
	if err != nil {
		return DiskConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	config, err := parseDisk(raw)
	if err != nil {
		return DiskConfig{}, fmt.Errorf("config %s: %w", path, err)
	}
	return config, nil
}
