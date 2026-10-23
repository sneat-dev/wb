package filewrite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Helpers kept for tests only: no production caller remains.

// WriteJSONAtomic encodes value as indented JSON followed by a newline and
// atomically replaces path, creating its parent directory when necessary.
func WriteJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return WriteBytesAtomic(filepath.Dir(path), filepath.Base(path), append(content, '\n'), mode)
}
