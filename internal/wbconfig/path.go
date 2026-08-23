// Package wbconfig resolves the user-level WB configuration file that several
// commands share (recipes for wb run, the remote section for wb remote).
package wbconfig

import (
	"os"
	"path/filepath"
)

// DefaultPath returns ~/.config/wb/wb.yaml.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "wb", "wb.yaml")
}
