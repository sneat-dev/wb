package wbconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"syscall"

	"gopkg.in/yaml.v3"
)

// DefaultDiskMinimumAvailable is the share of a filesystem below which wb disk
// and wb clean point to wb disk prune. A tenth of the volume is enough for a
// large link step and a test run.
const DefaultDiskMinimumAvailable = 0.10

// DiskConfig is wb.yaml's disk: section, after defaults
// (spec/features/disk-prune).
type DiskConfig struct {
	// MinimumAvailable is the free-space floor, a share of each filesystem.
	MinimumAvailable float64
}

type diskSection struct {
	MinimumAvailable *float64 `yaml:"minimum_available"`
}

// DefaultDiskConfig is the section's value when wb.yaml sets nothing.
func DefaultDiskConfig() DiskConfig {
	return DiskConfig{MinimumAvailable: DefaultDiskMinimumAvailable}
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
		if math.IsNaN(*section.MinimumAvailable) || *section.MinimumAvailable <= 0 || *section.MinimumAvailable >= 1 {
			return DiskConfig{}, fmt.Errorf("disk.minimum_available must be between 0 and 1 (exclusive), got %v", *section.MinimumAvailable)
		}
		config.MinimumAvailable = *section.MinimumAvailable
	}
	return config, nil
}

// LoadDisk reads the disk: section from path. An absent file or section is
// not an error: the defaults apply.
func LoadDisk(path string) (DiskConfig, error) {
	// Only a regular file is read, opened without blocking and checked on
	// the open descriptor: a FIFO under the name (even one swapped in after
	// a check) would block the daemon, which reads this file on every check,
	// and its shutdown.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultDiskConfig(), nil
	}
	var raw []byte
	if err == nil {
		defer func() { _ = file.Close() }()
		if info, statErr := file.Stat(); statErr != nil || !info.Mode().IsRegular() {
			return DiskConfig{}, fmt.Errorf("config %s is not a regular file", path)
		}
		raw, err = io.ReadAll(file)
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
