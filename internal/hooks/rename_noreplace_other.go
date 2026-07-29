//go:build !darwin && !linux

package hooks

import "fmt"

func renameNoReplace(_ int, _ string, _ int, _ string) error {
	return fmt.Errorf("atomic no-replace rename is unsupported on this platform")
}
