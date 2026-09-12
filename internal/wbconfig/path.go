// Package wbconfig resolves the user-level WB configuration file that several
// commands share (recipes for wb run, the remote section for wb remote).
package wbconfig

import (
	"os"
	"path/filepath"
)

// DefaultPath returns $XDG_CONFIG_HOME/wb/wb.yaml when configured, otherwise
// ~/.config/wb/wb.yaml.
func DefaultPath() string {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "wb", "wb.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "wb", "wb.yaml")
}
